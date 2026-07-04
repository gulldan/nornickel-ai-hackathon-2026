// Package email implements domain.TextExtractor for email files.
//
// Two formats are handled, distinguished by the raw bytes:
//
//   - RFC 822 .eml messages are parsed with the standard library
//     (net/mail + mime + mime/multipart): the Subject/From/To/Date headers are
//     captured, every text/plain body part is decoded (quoted-printable or
//     base64 per Content-Transfer-Encoding) and concatenated, and attachment
//     filenames are listed.
//   - Outlook .msg messages are OLE compound binaries that the standard library
//     cannot parse. They are delegated to Apache Tika when TIKA_URL is set;
//     otherwise extraction fails so the document is marked failed instead of
//     indexing a placeholder body.
//
// No third-party dependencies are introduced: only the standard library and an
// HTTP call to Tika.
package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/example/email-parser/internal/domain"
)

// oleSignature is the magic header of OLE2 compound files, which is the
// container format used by Outlook .msg messages. .eml messages are plain text
// and never start with these bytes, so it is a reliable discriminator.
const oleSignature = "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1"

// Extractor turns email bytes into plain text. It holds the optional Tika
// endpoint used for .msg files and an HTTP client for that call.
type Extractor struct {
	tikaURL string
	client  *http.Client
}

// NewExtractor builds an Extractor. tikaURL may be empty, in which case .msg
// files yield a placeholder instead of being parsed.
func NewExtractor(tikaURL string) *Extractor {
	return &Extractor{
		tikaURL: strings.TrimRight(tikaURL, "/"),
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Extract returns the plain-text representation of an email. The .msg branch is
// chosen by the OLE magic header; everything else is treated as RFC 822 .eml.
func (e *Extractor) Extract(ctx context.Context, data []byte) (string, error) {
	doc, err := e.ExtractWithMeta(ctx, data)
	return doc.Text, err
}

// ExtractWithMeta implements domain.MetaExtractor: for .eml it also captures the
// From/Date headers as author/publication metadata; the Tika .msg path yields
// text only.
func (e *Extractor) ExtractWithMeta(ctx context.Context, data []byte) (domain.ExtractedEmail, error) {
	if bytes.HasPrefix(data, []byte(oleSignature)) {
		text, err := e.extractMSG(ctx, data)
		return domain.ExtractedEmail{Text: text}, err
	}
	return e.extractEML(data)
}

// extractEML parses an RFC 822 message: headers, decoded text/plain body and a
// list of attachment filenames, plus the From/Date metadata.
func (e *Extractor) extractEML(data []byte) (domain.ExtractedEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return domain.ExtractedEmail{}, fmt.Errorf("read message: %w", err)
	}

	// Header block: Subject/From/To/Date, decoding any RFC 2047 encoded-words.
	dec := new(mime.WordDecoder)
	var header strings.Builder
	var author string
	for _, key := range []string{"Subject", "From", "To", "Date"} {
		raw := msg.Header.Get(key)
		if raw == "" {
			continue
		}
		value := raw
		if decoded, derr := dec.DecodeHeader(raw); derr == nil {
			value = decoded
		}
		if key == "From" {
			author = value
		}
		fmt.Fprintf(&header, "%s: %s\n", key, value)
	}

	body, attachments, err := e.readBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	if err != nil {
		return domain.ExtractedEmail{}, fmt.Errorf("read body: %w", err)
	}

	// Compose: headers + blank line + body + blank line + attachment list.
	var out strings.Builder
	out.WriteString(strings.TrimRight(header.String(), "\n"))
	out.WriteString("\n\n")
	out.WriteString(strings.TrimSpace(body))
	if len(attachments) > 0 {
		out.WriteString("\n\nAttachments: ")
		out.WriteString(strings.Join(attachments, ", "))
	}
	return domain.ExtractedEmail{
		Text:        strings.TrimSpace(out.String()),
		Author:      author,
		PublishedAt: publishedAt(msg.Header),
	}, nil
}

// publishedAt renders the Date header as RFC3339 when it parses, the raw header
// value otherwise, and "" when absent.
func publishedAt(h mail.Header) string {
	if d, err := h.Date(); err == nil {
		return d.Format(time.RFC3339)
	}
	return h.Get("Date")
}

// readBody dispatches on the message Content-Type: multipart messages are
// walked part by part, anything else is treated as a single body decoded with
// the top-level Content-Transfer-Encoding.
func (e *Extractor) readBody(contentType, encoding string, body io.Reader) (text string, attachments []string, err error) {
	mediaType, params, perr := mime.ParseMediaType(contentType)
	if perr != nil {
		// No (or malformed) Content-Type: treat the whole thing as plain text,
		// still honouring any declared transfer encoding.
		decoded, derr := decodePart(body, encoding)
		if derr != nil {
			return "", nil, fmt.Errorf("read plain body: %w", derr)
		}
		return string(decoded), nil, nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return "", nil, errors.New("multipart message without boundary")
		}
		return e.readMultipart(body, boundary)
	}

	// Single-part message (text/plain, text/html, ...): decode and keep the body
	// so the document is never empty, rather than dropping non-text parts.
	decoded, derr := decodePart(body, encoding)
	if derr != nil {
		return "", nil, fmt.Errorf("read body: %w", derr)
	}
	return string(decoded), nil, nil
}

