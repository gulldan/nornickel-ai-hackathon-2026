package messaging

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/example/archive-worker/internal/platform/contracts"
)

// Outcome labels dispatch reports to the recorder.
const (
	outcomeOK    = "ok"
	outcomeRetry = "retry"
	outcomeError = "error"
)

// fakeAck records the acknowledgement the dispatcher takes for one delivery.
type fakeAck struct {
	mu      sync.Mutex
	acked   bool
	nacked  bool
	requeue bool
}

func (f *fakeAck) Ack(_ uint64, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = true
	return nil
}

func (f *fakeAck) Nack(_ uint64, _, requeue bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nacked = true
	f.requeue = requeue
	return nil
}

func (f *fakeAck) Reject(_ uint64, _ bool) error { return nil }

// fakeRecorder captures the outcome label dispatch reports.
type fakeRecorder struct {
	queue   string
	outcome string
	calls   int
}

func (r *fakeRecorder) RecordMessage(queue, outcome string, _ float64) {
	r.queue = queue
	r.outcome = outcome
	r.calls++
}

// TestDispatch covers the ack/nack/dead-letter decision and metric outcome for
// every handler result, including the per-message timeout.
func TestDispatch(t *testing.T) {
	cases := []struct {
		name        string
		redelivered bool
		timeout     time.Duration
		handle      HandlerFunc
		wantAck     bool
		wantNack    bool
		wantRequeue bool
		wantOutcome string
	}{
		{
			name:        "success acks",
			handle:      func(context.Context, []byte) error { return nil },
			wantAck:     true,
			wantOutcome: outcomeOK,
		},
		{
			name:        "first failure requeues once",
			handle:      func(context.Context, []byte) error { return errors.New("boom") },
			wantNack:    true,
			wantRequeue: true,
			wantOutcome: outcomeRetry,
		},
		{
			name:        "redelivered failure dead-letters",
			redelivered: true,
			handle:      func(context.Context, []byte) error { return errors.New("boom") },
			wantNack:    true,
			wantRequeue: false,
			wantOutcome: outcomeError,
		},
		{
			name:    "handle timeout cancels context",
			timeout: time.Millisecond,
			handle: func(ctx context.Context, _ []byte) error {
				<-ctx.Done()
				return ctx.Err()
			},
			wantNack:    true,
			wantRequeue: true,
			wantOutcome: outcomeRetry,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Conn{log: zerolog.Nop()}
			ack := &fakeAck{}
			rec := &fakeRecorder{}
			cfg := ConsumerConfig{Queue: "q", Workers: 1, HandleTimeout: tc.timeout, Recorder: rec}
			d := amqp.Delivery{Acknowledger: ack, Body: []byte("payload"), Redelivered: tc.redelivered}
			c.dispatch(context.Background(), cfg, d, tc.handle)

			if ack.acked != tc.wantAck || ack.nacked != tc.wantNack {
				t.Fatalf("ack=%v nack=%v, want ack=%v nack=%v", ack.acked, ack.nacked, tc.wantAck, tc.wantNack)
			}
			if ack.nacked && ack.requeue != tc.wantRequeue {
				t.Fatalf("requeue=%v, want %v", ack.requeue, tc.wantRequeue)
			}
			if rec.calls != 1 || rec.outcome != tc.wantOutcome || rec.queue != "q" {
				t.Fatalf("recorder = %+v, want one %q on q", rec, tc.wantOutcome)
			}
		})
	}
}

// TestDispatchNilRecorder verifies a nil Recorder is tolerated.
func TestDispatchNilRecorder(t *testing.T) {
	c := &Conn{log: zerolog.Nop()}
	ack := &fakeAck{}
	d := amqp.Delivery{Acknowledger: ack, Body: nil}
	c.dispatch(context.Background(), ConsumerConfig{Queue: "q"}, d, func(context.Context, []byte) error { return nil })
	if !ack.acked {
		t.Fatal("delivery should be acked even without a recorder")
	}
}

// TestDialError returns promptly when the broker is unreachable and the context
// is already done.
func TestDialError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, "amqp://127.0.0.1:1/", zerolog.Nop()); err == nil {
		t.Fatal("Dial to an unreachable broker with a cancelled context must error")
	}
}

// TestCloseNil tolerates closing a connection that never dialled.
func TestCloseNil(t *testing.T) {
	if err := (&Conn{}).Close(); err != nil {
		t.Fatalf("Close on a nil connection = %v, want nil", err)
	}
}

// --- broker integration: runs only when RABBITMQ_TEST_URL is set ---

// requireBroker returns RABBITMQ_TEST_URL or skips the test when it is unset.
func requireBroker(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("set RABBITMQ_TEST_URL to run messaging broker tests")
	}
	return url
}

