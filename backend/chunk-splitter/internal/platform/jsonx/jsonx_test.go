package jsonx_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/example/chunk-splitter/internal/platform/jsonx"
)

type rec struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	in := rec{Name: "owl", N: 3}
	b, err := jsonx.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out rec
	if uerr := jsonx.Unmarshal(b, &out); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if out != in {
		t.Fatalf("round-trip = %+v, want %+v", out, in)
	}
}

func TestMarshalError(t *testing.T) {
	if _, err := jsonx.Marshal(make(chan int)); err == nil {
		t.Fatal("marshal of a channel should error")
	}
}

func TestUnmarshalError(t *testing.T) {
	var out map[string]any
	if err := jsonx.Unmarshal([]byte("{not json"), &out); err == nil {
		t.Fatal("unmarshal of invalid json should error")
	}
}

func TestEncoderDecoder(t *testing.T) {
	var buf bytes.Buffer
	if err := jsonx.NewEncoder(&buf).Encode(map[string]int{"a": 1}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(buf.String(), `"a"`) {
		t.Fatalf("encoded = %q, want it to contain \"a\"", buf.String())
	}
	var out map[string]int
	if err := jsonx.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["a"] != 1 {
		t.Fatalf("decoded a = %d, want 1", out["a"])
	}
}
