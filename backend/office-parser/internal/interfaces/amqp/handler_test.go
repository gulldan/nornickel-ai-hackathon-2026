package amqp_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/example/office-parser/internal/application"
	"github.com/example/office-parser/internal/domain"
	"github.com/example/office-parser/internal/interfaces/amqp"

	commonv1 "github.com/example/office-parser/internal/platform/genproto/common/v1"
)

// recordingExtractor reports plain text so the processor publishes by reference
// without touching the object store.
type recordingExtractor struct{}

func (recordingExtractor) Extract(context.Context, []byte, string, string) (domain.ExtractionResult, error) {
	return domain.ExtractionResult{}, nil
}
func (recordingExtractor) IsPlainText(string, string) bool { return true }

// nopStore satisfies application.ObjectStore but is never called on the plain
// text path.
type nopStore struct{}

func (nopStore) GetBytes(context.Context, string) ([]byte, error) { return nil, nil }
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
	p := application.New(recordingExtractor{}, nopStore{}, nopStatus{}, pub)
	body, err := proto.Marshal(&commonv1.DocumentUploaded{
		DocumentId: "doc-42", OwnerId: "o", ObjectKey: "raw/doc-42",
		Filename: "a.txt", MimeType: "", Size: 0, TraceId: "", ArchiveDepth: 0,
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
	p := application.New(recordingExtractor{}, nopStore{}, nopStatus{}, &capturingPublisher{})
	// A truncated/garbage protobuf wire payload that proto.Unmarshal rejects.
	err := amqp.Handler(p)(context.Background(), []byte{0xff, 0xff, 0xff})
	if err == nil {
		t.Fatalf("Handler expected unmarshal error, got nil")
	}
}

// TestHandlerPropagatesProcessError surfaces a processing failure (here a failed
// download) as the handler's returned error, which dead-letters the message.
func TestHandlerPropagatesProcessError(t *testing.T) {
	// Force the binary path so the download (and its failure) actually runs.
	p := application.New(downloadingExtractor{}, failingStore{}, nopStatus{}, &capturingPublisher{})
	body, err := proto.Marshal(&commonv1.DocumentUploaded{
		DocumentId: "doc-err", OwnerId: "o", ObjectKey: "raw/doc-err",
		Filename: "a.docx", MimeType: "", Size: 0, TraceId: "", ArchiveDepth: 0,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if herr := amqp.Handler(p)(context.Background(), body); herr == nil {
		t.Fatalf("Handler expected processing error, got nil")
	}
}

// downloadingExtractor forces the binary (download) path by reporting the source
// is not plain text.
type downloadingExtractor struct{}

func (downloadingExtractor) Extract(context.Context, []byte, string, string) (domain.ExtractionResult, error) {
	return domain.ExtractionResult{}, nil
}
func (downloadingExtractor) IsPlainText(string, string) bool { return false }
