// Package directory adapts db-service (via platform/dbclient) to the
// domain.UserDirectory port. It owns the bcrypt password comparison and the
// account-creation hashing, so neither the application nor the HTTP layer ever
// handles a raw password hash. A missing user and a wrong password both collapse
// to domain.ErrInvalidCredentials to avoid leaking which usernames exist.
package directory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/example/auth/internal/domain"
	"github.com/example/auth/internal/platform/dbclient"

	commonv1 "github.com/example/auth/internal/platform/genproto/common/v1"
)

// mustDummyHash builds a valid bcrypt hash at the default cost from a random
// secret. It is compared against when the looked-up user does not exist so an
// unknown username pays the same bcrypt CPU — and so takes the same time to
// answer — as a known one. Without this, an attacker could distinguish real
// accounts by timing the comparison we would otherwise skip. Generating it
// (rather than hardcoding a literal) guarantees the hash is valid at the running
// cost, so CompareHashAndPassword does the full work instead of returning early
// on a malformed hash.
func mustDummyHash() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte(uuid.NewString()), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt only fails on an out-of-range cost, which DefaultCost is not.
		panic(fmt.Sprintf("directory: generate dummy bcrypt hash: %v", err))
	}
	return h
}

// Directory verifies and creates users through db-service.
type Directory struct {
	db        *dbclient.Client
	timeout   time.Duration
	dummyHash []byte
}

// New builds a Directory over the given db-service client. timeout bounds each
// outbound gRPC call so a slow or wedged db-service cannot stall a login
// indefinitely; a non-positive value disables the deadline.
func New(db *dbclient.Client, timeout time.Duration) *Directory {
	return &Directory{db: db, timeout: timeout, dummyHash: mustDummyHash()}
}

// withTimeout derives a deadline-bounded context for a single db-service call.
func (d *Directory) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if d.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d.timeout)
}

// Authenticate looks the user up by username and compares the supplied password
// against the stored bcrypt hash. It returns domain.ErrInvalidCredentials on an
// unknown user or a mismatch. An unknown user still pays a bcrypt comparison
// against a dummy hash so the response time does not reveal which usernames
// exist (timing oracle).
func (d *Directory) Authenticate(ctx context.Context, username, password string) (*domain.Identity, error) {
	dbCtx, cancel := d.withTimeout(ctx)
	defer cancel()

	user, err := d.db.GetUserByUsername(dbCtx, username)
	if err != nil {
		if errors.Is(err, dbclient.ErrNotFound) {
			// Spend the same CPU as a real comparison to keep timing uniform,
			// then report the credentials as invalid.
			_ = bcrypt.CompareHashAndPassword(d.dummyHash, []byte(password))
			return nil, domain.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.GetPasswordHash()), []byte(password)); err != nil {
		// A hash mismatch (or malformed hash) means the credentials are wrong.
		return nil, domain.ErrInvalidCredentials
	}
	return identityFromUser(user), nil
}

// Register hashes the password with bcrypt and creates the user in db-service,
// returning the new account's Identity. A username collision is reported as
// domain.ErrUserExists so the HTTP layer can answer 409 without leaking the
// underlying database error.
func (d *Directory) Register(ctx context.Context, username, password string, roles []string) (*domain.Identity, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	dbCtx, cancel := d.withTimeout(ctx)
	defer cancel()

	user, err := d.db.CreateUser(dbCtx, username, string(hash), roles)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrUserExists
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return identityFromUser(user), nil
}

// isUniqueViolation reports whether err is db-service's response to a duplicate
// username. db-service flattens the Postgres error to a gRPC Internal status
// whose message carries the original text, so we match on the stable SQLSTATE
// 23505 marker (and the human-readable phrasings as a fallback) rather than the
// gRPC code, which Internal alone would not disambiguate.
func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint")
}

// identityFromUser maps a db-service protobuf User onto the domain Identity,
// dropping the password hash.
func identityFromUser(u *commonv1.User) *domain.Identity {
	return &domain.Identity{
		UserID:   u.GetId(),
		Username: u.GetUsername(),
		Roles:    u.GetRoles(),
	}
}
