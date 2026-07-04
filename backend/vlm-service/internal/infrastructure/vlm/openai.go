package vlm

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/example/vlm-service/internal/platform/aiclients"
	"github.com/example/vlm-service/internal/platform/jsonx"
)

// Reasoning выключен явно (reasoning_effort=none): у qwen3.6 скрытое
// рассуждение о схеме съедает весь бюджет и content приходит пустым; с none
// описание идёт с первого токена (проверено на схемах флотации кейса).
const (
	openAIMaxTokens       = 5000
	openAITemperature     = 0.2
	openAIReasoningEffort = "none"
)

// openAIVLM speaks the OpenAI vision chat/completions dialect: the image is an
// image_url content part carrying a base64 data URI. The base url has no /v1 —
// the client appends /v1/chat/completions, mirroring the generation client.
type openAIVLM struct {
	url    string
	model  string
	apiKey string
	httpc  *http.Client
}

func (v *openAIVLM) Describe(ctx context.Context, image []byte, mime, prompt string) (string, error) {
	body := map[string]any{
		"model": v.model,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": prompt},
				{"type": "image_url", "image_url": map[string]string{
					"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image),
				}},
			},
		}},
		"max_tokens":       openAIMaxTokens,
		"temperature":      openAITemperature,
		"reasoning_effort": openAIReasoningEffort,
	}
	raw, err := jsonx.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if v.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+v.apiKey)
	}
	// Opt-out логирования Yandex AI Studio; прочие бэкенды заголовок игнорируют.
	req.Header.Set("X-Data-Logging-Enabled", "false")
	resp, err := v.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("post %s: %w", v.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("post %s: status %d: %s", v.url, resp.StatusCode, string(msg))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if derr := jsonx.NewDecoder(resp.Body).Decode(&out); derr != nil {
		return "", fmt.Errorf("decode response: %w", derr)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("vlm upstream error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("vlm: empty choices")
	}
	// SanitizeAnswer strips <think> blocks the reasoning model may leak.
	text := aiclients.SanitizeAnswer(out.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("vlm: empty answer after sanitize")
	}
	return text, nil
}
