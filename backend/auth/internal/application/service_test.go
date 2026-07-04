package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/auth/internal/domain"
)

// ---- fakes for the domain ports ----

type fakeDir struct {
	authIdentity *domain.Identity
	authErr      error

	regIdentity *domain.Identity
	regErr      error
	gotRoles    []string // roles passed through to Register
}

func (f *fakeDir) Authenticate(_ context.Context, _, _ string) (*domain.Identity, error) {
	return f.authIdentity, f.authErr
}

func (f *fakeDir) Register(_ context.Context, _, _ string, roles []string) (*domain.Identity, error) {
	f.gotRoles = roles
	return f.regIdentity, f.regErr
}

type fakeSessions struct{ saved bool }

func (f *fakeSessions) Save(_ context.Context, _ string, _ *domain.Identity) error {
	f.saved = true
	return nil
}
func (f *fakeSessions) Load(_ context.Context, _ string) (*domain.Identity, error) {
	return nil, domain.ErrSessionNotFound
}
func (f *fakeSessions) Delete(_ context.Context, _ string) error { return nil }

type fakeIssuer struct{}

func (fakeIssuer) Issue(_, _ string, _ []string) (string, time.Time, error) {
	return "access-token", time.Now().Add(time.Hour), nil
}

type fakeThrottle struct {
	allowed    bool
	retryAfter time.Duration
	allowedErr error

	failures int
	resets   int
}

func (f *fakeThrottle) Allowed(_ context.Context, _ string) (bool, time.Duration, error) {
	return f.allowed, f.retryAfter, f.allowedErr
}
func (f *fakeThrottle) RecordFailure(_ context.Context, _ string) error { f.failures++; return nil }
func (f *fakeThrottle) Reset(_ context.Context, _ string) error         { f.resets++; return nil }

func newService(dir domain.UserDirectory, thr domain.LoginThrottle) (*AuthService, *fakeSessions) {
	sess := &fakeSessions{}
	return New(dir, sess, fakeIssuer{}, thr), sess
}

// ---- Login: rate limiting ----

func TestLoginBlockedWhenThrottled(t *testing.T) {
	dir := &fakeDir{authIdentity: &domain.Identity{UserID: "u1"}}
	thr := &fakeThrottle{allowed: false, retryAfter: 5 * time.Minute}
	svc, _ := newService(dir, thr)

	_, err := svc.Login(context.Background(), "bob", "pw", "1.2.3.4")
	if !errors.Is(err, domain.ErrTooManyAttempts) {
		t.Fatalf("want ErrTooManyAttempts, got %v", err)
	}
	var tma *domain.TooManyAttemptsError
	if !errors.As(err, &tma) || tma.RetryAfter != 5*time.Minute {
		t.Fatalf("want retry-after 5m, got %v", err)
	}
	if dir.gotRoles != nil {
		t.Fatalf("authenticate must not run when throttled")
	}
}

func TestLoginRecordsFailureOnBadCredentials(t *testing.T) {
	dir := &fakeDir{authErr: domain.ErrInvalidCredentials}
	thr := &fakeThrottle{allowed: true}
	svc, _ := newService(dir, thr)

	_, err := svc.Login(context.Background(), "bob", "wrong", "1.2.3.4")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials, got %v", err)
	}
	if thr.failures != 1 {
		t.Fatalf("want 1 recorded failure, got %d", thr.failures)
	}
	if thr.resets != 0 {
		t.Fatalf("must not reset on failure")
	}
}

func TestLoginResetsCounterOnSuccess(t *testing.T) {
	dir := &fakeDir{authIdentity: &domain.Identity{UserID: "u1", Username: "bob"}}
	thr := &fakeThrottle{allowed: true}
	svc, sess := newService(dir, thr)

	tokens, err := svc.Login(context.Background(), "bob", "right", "1.2.3.4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("expected tokens issued, got %+v", tokens)
	}
	if !sess.saved {
		t.Fatalf("expected refresh session saved")
	}
	if thr.resets != 1 {
		t.Fatalf("want 1 reset on success, got %d", thr.resets)
	}
	if thr.failures != 0 {
		t.Fatalf("must not record failure on success")
	}
}

