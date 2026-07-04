// Package application implements auth-service's use cases: logging in, refreshing
// an access token, logging out, and registering a user. It depends only on the
// domain ports (UserDirectory, SessionStore) and a small token-issuer port, so
// the same logic runs against any directory, session store or signer. Handlers
// in the interfaces layer call into AuthService; AuthService owns refresh-token
// generation and the assembly of the login response.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/platform/logger"
)

// Password length bounds enforced at registration. bcrypt silently truncates
// input past 72 bytes, so a password longer than that does not mean what the
// user thinks it does; we reject it instead of hashing a prefix.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
)

// defaultRole is the single non-privileged role granted to public registrations.
const defaultRole = "user"

// TokenIssuer mints signed access tokens. It is satisfied by
// platform/jwt.Manager; declaring it here keeps the use cases independent of the
// signing implementation.
type TokenIssuer interface {
	// Issue returns the signed token, its expiry, and any signing error.
	Issue(userID, username string, roles []string) (string, time.Time, error)
}

// AuthService wires the directory, session store, token issuer and login
// throttle into the authentication use cases.
type AuthService struct {
	dir      domain.UserDirectory
	sessions domain.SessionStore
	issuer   TokenIssuer
	throttle domain.LoginThrottle
}

// New constructs an AuthService.
func New(
	dir domain.UserDirectory,
	sessions domain.SessionStore,
	issuer TokenIssuer,
	throttle domain.LoginThrottle,
) *AuthService {
	return &AuthService{dir: dir, sessions: sessions, issuer: issuer, throttle: throttle}
}

// Tokens is the result of a successful login or refresh.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Identity     *domain.Identity
}

// Login verifies credentials, issues an access token, and stores a fresh refresh
// token. It returns domain.ErrInvalidCredentials on an unknown user or password
// mismatch so the caller can answer 401 without leaking which usernames exist.
//
// Before touching bcrypt it consults the throttle: too many recent failures for
// this client (keyed by username and, when known, source IP) yield
// domain.ErrTooManyAttempts so the caller answers 429 — this caps brute-force
// guessing and the CPU cost of bcrypt. A successful login clears the counter.
// The throttle fails open: if Valkey is down the login still proceeds (logged
// at warn) rather than locking everyone out.
func (s *AuthService) Login(ctx context.Context, username, password, clientIP string) (*Tokens, error) {
	key := throttleKey(username, clientIP)
	log := logger.From(ctx)

	if ok, retryAfter, err := s.throttle.Allowed(ctx, key); err != nil {
		log.Warn().Err(err).Msg("login throttle unavailable; allowing attempt")
	} else if !ok {
		return nil, &domain.TooManyAttemptsError{RetryAfter: retryAfter}
	}

	identity, err := s.dir.Authenticate(ctx, username, password)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			if rerr := s.throttle.RecordFailure(ctx, key); rerr != nil {
				log.Warn().Err(rerr).Msg("login throttle: record failure failed")
			}
		}
		return nil, err
	}

	if rerr := s.throttle.Reset(ctx, key); rerr != nil {
		log.Warn().Err(rerr).Msg("login throttle: reset failed")
	}
	return s.issue(ctx, identity, true)
}

// throttleKey identifies a login client for rate-limiting. It combines the
// username with the source IP when available so neither a single account nor a
// single host can be hammered, while a missing IP still keys by username alone.
func throttleKey(username, clientIP string) string {
	if clientIP == "" {
		return "user:" + username
	}
	return "user:" + username + "|ip:" + clientIP
}

// Refresh exchanges a valid refresh token for a new access token. The refresh
// token itself is left in place so it can be reused until it expires; logging
// out is the way to revoke it.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	if refreshToken == "" {
		return nil, domain.ErrSessionNotFound
	}
	identity, err := s.sessions.Load(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	access, exp, err := s.issuer.Issue(identity.UserID, identity.Username, identity.Roles)
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}
	return &Tokens{
		AccessToken:  access,
		RefreshToken: refreshToken,
		ExpiresAt:    exp,
		Identity:     identity,
	}, nil
}

// Logout revokes a refresh token. Revoking an unknown token is not an error.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	if err := s.sessions.Delete(ctx, refreshToken); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Register creates a new account and immediately logs it in, returning a token
// pair so the client need not make a second round trip.
//
// Any roles the client supplied are ignored: every public registration is
// assigned defaultRoles ("user"). Granting privileged roles is a separate,
// authenticated admin operation, so /register cannot be used to self-escalate.
// The password is validated against the length policy first (bcrypt truncates
// past 72 bytes, so an over-long password would not hash what the user typed).
func (s *AuthService) Register(ctx context.Context, username, password string, _ []string) (*Tokens, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	identity, err := s.dir.Register(ctx, username, password, []string{defaultRole})
	if err != nil {
		return nil, err
	}
	return s.issue(ctx, identity, true)
}

// validatePassword enforces the registration length policy, wrapping
// domain.ErrWeakPassword so the HTTP layer answers 400 with the reason.
func validatePassword(password string) error {
	if len(password) < minPasswordLen {
		return fmt.Errorf("%w: must be at least %d characters", domain.ErrWeakPassword, minPasswordLen)
	}
	if len(password) > maxPasswordLen {
		return fmt.Errorf("%w: must be at most %d bytes", domain.ErrWeakPassword, maxPasswordLen)
	}
	return nil
}

// issue mints an access token for identity and, when withRefresh is set, also
// generates and persists a new refresh token.
func (s *AuthService) issue(ctx context.Context, identity *domain.Identity, withRefresh bool) (*Tokens, error) {
	access, exp, err := s.issuer.Issue(identity.UserID, identity.Username, identity.Roles)
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}
	tokens := &Tokens{AccessToken: access, ExpiresAt: exp, Identity: identity}
	if withRefresh {
		tokens.RefreshToken = uuid.NewString()
		if err := s.sessions.Save(ctx, tokens.RefreshToken, identity); err != nil {
			return nil, fmt.Errorf("save session: %w", err)
		}
	}
	return tokens, nil
}
