package aiclients_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/ocr-service/internal/platform/aiclients"
)

// jsonHandler replies to every request with the given status and raw body.
func jsonHandler(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}
}

// decodeBody reads a request body into a generic map for assertions.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return m
}

// cancelledContext returns a context that is already cancelled.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// --- OCR -------------------------------------------------------------------

// TestNewOCRStubIsOffline verifies the stub OCR returns a labelled placeholder without I/O.
func TestNewOCRStubIsOffline(t *testing.T) {
	o := aiclients.NewOCR("", "any-model", nil)
	got, err := o.Recognize(context.Background(), []byte("abcd"), "image/png")
	if err != nil {
		t.Fatalf("stub Recognize: %v", err)
	}
	if !strings.Contains(got.Text, "[stub-ocr]") {
		t.Errorf("stub text = %q, want it to contain the stub marker", got.Text)
	}
	if !strings.Contains(got.Text, "4 bytes of image/png") {
		t.Errorf("stub text = %q, want byte count and mime echoed", got.Text)
	}
	if got.Pages != nil {
		t.Errorf("stub pages = %v, want none", got.Pages)
	}
}

// TestHTTPOCRParsesText drives the OCR client against a canned text response.
func TestHTTPOCRParsesText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["model"] != "paddleocr-vl" {
			t.Errorf("model field = %v, want paddleocr-vl", body["model"])
		}
		if body["mime"] != "image/jpeg" {
			t.Errorf("mime field = %v, want image/jpeg", body["mime"])
		}
		want := base64.StdEncoding.EncodeToString([]byte("img"))
		if body["image_b64"] != want {
			t.Errorf("image_b64 = %v, want %v", body["image_b64"], want)
		}
		jsonHandler(t, http.StatusOK, `{"text":"hello world","pages":["hello","world"]}`)(w, r)
	}))
	defer srv.Close()

	o := aiclients.NewOCR(srv.URL, "paddleocr-vl", srv.Client())
	got, err := o.Recognize(context.Background(), []byte("img"), "image/jpeg")
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got.Text != "hello world" {
		t.Errorf("text = %q, want %q", got.Text, "hello world")
	}
	if len(got.Pages) != 2 || got.Pages[0] != "hello" || got.Pages[1] != "world" {
		t.Errorf("pages = %v, want [hello world]", got.Pages)
	}
}

// TestHTTPOCRToleratesLegacyPageCount keeps working against older backends that
// report a numeric page count instead of the per-page texts.
func TestHTTPOCRToleratesLegacyPageCount(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"text":"scan","pages":3}`))
	defer srv.Close()

	o := aiclients.NewOCR(srv.URL, "paddleocr-vl", srv.Client())
	got, err := o.Recognize(context.Background(), []byte("x"), "application/pdf")
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got.Text != "scan" || got.Pages != nil {
		t.Errorf("got %+v, want text only", got)
	}
}

// TestHTTPOCRErrorStatus checks a non-2xx response surfaces as an error.
func TestHTTPOCRErrorStatus(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusInternalServerError, `boom`))
	defer srv.Close()

	o := aiclients.NewOCR(srv.URL, "paddleocr-vl", srv.Client())
	if _, err := o.Recognize(context.Background(), []byte("x"), "image/png"); err == nil {
		t.Fatal("expected error on 500 status")
	}
}

// TestHTTPOCRMalformedJSON checks an undecodable body surfaces as an error.
func TestHTTPOCRMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"text":`))
	defer srv.Close()

	o := aiclients.NewOCR(srv.URL, "paddleocr-vl", srv.Client())
	if _, err := o.Recognize(context.Background(), []byte("x"), "image/png"); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

