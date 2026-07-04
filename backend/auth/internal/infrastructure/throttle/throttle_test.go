package throttle

import (
	"context"
	"testing"
	"time"
)

// When limiting is disabled (non-positive config) every method is a no-op and
// Allowed always returns true — without touching Valkey, so a nil client is fine.
func TestDisabledThrottleIsNoop(t *testing.T) {
	configs := []Config{
		{}, // all zero
		{MaxAttempts: 0, Window: time.Minute, Lockout: time.Minute},
		{MaxAttempts: 5, Window: 0, Lockout: time.Minute},
		{MaxAttempts: 5, Window: time.Minute, Lockout: 0},
	}
	for i, cfg := range configs {
		thr := New(nil, cfg) // nil client must never be dereferenced when disabled
		ok, retry, err := thr.Allowed(context.Background(), "user:bob")
		if err != nil || !ok || retry != 0 {
			t.Fatalf("case %d: Allowed = (%v, %v, %v), want (true, 0, nil)", i, ok, retry, err)
		}
		if err := thr.RecordFailure(context.Background(), "user:bob"); err != nil {
			t.Fatalf("case %d: RecordFailure = %v", i, err)
		}
		if err := thr.Reset(context.Background(), "user:bob"); err != nil {
			t.Fatalf("case %d: Reset = %v", i, err)
		}
	}
}

func TestKeyHelpers(t *testing.T) {
	if got := attemptsKey("user:bob"); got != "login_attempts:user:bob" {
		t.Fatalf("attemptsKey = %q", got)
	}
	if got := lockoutKey("user:bob"); got != "login_lockout:user:bob" {
		t.Fatalf("lockoutKey = %q", got)
	}
}
