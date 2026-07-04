// Package amqp is vlm-service's delivery layer. It decodes the protobuf AMQP
// body into a DocumentUploaded event and hands it to the application
// processor. The returned function satisfies platform/messaging.HandlerFunc.
package amqp

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/example/vlm-service/internal/application"

	commonv1 "github.com/example/vlm-service/internal/platform/genproto/common/v1"
)

// Handler adapts a Processor to the messaging consumer signature.
func Handler(p *application.Processor) func(ctx context.Context, body []byte) error {
	return func(ctx context.Context, body []byte) error {
		var evt commonv1.DocumentUploaded
		if err := proto.Unmarshal(body, &evt); err != nil {
			return fmt.Errorf("unmarshal DocumentUploaded: %w", err)
		}
		return p.Process(ctx, &evt)
	}
}
