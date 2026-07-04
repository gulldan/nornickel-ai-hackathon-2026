package grpcx

import (
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// maxMsgSize bounds gRPC message size in both directions. The 4 MiB default
// breaks DocumentChunks for large multi-page documents (the norm for this
// audience); 64 MiB gives headroom while still capping memory. Shared by the
// client (here) and the server (server.go) since both live in package grpcx.
const maxMsgSize = 64 << 20

// retryServiceConfig enables transparent retries on UNAVAILABLE so a client that
// starts before its server (common under docker-compose) recovers automatically.
const retryServiceConfig = `{
  "methodConfig": [{
    "name": [{}],
    "retryPolicy": {
      "maxAttempts": 5,
      "initialBackoff": "0.2s",
      "maxBackoff": "5s",
      "backoffMultiplier": 2.0,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// Dial creates a lazy client connection to a gRPC server. Transport security is
// insecure because TLS terminates at the edge / mesh; internal traffic is on the
// private network. grpc.NewClient connects on first use and reconnects with
// backoff, so callers need not retry the dial themselves.
func Dial(addr string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(retryServiceConfig),
		// Ping idle connections so a silently dropped TCP link (NAT / idle
		// timeout) is detected and re-dialled instead of failing the next RPC.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMsgSize),
			grpc.MaxCallSendMsgSize(maxMsgSize),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}
	return conn, nil
}
