// Package messaging wraps RabbitMQ (amqp091) with the platform topology and
// ergonomics: a topic "ingestion" exchange feeding durable parser queues, a
// fanout "events" exchange for live progress, and a dead-letter exchange for
// poison messages. Event bodies are protobuf binary (compact, low-allocation).
// Workers are competing consumers, so scaling a worker horizontally lets
// RabbitMQ load-balance deliveries across replicas.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/example/main-service/internal/platform/contracts"
	"github.com/example/main-service/internal/platform/logger"
)

// protoSchemaVersion tags published messages with the proto contract version so
// consumers can observe — and, if ever needed, filter by — the schema generation.
const protoSchemaVersion = "v1"

// Recorder records per-message metrics. *observability.Metrics satisfies it; it
// is injected so this package keeps no global state.
type Recorder interface {
	RecordMessage(queue, outcome string, seconds float64)
}

// Conn owns an AMQP connection.
type Conn struct {
	conn *amqp.Connection
	log  zerolog.Logger
}

// Dial connects to RabbitMQ, retrying with backoff so services tolerate the
// broker booting after them.
func Dial(ctx context.Context, url string, log zerolog.Logger) (*Conn, error) {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		conn, err := amqp.Dial(url)
		if err == nil {
			log.Info().Msg("connected to rabbitmq")
			return &Conn{conn: conn, log: log}, nil
		}
		lastErr = err
		log.Warn().Int("attempt", attempt).Err(err).Msg("rabbitmq not ready, retrying")
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial rabbitmq: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("dial rabbitmq: %w", lastErr)
}

// Close tears down the connection.
func (c *Conn) Close() error {
	if c.conn == nil {
		return nil
	}
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close rabbitmq: %w", err)
	}
	return nil
}

// DeclareTopology idempotently declares every exchange, queue and binding.
func (c *Conn) DeclareTopology() error {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	for _, ex := range []struct{ name, kind string }{
		{contracts.ExchangeIngestion, "topic"},
		{contracts.ExchangeEvents, "fanout"},
		{contracts.ExchangeDLX, "fanout"},
	} {
		if err = ch.ExchangeDeclare(ex.name, ex.kind, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %s: %w", ex.name, err)
		}
	}

	if _, err = ch.QueueDeclare(contracts.QueueDead, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead queue: %w", err)
	}
	if err = ch.QueueBind(contracts.QueueDead, "", contracts.ExchangeDLX, false, nil); err != nil {
		return fmt.Errorf("bind dead queue: %w", err)
	}

	args := amqp.Table{"x-dead-letter-exchange": contracts.ExchangeDLX}
	for queue, key := range contracts.ParserQueues() {
		if _, err = ch.QueueDeclare(queue, true, false, false, false, args); err != nil {
			return fmt.Errorf("declare queue %s: %w", queue, err)
		}
		if err = ch.QueueBind(queue, key, contracts.ExchangeIngestion, false, nil); err != nil {
			return fmt.Errorf("bind queue %s: %w", queue, err)
		}
	}
	return nil
}

// Publisher publishes persistent messages. Its channel is mutex-guarded because
// amqp091 channels are not safe for concurrent writes.
type Publisher struct {
	ch  *amqp.Channel
	mu  sync.Mutex
	log zerolog.Logger
}

// NewPublisher opens a dedicated publishing channel in confirm mode.
func (c *Conn) NewPublisher() (*Publisher, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open publish channel: %w", err)
	}
	// Confirm mode: the broker acks a publish once it has taken responsibility
	// for the message (routed/persisted). Without it a publish the broker drops
	// (channel error, broker restart mid-publish) is lost silently and the
	// document stalls forever in its current status — the failure mode the audit
	// flagged. PublishProto waits for this confirm before returning.
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("enable publish confirms: %w", err)
	}
	return &Publisher{ch: ch, mu: sync.Mutex{}, log: c.log}, nil
}

// PublishProto marshals msg to protobuf and publishes it, then blocks until the
// broker confirms it so a dropped publish surfaces as an error to the caller
// instead of silently losing the message.
func (p *Publisher) PublishProto(ctx context.Context, exchange, routingKey string, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal proto message: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	dc, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/x-protobuf",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now().UTC(),
		Headers: amqp.Table{
			"x-proto-type":    string(msg.ProtoReflect().Descriptor().FullName()),
			"x-proto-version": protoSchemaVersion,
		},
		Body: body,
	})
	if err != nil {
		return fmt.Errorf("publish to %s/%s: %w", exchange, routingKey, err)
	}
	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("await confirm for %s/%s: %w", exchange, routingKey, err)
	}
	if !acked {
		return fmt.Errorf("publish to %s/%s nacked by broker", exchange, routingKey)
	}
	return nil
}

