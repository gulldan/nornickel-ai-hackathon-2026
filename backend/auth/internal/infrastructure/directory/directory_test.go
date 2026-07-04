package directory

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"sqlstate", "rpc error: code = Internal desc = create user: insert user: ERROR: " +
			"duplicate key value violates unique constraint \"users_username_key\" (SQLSTATE 23505)", true},
		{"duplicate key phrase", "insert user: duplicate key value", true},
		{"unique constraint phrase", "violates UNIQUE constraint", true},
		{"unrelated internal error", "rpc error: code = Internal desc = connection refused", false},
		{"not found", "dbclient: not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUniqueViolation(stringError(tc.msg)); got != tc.want {
				t.Fatalf("isUniqueViolation(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// stringError is a trivial error carrying a fixed message.
type stringError string

func (e stringError) Error() string { return string(e) }

// TestDummyHashIsValid guards the timing-oracle fix: the dummy hash must be a
// real bcrypt hash so CompareHashAndPassword does the full work (a malformed
// hash returns instantly, which would re-introduce the timing leak).
func TestDummyHashIsValid(t *testing.T) {
	dummyHash := mustDummyHash()
	if _, err := bcrypt.Cost(dummyHash); err != nil {
		t.Fatalf("dummy hash is not a valid bcrypt hash: %v", err)
	}
	// A non-matching password must report a mismatch (not a malformed-hash error).
	if err := bcrypt.CompareHashAndPassword(dummyHash, []byte("anything")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("want mismatch against dummy hash, got %v", err)
	}
}
