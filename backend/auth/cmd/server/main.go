// Command auth-service issues and verifies the platform's JWTs. It verifies
// credentials through db-service, signs short-lived access tokens, and keeps
// refresh tokens in Valkey. It is stateless and horizontally scalable: all
// session state lives in Valkey.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/example/auth/internal/application"
	"github.com/example/auth/internal/infrastructure/directory"
	"github.com/example/auth/internal/infrastructure/sessions"
	"github.com/example/auth/internal/infrastructure/throttle"
	"github.com/example/auth/internal/interfaces/httpapi"
	"github.com/example/auth/internal/platform/config"
	"github.com/example/auth/internal/platform/dbclient"
	"github.com/example/auth/internal/platform/httpx"
	"github.com/example/auth/internal/platform/jwt"
	"github.com/example/auth/internal/platform/logger"
	"github.com/example/auth/internal/platform/observability"
	"github.com/example/auth/internal/platform/valkey"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(config.Get("METRICS_ADDR", ":9090")); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	log := logger.New("auth-service", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("auth-service stopped with error")
		os.Exit(1)
	}
}

func healthcheck(metricsAddr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL(metricsAddr), nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("readyz: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("readyz status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func readyURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/") + "/readyz"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	} else if host, port, err := net.SplitHostPort(addr); err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
			addr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	return "http://" + addr + "/readyz"
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Token signer: HS256 access tokens shared with every other HTTP service.
	// Fail closed on the shipped dev default: compose falls back to it when
	// JWT_SECRET is unset in .env, which would silently sign forgeable tokens.
	jwtSecret := config.MustGet("JWT_SECRET")
	if jwtSecret == "dev-secret-change-me" {
		log.Fatal().Msg("JWT_SECRET is the insecure default 'dev-secret-change-me'; set a strong secret in infra/.env before starting")
	}
	manager := jwt.NewManager(
		jwtSecret,
		config.Get("JWT_ISSUER", "rag-platform"),
		config.GetDuration("JWT_ACCESS_TTL", 30*time.Minute),
	)

	// System of record for credentials.
	db, err := dbclient.New(config.MustGet("DB_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Refresh-token / session store.
	vk, err := valkey.New(ctx, config.Get("VALKEY_ADDR", "valkey:6379"), config.Get("VALKEY_PASSWORD", ""), 0)
	if err != nil {
		return err
	}
	defer func() { _ = vk.Close() }()

	// Failed-login limiter: blunts brute-force and the bcrypt CPU cost. Fails
	// open if Valkey is down (the service logs and lets the login through).
	loginThrottle := throttle.New(vk, throttle.Config{
		MaxAttempts: config.GetInt("AUTH_LOGIN_MAX_ATTEMPTS", 10),
		Window:      time.Duration(config.GetInt("AUTH_LOGIN_WINDOW_SEC", 900)) * time.Second,
		Lockout:     time.Duration(config.GetInt("AUTH_LOGIN_LOCKOUT_SEC", 900)) * time.Second,
	})

	// Bound each outbound db-service call so a wedged dependency cannot stall a
	// login indefinitely.
	dbTimeout := time.Duration(config.GetInt("AUTH_DB_TIMEOUT_SEC", 5)) * time.Second

	svc := application.New(
		directory.New(db, dbTimeout),
		sessions.New(vk, config.GetDuration("REFRESH_TTL", 720*time.Hour)),
		manager,
		loginThrottle,
	)

	// Readiness depends on both hard auth dependencies: db-service for
	// credentials and Valkey for refresh-token/session state.
	metrics := observability.NewMetrics()
	go func() {
		_ = observability.RunOps(ctx, config.Get("METRICS_ADDR", ":9090"), metrics, readiness(db, vk), log)
	}()

	mux := http.NewServeMux()
	httpapi.New(svc).Routes(mux)

	handler := httpx.Chain(gateRegistration(manager, mux),
		httpx.RequestID,
		httpx.Recover(log),
		httpx.LogRequests(log),
	)

	srv := httpx.NewServer(config.Get("HTTP_ADDR", ":8080"), handler, log)
	return srv.Run(ctx)
}

// gateRegistration blocks anonymous POST /register when REGISTRATION_ADMIN_ONLY
// is set (public deployments): only a caller with a valid admin bearer token
// passes, so the admin panel keeps working while self-registration from the
// internet is refused. Default on keeps standalone runs fail-closed; local
// dev/e2e can set REGISTRATION_ADMIN_ONLY=false. The gateway rewrites
// /api/v1/auth/register to /register before it reaches this service.
func gateRegistration(manager *jwt.Manager, next http.Handler) http.Handler {
	adminOnly := config.GetBool("REGISTRATION_ADMIN_ONLY", true)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminOnly && r.Method == http.MethodPost && r.URL.Path == "/register" && !callerIsAdmin(manager, r) {
			http.Error(w, `{"error":"registration is disabled on this deployment"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// callerIsAdmin reports whether the request carries a valid admin bearer token.
func callerIsAdmin(manager *jwt.Manager, r *http.Request) bool {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return false
	}
	claims, err := manager.Parse(parts[1])
	if err != nil {
		return false
	}
	for _, role := range claims.Roles {
		if role == "admin" {
			return true
		}
	}
	return false
}

func readiness(db *dbclient.Client, vk *valkey.Client) observability.ReadyFunc {
	return func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return err
		}
		return vk.Ping(ctx)
	}
}