// TestHTTPOCRContextCancelled checks a cancelled context aborts the request.
func TestHTTPOCRContextCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"text":"x"}`))
	defer srv.Close()

	o := aiclients.NewOCR(srv.URL, "paddleocr-vl", srv.Client())
	if _, err := o.Recognize(cancelledContext(), []byte("x"), "image/png"); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// --- VLM -------------------------------------------------------------------

// TestNewVLMStubIsOffline verifies the stub VLM returns a labelled placeholder without I/O.
func TestNewVLMStubIsOffline(t *testing.T) {
	v := aiclients.NewVLM("", "any-model", nil)
	got, err := v.Describe(context.Background(), []byte("xy"), "image/png", "describe")
	if err != nil {
		t.Fatalf("stub Describe: %v", err)
	}
	if !strings.Contains(got, "[stub-vlm]") {
		t.Errorf("stub text = %q, want the stub marker", got)
	}
	if !strings.Contains(got, "2 bytes of image/png") {
		t.Errorf("stub text = %q, want byte count and mime echoed", got)
	}
}

// TestHTTPVLMParsesText drives the VLM client against a canned text response.
func TestHTTPVLMParsesText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["prompt"] != "what is this" {
			t.Errorf("prompt field = %v, want %q", body["prompt"], "what is this")
		}
		if body["model"] != "qwen-vl" {
			t.Errorf("model field = %v, want qwen-vl", body["model"])
		}
		jsonHandler(t, http.StatusOK, `{"text":"a cat"}`)(w, r)
	}))
	defer srv.Close()

	v := aiclients.NewVLM(srv.URL, "qwen-vl", srv.Client())
	got, err := v.Describe(context.Background(), []byte("img"), "image/png", "what is this")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got != "a cat" {
		t.Errorf("text = %q, want %q", got, "a cat")
	}
}

// TestHTTPVLMErrorStatus checks a non-2xx response surfaces as an error.
func TestHTTPVLMErrorStatus(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusBadGateway, `down`))
	defer srv.Close()

	v := aiclients.NewVLM(srv.URL, "qwen-vl", srv.Client())
	if _, err := v.Describe(context.Background(), []byte("x"), "image/png", "p"); err == nil {
		t.Fatal("expected error on 502 status")
	}
}

// TestHTTPVLMContextCancelled checks a cancelled context aborts the request.
func TestHTTPVLMContextCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"text":"x"}`))
	defer srv.Close()

	v := aiclients.NewVLM(srv.URL, "qwen-vl", srv.Client())
	if _, err := v.Describe(cancelledContext(), []byte("x"), "image/png", "p"); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// --- Embedder --------------------------------------------------------------

// TestNewEmbedderStubDeterministic verifies the stub yields stable, normalised vectors offline.
func TestNewEmbedderStubDeterministic(t *testing.T) {
	e := aiclients.NewEmbedder("", "any", 8, nil)
	if e.Dim() != 8 {
		t.Fatalf("Dim() = %d, want 8", e.Dim())
	}
	first, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("got %d vectors, want 2", len(first))
	}
	for _, v := range first {
		if len(v) != 8 {
			t.Fatalf("vector dim = %d, want 8", len(v))
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if sum < 0.99 || sum > 1.01 {
			t.Errorf("vector not L2-normalised: |v|^2 = %f", sum)
		}
	}
	second, err := e.Embed(context.Background(), []string{"alpha"})
	if err != nil {
		t.Fatalf("Embed (repeat): %v", err)
	}
	for i := range first[0] {
		if first[0][i] != second[0][i] {
			t.Fatalf("stub not deterministic at %d: %f vs %f", i, first[0][i], second[0][i])
		}
	}
	if first[0][0] == first[1][0] && first[0][1] == first[1][1] {
		t.Error("distinct texts produced identical vectors")
	}
}

// TestNewEmbedderDefaultDim verifies a non-positive dim falls back to 1024.
func TestNewEmbedderDefaultDim(t *testing.T) {
	if d := aiclients.NewEmbedder("", "any", 0, nil).Dim(); d != 1024 {
		t.Errorf("Dim() with dim=0 = %d, want 1024", d)
	}
	if d := aiclients.NewEmbedder("", "any", -5, nil).Dim(); d != 1024 {
		t.Errorf("Dim() with dim=-5 = %d, want 1024", d)
	}
}

