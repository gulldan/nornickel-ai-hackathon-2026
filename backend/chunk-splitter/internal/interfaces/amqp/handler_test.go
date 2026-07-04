package amqp_test

import (
	"context"
	"testing"

	"github.com/example/chunk-splitter/internal/interfaces/amqp"
)

// A body that is not a valid DocumentParsed must fail before the indexer runs.
func TestHandlerBadBody(t *testing.T) {
	if err := amqp.Handler(nil)(context.Background(), []byte{0xff, 0xff, 0xff}); err == nil {
		t.Fatalf("Handler expected unmarshal error, got nil")
	}
}
