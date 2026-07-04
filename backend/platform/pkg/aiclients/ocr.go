package aiclients

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/example/rag-mvp/pkg/jsonx"
)

// OCRResult is one recognition: the full text plus, when the backend reports
// them, the per-page texts in reading order (the source of page provenance for
// downstream chunking). Pages is empty when the backend has no page structure.
type OCRResult struct {
	Text  string
	Pages []string
}

// OCR extracts text from scanned PDFs and images (the OCR engine role).
type OCR interface {
	Recognize(ctx context.Context, image []byte, mime string) (OCRResult, error)
}

// NewOCR returns an HTTP OCR client when url is set, otherwise a stub.
func NewOCR(url, model string, c *http.Client) OCR {
	if url == "" {
		return &stubOCR{}
	}
	return &httpOCR{url: url, model: model, httpc: httpClient(c)}
}

// httpOCR posts {modelField:..,"image_b64":..,"mime":..} expecting
// {"text":..,"pages":[..]} where pages is the per-page texts. Older backends
// send a page COUNT in "pages"; that shape is tolerated and yields no pages.
type httpOCR struct {
	url   string
	model string
	httpc *http.Client
}

func (o *httpOCR) Recognize(ctx context.Context, image []byte, mime string) (OCRResult, error) {
	req := map[string]any{modelField: o.model, "image_b64": base64.StdEncoding.EncodeToString(image), "mime": mime}
	var resp struct {
		Text  string          `json:"text"`
		Pages json.RawMessage `json:"pages"`
	}
	if err := postJSON(ctx, o.httpc, o.url, req, &resp); err != nil {
		return OCRResult{}, err
	}
	out := OCRResult{Text: resp.Text}
	var pages []string
	if len(resp.Pages) > 0 && jsonx.Unmarshal(resp.Pages, &pages) == nil {
		out.Pages = pages
	}
	return out, nil
}

// stubOCR returns a clearly-labelled placeholder so downstream chunking still
// runs end to end without an OCR model.
type stubOCR struct{}

func (o *stubOCR) Recognize(_ context.Context, image []byte, mime string) (OCRResult, error) {
	text := fmt.Sprintf("[stub-ocr] OCR backend is not configured (set OCR_ENGINE_URL to enable). "+
		"Received %d bytes of %s for recognition.", len(image), mime)
	return OCRResult{Text: text}, nil
}
