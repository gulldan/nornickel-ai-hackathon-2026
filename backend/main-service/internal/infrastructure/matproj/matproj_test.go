package matproj

import (
	"context"
	"testing"
)

// A keyless client is a safe stub: Summary reports not-known and Known returns
// false, both without error and without any network call.
func TestStubClient_NoKey(t *testing.T) {
	c := New("")
	got, err := c.Summary(context.Background(), "Al2O3")
	if err != nil {
		t.Fatalf("stub Summary must not error: %v", err)
	}
	if got == nil || got.Known {
		t.Fatalf("stub must report not-known, got %+v", got)
	}
	known, err := c.Known(context.Background(), "Al2O3")
	if err != nil {
		t.Fatalf("stub Known must not error: %v", err)
	}
	if known {
		t.Fatal("stub Known must be false")
	}
}

// A blank formula short-circuits to not-known even when a key is configured.
func TestClient_BlankFormula(t *testing.T) {
	c := New("dummy-key")
	got, err := c.Summary(context.Background(), "   ")
	if err != nil {
		t.Fatalf("blank formula must not error: %v", err)
	}
	if got == nil || got.Known {
		t.Fatalf("blank formula must be not-known, got %+v", got)
	}
}
