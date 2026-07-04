package amqp_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/email-parser/internal/application"
	amqpiface "github.com/example/email-parser/internal/interfaces/amqp"
	"github.com/example/email-parser/internal/platform/contracts"

	commonv1 "github.com/example/email-parser/internal/platform/genproto/common/v1"
)

// recordingStore serves canned bytes and never fails.
type recordingStore struct{ data []byte }

func (s *recordingStore) GetBytes(_ context.Context, _ string) ([]byte, error) {
	return s.data, nil
}

func (s *recordingStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) error {
	return nil
}

// staticExtractor returns a fixed text.
type staticExtractor struct{ text string }

func (e *staticExtractor) Extract(_ context.Context, _ []byte) (string, error) {
	return e.text, nil
}

// noopStatus accepts every status transition.
type noopStatus struct{}

func (noopStatus) UpdateDocumentStatus(_ context.Context, _, _, _ string, _ *int32) error {
	return nil
}

// capturingPublisher records the document ids seen on the chunk route.
type capturingPublisher struct{ chunkDocIDs []string }

func (p *capturingPublisher) PublishProto(_ context.Context, _, routingKey string, msg proto.Message) error {
	if routingKey == contracts.RouteChunk {
		if parsed, ok := msg.(*commonv1.DocumentParsed); ok {
			p.chunkDocIDs = append(p.chunkDocIDs, parsed.GetDocumentId())
		}
	}
	return nil
}

// TestHandlerDecodesAndProcesses checks that a valid protobuf body is decoded
// into a DocumentUploaded and forwarded to the processor.
func TestHandlerDecodesAndProcesses(t *testing.T) {
	t.Parallel()

	pub := &capturingPublisher{}
	proc := application.New(&staticExtractor{text: "body"}, &recordingStore{data: []byte("raw")}, noopStatus{}, pub)
	handle := amqpiface.Handler(proc)

	body, err := proto.Marshal(&commonv1.DocumentUploaded{DocumentId: "doc-42", ObjectKey: "raw/x.eml"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	if herr := handle(context.Background(), body); herr != nil {
		t.Fatalf("handler returned error: %v", herr)
	}
	if len(pub.chunkDocIDs) != 1 || pub.chunkDocIDs[0] != "doc-42" {
		t.Errorf("processor was not invoked with document doc-42: %v", pub.chunkDocIDs)
	}
}

// TestHandlerUnmarshalError checks that a non-protobuf body returns a decode
// error and never reaches the processor.
func TestHandlerUnmarshalError(t *testing.T) {
	t.Parallel()

	pub := &capturingPublisher{}
	proc := application.New(&staticExtractor{text: "body"}, &recordingStore{data: []byte("raw")}, noopStatus{}, pub)
	handle := amqpiface.Handler(proc)

	// A wire-type-invalid protobuf payload that proto.Unmarshal rejects.
	err := handle(context.Background(), []byte{0xff, 0xff, 0xff, 0xff})
	if err == nil {
		t.Fatalf("expected unmarshal error, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal DocumentUploaded") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(pub.chunkDocIDs) != 0 {
		t.Errorf("processor should not run on a decode failure")
	}
}

// TestHandlerPropagatesProcessError checks that an error from the processor is
// surfaced by the handler (so the message is dead-lettered).
func TestHandlerPropagatesProcessError(t *testing.T) {
	t.Parallel()

	proc := application.New(&staticExtractor{text: "body"}, &failingStore{}, noopStatus{}, &capturingPublisher{})
	handle := amqpiface.Handler(proc)

	body, err := proto.Marshal(&commonv1.DocumentUploaded{DocumentId: "doc-err"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	if herr := handle(context.Background(), body); herr == nil {
		t.Fatalf("expected processing error to propagate, got nil")
	}
}

// failingStore makes GetBytes fail so Process returns an error.
type failingStore struct{}

func (failingStore) GetBytes(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("download failed")
}

func (failingStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) error {
	return nil
}