// TestHTTPEmbedderEmbeddingsShape parses the Triton {"embeddings":[..]} shape.
func TestHTTPEmbedderEmbeddingsShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["model"] != "e5" {
			t.Errorf("model field = %v, want e5", body["model"])
		}
		if _, ok := body["input"].([]any); !ok {
			t.Errorf("input field = %v, want an array", body["input"])
		}
		jsonHandler(t, http.StatusOK, `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)(w, r)
	}))
	defer srv.Close()

	e := aiclients.NewEmbedder(srv.URL, "e5", 2, srv.Client())
	out, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 2 || out[0][0] != 0.1 || out[1][1] != 0.4 {
		t.Errorf("embeddings = %v, want [[0.1 0.2] [0.3 0.4]]", out)
	}
}

// TestHTTPEmbedderOpenAIShape parses the OpenAI {"data":[{"embedding":..}]} shape.
func TestHTTPEmbedderOpenAIShape(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"data":[{"embedding":[1.0,2.0]},{"embedding":[3.0,4.0]}]}`))
	defer srv.Close()

	e := aiclients.NewEmbedder(srv.URL, "e5", 2, srv.Client())
	out, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 2 || out[0][0] != 1.0 || out[1][0] != 3.0 {
		t.Errorf("embeddings = %v, want [[1 2] [3 4]]", out)
	}
}

// TestHTTPEmbedderDimAndNilClient covers Dim() on the HTTP embedder and the default-client path.
func TestHTTPEmbedderDimAndNilClient(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"embeddings":[[0.5,0.6,0.7]]}`))
	defer srv.Close()

	// Passing a nil *http.Client exercises httpClient's default-timeout branch.
	e := aiclients.NewEmbedder(srv.URL, "e5", 3, nil)
	if e.Dim() != 3 {
		t.Errorf("Dim() = %d, want 3", e.Dim())
	}
	out, err := e.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 1 || len(out[0]) != 3 {
		t.Errorf("embeddings = %v, want one 3-dim vector", out)
	}
}

// TestHTTPEmbedderErrorStatus checks a non-2xx response surfaces as an error.
func TestHTTPEmbedderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusServiceUnavailable, `nope`))
	defer srv.Close()

	e := aiclients.NewEmbedder(srv.URL, "e5", 2, srv.Client())
	if _, err := e.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("expected error on 503 status")
	}
}

// TestHTTPEmbedderMalformedJSON checks an undecodable body surfaces as an error.
func TestHTTPEmbedderMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"embeddings":`))
	defer srv.Close()

	e := aiclients.NewEmbedder(srv.URL, "e5", 2, srv.Client())
	if _, err := e.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

// TestHTTPEmbedderContextCancelled checks a cancelled context aborts the request.
func TestHTTPEmbedderContextCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"embeddings":[[0.1]]}`))
	defer srv.Close()

	e := aiclients.NewEmbedder(srv.URL, "e5", 1, srv.Client())
	if _, err := e.Embed(cancelledContext(), []string{"a"}); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// --- Reranker --------------------------------------------------------------

// TestNewRerankerStubLexical verifies the stub scores by Jaccard token overlap offline.
func TestNewRerankerStubLexical(t *testing.T) {
	r := aiclients.NewReranker("", "any", nil)
	scores, err := r.Rerank(context.Background(), "the quick fox",
		[]string{"the quick fox", "an unrelated sentence", ""})
	if err != nil {
		t.Fatalf("stub Rerank: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("got %d scores, want 3", len(scores))
	}
	if scores[0] != 1 {
		t.Errorf("identical doc score = %f, want 1", scores[0])
	}
	if scores[1] != 0 {
		t.Errorf("disjoint doc score = %f, want 0", scores[1])
	}
	if scores[2] != 0 {
		t.Errorf("empty doc score = %f, want 0", scores[2])
	}
}

// TestStubRerankerEmptyQuery verifies an empty query yields all-zero scores.
func TestStubRerankerEmptyQuery(t *testing.T) {
	r := aiclients.NewReranker("", "any", nil)
	scores, err := r.Rerank(context.Background(), "", []string{"anything"})
	if err != nil {
		t.Fatalf("stub Rerank: %v", err)
	}
	if len(scores) != 1 || scores[0] != 0 {
		t.Errorf("scores = %v, want [0]", scores)
	}
}

// TestHTTPRerankerScoresShape parses the Triton {"scores":[..]} shape.
func TestHTTPRerankerScoresShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["query"] != "q" {
			t.Errorf("query field = %v, want q", body["query"])
		}
		if body["model"] != "qwen-rr" {
			t.Errorf("model field = %v, want qwen-rr", body["model"])
		}
		jsonHandler(t, http.StatusOK, `{"scores":[0.9,0.1]}`)(w, r)
	}))
	defer srv.Close()

	r := aiclients.NewReranker(srv.URL, "qwen-rr", srv.Client())
	scores, err := r.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.1 {
		t.Errorf("scores = %v, want [0.9 0.1]", scores)
	}
}

// TestHTTPRerankerResultsShape parses the Jina/Cohere results shape and reorders to doc order.
func TestHTTPRerankerResultsShape(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"results":[{"index":2,"relevance_score":0.7},{"index":0,"relevance_score":0.3}]}`))
	defer srv.Close()

	r := aiclients.NewReranker(srv.URL, "qwen-rr", srv.Client())
	scores, err := r.Rerank(context.Background(), "q", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	want := []float32{0.3, 0, 0.7}
	if len(scores) != 3 {
		t.Fatalf("got %d scores, want 3", len(scores))
	}
	for i := range want {
		if scores[i] != want[i] {
			t.Errorf("scores[%d] = %f, want %f", i, scores[i], want[i])
		}
	}
}

