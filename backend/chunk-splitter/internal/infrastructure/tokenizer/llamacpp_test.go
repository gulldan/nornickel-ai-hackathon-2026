package tokenizer

// Tests for the llama.cpp /tokenize adapter, with a fake HTTP server (httptest)
// standing in for the model — NO live server required. They pin the exact wire
// contract (POST {base}/tokenize with {"content":...}, count = len(tokens)), the
// base-vs-full URL normalisation, the empty-string short-circuit, and the error
// path the indexer relies on for its rune fallback (transport/status/decode all
// return an error rather than a bogus count).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLlamaCPP_CountTokens_ExactWireContract(t *testing.T) {
	var gotPath, gotContent, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(body, &req)
		gotContent = req.Content
		// Echo a token id per word so the count is predictable from the input.
		ids := make([]int, len(strings.Fields(req.Content)))
		for i := range ids {
			ids[i] = 1000 + i
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": ids})
	}))
	defer srv.Close()

	// Construct from a BASE URL; the adapter must append /tokenize.
	tk := New(srv.URL, srv.Client())
	if tk == nil {
		t.Fatal("New returned nil for a non-empty URL")
	}

	n, err := tk.CountTokens(context.Background(), "alpha beta gamma delta")
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if n != 4 {
		t.Fatalf("token count = %d, want 4", n)
	}
	if gotPath != "/tokenize" {
		t.Fatalf("request path = %q, want /tokenize", gotPath)
	}
	if gotContent != "alpha beta gamma delta" {
		t.Fatalf("request content = %q, want the input text", gotContent)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotCT)
	}
}

func TestLlamaCPP_New_URLNormalisation(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tokenize" {
			hits++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": []int{1}})
	}))
	defer srv.Close()

	// A URL already ending in /tokenize must NOT get a doubled suffix; a trailing
	// slash must be tolerated.
	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "/tokenize"} {
		tk := New(base, srv.Client())
		if _, err := tk.CountTokens(context.Background(), "x"); err != nil {
			t.Fatalf("CountTokens for base %q: %v", base, err)
		}
	}
	if hits != 3 {
		t.Fatalf("expected all 3 forms to hit /tokenize, got %d", hits)
	}
}

func TestLlamaCPP_New_EmptyURLIsNil(t *testing.T) {
	if tk := New("", nil); tk != nil {
		t.Fatalf("New(\"\") should be nil (no tokenizer configured), got %v", tk)
	}
	if tk := New("   ", nil); tk != nil {
		t.Fatalf("New(whitespace) should be nil, got %v", tk)
	}
}

func TestLlamaCPP_CountTokens_EmptyStringNoRoundTrip(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer srv.Close()

	tk := New(srv.URL, srv.Client())
	n, err := tk.CountTokens(context.Background(), "")
	if err != nil {
		t.Fatalf("empty string should not error: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty string token count = %d, want 0", n)
	}
	if called {
		t.Fatal("empty string must not make an HTTP round-trip")
	}
}

// TestLlamaCPP_CountTokens_ErrorPath proves the adapter surfaces failures as
// errors (so the indexer's measure closure falls back to rune length) rather than
// returning a misleading zero/positive count.
func TestLlamaCPP_CountTokens_ErrorPath(t *testing.T) {
	t.Run("non-2xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "input too large to process", http.StatusInternalServerError)
		}))
		defer srv.Close()
		tk := New(srv.URL, srv.Client())
		if _, err := tk.CountTokens(context.Background(), "boom"); err == nil {
			t.Fatal("expected an error for a 500 response")
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "not json")
		}))
		defer srv.Close()
		tk := New(srv.URL, srv.Client())
		if _, err := tk.CountTokens(context.Background(), "x"); err == nil {
			t.Fatal("expected a decode error for a non-JSON body")
		}
	})

	t.Run("dead server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening now → transport error
		tk := New(url, &http.Client{})
		if _, err := tk.CountTokens(context.Background(), "x"); err == nil {
			t.Fatal("expected a transport error against a closed server")
		}
	})
}