func TestLoginFailsOpenWhenThrottleErrors(t *testing.T) {
	// Throttle backing store down: login must still proceed.
	dir := &fakeDir{authIdentity: &domain.Identity{UserID: "u1"}}
	thr := &fakeThrottle{allowed: false, allowedErr: errors.New("valkey down")}
	svc, _ := newService(dir, thr)

	if _, err := svc.Login(context.Background(), "bob", "right", ""); err != nil {
		t.Fatalf("login should fail open when throttle errors, got %v", err)
	}
}

func TestLoginDoesNotRecordFailureOnInternalError(t *testing.T) {
	// A non-credential error (e.g. db down) must not count against the user.
	dir := &fakeDir{authErr: errors.New("db unreachable")}
	thr := &fakeThrottle{allowed: true}
	svc, _ := newService(dir, thr)

	if _, err := svc.Login(context.Background(), "bob", "pw", ""); err == nil {
		t.Fatal("expected error")
	}
	if thr.failures != 0 {
		t.Fatalf("internal errors must not record a failed attempt, got %d", thr.failures)
	}
}

// ---- Register: role forcing & password policy ----

func TestRegisterForcesDefaultRoles(t *testing.T) {
	dir := &fakeDir{regIdentity: &domain.Identity{UserID: "u1", Username: "bob", Roles: []string{defaultRole}}}
	thr := &fakeThrottle{allowed: true}
	svc, _ := newService(dir, thr)

	// Client tries to self-escalate to admin; it must be ignored.
	if _, err := svc.Register(context.Background(), "bob", "longenough", []string{"admin", "root"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dir.gotRoles) != 1 || dir.gotRoles[0] != defaultRole {
		t.Fatalf("register must force [user], got %v", dir.gotRoles)
	}
}

func TestRegisterRejectsShortPassword(t *testing.T) {
	dir := &fakeDir{}
	svc, _ := newService(dir, &fakeThrottle{allowed: true})

	_, err := svc.Register(context.Background(), "bob", "short", nil)
	if !errors.Is(err, domain.ErrWeakPassword) {
		t.Fatalf("want ErrWeakPassword, got %v", err)
	}
	if dir.gotRoles != nil {
		t.Fatal("directory must not be called for an invalid password")
	}
}

func TestRegisterRejectsOverlongPassword(t *testing.T) {
	dir := &fakeDir{}
	svc, _ := newService(dir, &fakeThrottle{allowed: true})

	long := make([]byte, maxPasswordLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := svc.Register(context.Background(), "bob", string(long), nil)
	if !errors.Is(err, domain.ErrWeakPassword) {
		t.Fatalf("want ErrWeakPassword for >72 bytes, got %v", err)
	}
}

func TestRegisterAcceptsBoundaryPassword(t *testing.T) {
	dir := &fakeDir{regIdentity: &domain.Identity{UserID: "u1", Roles: []string{defaultRole}}}
	svc, _ := newService(dir, &fakeThrottle{allowed: true})

	exact := make([]byte, maxPasswordLen) // exactly 72 bytes is allowed
	for i := range exact {
		exact[i] = 'a'
	}
	if _, err := svc.Register(context.Background(), "bob", string(exact), nil); err != nil {
		t.Fatalf("72-byte password should be accepted, got %v", err)
	}
}

func TestThrottleKey(t *testing.T) {
	if got := throttleKey("bob", ""); got != "user:bob" {
		t.Fatalf("no-IP key = %q", got)
	}
	if got := throttleKey("bob", "1.2.3.4"); got != "user:bob|ip:1.2.3.4" {
		t.Fatalf("with-IP key = %q", got)
	}
}
