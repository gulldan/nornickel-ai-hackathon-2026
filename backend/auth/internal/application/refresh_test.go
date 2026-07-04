package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/auth/internal/domain"
)

// ---- configurable fakes for refresh/logout/issue branches ----

// scriptedSessions records calls and returns scripted results.
type scriptedSessions struct {
	loadIdentity *domain.Identity
	loadErr      error
	saveErr      error
	deleteErr    error

	deleted bool
}

func (s *scriptedSessions) Save(_ context.Context, _ string, _ *domain.Identity) error {
	return s.saveErr
}

func (s *scriptedSessions) Load(_ context.Context, _ string) (*domain.Identity, error) {
	return s.loadIdentity, s.loadErr
}

func (s *scriptedSessions) Delete(_ context.Context, _ string) error {
	s.deleted = true
	return s.deleteErr
}

// erroringIssuer always fails to mint a token.
type erroringIssuer struct{}

func (erroringIssuer) Issue(_, _ string, _ []string) (string, time.Time, error) {
	return "", time.Time{}, errors.New("signer unavailable")
}

func allowedThrottle() domain.LoginThrottle { return &fakeThrottle{allowed: true} }

// noisyThrottle allows the attempt but fails to record/reset, so the service
// must log at warn and continue rather than fail the login.
type noisyThrottle struct{}

func (noisyThrottle) Allowed(_ context.Context, _ string) (bool, time.Duration, error) {
	return true, 0, nil
}
func (noisyThrottle) RecordFailure(_ context.Context, _ string) error {
	return errors.New("record failed")
}
func (noisyThrottle) Reset(_ context.Context, _ string) error { return errors.New("reset failed") }

// ---- Refresh ----

func TestRefreshEmptyToken(t *testing.T) {
	svc := New(&fakeDir{}, &scriptedSessions{}, fakeIssuer{}, allowedThrottle())
	if _, err := svc.Refresh(context.Background(), ""); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Refresh(empty) = %v, want ErrSessionNotFound", err)
	}
}

func TestRefreshSuccess(t *testing.T) {
	sess := &scriptedSessions{loadIdentity: &domain.Identity{UserID: "u1", Username: "bob", Roles: []string{"user"}}}
	svc := New(&fakeDir{}, sess, fakeIssuer{}, allowedThrottle())

	tokens, err := svc.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.AccessToken != "access-token" {
		t.Fatalf("access token = %q", tokens.AccessToken)
	}
	// The same refresh token is echoed back (it is not rotated).
	if tokens.RefreshToken != "rt-1" {
		t.Fatalf("refresh token = %q, want rt-1", tokens.RefreshToken)
	}
	if tokens.Identity == nil || tokens.Identity.UserID != "u1" {
		t.Fatalf("identity = %+v", tokens.Identity)
	}
}

func TestRefreshSessionNotFound(t *testing.T) {
	sess := &scriptedSessions{loadErr: domain.ErrSessionNotFound}
	svc := New(&fakeDir{}, sess, fakeIssuer{}, allowedThrottle())
	if _, err := svc.Refresh(context.Background(), "gone"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Refresh(unknown) = %v, want ErrSessionNotFound", err)
	}
}

func TestRefreshIssuerError(t *testing.T) {
	sess := &scriptedSessions{loadIdentity: &domain.Identity{UserID: "u1"}}
	svc := New(&fakeDir{}, sess, erroringIssuer{}, allowedThrottle())
	_, err := svc.Refresh(context.Background(), "rt-1")
	if err == nil || errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Refresh with failing issuer = %v, want a wrapped signing error", err)
	}
}

// ---- Logout ----

func TestLogoutEmptyTokenIsNoop(t *testing.T) {
	sess := &scriptedSessions{}
	svc := New(&fakeDir{}, sess, fakeIssuer{}, allowedThrottle())
	if err := svc.Logout(context.Background(), ""); err != nil {
		t.Fatalf("Logout(empty) = %v, want nil", err)
	}
	if sess.deleted {
		t.Fatal("Logout(empty) must not touch the store")
	}
}

func TestLogoutSuccess(t *testing.T) {
	sess := &scriptedSessions{}
	svc := New(&fakeDir{}, sess, fakeIssuer{}, allowedThrottle())
	if err := svc.Logout(context.Background(), "rt-1"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !sess.deleted {
		t.Fatal("Logout must delete the session")
	}
}

func TestLogoutDeleteError(t *testing.T) {
	sess := &scriptedSessions{deleteErr: errors.New("valkey down")}
	svc := New(&fakeDir{}, sess, fakeIssuer{}, allowedThrottle())
	if err := svc.Logout(context.Background(), "rt-1"); err == nil {
		t.Fatal("Logout must surface a delete failure")
	}
}

// ---- issue: error branches via Login ----

func TestLoginIssuerError(t *testing.T) {
	// A signing failure on the issue path must propagate from Login.
	dir := &fakeDir{authIdentity: &domain.Identity{UserID: "u1"}}
	svc := New(dir, &scriptedSessions{}, erroringIssuer{}, allowedThrottle())
	if _, err := svc.Login(context.Background(), "bob", "right", ""); err == nil {
		t.Fatal("expected a signing error from Login")
	}
}

func TestLoginSaveSessionError(t *testing.T) {
	// Failing to persist the refresh token must fail the login.
	dir := &fakeDir{authIdentity: &domain.Identity{UserID: "u1"}}
	sess := &scriptedSessions{saveErr: errors.New("valkey down")}
	svc := New(dir, sess, fakeIssuer{}, allowedThrottle())
	if _, err := svc.Login(context.Background(), "bob", "right", ""); err == nil {
		t.Fatal("expected a save-session error from Login")
	}
}

// TestLoginRecordFailureErrorIsTolerated covers the warn-log branch: a throttle
// that errors while recording a failed attempt must not change the outcome.
func TestLoginRecordFailureErrorIsTolerated(t *testing.T) {
	dir := &fakeDir{authErr: domain.ErrInvalidCredentials}
	svc := New(dir, &scriptedSessions{}, fakeIssuer{}, noisyThrottle{})
	if _, err := svc.Login(context.Background(), "bob", "wrong", ""); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Login = %v, want ErrInvalidCredentials despite a throttle record error", err)
	}
}

// TestLoginResetErrorIsTolerated covers the warn-log branch: a throttle that
// errors while clearing the counter on success must still let the login succeed.
func TestLoginResetErrorIsTolerated(t *testing.T) {
	dir := &fakeDir{authIdentity: &domain.Identity{UserID: "u1", Username: "bob"}}
	svc := New(dir, &scriptedSessions{}, fakeIssuer{}, noisyThrottle{})
	if _, err := svc.Login(context.Background(), "bob", "right", ""); err != nil {
		t.Fatalf("Login = %v, want success despite a throttle reset error", err)
	}
}

// TestRegisterDirectoryError covers Register's generic error branch: a
// non-duplicate directory failure propagates unchanged.
func TestRegisterDirectoryError(t *testing.T) {
	dir := &fakeDir{regErr: errors.New("db unreachable")}
	svc := New(dir, &scriptedSessions{}, fakeIssuer{}, allowedThrottle())
	_, err := svc.Register(context.Background(), "carol", "longenough", nil)
	if err == nil || errors.Is(err, domain.ErrUserExists) {
		t.Fatalf("Register = %v, want the underlying directory error", err)
	}
}
