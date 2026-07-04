// Package amqp is chunk-splitter's delivery layer. It decodes the protobuf
// AMQP body into a DocumentParsed event and hands it to the application
// indexer. The returned function satisfies platform/messaging.HandlerFunc.
package amqp

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/example/chunk-splitter/internal/application"

	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

// Handler adapts an Indexer to the messaging consumer signature.
func Handler(ix *application.Indexer) func(ctx context.Context, body []byte) error {
	return func(ctx context.Context, body []byte) error {
		var evt commonv1.DocumentParsed
		if err := proto.Unmarshal(body, &evt); err != nil {
			return fmt.Errorf("unmarshal DocumentParsed: %w", err)
		}
		return ix.Process(ctx, &evt)
	}
}
