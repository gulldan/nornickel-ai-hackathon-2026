package email_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/email-parser/internal/infrastructure/email"
)

// oleSignature is the OLE2 magic header that selects the .msg (Tika) branch.
const oleSignature = "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1"

// crlf joins lines with CRLF, the line ending RFC 822 messages use.
func crlf(lines ...string) string {
	return strings.Join(lines, "\r\n")
}

// TestExtractEMLPlain checks a single-part text/plain message: headers are
// rendered and the body is preserved.
func TestExtractEMLPlain(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: Quarterly report",
		"From: alice@example.com",
		"To: bob@example.com",
		"Date: Mon, 02 Jan 2006 15:04:05 -0700",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hello Bob, the numbers look good.",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	for _, want := range []string{
		"Subject: Quarterly report",
		"From: alice@example.com",
		"To: bob@example.com",
		"Hello Bob, the numbers look good.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestExtractEMLNoContentType checks that a message without a Content-Type
// header is treated as plain text rather than failing.
func TestExtractEMLNoContentType(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: Plain",
		"",
		"Just a body, no content type.",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "Just a body, no content type.") {
		t.Errorf("body missing from output: %q", got)
	}
}

// TestExtractEMLEncodedSubject checks that an RFC 2047 encoded-word subject is
// decoded back to its Unicode form.
func TestExtractEMLEncodedSubject(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: =?utf-8?B?w4ViZWNrYQ==?=",
		"Content-Type: text/plain",
		"",
		"body",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "Subject: Åbecka") {
		t.Errorf("encoded subject not decoded: %q", got)
	}
}

// TestExtractEMLQuotedPrintable checks quoted-printable body decoding for a
// single-part message.
func TestExtractEMLQuotedPrintable(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: QP",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Caf=C3=A9 au lait",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "Café au lait") {
		t.Errorf("quoted-printable body not decoded: %q", got)
	}
}

// TestExtractEMLBase64SinglePart checks base64 body decoding for a single-part
// message, including the line-unwrapping path.
func TestExtractEMLBase64SinglePart(t *testing.T) {
	t.Parallel()

	// "Hello base64 world" split across two wrapped base64 lines.
	raw := crlf(
		"Subject: B64",
		"Content-Type: text/plain",
		"Content-Transfer-Encoding: base64",
		"",
		"SGVsbG8gYmFz",
		"ZTY0IHdvcmxk",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "Hello base64 world") {
		t.Errorf("base64 body not decoded: %q", got)
	}
}

// TestExtractEMLMultipartMixed checks a multipart/mixed message: the text part
// is decoded and the attachment filename is listed.
func TestExtractEMLMultipartMixed(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: With attachment",
		"From: alice@example.com",
		`Content-Type: multipart/mixed; boundary="BOUNDARY"`,
		"",
		"--BOUNDARY",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"See the attached spreadsheet.",
		"--BOUNDARY",
		"Content-Type: application/octet-stream",
		`Content-Disposition: attachment; filename="report.xlsx"`,
		"Content-Transfer-Encoding: base64",
		"",
		"AAEC",
		"--BOUNDARY--",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "See the attached spreadsheet.") {
		t.Errorf("text part missing: %q", got)
	}
	if !strings.Contains(got, "Attachments: report.xlsx") {
		t.Errorf("attachment not listed: %q", got)
	}
	if strings.Contains(got, "AAEC") {
		t.Errorf("attachment body should not be inlined: %q", got)
	}
}

// TestExtractEMLAttachmentNameFallback checks that an attachment with no
// Content-Disposition filename falls back to the Content-Type name param.
func TestExtractEMLAttachmentNameFallback(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: Fallback name",
		`Content-Type: multipart/mixed; boundary="B"`,
		"",
		"--B",
		"Content-Type: text/plain",
		"",
		"body",
		"--B",
		`Content-Type: image/png; name="picture.png"`,
		"Content-Disposition: attachment",
		"",
		"binary",
		"--B--",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "Attachments: picture.png") {
		t.Errorf("fallback attachment name missing: %q", got)
	}
}

