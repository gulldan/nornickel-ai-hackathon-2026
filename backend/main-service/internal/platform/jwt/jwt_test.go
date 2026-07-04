package jwt_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/example/main-service/internal/platform/jwt"
)

const (
	testSecret = "super-secret-key"
	testIssuer = "auth-service"
)

// TestIssueParseRoundTrip mints a token and reads identical claims back out.
func TestIssueParseRoundTrip(t *testing.T) {
	m := jwt.NewManager(testSecret, testIssuer, time.Hour)
	roles := []string{"admin", "user"}

	tok, exp, err := m.Issue("u1", "alice", roles)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok == "" {
		t.Fatal("Issue returned an empty token")
	}
	if !exp.After(time.Now()) {
		t.Fatalf("expiry %s is not in the future", exp)
	}

	claims, err := m.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != "u1" || claims.Username != "alice" {
		t.Fatalf("claims identity = %q/%q, want u1/alice", claims.UserID, claims.Username)
	}
	if claims.Subject != "u1" || claims.Issuer != testIssuer {
		t.Fatalf("registered claims = sub %q iss %q, want u1/%s", claims.Subject, claims.Issuer, testIssuer)
	}
	if strings.Join(claims.Roles, ",") != "admin,user" {
		t.Fatalf("roles = %v, want [admin user]", claims.Roles)
	}
}

// TestParseExpiredToken rejects a token whose lifetime has already elapsed.
func TestParseExpiredToken(t *testing.T) {
	m := jwt.NewManager(testSecret, testIssuer, -time.Minute)
	tok, _, err := m.Issue("u1", "alice", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, perr := m.Parse(tok); perr == nil {
		t.Fatal("Parse accepted an expired token")
	}
}

// TestParseWrongSecret rejects a token signed by a different key.
func TestParseWrongSecret(t *testing.T) {
	signer := jwt.NewManager(testSecret, testIssuer, time.Hour)
	verifier := jwt.NewManager("a-different-secret", testIssuer, time.Hour)
	tok, _, err := signer.Issue("u1", "alice", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, perr := verifier.Parse(tok); perr == nil {
		t.Fatal("Parse accepted a token signed with the wrong secret")
	}
}

// TestParseWrongIssuer rejects a token minted by an unexpected issuer.
func TestParseWrongIssuer(t *testing.T) {
	signer := jwt.NewManager(testSecret, "other-issuer", time.Hour)
	verifier := jwt.NewManager(testSecret, testIssuer, time.Hour)
	tok, _, err := signer.Issue("u1", "alice", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, perr := verifier.Parse(tok); perr == nil {
		t.Fatal("Parse accepted a token from the wrong issuer")
	}
}

// TestParseGarbage rejects strings that are not valid tokens.
func TestParseGarbage(t *testing.T) {
	m := jwt.NewManager(testSecret, testIssuer, time.Hour)
	for _, bad := range []string{"", "not-a-jwt", "a.b.c", "header.payload"} {
		if _, err := m.Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", bad)
		}
	}
}

// TestParseRejectsNoneAlg rejects an unsigned alg=none token (alg confusion).
func TestParseRejectsNoneAlg(t *testing.T) {
	m := jwt.NewManager(testSecret, testIssuer, time.Hour)
	claims := jwt.Claims{
		UserID: "u1",
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    testIssuer,
			ExpiresAt: gojwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(gojwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}
	if _, perr := m.Parse(signed); perr == nil {
		t.Fatal("Parse accepted an alg=none token")
	}
}

// TestMiddlewareValidToken passes the request through and exposes the claims.
func TestMiddlewareValidToken(t *testing.T) {
	m := jwt.NewManager(testSecret, testIssuer, time.Hour)
	tok, _, err := m.Issue("u42", "bob", []string{"reader"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		claims, ok := jwt.ClaimsFromContext(r.Context())
		if !ok {
			t.Error("ClaimsFromContext found no claims in a guarded handler")
		} else if claims.UserID != "u42" {
			t.Errorf("claims UserID = %q, want u42", claims.UserID)
		}
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	m.Middleware(next).ServeHTTP(rec, req)

	if !reached {
		t.Fatal("middleware did not reach the next handler for a valid token")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestMiddlewareRejections returns 401 for missing, malformed and invalid bearers.
func TestMiddlewareRejections(t *testing.T) {
	m := jwt.NewManager(testSecret, testIssuer, time.Hour)
	cases := []struct {
		name, header string
	}{
		{"no header", ""},
		{"wrong scheme", "Basic abc"},
		{"bearer no token", "Bearer "},
		{"single token field", "abc"},
		{"invalid token", "Bearer not-a-real-jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			m.Middleware(next).ServeHTTP(rec, req)

			if reached {
				t.Fatalf("middleware reached the handler for %q", tc.name)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestClaimsFromContextEmpty reports absence when no middleware ran.
func TestClaimsFromContextEmpty(t *testing.T) {
	if _, ok := jwt.ClaimsFromContext(context.Background()); ok {
		t.Fatal("ClaimsFromContext found claims on a bare context")
	}
}