// TestHTTPRerankerResultsOutOfRange ignores result indices outside the docs range.
func TestHTTPRerankerResultsOutOfRange(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"results":[{"index":-1,"relevance_score":0.5},{"index":9,"relevance_score":0.6},{"index":1,"relevance_score":0.4}]}`))
	defer srv.Close()

	r := aiclients.NewReranker(srv.URL, "qwen-rr", srv.Client())
	scores, err := r.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 || scores[0] != 0 || scores[1] != 0.4 {
		t.Errorf("scores = %v, want [0 0.4]", scores)
	}
}

// TestHTTPRerankerErrorStatus checks a non-2xx response surfaces as an error.
func TestHTTPRerankerErrorStatus(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusInternalServerError, `err`))
	defer srv.Close()

	r := aiclients.NewReranker(srv.URL, "qwen-rr", srv.Client())
	if _, err := r.Rerank(context.Background(), "q", []string{"a"}); err == nil {
		t.Fatal("expected error on 500 status")
	}
}

// TestHTTPRerankerMalformedJSON checks an undecodable body surfaces as an error.
func TestHTTPRerankerMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"scores":`))
	defer srv.Close()

	r := aiclients.NewReranker(srv.URL, "qwen-rr", srv.Client())
	if _, err := r.Rerank(context.Background(), "q", []string{"a"}); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

