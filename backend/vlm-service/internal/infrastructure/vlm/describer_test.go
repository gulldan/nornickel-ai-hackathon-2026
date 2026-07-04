package vlm_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/vlm-service/internal/infrastructure/vlm"
)

// TestDescriberOpenAIStyle verifies the OpenAI mode: request shape (vision
// content parts with a data-URI image), Bearer auth, path, and that the answer
// is stripped of <think> reasoning blocks.
func TestDescriberOpenAIStyle(t *testing.T) {
	img := []byte("png-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key-1" {
			t.Errorf("Authorization = %q, want Bearer key-1", got)
		}
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "gpt://folder/qwen3.6-35b-a3b/latest" {
			t.Errorf("model = %q", body.Model)
		}
		if body.MaxTokens <= 0 {
			t.Errorf("max_tokens = %d, want > 0", body.MaxTokens)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
			t.Fatalf("messages shape = %+v, want 1 message with 2 content parts", body.Messages)
		}
		if p := body.Messages[0].Content[0]; p.Type != "text" || p.Text == "" {
			t.Errorf("first part = %+v, want non-empty text", p)
		}
		wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
		if p := body.Messages[0].Content[1]; p.Type != "image_url" || p.ImageURL.URL != wantURL {
			t.Errorf("second part = %+v, want image_url with data URI", p)
		}
		w.Header().Set("Content-Type", "application/json")
		reply := `{"choices":[{"message":{"content":"<think>скрытое рассуждение</think>Схема установки"}}]}`
		if _, err := w.Write([]byte(reply)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	d := vlm.NewDescriber(srv.URL, "gpt://folder/qwen3.6-35b-a3b/latest", "key-1", vlm.StyleOpenAI)
	got, err := d.Describe(context.Background(), img, "image/png")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got != "Схема установки" {
		t.Errorf("text = %q, want %q", got, "Схема установки")
	}
}

// TestDescriberDefaultIsStub keeps the offline stub on an empty URL.
func TestDescriberDefaultIsStub(t *testing.T) {
	d := vlm.NewDescriber("", "", "", "native")
	got, err := d.Describe(context.Background(), []byte("x"), "image/png")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(got, "[stub-vlm]") {
		t.Errorf("text = %q, want stub marker", got)
	}
}
