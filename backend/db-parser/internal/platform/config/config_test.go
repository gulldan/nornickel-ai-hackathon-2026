package config_test

import (
	"testing"
	"time"

	"github.com/example/db-parser/internal/platform/config"
)

func TestGet(t *testing.T) {
	t.Setenv("X_STR", "value")
	if got := config.Get("X_STR", "def"); got != "value" {
		t.Fatalf("Get set = %q, want value", got)
	}
	t.Setenv("X_EMPTY", "")
	if got := config.Get("X_EMPTY", "def"); got != "def" {
		t.Fatalf("Get empty = %q, want def", got)
	}
	if got := config.Get("X_UNSET", "def"); got != "def" {
		t.Fatalf("Get unset = %q, want def", got)
	}
}

func TestMustGet(t *testing.T) {
	t.Setenv("X_MUST", "ok")
	if got := config.MustGet("X_MUST"); got != "ok" {
		t.Fatalf("MustGet = %q, want ok", got)
	}
}

func TestGetInt(t *testing.T) {
	cases := []struct {
		name, val string
		set       bool
		want      int
	}{
		{"valid", "42", true, 42},
		{"invalid falls back", "abc", true, 7},
		{"unset falls back", "", false, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("X_INT", tc.val)
			}
			if got := config.GetInt("X_INT", 7); got != tc.want {
				t.Fatalf("GetInt = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGetFloat(t *testing.T) {
	t.Setenv("X_F", "1.5")
	if got := config.GetFloat("X_F", 0.1); got != 1.5 {
		t.Fatalf("GetFloat = %g, want 1.5", got)
	}
	t.Setenv("X_F", "nope")
	if got := config.GetFloat("X_F", 0.1); got != 0.1 {
		t.Fatalf("GetFloat invalid = %g, want default 0.1", got)
	}
}

func TestGetBool(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"true", true}, {"1", true}, {"yes", true}, {"on", true},
		{"false", false}, {"0", false}, {"off", false},
		{"garbage", true}, // unrecognised => default (true here)
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("X_BOOL", tc.val)
			if got := config.GetBool("X_BOOL", true); got != tc.want {
				t.Fatalf("GetBool(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
	if !config.GetBool("X_BOOL_UNSET", true) {
		t.Fatal("GetBool unset should return default")
	}
}

func TestGetDuration(t *testing.T) {
	t.Setenv("X_DUR", "750ms")
	if got := config.GetDuration("X_DUR", time.Second); got != 750*time.Millisecond {
		t.Fatalf("GetDuration = %s, want 750ms", got)
	}
	t.Setenv("X_DUR", "bad")
	if got := config.GetDuration("X_DUR", time.Second); got != time.Second {
		t.Fatalf("GetDuration invalid = %s, want default 1s", got)
	}
}

func TestGetSlice(t *testing.T) {
	t.Setenv("X_SLICE", " a , b ,, c ")
	got := config.GetSlice("X_SLICE", "")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("GetSlice = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GetSlice[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if config.GetSlice("X_SLICE_UNSET", "") != nil {
		t.Fatal("GetSlice of empty should be nil")
	}
}