// readMultipart walks the parts of a multipart message, collecting decoded
// text/plain bodies and the filenames of any attachments. Nested multipart
// parts (e.g. multipart/alternative inside multipart/mixed) are recursed into.
func (e *Extractor) readMultipart(body io.Reader, boundary string) (string, []string, error) {
	mr := multipart.NewReader(body, boundary)
	var texts []string
	var attachments []string

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("next part: %w", err)
		}
		if herr := e.handlePart(part, &texts, &attachments); herr != nil {
			return "", nil, herr
		}
	}

	return strings.Join(texts, "\n"), attachments, nil
}

// handlePart processes one MIME part of a multipart message, accumulating into
// texts and attachments: attachment filenames are noted, nested multipart
// bodies are recursed into, and text/plain bodies are decoded and collected.
func (e *Extractor) handlePart(part *multipart.Part, texts, attachments *[]string) error {
	partType, partParams, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
	disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

	// Note attachment filenames; do not inline their (often binary) bodies.
	if disposition == "attachment" {
		if name := attachmentName(dispParams, partParams); name != "" {
			*attachments = append(*attachments, name)
		}
		_ = part.Close()
		return nil
	}

	// Recurse into nested multipart bodies.
	if strings.HasPrefix(partType, "multipart/") {
		nestedText, nestedAtt, nerr := e.readMultipart(part, partParams["boundary"])
		if nerr != nil {
			return nerr
		}
		if nestedText != "" {
			*texts = append(*texts, nestedText)
		}
		*attachments = append(*attachments, nestedAtt...)
		return nil
	}

	// Collect text/plain bodies, decoding per Content-Transfer-Encoding.
	if partType == "text/plain" {
		decoded, derr := decodePart(part, part.Header.Get("Content-Transfer-Encoding"))
		if derr != nil {
			return fmt.Errorf("decode text part: %w", derr)
		}
		*texts = append(*texts, string(decoded))
	}
	return nil
}

// attachmentName resolves an attachment's filename from the Content-Disposition
// params first, then the Content-Type "name" param as a fallback.
func attachmentName(dispParams, partParams map[string]string) string {
	if name := dispParams["filename"]; name != "" {
		return name
	}
	return partParams["name"]
}

// decodePart reads a MIME part body and decodes it according to its
// Content-Transfer-Encoding. Unknown or empty encodings are returned verbatim.
func decodePart(r io.Reader, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		raw, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("read base64 part: %w", err)
		}
		// Mail base64 bodies are line-wrapped; strip whitespace before decoding.
		clean := strings.Map(func(c rune) rune {
			if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
				return -1
			}
			return c
		}, string(raw))
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, fmt.Errorf("decode base64: %w", err)
		}
		return decoded, nil
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(r))
		if err != nil {
			return nil, fmt.Errorf("decode quoted-printable: %w", err)
		}
		return decoded, nil
	default:
		raw, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("read part: %w", err)
		}
		return raw, nil
	}
}

// extractMSG handles Outlook .msg files. The standard library cannot parse the
// OLE container, so the bytes are sent to Apache Tika when configured;
// without Tika the document must fail instead of indexing a placeholder.
func (e *Extractor) extractMSG(ctx context.Context, data []byte) (string, error) {
	if e.tikaURL == "" {
		return "", errors.New(".msg parsing requires Tika; set TIKA_URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, e.tikaURL+"/tika", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("build tika request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Content-Type", "application/vnd.ms-outlook")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call tika: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read tika response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tika returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
