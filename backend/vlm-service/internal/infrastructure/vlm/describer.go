// Package vlm implements domain.ImageDescriber on top of the platform VLM
// client (the vision-language model role). With VLM_ENGINE_URL unset the client
// is a deterministic stub, so the worker runs end to end without a model.
package vlm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/example/vlm-service/internal/platform/aiclients"
)

// describePrompt asks for dense retrieval-friendly text; on process diagrams it
// pulls out the equipment, flows and labels so they become searchable.
const describePrompt = "Опиши изображение подробно, плотным текстом для поискового индекса. " +
	"Если это технологическая схема или чертёж: перечисли все блоки и оборудование с марками " +
	"и обозначениями, опиши потоки по стрелкам (что, откуда и куда идёт), приведи все подписи, " +
	"параметры и единицы измерения. Для остальных изображений опиши содержимое, видимый текст " +
	"и ключевые детали."

// StyleOpenAI selects the OpenAI vision chat/completions dialect
// (Yandex AI Studio, vLLM and other compatible gateways); any other value keeps
// the native {model,image_b64,mime,prompt} protocol.
const StyleOpenAI = "openai"

// Describer adapts an aiclients.VLM to domain.ImageDescriber.
type Describer struct {
	client aiclients.VLM
}

// NewDescriber builds a Describer. An empty url yields the platform stub; style
// (VLM_API_STYLE) picks the wire protocol and apiKey (VLM_API_KEY) is sent as a
// Bearer token in the OpenAI mode.
func NewDescriber(url, model, apiKey, style string) *Describer {
	if url != "" && style == StyleOpenAI {
		return &Describer{client: &openAIVLM{
			url:    strings.TrimRight(url, "/"),
			model:  model,
			apiKey: apiKey,
			httpc:  &http.Client{Timeout: 120 * time.Second},
		}}
	}
	return &Describer{client: aiclients.NewVLM(url, model, nil)}
}

// Describe returns a textual description of the image bytes.
func (d *Describer) Describe(ctx context.Context, data []byte, mime string) (string, error) {
	text, err := d.client.Describe(ctx, data, mime, describePrompt)
	if err != nil {
		return "", fmt.Errorf("vlm describe: %w", err)
	}
	return text, nil
}
