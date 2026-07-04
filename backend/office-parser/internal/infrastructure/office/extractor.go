// Package office implements domain.TextExtractor for OOXML office documents.
// It selects a strategy by filename extension (falling back to MIME type):
// .docx and .pptx are ZIP containers of XML parts read with archive/zip +
// encoding/xml, while workbooks prefer the dedicated workbook-parser service
// and fall back to the pure-Go xuri/excelize library.
// When the format is unknown, or a known format yields no text, it optionally
// falls back to an Apache Tika server (PUT {TIKA_URL}/tika) if one is configured.
// Extraction is guarded against panics because the libraries can choke on
// malformed files in the wild.
package office

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/office-parser/internal/domain"
	"github.com/example/office-parser/internal/platform/logger"
	"github.com/xuri/excelize/v2"
)

// Extractor extracts plain text from office documents, with an optional Tika
// fallback for unknown or empty results.
type Extractor struct {
	tikaURL     string
	workbookURL string
	http        *http.Client
}

// NewExtractor builds an Extractor. A non-empty tikaURL enables the Tika
// fallback; the empty string disables it (matching the platform's "empty URL ⇒
// no remote dependency" convention).
func NewExtractor(tikaURL string) *Extractor {
	return NewExtractorWithWorkbookParser(tikaURL, "")
}

// NewExtractorWithWorkbookParser builds an Extractor with an optional workbook
// parser service. A non-empty workbookParserURL routes workbook formats through
// POST /v1/parse before using the legacy text fallback.
func NewExtractorWithWorkbookParser(tikaURL, workbookParserURL string) *Extractor {
	return &Extractor{
		tikaURL:     strings.TrimRight(tikaURL, "/"),
		workbookURL: strings.TrimRight(workbookParserURL, "/"),
		http:        &http.Client{Timeout: 60 * time.Second},
	}
}

// Extract returns the searchable text and parser artifacts for an office
// document. The format is chosen by the filename extension first, then the MIME
// type. An empty result with a nil error means no text could be extracted and no
// Tika fallback was available.
func (e *Extractor) Extract(
	ctx context.Context, data []byte, filename, mimeType string,
) (result domain.ExtractionResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("office parsing panicked: %v", r)
		}
	}()

	var text string
	switch kind := classify(filename, mimeType); kind {
	case kindDocx:
		text, err = extractDocx(data)
	case kindPptx:
		text, err = extractPptx(data)
	case kindXlsx:
		if e.workbookURL != "" {
			wb, werr := e.workbook(ctx, data, filename)
			if werr == nil && strings.TrimSpace(wb.Text) != "" {
				return wb, nil
			}
			if werr != nil {
				logger.From(ctx).Warn().Err(werr).Str("filename", filename).Msg("workbook parser failed, falling back to excelize text")
			}
		}
		text, err = extractXlsx(data)
		result.Metadata = flatWorkbookMetadata("xlsx", "excelize")
	case kindLegacyXls:
		result, err = e.tika(ctx, data, filename)
		if err != nil {
			return domain.ExtractionResult{}, err
		}
		if strings.TrimSpace(result.Text) != "" {
			result.Metadata = mergeMetadata(result.Metadata, degradedWorkbookMetadata("xls", "tika"))
		}
		return result, nil
	case kindText:
		// Already text: Tika is unnecessary (and it fails on hundreds of MB). We
		// rarely reach here — the processor hands plain-text sources off via a
		// claim check without downloading (IsPlainText); this is the fallback for
		// direct calls.
		return domain.ExtractionResult{Text: string(bytes.ToValidUTF8(data, []byte("�")))}, nil
	case kindUnknown:
		// Unknown format: defer entirely to Tika when configured.
		return e.tika(ctx, data, filename)
	}
	if err != nil {
		return domain.ExtractionResult{}, err
	}
	result.Text = text

	// A known format that yielded nothing (e.g. an image-only document) is worth
	// a second pass through Tika when one is configured.
	if strings.TrimSpace(result.Text) == "" && e.tikaURL != "" {
		logger.From(ctx).Info().Str("filename", filename).Msg("no text extracted, falling back to Tika")
		return e.tika(ctx, data, filename)
	}
	return result, nil
}

// IsPlainText reports that the file is already plain text and needs no
// extraction at all (see domain.TextExtractor).
func (e *Extractor) IsPlainText(filename, mimeType string) bool {
	return classify(filename, mimeType) == kindText
}

// docKind enumerates the office formats this extractor handles natively.
type docKind int

const (
	kindUnknown docKind = iota
	kindDocx
	kindPptx
	kindXlsx
	kindLegacyXls
	kindText
)

