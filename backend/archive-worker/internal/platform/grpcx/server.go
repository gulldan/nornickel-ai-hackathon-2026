// Package grpcx wraps google.golang.org/grpc with the server bootstrap and the
// client dialling the platform standardises on: panic-recovery and zerolog
// logging interceptors, a registered health service, and context-driven
// graceful shutdown. Internal service-to-service traffic (main->db, main->llm,
// auth->db, workers->db) uses gRPC; the browser edge stays REST.
package grpcx

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/example/archive-worker/internal/platform/logger"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Server is a gRPC server with standard interceptors and graceful shutdown.
type Server struct {
	gs  *grpc.Server
	log zerolog.Logger
}

// NewServer builds a gRPC server with recovery and logging interceptors and a
// health service already registered and serving.
func NewServer(log zerolog.Logger) *Server {
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			recoveryInterceptor(log),
			loggingInterceptor(log),
		),
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		// Accept client keepalive pings (clientKeepalive sends every 30s) without
		// tearing the connection down; allow pings on idle connections.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	)
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(gs, hs)
	return &Server{gs: gs, log: log}
}

// Registrar exposes the underlying server for service registration.
func (s *Server) Registrar() grpc.ServiceRegistrar { return s.gs }

// Run listens on addr and serves until ctx is cancelled, then stops gracefully.
func (s *Server) Run(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc listen %s: %w", addr, err)
	}
	go func() {
		<-ctx.Done()
		s.gs.GracefulStop()
	}()
	s.log.Info().Str("addr", addr).Msg("grpc server listening")
	if err := s.gs.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

func recoveryInterceptor(log zerolog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Str("method", info.FullMethod).Msg("recovered panic")
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func loggingInterceptor(log zerolog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		// Bind the server logger onto the call context so handler code that uses
		// logger.From(ctx) inherits the service tag instead of the fallback.
		resp, err := handler(logger.Into(ctx, log), req)
		log.Info().
			Str("method", info.FullMethod).
			Str("code", status.Code(err).String()).
			Dur("took", time.Since(start)).
			Msg("grpc call")
		return resp, err
	}
}