// TestExtractEMLNestedMultipart checks that a multipart/alternative nested
// inside multipart/mixed is recursed into so the inner text is collected.
func TestExtractEMLNestedMultipart(t *testing.T) {
	t.Parallel()

	raw := crlf(
		"Subject: Nested",
		`Content-Type: multipart/mixed; boundary="OUTER"`,
		"",
		"--OUTER",
		`Content-Type: multipart/alternative; boundary="INNER"`,
		"",
		"--INNER",
		"Content-Type: text/plain",
		"",
		"plain alternative text",
		"--INNER",
		"Content-Type: text/html",
		"",
		"<p>html alternative</p>",
		"--INNER--",
		"--OUTER",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="doc.pdf"`,
		"",
		"pdfbytes",
		"--OUTER--",
		"",
	)

	got, err := email.NewExtractor("").Extract(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(got, "plain alternative text") {
		t.Errorf("nested text part missing: %q", got)
	}
	if !strings.Contains(got, "Attachments: doc.pdf") {
		t.Errorf("attachment from outer part missing: %q", got)
	}
}

// TestExtractEMLErrors covers the malformed-input error branches.
func TestExtractEMLErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "unparseable headers",
			raw:  "this is not a header block at all",
			want: "read message",
		},
		{
			name: "multipart without boundary",
			raw: crlf(
				"Subject: bad",
				"Content-Type: multipart/mixed",
				"",
				"body",
				"",
			),
			want: "read body",
		},
		{
			name: "invalid base64 body",
			raw: crlf(
				"Subject: bad b64",
				"Content-Type: text/plain",
				"Content-Transfer-Encoding: base64",
				"",
				"!!!not base64!!!",
				"",
			),
			want: "read body",
		},
		{
			name: "malformed multipart part header",
			raw: crlf(
				`Content-Type: multipart/mixed; boundary="X"`,
				"",
				"--X",
				"this-line-has-no-colon",
				"",
				"body",
				"--X--",
				"",
			),
			want: "next part",
		},
		{
			name: "invalid base64 text part",
			raw: crlf(
				`Content-Type: multipart/mixed; boundary="X"`,
				"",
				"--X",
				"Content-Type: text/plain",
				"Content-Transfer-Encoding: base64",
				"",
				"!!!bad!!!",
				"--X--",
				"",
			),
			want: "decode text part",
		},
		{
			name: "malformed nested multipart part header",
			raw: crlf(
				`Content-Type: multipart/mixed; boundary="OUTER"`,
				"",
				"--OUTER",
				`Content-Type: multipart/alternative; boundary="INNER"`,
				"",
				"--INNER",
				"no-colon-here",
				"",
				"inner body",
				"--INNER--",
				"--OUTER--",
				"",
			),
			want: "next part",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := email.NewExtractor("").Extract(context.Background(), []byte(tt.raw))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

// TestExtractMSGPlaceholder checks that without TIKA_URL a .msg file fails
// instead of indexing a placeholder body.
func TestExtractMSGPlaceholder(t *testing.T) {
	t.Parallel()

	data := []byte(oleSignature + "garbage ole payload")

	_, err := email.NewExtractor("").Extract(context.Background(), data)
	if err == nil {
		t.Fatal("Extract succeeded, want error without TIKA_URL")
	}
	if !strings.Contains(err.Error(), "TIKA_URL") {
		t.Errorf("error missing TIKA_URL hint: %v", err)
	}
}

// TestExtractMSGViaTika checks the .msg success path: bytes are PUT to Tika and
// the trimmed response body is returned.
func TestExtractMSGViaTika(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/tika" {
			t.Errorf("expected /tika path, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(string(body), oleSignature) {
			t.Errorf("Tika did not receive the OLE payload")
		}
		_, _ = io.WriteString(w, "  Extracted message text\n")
	}))
	defer srv.Close()

	data := []byte(oleSignature + "ole payload")

	got, err := email.NewExtractor(srv.URL+"/").Extract(context.Background(), data)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if got != "Extracted message text" {
		t.Errorf("unexpected Tika text: %q", got)
	}
}

// TestExtractMSGTikaError covers the non-200 Tika response branch.
func TestExtractMSGTikaError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()

	data := []byte(oleSignature + "ole payload")

	_, err := email.NewExtractor(srv.URL).Extract(context.Background(), data)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tika returned status 500") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestExtractMSGTikaUnreachable covers the transport-failure branch when Tika
// cannot be reached.
func TestExtractMSGTikaUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // Close immediately so the connection is refused.

	data := []byte(oleSignature + "ole payload")

	_, err := email.NewExtractor(url).Extract(context.Background(), data)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "call tika") {
		t.Errorf("unexpected error: %v", err)
	}
}
