package directory

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/platform/dbclient"

	commonv1 "github.com/example/auth/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/auth/internal/platform/genproto/db/v1"
)

// fakeDB is a scriptable db-service used to drive Directory over a real gRPC
// connection. Each user-facing method returns the configured response/error.
type fakeDB struct {
	dbv1.UnimplementedDbServiceServer

	getUser    *commonv1.User
	getUserErr error

	createUser    *commonv1.User
	createUserErr error
}

func (f *fakeDB) GetUserByUsername(_ context.Context, _ *dbv1.GetUserByUsernameRequest) (*commonv1.User, error) {
	return f.getUser, f.getUserErr
}

func (f *fakeDB) CreateUser(_ context.Context, _ *dbv1.CreateUserRequest) (*commonv1.User, error) {
	return f.createUser, f.createUserErr
}

// newDirectory starts a loopback gRPC server serving fake and returns a
// Directory wired to it with the given per-call timeout.
func newDirectory(t *testing.T, fake *fakeDB, timeout time.Duration) *Directory {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	dbv1.RegisterDbServiceServer(s, fake)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	client, err := dbclient.New(lis.Addr().String())
	if err != nil {
		t.Fatalf("dbclient.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return New(client, timeout)
}

// hashFor returns a bcrypt hash of password at the minimum cost for speed.
func hashFor(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return string(h)
}

// TestAuthenticateSuccess returns the identity when the stored hash matches.
func TestAuthenticateSuccess(t *testing.T) {
	fake := &fakeDB{getUser: &commonv1.User{
		Id: "u1", Username: "bob", Roles: []string{"user"}, PasswordHash: hashFor(t, "secret"),
	}}
	dir := newDirectory(t, fake, time.Second)

	id, err := dir.Authenticate(context.Background(), "bob", "secret")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.UserID != "u1" || id.Username != "bob" || len(id.Roles) != 1 {
		t.Fatalf("identity = %+v", id)
	}
}

// TestAuthenticateWrongPassword maps a hash mismatch to ErrInvalidCredentials.
func TestAuthenticateWrongPassword(t *testing.T) {
	fake := &fakeDB{getUser: &commonv1.User{Id: "u1", Username: "bob", PasswordHash: hashFor(t, "secret")}}
	dir := newDirectory(t, fake, time.Second)

	if _, err := dir.Authenticate(context.Background(), "bob", "wrong"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Authenticate wrong password = %v, want ErrInvalidCredentials", err)
	}
}

// TestAuthenticateUnknownUser maps a NotFound lookup to ErrInvalidCredentials
// after spending the dummy-hash comparison.
func TestAuthenticateUnknownUser(t *testing.T) {
	fake := &fakeDB{getUserErr: status.Error(codes.NotFound, "no such user")}
	dir := newDirectory(t, fake, time.Second)

	if _, err := dir.Authenticate(context.Background(), "ghost", "secret"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Authenticate unknown user = %v, want ErrInvalidCredentials", err)
	}
}

// TestAuthenticateLookupError surfaces a non-NotFound lookup failure verbatim
// (wrapped), not as ErrInvalidCredentials.
func TestAuthenticateLookupError(t *testing.T) {
	fake := &fakeDB{getUserErr: status.Error(codes.Unavailable, "db down")}
	dir := newDirectory(t, fake, time.Second)

	id, err := dir.Authenticate(context.Background(), "bob", "secret")
	if id != nil {
		t.Fatalf("expected nil identity, got %+v", id)
	}
	if err == nil || errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("lookup error = %v, want a non-credential error", err)
	}
}

// TestRegisterSuccess hashes the password, forwards roles and returns the new
// account's identity.
func TestRegisterSuccess(t *testing.T) {
	fake := &fakeDB{createUser: &commonv1.User{Id: "u2", Username: "carol", Roles: []string{"user"}}}
	dir := newDirectory(t, fake, time.Second)

	id, err := dir.Register(context.Background(), "carol", "longenough", []string{"user"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if id.UserID != "u2" || id.Username != "carol" {
		t.Fatalf("identity = %+v", id)
	}
}

// TestRegisterDuplicate maps a unique-violation message to ErrUserExists.
func TestRegisterDuplicate(t *testing.T) {
	fake := &fakeDB{createUserErr: status.Error(codes.Internal,
		"create user: duplicate key value violates unique constraint (SQLSTATE 23505)")}
	dir := newDirectory(t, fake, time.Second)

	if _, err := dir.Register(context.Background(), "carol", "longenough", nil); !errors.Is(err, domain.ErrUserExists) {
		t.Fatalf("Register duplicate = %v, want ErrUserExists", err)
	}
}

// TestRegisterOtherError surfaces a non-duplicate create failure verbatim.
func TestRegisterOtherError(t *testing.T) {
	fake := &fakeDB{createUserErr: status.Error(codes.Internal, "create user: connection refused")}
	dir := newDirectory(t, fake, time.Second)

	_, err := dir.Register(context.Background(), "carol", "longenough", nil)
	if err == nil || errors.Is(err, domain.ErrUserExists) {
		t.Fatalf("Register other error = %v, want a non-ErrUserExists error", err)
	}
}

// TestWithTimeoutDisabled covers the zero-timeout branch: a non-positive
// timeout leaves the context deadline untouched yet the call still works.
func TestWithTimeoutDisabled(t *testing.T) {
	fake := &fakeDB{getUser: &commonv1.User{Id: "u1", Username: "bob", PasswordHash: hashFor(t, "secret")}}
	dir := newDirectory(t, fake, 0) // disables the per-call deadline

	if _, err := dir.Authenticate(context.Background(), "bob", "secret"); err != nil {
		t.Fatalf("Authenticate with no timeout = %v", err)
	}
}
