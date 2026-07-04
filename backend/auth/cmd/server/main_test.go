package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtpkg "github.com/example/auth/internal/platform/jwt"
)

func TestReadyURL(t *testing.T) {
	cases := map[string]string{
		":9090":                 "http://127.0.0.1:9090/readyz",
		"0.0.0.0:9090":          "http://127.0.0.1:9090/readyz",
		"127.0.0.1:9090":        "http://127.0.0.1:9090/readyz",
		"http://localhost:9090": "http://localhost:9090/readyz",
	}
	for in, want := range cases {
		if got := readyURL(in); got != want {
			t.Fatalf("readyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/readyz" {
				t.Fatalf("path = %q, want /readyz", r.URL.Path)
			}
			_, _ = fmt.Fprint(w, "ready")
		}))
		t.Cleanup(ok.Close)
		if err := healthcheck(ok.URL); err != nil {
			t.Fatalf("healthcheck: %v", err)
		}
	})

	t.Run("not ready", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "valkey down", http.StatusServiceUnavailable)
		}))
		t.Cleanup(bad.Close)
		if err := healthcheck(bad.URL); err == nil {
			t.Fatal("expected healthcheck to fail on non-2xx readyz")
		}
	})
}

func TestGateRegistration(t *testing.T) {
	manager := jwtpkg.NewManager("test-secret", "test-issuer", time.Minute)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("default blocks anonymous register", func(t *testing.T) {
		t.Setenv("REGISTRATION_ADMIN_ONLY", "")
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/register", nil)

		gateRegistration(manager, next).ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})

	t.Run("dev mode allows anonymous register", func(t *testing.T) {
		t.Setenv("REGISTRATION_ADMIN_ONLY", "false")
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/register", nil)

		gateRegistration(manager, next).ServeHTTP(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
		}
	})

	t.Run("admin token is allowed", func(t *testing.T) {
		t.Setenv("REGISTRATION_ADMIN_ONLY", "")
		token, _, err := manager.Issue("u-admin", "admin", []string{"admin"})
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/register", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		gateRegistration(manager, next).ServeHTTP(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
		}
	})
}
