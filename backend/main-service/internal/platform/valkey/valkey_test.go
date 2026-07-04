package valkey_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/example/main-service/internal/platform/valkey"
)

// payload is a small struct exercised by the JSON round-trip tests.
type payload struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// newClient starts an in-process Redis and returns a connected client.
func newClient(t *testing.T) *valkey.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := valkey.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() {
		if cerr := c.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return c
}

// TestNewError reports an error when the address is unreachable.
func TestNewError(t *testing.T) {
	if _, err := valkey.New(context.Background(), "127.0.0.1:1", "", 0); err == nil {
		t.Fatal("expected an error connecting to a dead address")
	}
}

// TestPing verifies connectivity against a live server.
func TestPing(t *testing.T) {
	c := newClient(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// TestSetGet round-trips a raw string value and reports the hit flag.
func TestSetGet(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := c.Set(ctx, "k", "v", time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok || got != "v" {
		t.Fatalf("get = (%q, %v), want (\"v\", true)", got, ok)
	}
}

// TestGetMiss returns ("", false, nil) for an absent key.
func TestGetMiss(t *testing.T) {
	c := newClient(t)
	got, ok, err := c.Get(context.Background(), "absent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok || got != "" {
		t.Fatalf("get miss = (%q, %v), want (\"\", false)", got, ok)
	}
}

// TestSetGetJSON round-trips a struct through SetJSON/GetJSON.
func TestSetGetJSON(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	in := payload{Name: "owl", N: 7}
	if err := c.SetJSON(ctx, "j", in, time.Minute); err != nil {
		t.Fatalf("set json: %v", err)
	}
	var out payload
	ok, err := c.GetJSON(ctx, "j", &out)
	if err != nil {
		t.Fatalf("get json: %v", err)
	}
	if !ok || out != in {
		t.Fatalf("get json = (%+v, %v), want (%+v, true)", out, ok, in)
	}
}

// TestGetJSONMiss returns (false, nil) when the key is absent.
func TestGetJSONMiss(t *testing.T) {
	c := newClient(t)
	var out payload
	ok, err := c.GetJSON(context.Background(), "absent", &out)
	if err != nil {
		t.Fatalf("get json: %v", err)
	}
	if ok {
		t.Fatal("expected a cache miss for an absent key")
	}
}

// TestGetJSONDecodeError surfaces an error when stored bytes are not valid JSON.
func TestGetJSONDecodeError(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := c.Set(ctx, "bad", "{not json", time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	var out payload
	if _, err := c.GetJSON(ctx, "bad", &out); err == nil {
		t.Fatal("expected a decode error for malformed JSON")
	}
}

// TestSetJSONEncodeError surfaces an error for an unencodable value.
func TestSetJSONEncodeError(t *testing.T) {
	c := newClient(t)
	if err := c.SetJSON(context.Background(), "ch", make(chan int), time.Minute); err == nil {
		t.Fatal("expected an encode error for a channel value")
	}
}

// TestSetNX stores only when the key is unset, then refuses an existing key.
func TestSetNX(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	ok, err := c.SetNX(ctx, "nx", "first", time.Minute)
	if err != nil {
		t.Fatalf("setnx: %v", err)
	}
	if !ok {
		t.Fatal("first SetNX should succeed")
	}
	ok, err = c.SetNX(ctx, "nx", "second", time.Minute)
	if err != nil {
		t.Fatalf("setnx existing: %v", err)
	}
	if ok {
		t.Fatal("SetNX on an existing key should return false")
	}
	got, _, err := c.Get(ctx, "nx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "first" {
		t.Fatalf("value = %q, want %q", got, "first")
	}
}

// TestDel removes keys so subsequent reads miss.
func TestDel(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := c.Set(ctx, "d1", "a", time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := c.Del(ctx, "d1", "d2"); err != nil {
		t.Fatalf("del: %v", err)
	}
	_, ok, err := c.Get(ctx, "d1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("deleted key should miss")
	}
}

// TestListOps exercises LPush ordering plus LTrim and LRange.
func TestListOps(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := c.LPush(ctx, "list", "a", "b", "c"); err != nil {
		t.Fatalf("lpush: %v", err)
	}
	got, err := c.LRange(ctx, "list", 0, -1)
	if err != nil {
		t.Fatalf("lrange: %v", err)
	}
	// LPush prepends each value, so the last pushed ("c") ends up first.
	want := []string{"c", "b", "a"}
	if len(got) != len(want) {
		t.Fatalf("lrange = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lrange[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if terr := c.LTrim(ctx, "list", 0, 1); terr != nil {
		t.Fatalf("ltrim: %v", terr)
	}
	got, err = c.LRange(ctx, "list", 0, -1)
	if err != nil {
		t.Fatalf("lrange after trim: %v", err)
	}
	if len(got) != 2 || got[0] != "c" || got[1] != "b" {
		t.Fatalf("trimmed list = %v, want [c b]", got)
	}
}

// TestIncrTTLDecrGetInt covers the integer counter helpers and their TTL refresh.
func TestIncrTTLDecrGetInt(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	n, err := c.IncrTTL(ctx, "ctr", time.Minute)
	if err != nil {
		t.Fatalf("incr: %v", err)
	}
	if n != 1 {
		t.Fatalf("first incr = %d, want 1", n)
	}
	if n, err = c.IncrTTL(ctx, "ctr", 0); err != nil {
		t.Fatalf("incr no ttl: %v", err)
	}
	if n != 2 {
		t.Fatalf("second incr = %d, want 2", n)
	}
	if n, err = c.Decr(ctx, "ctr"); err != nil {
		t.Fatalf("decr: %v", err)
	}
	if n != 1 {
		t.Fatalf("decr = %d, want 1", n)
	}
	got, err := c.GetInt(ctx, "ctr")
	if err != nil {
		t.Fatalf("get int: %v", err)
	}
	if got != 1 {
		t.Fatalf("get int = %d, want 1", got)
	}
}

// TestGetIntMiss returns zero for an absent key.
func TestGetIntMiss(t *testing.T) {
	c := newClient(t)
	got, err := c.GetInt(context.Background(), "absent")
	if err != nil {
		t.Fatalf("get int: %v", err)
	}
	if got != 0 {
		t.Fatalf("get int miss = %d, want 0", got)
	}
}

// TestGetIntParseError surfaces an error when the stored value is not numeric.
func TestGetIntParseError(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := c.Set(ctx, "nan", "not-a-number", time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := c.GetInt(ctx, "nan"); err == nil {
		t.Fatal("expected a parse error for a non-numeric value")
	}
}

// TestOpsAfterClose surfaces errors once the connection pool is closed.
func TestOpsAfterClose(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := valkey.New(context.Background(), mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if cerr := c.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	ctx := context.Background()
	if perr := c.Ping(ctx); perr == nil {
		t.Error("ping after close should fail")
	}
	if serr := c.Set(ctx, "k", "v", time.Minute); serr == nil {
		t.Error("set after close should fail")
	}
	if _, _, gerr := c.Get(ctx, "k"); gerr == nil {
		t.Error("get after close should fail")
	}
	if _, gerr := c.GetJSON(ctx, "k", &payload{}); gerr == nil {
		t.Error("get json after close should fail")
	}
	if _, gerr := c.GetInt(ctx, "k"); gerr == nil {
		t.Error("get int after close should fail")
	}
	if _, nxerr := c.SetNX(ctx, "k", "v", time.Minute); nxerr == nil {
		t.Error("setnx after close should fail")
	}
	if derr := c.Del(ctx, "k"); derr == nil {
		t.Error("del after close should fail")
	}
	if perr := c.LPush(ctx, "l", "a"); perr == nil {
		t.Error("lpush after close should fail")
	}
	if terr := c.LTrim(ctx, "l", 0, 1); terr == nil {
		t.Error("ltrim after close should fail")
	}
	if _, rerr := c.LRange(ctx, "l", 0, -1); rerr == nil {
		t.Error("lrange after close should fail")
	}
	if _, ierr := c.IncrTTL(ctx, "c", time.Minute); ierr == nil {
		t.Error("incr after close should fail")
	}
	if _, derr := c.Decr(ctx, "c"); derr == nil {
		t.Error("decr after close should fail")
	}
}