// TestHTTPRerankerContextCancelled checks a cancelled context aborts the request.
func TestHTTPRerankerContextCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"scores":[0.1]}`))
	defer srv.Close()

	r := aiclients.NewReranker(srv.URL, "qwen-rr", srv.Client())
	if _, err := r.Rerank(cancelledContext(), "q", []string{"a"}); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// --- Generator: stub -------------------------------------------------------

// TestNewGeneratorStubWithContext verifies the stub extracts from the supplied context.
func TestNewGeneratorStubWithContext(t *testing.T) {
	g := aiclients.NewGenerator("", "", "", 0, nil)
	resp, err := g.Generate(context.Background(), aiclients.GenRequest{
		Query:   "what?",
		Context: "the relevant passage of evidence",
	})
	if err != nil {
		t.Fatalf("stub Generate: %v", err)
	}
	if resp.Model != "stub-llm" {
		t.Errorf("model = %q, want stub-llm", resp.Model)
	}
	if !strings.Contains(resp.Text, "[stub-llm]") {
		t.Errorf("text = %q, want the stub marker", resp.Text)
	}
	if !strings.Contains(resp.Text, "the relevant passage of evidence") {
		t.Errorf("text = %q, want the context echoed", resp.Text)
	}
}

// TestStubGeneratorNoContext verifies the no-context branch names the query.
func TestStubGeneratorNoContext(t *testing.T) {
	g := aiclients.NewGenerator("", "", "", 0, nil)
	resp, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "lonely question"})
	if err != nil {
		t.Fatalf("stub Generate: %v", err)
	}
	if !strings.Contains(resp.Text, "No relevant context") {
		t.Errorf("text = %q, want the no-context branch", resp.Text)
	}
	if !strings.Contains(resp.Text, "lonely question") {
		t.Errorf("text = %q, want the query echoed", resp.Text)
	}
}

// TestStubGeneratorTruncatesLongContext verifies oversized context is truncated on a rune boundary.
func TestStubGeneratorTruncatesLongContext(t *testing.T) {
	g := aiclients.NewGenerator("", "", "", 0, nil)
	// Multibyte runes past the 800-byte cap must not be split mid-character.
	long := strings.Repeat("я", 1000)
	resp, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q", Context: long})
	if err != nil {
		t.Fatalf("stub Generate: %v", err)
	}
	if !strings.HasSuffix(resp.Text, "…") {
		t.Errorf("text = %q, want an ellipsis suffix on truncation", resp.Text)
	}
	if !utf8ValidTail(resp.Text) {
		t.Error("truncated text is not valid UTF-8")
	}
}

// utf8ValidTail reports whether the string is valid UTF-8.
func utf8ValidTail(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

// TestStubGeneratorStream verifies the stub streams whitespace-split tokens and assembles the answer.
func TestStubGeneratorStream(t *testing.T) {
	g := aiclients.NewGenerator("", "", "", 0, nil)
	var got strings.Builder
	resp, err := g.Stream(context.Background(),
		aiclients.GenRequest{Query: "q", Context: "one two three"},
		func(tok string) error {
			got.WriteString(tok)
			return nil
		})
	if err != nil {
		t.Fatalf("stub Stream: %v", err)
	}
	if got.String() != resp.Text {
		t.Errorf("streamed %q, final %q — want them equal", got.String(), resp.Text)
	}
	if resp.Model != "stub-llm" {
		t.Errorf("model = %q, want stub-llm", resp.Model)
	}
}

// TestStubGeneratorStreamOnTokenError verifies an onToken error aborts the stub stream.
func TestStubGeneratorStreamOnTokenError(t *testing.T) {
	g := aiclients.NewGenerator("", "", "", 0, nil)
	sentinel := errors.New("stop now")
	_, err := g.Stream(context.Background(),
		aiclients.GenRequest{Query: "q", Context: "alpha beta gamma"},
		func(string) error { return sentinel })
	if err == nil {
		t.Fatal("expected the onToken error to propagate")
	}
}

// TestStubGeneratorStreamContextCancelled verifies a cancelled context aborts the stub stream.
func TestStubGeneratorStreamContextCancelled(t *testing.T) {
	g := aiclients.NewGenerator("", "", "", 0, nil)
	_, err := g.Stream(cancelledContext(),
		aiclients.GenRequest{Query: "q", Context: "alpha beta gamma"},
		func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// --- Generator: HTTP Generate ---------------------------------------------

// TestHTTPGeneratorGenerate parses a chat-completions answer and asserts the request shape.
func TestHTTPGeneratorGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-key" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		body := decodeBody(t, r)
		if body["model"] != "qwen" {
			t.Errorf("model field = %v, want qwen", body["model"])
		}
		if body["stream"] != false {
			t.Errorf("stream field = %v, want false", body["stream"])
		}
		if mt, ok := body["max_tokens"].(float64); !ok || mt != 256 {
			t.Errorf("max_tokens = %v, want 256", body["max_tokens"])
		}
		jsonHandler(t, http.StatusOK,
			`{"model":"qwen-served","choices":[{"message":{"content":"the answer"}}]}`)(w, r)
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL+"/", "qwen", "secret-key", 0, srv.Client())
	resp, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q", MaxTokens: 256})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "the answer" {
		t.Errorf("text = %q, want %q", resp.Text, "the answer")
	}
	if resp.Model != "qwen-served" {
		t.Errorf("model = %q, want qwen-served", resp.Model)
	}
}

// TestHTTPGeneratorNoAPIKey verifies a keyless generator omits the Authorization header.
func TestHTTPGeneratorNoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want it absent for a keyless generator", got)
		}
		jsonHandler(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)(w, r)
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	if _, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

// TestHTTPGeneratorSystemAndCite verifies a custom system prompt plus citation instruction reach the wire.
func TestHTTPGeneratorSystemAndCite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 2 {
			t.Fatalf("messages = %v, want two messages", body["messages"])
		}
		sys, _ := msgs[0].(map[string]any)
		if sys["content"] != "custom-system The context passages are labelled [S1], [S2], and so on. "+
			"Cite the passages you rely on inline using these labels, e.g. [S1] or [S2][S3]." {
			t.Errorf("system content = %v, want custom prompt plus cite instruction", sys["content"])
		}
		usr, _ := msgs[1].(map[string]any)
		content, _ := usr["content"].(string)
		if !strings.Contains(content, "Context:\nthe ctx\n\nQuestion: q") {
			t.Errorf("user content = %v, want context-wrapped query", usr["content"])
		}
		jsonHandler(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)(w, r)
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	_, err := g.Generate(context.Background(), aiclients.GenRequest{
		System: "custom-system", Query: "q", Context: "the ctx", Cite: true,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

// TestHTTPGeneratorDefaultSystemPrompt verifies an unset system prompt uses the safe default.
func TestHTTPGeneratorDefaultSystemPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		msgs, _ := body["messages"].([]any)
		sys, _ := msgs[0].(map[string]any)
		content, _ := sys["content"].(string)
		if !strings.Contains(content, "helpful assistant") {
			t.Errorf("system content = %q, want the default prompt", content)
		}
		if strings.Contains(content, "[S1]") {
			t.Errorf("system content = %q, must not cite without Cite set", content)
		}
		jsonHandler(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)(w, r)
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	if _, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

// TestHTTPGeneratorUpstreamError surfaces an inline upstream error after exhausting retries.
func TestHTTPGeneratorUpstreamError(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		jsonHandler(t, http.StatusOK, `{"error":{"message":"rate limited"}}`)(w, r)
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	_, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want an upstream error mentioning rate limited", err)
	}
	if hits != 3 {
		t.Errorf("upstream hit %d times, want 3 retry attempts", hits)
	}
}

// TestHTTPGeneratorEmptyChoices surfaces an empty-choices error after retries.
func TestHTTPGeneratorEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK, `{"choices":[]}`))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	_, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"})
	if err == nil || !strings.Contains(err.Error(), "empty choices") {
		t.Fatalf("err = %v, want an empty-choices error", err)
	}
}

// TestHTTPGeneratorBlankContentThenAnswer recovers when a blank answer is followed by a real one.
func TestHTTPGeneratorBlankContentThenAnswer(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			jsonHandler(t, http.StatusOK, `{"choices":[{"message":{"content":"   "}}]}`)(w, r)
			return
		}
		jsonHandler(t, http.StatusOK, `{"choices":[{"message":{"content":"recovered"}}]}`)(w, r)
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	resp, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "recovered" {
		t.Errorf("text = %q, want recovered after a blank first attempt", resp.Text)
	}
	if hits != 2 {
		t.Errorf("upstream hit %d times, want 2", hits)
	}
}

// TestHTTPGeneratorErrorStatus surfaces a non-2xx status after retries.
func TestHTTPGeneratorErrorStatus(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusInternalServerError, `boom`))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	if _, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"}); err == nil {
		t.Fatal("expected error on 500 status")
	}
}

// TestHTTPGeneratorContextCancelled checks a cancelled context aborts Generate.
func TestHTTPGeneratorContextCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"choices":[{"message":{"content":"x"}}]}`))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	if _, err := g.Generate(cancelledContext(), aiclients.GenRequest{Query: "q"}); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// TestHTTPGeneratorRetryCancelled verifies cancellation between retries returns promptly.
func TestHTTPGeneratorRetryCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusBadGateway, `down`))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	start := time.Now()
	if _, err := g.Generate(ctx, aiclients.GenRequest{Query: "q"}); err == nil {
		t.Fatal("expected error when the context expires during backoff")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Generate took %v, want a prompt cancellation under the retry backoff", elapsed)
	}
}

