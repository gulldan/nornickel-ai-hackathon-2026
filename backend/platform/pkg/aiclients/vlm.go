package aiclients

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
)

// VLM produces a textual description/analysis of an image (the Qwen-VL role).
type VLM interface {
	Describe(ctx context.Context, image []byte, mime, prompt string) (string, error)
}

// NewVLM returns an HTTP VLM client when url is set, otherwise a stub.
func NewVLM(url, model string, c *http.Client) VLM {
	if url == "" {
		return &stubVLM{}
	}
	return &httpVLM{url: url, model: model, httpc: httpClient(c)}
}

// httpVLM posts {modelField:..,"image_b64":..,"mime":..,"prompt":..} expecting {"text":..}.
type httpVLM struct {
	url   string
	model string
	httpc *http.Client
}

func (v *httpVLM) Describe(ctx context.Context, image []byte, mime, prompt string) (string, error) {
	req := map[string]any{
		modelField:  v.model,
		"image_b64": base64.StdEncoding.EncodeToString(image),
		"mime":      mime,
		"prompt":    prompt,
	}
	var resp struct {
		Text string `json:"text"`
	}
	if err := postJSON(ctx, v.httpc, v.url, req, &resp); err != nil {
		return "", err
	}
	return resp.Text, nil
}

// stubVLM returns a clearly-labelled placeholder description.
type stubVLM struct{}

func (v *stubVLM) Describe(_ context.Context, image []byte, mime, _ string) (string, error) {
	return fmt.Sprintf("[stub-vlm] VLM backend is not configured (set VLM_ENGINE_URL to enable). "+
		"Received %d bytes of %s for analysis.", len(image), mime), nil
}
