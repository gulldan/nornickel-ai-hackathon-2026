package httpapi

import (
	"context"
	"net/http"
	"testing"
)

// TestClientIP exercises every source-address branch, including the fallback
// where RemoteAddr carries no port and SplitHostPort fails.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		realIP     string
		forwarded  string
		remoteAddr string
		want       string
	}{
		{name: "x-real-ip wins", realIP: "203.0.113.7", forwarded: "198.51.100.10", remoteAddr: "1.2.3.4:5", want: "203.0.113.7"},
		{name: "first forwarded hop", forwarded: "203.0.113.9, 198.51.100.10", remoteAddr: "1.2.3.4:5", want: "203.0.113.9"},
		{name: "single forwarded value", forwarded: "198.51.100.2", remoteAddr: "1.2.3.4:5", want: "198.51.100.2"},
		{name: "remote addr with port", remoteAddr: "192.0.2.5:4321", want: "192.0.2.5"},
		{name: "remote addr without port", remoteAddr: "192.0.2.9", want: "192.0.2.9"},
		{name: "blank forwarded falls through", forwarded: "   ", remoteAddr: "192.0.2.5:4321", want: "192.0.2.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/login", http.NoBody)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.RemoteAddr = tc.remoteAddr
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := clientIP(req); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
