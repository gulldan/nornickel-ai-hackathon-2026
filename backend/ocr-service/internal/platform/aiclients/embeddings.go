package aiclients

import (
	"context"
	"hash/fnv"
	"math"
	"net/http"
)

// Embedder turns text into dense vectors for similarity search.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
}

// NewEmbedder returns an HTTP-backed embedder when url is set, otherwise a
// deterministic stub. dim should match the production model (e5-large = 1024).
func NewEmbedder(url, model string, dim int, c *http.Client) Embedder {
	if dim <= 0 {
		dim = 1024
	}
	if url == "" {
		return &stubEmbedder{dim: dim}
	}
	return &httpEmbedder{url: url, model: model, dim: dim, httpc: httpClient(c)}
}

// httpEmbedder calls a service exposing POST {url} with body
// {modelField:..,"input":[texts]}. It accepts either {"embeddings":[[...]]}
// (Triton / text-embeddings-inference) or the OpenAI {"data":[{"embedding":..}]}
// shape that llama.cpp (--embeddings), LM Studio and vLLM serve at
// /v1/embeddings — so any of them drops in by pointing EMBEDDINGS_URL at it.
type httpEmbedder struct {
	url   string
	model string
	dim   int
	httpc *http.Client
}

func (e *httpEmbedder) Dim() int { return e.dim }

func (e *httpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	req := map[string]any{modelField: e.model, "input": texts}
	var resp struct {
		Embeddings [][]float32 `json:"embeddings"`
		Data       []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := postJSON(ctx, e.httpc, e.url, req, &resp); err != nil {
		return nil, err
	}
	// OpenAI shape ({"data":[{"embedding":..}]}) — used by llama.cpp/vLLM/LM Studio.
	if len(resp.Embeddings) == 0 && len(resp.Data) > 0 {
		out := make([][]float32, len(resp.Data))
		for i, d := range resp.Data {
			out[i] = d.Embedding
		}
		return out, nil
	}
	return resp.Embeddings, nil
}

// stubEmbedder produces deterministic, L2-normalised vectors from a hash of the
// text. Identical text maps to the identical vector, so cosine search is
// self-consistent — enough to exercise retrieval without a model.
type stubEmbedder struct{ dim int }

func (e *stubEmbedder) Dim() int { return e.dim }

func (e *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = hashVector(t, e.dim)
	}
	return out, nil
}

// hashVector seeds an xorshift64 stream from the FNV hash of text and fills a
// normalised vector. Deterministic and dependency-free.
func hashVector(text string, dim int) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	state := h.Sum64() | 1

	vec := make([]float32, dim)
	var norm float64
	for i := range dim {
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		// High 53 bits -> [0,1), then mapped to [-1,1).
		f := float64(state>>11)/float64(uint64(1)<<53)*2 - 1
		vec[i] = float32(f)
		norm += f * f
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		norm = 1
	}
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}
	return vec
}
