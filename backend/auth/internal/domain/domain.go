// Package domain holds auth-service's core entity and the ports the application
// layer depends on. Authentication has a small domain: an Identity (who the user
// is) plus two ports — a UserDirectory that verifies credentials and a
// SessionStore that persists refresh tokens. HTTP, db-service and Valkey live in
// the interfaces and infrastructure layers respectively (DDD ports & adapters),
// so the use cases stay transport- and storage-agnostic.
package domain

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidCredentials is returned when a username is unknown or the supplied
// password does not match. The HTTP layer maps it to 401; it is deliberately
// indistinguishable from a missing user so we do not leak which usernames exist.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrSessionNotFound is returned when a refresh token has no live session (it
// expired or was revoked). The HTTP layer maps it to 401.
var ErrSessionNotFound = errors.New("session not found")

// ErrUserExists is returned by Register when the username is already taken. The
// HTTP layer maps it to 409 Conflict, so a duplicate registration never leaks
// the underlying database error.
var ErrUserExists = errors.New("username already taken")

// ErrWeakPassword is returned by Register when the supplied password fails the
// length policy. The HTTP layer maps it to 400 Bad Request; the wrapped message
// carries the human-readable reason.
var ErrWeakPassword = errors.New("password does not meet requirements")

// ErrTooManyAttempts is returned by Login when the failed-attempt threshold for
// a client has been exceeded. The HTTP layer maps it to 429 Too Many Requests.
// A TooManyAttemptsError carries the lockout window for the Retry-After header.
var ErrTooManyAttempts = errors.New("too many failed login attempts")

// TooManyAttemptsError reports a login lockout and how long the caller must wait.
// It unwraps to ErrTooManyAttempts so callers can match it with errors.Is.
type TooManyAttemptsError struct {
	RetryAfter time.Duration
}

func (e *TooManyAttemptsError) Error() string {
	return fmt.Sprintf("%s: retry after %s", ErrTooManyAttempts.Error(), e.RetryAfter)
}

// Unwrap lets errors.Is(err, ErrTooManyAttempts) succeed.
func (e *TooManyAttemptsError) Unwrap() error { return ErrTooManyAttempts }

// Identity describes an authenticated principal. It is the JSON payload stored
// alongside a refresh token and the user block echoed back to clients.
type Identity struct {
	UserID   string   `json:"user_id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
}

// UserDirectory verifies credentials against the system of record (db-service).
// The infrastructure adapter performs the lookup and the bcrypt comparison so
// that the application layer never sees a password hash.
type UserDirectory interface {
	// Authenticate returns the matching Identity when username/password are
	// valid, or ErrInvalidCredentials otherwise.
	Authenticate(ctx context.Context, username, password string) (*Identity, error)
	// Register creates a new account and returns its Identity.
	Register(ctx context.Context, username, password string, roles []string) (*Identity, error)
}

// LoginThrottle rate-limits authentication attempts to blunt brute-force and
// the CPU cost of bcrypt on the hot path. It is backed by Valkey so the limit
// is shared across auth-service replicas. Implementations fail open: when the
// backing store is unavailable they must not block logins, only stop counting.
type LoginThrottle interface {
	// Allowed reports whether a client (keyed by username and/or IP) may attempt
	// a login. When blocked it returns false and the remaining lockout duration.
	Allowed(ctx context.Context, key string) (ok bool, retryAfter time.Duration, err error)
	// RecordFailure registers one failed attempt for the key.
	RecordFailure(ctx context.Context, key string) error
	// Reset clears a key's counter after a successful login.
	Reset(ctx context.Context, key string) error
}

// SessionStore persists refresh tokens and the identity they grant. It is
// backed by Valkey with a TTL, so sessions expire on their own.
type SessionStore interface {
	// Save associates a refresh token with an Identity for the session lifetime.
	Save(ctx context.Context, token string, identity *Identity) error
	// Load returns the Identity behind a refresh token, or ErrSessionNotFound.
	Load(ctx context.Context, token string) (*Identity, error)
	// Delete revokes a refresh token (logout). Deleting an absent token is a no-op.
	Delete(ctx context.Context, token string) error
}
