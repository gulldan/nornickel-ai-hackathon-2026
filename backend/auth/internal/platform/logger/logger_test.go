package logger_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/example/auth/internal/platform/logger"
)

// TestNew builds a logger that emits the configured service field at info level.
func TestNew(t *testing.T) {
	var buf strings.Builder
	l := logger.New("svc", "info").Output(&buf)
	l.Info().Msg("hello")
	out := buf.String()
	if !strings.Contains(out, `"service":"svc"`) {
		t.Fatalf("log = %q, want it to carry the service field", out)
	}
	if !strings.Contains(out, `"message":"hello"`) {
		t.Fatalf("log = %q, want it to carry the message", out)
	}
}

// TestNewLevelFiltering checks that the level argument silences lower events.
func TestNewLevelFiltering(t *testing.T) {
	cases := []struct {
		level     string
		emitDebug bool
	}{
		{"debug", true},
		{"info", false},
		{"warn", false},
		{"warning", false},
		{"error", false},
		{"", false},         // empty falls back to info.
		{"  DEBUG  ", true}, // trimmed and lower-cased.
		{"nonsense", false}, // unknown falls back to info.
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			var buf strings.Builder
			l := logger.New("svc", tc.level).Output(&buf)
			l.Debug().Msg("dbg")
			got := strings.Contains(buf.String(), "dbg")
			if got != tc.emitDebug {
				t.Fatalf("level %q debug emitted = %v, want %v (log=%q)", tc.level, got, tc.emitDebug, buf.String())
			}
		})
	}
}

// TestIntoFromRoundTrip confirms From returns the exact logger stored by Into.
func TestIntoFromRoundTrip(t *testing.T) {
	var buf strings.Builder
	stored := zerolog.New(&buf).With().Str("marker", "carried").Logger()
	ctx := logger.Into(context.Background(), stored)

	got := logger.From(ctx)
	got.Info().Msg("x")
	if !strings.Contains(buf.String(), `"marker":"carried"`) {
		t.Fatalf("From did not return the stored logger; log=%q", buf.String())
	}
}

// TestFromFallback returns a usable logger when the context carries none.
func TestFromFallback(t *testing.T) {
	l := logger.From(context.Background())
	if l == nil {
		t.Fatal("From returned nil for an empty context")
	}
	// The fallback writes to stderr; redirect it so the assertion is hermetic.
	var buf strings.Builder
	red := l.Output(&buf)
	red.Info().Msg("fallback")
	out := buf.String()
	if !strings.Contains(out, `"service":"unknown"`) {
		t.Fatalf("fallback log = %q, want the unknown service tag", out)
	}
	if !strings.Contains(out, `"message":"fallback"`) {
		t.Fatalf("fallback log = %q, want the emitted message", out)
	}
}

// TestFromFallbackFiltersDebug confirms the fallback logger sits at info level.
func TestFromFallbackFiltersDebug(t *testing.T) {
	var buf strings.Builder
	red := logger.From(context.Background()).Output(&buf)
	red.Debug().Msg("should-not-appear")
	if strings.Contains(buf.String(), "should-not-appear") {
		t.Fatalf("fallback logger emitted a debug event: %q", buf.String())
	}
}

// TestIntoReturnsDerivedContext checks Into yields a context distinct from its parent.
func TestIntoReturnsDerivedContext(t *testing.T) {
	parent := context.Background()
	child := logger.Into(parent, zerolog.Nop())
	if child == parent {
		t.Fatal("Into returned the parent context unchanged")
	}
}