// classify selects an extraction strategy from the filename extension, falling
// back to the MIME type when the extension is missing or unrecognised.
func classify(filename, mimeType string) docKind {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".docx":
		return kindDocx
	case ".pptx":
		return kindPptx
	// Макро-книги и шаблоны — тот же OOXML-контейнер, excelize читает их так же.
	case ".xlsx", ".xlsm", ".xltx", ".xltm":
		return kindXlsx
	case ".xls":
		return kindLegacyXls
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".log":
		return kindText
	}
	mimeType = strings.ToLower(mimeType)
	switch {
	case strings.Contains(mimeType, "wordprocessingml"):
		return kindDocx
	case strings.Contains(mimeType, "presentationml"):
		return kindPptx
	case strings.Contains(mimeType, "spreadsheetml"),
		strings.Contains(mimeType, "ms-excel.sheet"):
		return kindXlsx
	case mimeType == "application/vnd.ms-excel":
		return kindLegacyXls
	case strings.HasPrefix(mimeType, "text/"):
		return kindText
	}
	return kindUnknown
}

// extractDocx reads word/document.xml from the .docx ZIP and concatenates the
// character data inside every <w:t> run.
func extractDocx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open docx zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, oerr := f.Open()
		if oerr != nil {
			return "", fmt.Errorf("open word/document.xml: %w", oerr)
		}
		text, terr := collectText(rc, "t")
		_ = rc.Close()
		return text, terr
	}
	return "", nil
}

// extractPptx reads every ppt/slides/slideN.xml part in slide order and
// concatenates the character data inside <a:t> runs, separating slides with
// blank lines.
func extractPptx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open pptx zip: %w", err)
	}
	var b strings.Builder
	for _, f := range zr.File {
		name := f.Name
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open %s: %w", name, err)
		}
		slide, err := collectText(rc, "t")
		_ = rc.Close()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		if slide == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(slide)
	}
	return b.String(), nil
}

// collectText walks the XML token stream and joins the character data of every
// element whose local name matches local (e.g. "t" for <w:t>/<a:t>), inserting a
// space between runs so adjacent words do not merge.
func collectText(r io.Reader, local string) (string, error) {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	inRun := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("decode xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == local {
				inRun = true
			}
		case xml.EndElement:
			if t.Name.Local == local {
				inRun = false
			}
		case xml.CharData:
			if inRun {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

// extractXlsx reads every sheet with excelize: ячейки через « | », пустое
// отбрасывается, у многолистовой книги каждый лист получает «Лист: <имя>».
func extractXlsx(data []byte) (string, error) {
	fx, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("open xlsx: %w", err)
	}
	defer func() { _ = fx.Close() }()

	type sheetText struct{ name, text string }
	var sheets []sheetText
	for _, sheet := range fx.GetSheetList() {
		rows, err := fx.GetRows(sheet)
		if err != nil {
			return "", fmt.Errorf("read sheet %q: %w", sheet, err)
		}
		var b strings.Builder
		for _, row := range rows {
			line := rowLine(row)
			if line == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(line)
		}
		if b.Len() > 0 {
			sheets = append(sheets, sheetText{name: sheet, text: b.String()})
		}
	}

	var b strings.Builder
	for i, s := range sheets {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if len(sheets) > 1 {
			b.WriteString("Лист: " + s.name + "\n")
		}
		b.WriteString(s.text)
	}
	return b.String(), nil
}

// rowLine joins a sheet row with « | », dropping trailing empty cells;
// "" means the row is entirely empty and should be skipped.
func rowLine(row []string) string {
	last := -1
	for i, c := range row {
		if strings.TrimSpace(c) != "" {
			last = i
		}
	}
	if last < 0 {
		return ""
	}
	return strings.Join(row[:last+1], " | ")
}

// tika offloads extraction to an Apache Tika server by PUTting the raw bytes to
// {TIKA_URL}/tika with Accept: text/plain. It returns an empty string (no error)
// when no Tika server is configured, so callers can treat that as "no text".
func (e *Extractor) tika(ctx context.Context, data []byte, filename string) (domain.ExtractionResult, error) {
	if e.tikaURL == "" {
		return domain.ExtractionResult{}, nil
	}
	url := e.tikaURL + "/tika"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("build tika request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := e.http.Do(req)
	if err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("call tika: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return domain.ExtractionResult{}, fmt.Errorf("read tika response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return domain.ExtractionResult{}, fmt.Errorf(
			"tika %s: status %d: %s", filename, resp.StatusCode, strings.TrimSpace(string(body)),
		)
	}
	return domain.ExtractionResult{Text: string(body)}, nil
}
