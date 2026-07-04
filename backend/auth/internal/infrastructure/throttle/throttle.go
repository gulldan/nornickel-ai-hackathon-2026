// Package throttle adapts Valkey (via platform/valkey) to the
// domain.LoginThrottle port. It rate-limits failed logins to blunt brute-force
// guessing and the CPU cost of bcrypt on the hot path. Counting lives in Valkey
// (not process memory) so the limit is shared across auth-service replicas.
//
// Two keys back each client identifier: an attempt counter that expires after a
// rolling window, and — once the threshold is crossed — a lockout marker that
// holds the absolute expiry time so Allowed can report a precise Retry-After.
// The throttle fails open: if Valkey is unavailable it never blocks a login, it
// only stops counting (errors are surfaced for the caller to log at warn).
package throttle

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/platform/valkey"
)

const (
	attemptsPrefix = "login_attempts:"
	lockoutPrefix  = "login_lockout:"
)

// Config tunes the failed-login limiter.
type Config struct {
	// MaxAttempts is the number of failures within Window that triggers a lockout.
	MaxAttempts int
	// Window is the rolling period over which failures accumulate.
	Window time.Duration
	// Lockout is how long a client is blocked once MaxAttempts is reached.
	Lockout time.Duration
}

// Throttle is a Valkey-backed domain.LoginThrottle.
type Throttle struct {
	vk  *valkey.Client
	cfg Config
}

var _ domain.LoginThrottle = (*Throttle)(nil)

// New builds a Throttle. A non-positive MaxAttempts disables limiting entirely.
func New(vk *valkey.Client, cfg Config) *Throttle {
	return &Throttle{vk: vk, cfg: cfg}
}

// enabled reports whether limiting is configured on.
func (t *Throttle) enabled() bool {
	return t.cfg.MaxAttempts > 0 && t.cfg.Window > 0 && t.cfg.Lockout > 0
}

// Allowed reports whether key may attempt a login. When a lockout marker is
// present it returns false plus the remaining lockout time for Retry-After.
func (t *Throttle) Allowed(ctx context.Context, key string) (bool, time.Duration, error) {
	if !t.enabled() {
		return true, 0, nil
	}
	raw, found, err := t.vk.Get(ctx, lockoutKey(key))
	if err != nil {
		// Fail open: a Valkey outage must not lock everyone out.
		return true, 0, fmt.Errorf("read lockout: %w", err)
	}
	if !found {
		return true, 0, nil
	}
	retryAfter := t.cfg.Lockout
	if unix, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
		if d := time.Until(time.Unix(unix, 0)); d > 0 {
			retryAfter = d
		} else {
			// Marker is past its stored expiry (clock skew / stale read): treat
			// as cleared rather than reporting a negative wait.
			return true, 0, nil
		}
	}
	return false, retryAfter, nil
}

// RecordFailure registers one failed attempt and, on reaching MaxAttempts,
// writes the lockout marker.
func (t *Throttle) RecordFailure(ctx context.Context, key string) error {
	if !t.enabled() {
		return nil
	}
	n, err := t.vk.IncrTTL(ctx, attemptsKey(key), t.cfg.Window)
	if err != nil {
		return fmt.Errorf("increment attempts: %w", err)
	}
	if n >= int64(t.cfg.MaxAttempts) {
		expiry := time.Now().Add(t.cfg.Lockout).Unix()
		if serr := t.vk.Set(ctx, lockoutKey(key), strconv.FormatInt(expiry, 10), t.cfg.Lockout); serr != nil {
			return fmt.Errorf("set lockout: %w", serr)
		}
	}
	return nil
}

// Reset clears a client's counter and lockout after a successful login.
func (t *Throttle) Reset(ctx context.Context, key string) error {
	if !t.enabled() {
		return nil
	}
	if err := t.vk.Del(ctx, attemptsKey(key), lockoutKey(key)); err != nil {
		return fmt.Errorf("clear attempts: %w", err)
	}
	return nil
}

func attemptsKey(key string) string { return attemptsPrefix + key }
func lockoutKey(key string) string  { return lockoutPrefix + key }
