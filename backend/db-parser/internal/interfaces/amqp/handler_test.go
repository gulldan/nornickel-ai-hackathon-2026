package amqp_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/db-parser/internal/application"
	"github.com/example/db-parser/internal/interfaces/amqp"

	commonv1 "github.com/example/db-parser/internal/platform/genproto/common/v1"
)

// stubExtractor returns fixed text so the processor publishes downstream.
type stubExtractor struct{}

func (stubExtractor) Extract(context.Context, []byte, string, string) (string, error) {
	return "text", nil
}

// nopStore serves empty bytes and accepts every Put.
type nopStore struct{}

func (nopStore) GetBytes(context.Context, string) ([]byte, error) { return []byte("raw"), nil }
func (nopStore) Put(context.Context, string, io.Reader, int64, string) error {
	return nil
}

// nopStatus records nothing and never fails.
type nopStatus struct{}

func (nopStatus) UpdateDocumentStatus(context.Context, string, string, string, *int32) error {
	return nil
}

// capturingPublisher records the document id of the first published message so
// the handler's decode-and-dispatch can be asserted.
type capturingPublisher struct{ ids []string }

func (c *capturingPublisher) PublishProto(_ context.Context, _, _ string, msg proto.Message) error {
	if p, ok := msg.(*commonv1.DocumentParsed); ok {
		c.ids = append(c.ids, p.GetDocumentId())
	}
	return nil
}

// failingStore fails every download so the processor's fail path returns an
// error that the handler propagates.
type failingStore struct{}

func (failingStore) GetBytes(context.Context, string) ([]byte, error) {
	return nil, errors.New("download failed")
}
func (failingStore) Put(context.Context, string, io.Reader, int64, string) error {
	return nil
}

// TestHandlerDispatches decodes a valid protobuf body and forwards the event to
// the processor, which publishes a message carrying the same document id.
func TestHandlerDispatches(t *testing.T) {
	pub := &capturingPublisher{}
	p := application.New(stubExtractor{}, nopStore{}, nopStatus{}, pub)
	body, err := proto.Marshal(&commonv1.DocumentUploaded{
		DocumentId: "doc-42", OwnerId: "o", ObjectKey: "raw/doc-42",
		Filename: "a.db", MimeType: "application/vnd.sqlite3",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if herr := amqp.Handler(p)(context.Background(), body); herr != nil {
		t.Fatalf("Handler: %v", herr)
	}
	if len(pub.ids) == 0 || pub.ids[0] != "doc-42" {
		t.Fatalf("published ids = %v, want [doc-42]", pub.ids)
	}
}

// TestHandlerBadBody returns a decode error for a body that is not a valid
// DocumentUploaded message.
func TestHandlerBadBody(t *testing.T) {
	p := application.New(stubExtractor{}, nopStore{}, nopStatus{}, &capturingPublisher{})
	// A truncated/garbage protobuf wire payload that proto.Unmarshal rejects.
	err := amqp.Handler(p)(context.Background(), []byte{0xff, 0xff, 0xff})
	if err == nil {
		t.Fatalf("Handler expected unmarshal error, got nil")
	}
}

// TestHandlerPropagatesProcessError surfaces a processing failure (here a failed
// download) as the handler's returned error, which dead-letters the message.
func TestHandlerPropagatesProcessError(t *testing.T) {
	p := application.New(stubExtractor{}, failingStore{}, nopStatus{}, &capturingPublisher{})
	body, err := proto.Marshal(&commonv1.DocumentUploaded{
		DocumentId: "doc-err", OwnerId: "o", ObjectKey: "raw/doc-err",
		Filename: "a.db", MimeType: "application/vnd.sqlite3",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if herr := amqp.Handler(p)(context.Background(), body); herr == nil {
		t.Fatalf("Handler expected processing error, got nil")
	}
}