// --- Generator: HTTP Stream ------------------------------------------------

// TestHTTPGeneratorStream consumes an SSE token stream and assembles the answer.
func TestHTTPGeneratorStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["stream"] != true {
			t.Errorf("stream field = %v, want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Mix in a blank delta, a non-data line and a trailing [DONE] sentinel.
		fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n"+
				": comment line\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n"+
				"data: [DONE]\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"ignored\"}}]}\n")
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	var tokens []string
	resp, err := g.Stream(context.Background(), aiclients.GenRequest{Query: "q"},
		func(tok string) error {
			tokens = append(tokens, tok)
			return nil
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Text != "Hello" {
		t.Errorf("assembled text = %q, want Hello", resp.Text)
	}
	if resp.Model != "qwen" {
		t.Errorf("model = %q, want qwen", resp.Model)
	}
	if len(tokens) != 2 || tokens[0] != "Hel" || tokens[1] != "lo" {
		t.Errorf("tokens = %v, want [Hel lo]", tokens)
	}
}

// TestHTTPGeneratorStreamSkipsMalformedChunks tolerates undecodable data lines mid-stream.
func TestHTTPGeneratorStreamSkipsMalformedChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w,
			"data: {not json}\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"good\"}}]}\n"+
				"data: [DONE]\n")
	}))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	resp, err := g.Stream(context.Background(), aiclients.GenRequest{Query: "q"},
		func(string) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Text != "good" {
		t.Errorf("text = %q, want good (malformed chunk skipped)", resp.Text)
	}
}

