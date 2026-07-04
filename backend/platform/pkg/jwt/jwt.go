// Package jwt issues and validates the HS256 access tokens used across the
// platform. auth-service signs them; every other HTTP service validates them
// locally with the shared secret, so no network hop to auth is needed on the
// hot path. The middleware places the verified claims on the request context.
package jwt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

type ctxKey struct{}

// Claims is the JWT payload. Roles drives coarse authorization decisions.
type Claims struct {
	UserID   string   `json:"uid"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	gojwt.RegisteredClaims
}

// Manager signs and verifies tokens. It is safe for concurrent use.
type Manager struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

// NewManager builds a Manager. accessTTL bounds how long an access token lives.
func NewManager(secret, issuer string, accessTTL time.Duration) *Manager {
	return &Manager{secret: []byte(secret), issuer: issuer, accessTTL: accessTTL}
}

// Issue mints a signed access token for the given identity.
func (m *Manager) Issue(userID, username string, roles []string) (string, time.Time, error) {
	exp := nowUTC().Add(m.accessTTL)
	claims := Claims{
		UserID:   userID,
		Username: username,
		Roles:    roles,
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			IssuedAt:  gojwt.NewNumericDate(nowUTC()),
			ExpiresAt: gojwt.NewNumericDate(exp),
		},
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, exp, nil
}

// Parse validates a token string and returns its claims.
func (m *Manager) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := gojwt.ParseWithClaims(token, claims, func(t *gojwt.Token) (any, error) {
		if _, ok := t.Method.(*gojwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	}, gojwt.WithIssuer(m.issuer), gojwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	return claims, nil
}

// Middleware rejects requests without a valid Bearer token and otherwise stores
// the claims on the context for downstream handlers.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := bearer(r)
		if err != nil {
			http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		claims, err := m.Parse(raw)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClaimsFromContext returns the claims placed by Middleware.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}

func bearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("no authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", errors.New("malformed authorization header")
	}
	return parts[1], nil
}

func nowUTC() time.Time { return time.Now().UTC() }
