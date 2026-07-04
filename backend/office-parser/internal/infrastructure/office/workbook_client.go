package office

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/example/office-parser/internal/domain"
)

type workbookParseResponse struct {
	Text     string                    `json:"text"`
	Metadata map[string]string         `json:"metadata"`
	Sidecars []workbookSidecarResponse `json:"sidecars"`
}

type workbookSidecarResponse struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Text        string `json:"text"`
}

func (e *Extractor) workbook(ctx context.Context, data []byte, filename string) (domain.ExtractionResult, error) {
	if e.workbookURL == "" {
		return domain.ExtractionResult{}, nil
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", workbookFilename(filename))
	if err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("create multipart file: %w", err)
	}
	if _, err = part.Write(data); err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("write multipart file: %w", err)
	}
	if err = writer.Close(); err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("close multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.workbookURL+"/v1/parse", &body)
	if err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("build workbook parser request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("call workbook parser: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.ExtractionResult{}, fmt.Errorf("workbook parser status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed workbookParseResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("decode workbook parser response: %w", err)
	}
	result := domain.ExtractionResult{
		Text:     parsed.Text,
		Metadata: parsed.Metadata,
		Sidecars: make([]domain.SidecarArtifact, 0, len(parsed.Sidecars)),
	}
	for _, sidecar := range parsed.Sidecars {
		result.Sidecars = append(result.Sidecars, domain.SidecarArtifact{
			Name:        sidecar.Name,
			ContentType: sidecar.ContentType,
			Text:        sidecar.Text,
		})
	}
	return result, nil
}

func workbookFilename(filename string) string {
	if strings.TrimSpace(filename) == "" {
		return "workbook.xlsx"
	}
	return filename
}