// Close releases the publishing channel.
func (p *Publisher) Close() error {
	if err := p.ch.Close(); err != nil {
		return fmt.Errorf("close publish channel: %w", err)
	}
	return nil
}

// HandlerFunc processes one message body. Returning nil acks the message;
// returning an error nacks it without requeue, sending it to the DLX.
type HandlerFunc func(ctx context.Context, body []byte) error

// ConsumerConfig tunes a consumer.
type ConsumerConfig struct {
	Queue    string
	Workers  int
	Prefetch int
	// HandleTimeout bounds how long one message may be processed before its
	// context is cancelled. Zero means no per-message deadline. Set it per queue
	// (generous for OCR/archive, minutes for office/email) so a hung backend
	// frees the worker slot via dead-letter instead of pinning it forever.
	HandleTimeout time.Duration
	Recorder      Recorder
}

// Consume runs a competing-consumer loop on cfg.Queue until ctx is cancelled.
func (c *Conn) Consume(ctx context.Context, cfg ConsumerConfig, handle HandlerFunc) error {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.Prefetch <= 0 {
		cfg.Prefetch = cfg.Workers
	}
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("open consume channel: %w", err)
	}
	defer func() { _ = ch.Close() }()
	// A closed channel/connection (broker restart, network drop) must stop the
	// service with an error: deliveries closes silently, and without this the
	// process would hang forever as a "consumer with no subscription" while the
	// orchestrator never learns it should restart.
	closed := make(chan *amqp.Error, 1)
	ch.NotifyClose(closed)

	if err = ch.Qos(cfg.Prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}
	deliveries, err := ch.Consume(cfg.Queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", cfg.Queue, err)
	}
	c.log.Info().Str("queue", cfg.Queue).Int("workers", cfg.Workers).Msg("consuming")

	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range deliveries {
				c.dispatch(ctx, cfg, d, handle)
			}
		}()
	}
	select {
	case <-ctx.Done():
		_ = ch.Cancel("", false)
		wg.Wait()
		return nil
	case amqpErr := <-closed:
		wg.Wait()
		if amqpErr != nil {
			return fmt.Errorf("consume channel closed: %w", amqpErr)
		}
		return errors.New("consume channel closed")
	}
}

func (c *Conn) dispatch(ctx context.Context, cfg ConsumerConfig, d amqp.Delivery, handle HandlerFunc) {
	start := time.Now()
	// Bind the connection logger onto the message context so handler code that
	// uses logger.From(ctx) inherits the service tag instead of the fallback.
	// The redelivered flag rides along so handlers know whether a failure still
	// has the automatic requeue-once retry ahead of it.
	mctx := contracts.WithRedelivered(logger.Into(ctx, c.log), d.Redelivered)
	if cfg.HandleTimeout > 0 {
		var cancel context.CancelFunc
		mctx, cancel = context.WithTimeout(mctx, cfg.HandleTimeout)
		defer cancel()
	}
	err := handle(mctx, d.Body)
	outcome := "ok"
	switch {
	case err == nil:
		_ = d.Ack(false)
	case !d.Redelivered:
		// First failure: requeue once so a transient error (a momentary store or
		// broker blip) gets one retry before the message is dead-lettered.
		outcome = "retry"
		c.log.Warn().Err(err).Str("queue", cfg.Queue).Msg("handling failed; requeuing once")
		_ = d.Nack(false, true)
	default:
		// Already retried once: dead-letter so a genuine poison message cannot loop.
		outcome = "error"
		c.log.Error().Err(err).Str("queue", cfg.Queue).Msg("handling failed after retry; dead-lettering")
		_ = d.Nack(false, false)
	}
	if cfg.Recorder != nil {
		cfg.Recorder.RecordMessage(cfg.Queue, outcome, time.Since(start).Seconds())
	}
}

// ConsumeEvents binds an exclusive, auto-deleting queue to the events fanout and
// invokes handle for each broadcast. Events are best-effort, so deliveries are
// auto-acked.
func (c *Conn) ConsumeEvents(ctx context.Context, handle HandlerFunc) error {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("open events channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		return fmt.Errorf("declare events queue: %w", err)
	}
	if err = ch.QueueBind(q.Name, "", contracts.ExchangeEvents, false, nil); err != nil {
		return fmt.Errorf("bind events queue: %w", err)
	}
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume events: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				// The channel closed not on our initiative — report upward, else
				// live updates die silently until the process restarts.
				return errors.New("events channel closed")
			}
			if herr := handle(logger.Into(ctx, c.log), d.Body); herr != nil {
				c.log.Warn().Err(herr).Msg("events handler error")
			}
		}
	}
}
