package pdf

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

// minimalPDF assembles a one-page PDF whose content stream shows showText with
// Helvetica, including a correct cross-reference table so ledongthuc parses it.
// It is the smallest input that exercises the happy path of Extractor.Extract.
func minimalPDF(showText string) []byte {
	stream := "BT /F1 24 Tf 72 700 Td (" + showText + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefPos := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefPos)
	return buf.Bytes()
}

// A well-formed PDF with a text layer extracts its visible text.
func TestExtractorExtractSuccess(t *testing.T) {
	got, err := NewExtractor().Extract(context.Background(), minimalPDF("Hello PDF World"))
	if err != nil {
		t.Fatalf("Extract returned error, want nil: %v", err)
	}
	if !strings.Contains(got, "Hello PDF World") {
		t.Errorf("extracted text = %q, want it to contain %q", got, "Hello PDF World")
	}
}

// Bytes that are not a PDF surface as an open error (the header check fails).
func TestExtractorExtractNotPDF(t *testing.T) {
	_, err := NewExtractor().Extract(context.Background(), []byte("this is plain text, not a pdf"))
	if err == nil {
		t.Fatal("Extract returned nil error for non-PDF input, want an error")
	}
	if !strings.Contains(err.Error(), "open pdf") {
		t.Errorf("error = %v, want it to mention open pdf", err)
	}
}

// A PDF that passes the header check but references a dangling object makes the
// library panic; Extract must recover it into an error rather than crashing.
func TestExtractorExtractPanicRecovered(t *testing.T) {
	// Valid header and %%EOF, a startxref pointing at the xref keyword, but a
	// trailer Root pointing at an object whose recorded offset is past EOF.
	dangling := []byte("%PDF-1.7\nxref\n0 2\n0000000000 65535 f \n" +
		"0000000200 00000 n \ntrailer\n<< /Size 2 /Root 1 0 R >>\nstartxref\n9\n%%EOF\n")

	_, err := NewExtractor().Extract(context.Background(), dangling)
	if err == nil {
		t.Fatal("Extract returned nil error for a panicking PDF, want a recovered error")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("error = %v, want it to report the recovered panic", err)
	}
}