// TestHTTPGeneratorStreamOnTokenError verifies an onToken error aborts the HTTP stream.
func TestHTTPGeneratorStreamOnTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n"+
				"data: [DONE]\n")
	}))
	defer srv.Close()

	sentinel := errors.New("abort")
	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	_, err := g.Stream(context.Background(), aiclients.GenRequest{Query: "q"},
		func(string) error { return sentinel })
	if err == nil {
		t.Fatal("expected the onToken error to propagate")
	}
}

// TestHTTPGeneratorStreamErrorStatus checks a non-2xx status aborts the stream.
func TestHTTPGeneratorStreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusForbidden, `denied`))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	_, err := g.Stream(context.Background(), aiclients.GenRequest{Query: "q"},
		func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err = %v, want a status 403 error", err)
	}
}

// TestHTTPGeneratorStreamContextCancelled checks a cancelled context aborts the stream.
func TestHTTPGeneratorStreamContextCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`data: {"choices":[{"delta":{"content":"x"}}]}`+"\n"))
	defer srv.Close()

	g := aiclients.NewGenerator(srv.URL, "qwen", "", 0, srv.Client())
	_, err := g.Stream(cancelledContext(), aiclients.GenRequest{Query: "q"},
		func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// --- Generator: throttle ---------------------------------------------------

// TestHTTPGeneratorThrottle verifies rpm spacing delays the second request.
func TestHTTPGeneratorThrottle(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"choices":[{"message":{"content":"x"}}]}`))
	defer srv.Close()

	// 600 rpm => a 100ms minimum gap between calls on the same generator.
	g := aiclients.NewGenerator(srv.URL, "qwen", "", 600, srv.Client())
	if _, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	start := time.Now()
	if _, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"}); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("second call waited %v, want at least the throttle gap", elapsed)
	}
}

// TestHTTPGeneratorThrottleCancelled verifies a cancelled context interrupts the throttle wait.
func TestHTTPGeneratorThrottleCancelled(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(t, http.StatusOK,
		`{"choices":[{"message":{"content":"x"}}]}`))
	defer srv.Close()

	// 6 rpm => a 10s gap; the first call primes "last", the second must abort fast.
	g := aiclients.NewGenerator(srv.URL, "qwen", "", 6, srv.Client())
	if _, err := g.Generate(context.Background(), aiclients.GenRequest{Query: "q"}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := g.Generate(ctx, aiclients.GenRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected the throttle wait to be cancelled")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("throttle cancellation took %v, want a prompt return", elapsed)
	}
}

// TestSanitizeAnswer вырезает think-блоки reasoning-моделей и потоки <unk>;
// пустой результат заставляет генератор ретраить попытку.
func TestSanitizeAnswer(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "Ответ готов.", "Ответ готов."},
		{"closed think", "<think>reasoning here</think>\nОтвет.", "Ответ."},
		{"orphan close", "We need to answer the question…</think>Ответ.", "Ответ."},
		{"unclosed think", "<think>only reasoning, no answer", ""},
		{"unk flood", "<unk><unk><unk><unk>", ""},
		{"unk inline", "было<unk> слово", "было слово"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aiclients.SanitizeAnswer(tc.in); got != tc.want {
				t.Fatalf("SanitizeAnswer(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