func dialTest(t *testing.T) *Conn {
	t.Helper()
	conn, err := Dial(context.Background(), requireBroker(t), zerolog.Nop())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestBrokerPublishConsume declares the topology, publishes a confirmed proto
// message to an isolated exchange/queue and consumes it back.
func TestBrokerPublishConsume(t *testing.T) {
	conn := dialTest(t)
	if err := conn.DeclareTopology(); err != nil {
		t.Fatalf("declare topology: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	ex, q, rk := "test.ex."+suffix, "test.q."+suffix, "rk"
	ch, err := conn.conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	if derr := ch.ExchangeDeclare(ex, "topic", false, true, false, false, nil); derr != nil {
		t.Fatalf("declare exchange: %v", derr)
	}
	// Exclusive + auto-delete: scoped to this connection (test isolation) and
	// allowed to be transient on RabbitMQ 4, which rejects transient non-exclusive queues.
	if _, derr := ch.QueueDeclare(q, false, true, true, false, nil); derr != nil {
		t.Fatalf("declare queue: %v", derr)
	}
	if berr := ch.QueueBind(q, rk, ex, false, nil); berr != nil {
		t.Fatalf("bind: %v", berr)
	}
	_ = ch.Close()

	pub, err := conn.NewPublisher()
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })
	if perr := pub.PublishProto(context.Background(), ex, rk, timestamppb.New(time.Now())); perr != nil {
		t.Fatalf("publish: %v", perr)
	}

	got := make(chan []byte, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = conn.Consume(ctx, ConsumerConfig{Queue: q, Workers: 1, Recorder: &fakeRecorder{}},
			func(_ context.Context, body []byte) error {
				select {
				case got <- body:
				default:
				}
				return nil
			})
	}()
	select {
	case body := <-got:
		if len(body) == 0 {
			t.Fatal("consumed an empty body")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the published message")
	}
}

// dialOwn dials a connection the caller closes itself (no t.Cleanup), for the
// teardown-path tests below.
func dialOwn(t *testing.T) *Conn {
	t.Helper()
	conn, err := Dial(context.Background(), requireBroker(t), zerolog.Nop())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// TestBrokerConsumeStopsOnClose verifies Consume returns an error when the
// connection drops out from under it (broker restart / network blip).
func TestBrokerConsumeStopsOnClose(t *testing.T) {
	conn := dialOwn(t)
	if err := conn.DeclareTopology(); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	var queue string
	for q := range contracts.ParserQueues() {
		queue = q
		break
	}
	done := make(chan error, 1)
	go func() {
		done <- conn.Consume(context.Background(), ConsumerConfig{Queue: queue, Workers: 1},
			func(context.Context, []byte) error { return nil })
	}()
	time.Sleep(300 * time.Millisecond) // let the consumer subscribe
	_ = conn.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Consume should error when the connection closes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Consume did not return after the connection closed")
	}
}

// TestBrokerConsumeEventsStopsOnClose verifies ConsumeEvents returns when its
// channel closes unexpectedly.
func TestBrokerConsumeEventsStopsOnClose(t *testing.T) {
	conn := dialOwn(t)
	if err := conn.DeclareTopology(); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- conn.ConsumeEvents(context.Background(), func(context.Context, []byte) error { return nil })
	}()
	time.Sleep(300 * time.Millisecond)
	_ = conn.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ConsumeEvents should error when the channel closes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConsumeEvents did not return after the connection closed")
	}
}

// TestBrokerPublishAfterClose surfaces a publish on a closed channel as an error.
func TestBrokerPublishAfterClose(t *testing.T) {
	conn := dialTest(t)
	pub, err := conn.NewPublisher()
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	if cerr := pub.Close(); cerr != nil {
		t.Fatalf("close publisher: %v", cerr)
	}
	if perr := pub.PublishProto(context.Background(), contracts.ExchangeEvents, "", timestamppb.New(time.Now())); perr == nil {
		t.Fatal("PublishProto on a closed publisher must error")
	}
}

// TestBrokerConsumeEvents receives a broadcast off the events fanout. The
// consumer must be bound before the publish lands, so publishing retries.
func TestBrokerConsumeEvents(t *testing.T) {
	conn := dialTest(t)
	if err := conn.DeclareTopology(); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	pub, err := conn.NewPublisher()
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	got := make(chan []byte, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = conn.ConsumeEvents(ctx, func(_ context.Context, body []byte) error {
			select {
			case got <- body:
			default:
			}
			return nil
		})
	}()

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-tick.C:
			if perr := pub.PublishProto(ctx, contracts.ExchangeEvents, "", timestamppb.New(time.Now())); perr != nil {
				t.Fatalf("publish event: %v", perr)
			}
		case body := <-got:
			if len(body) == 0 {
				t.Fatal("consumed an empty event body")
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for a broadcast event")
		}
	}
}
