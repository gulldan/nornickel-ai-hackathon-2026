package contracts_test

import (
	"testing"

	"github.com/example/office-parser/internal/platform/contracts"
)

// TestParserQueues checks the queue->binding-key map is complete and identity-mapped.
func TestParserQueues(t *testing.T) {
	want := map[string]string{
		contracts.RouteParseEmail:  contracts.RouteParseEmail,
		contracts.RouteParseOffice: contracts.RouteParseOffice,
		contracts.RouteParsePDF:    contracts.RouteParsePDF,
		contracts.RouteParseOCR:    contracts.RouteParseOCR,
		contracts.RouteParseVLM:    contracts.RouteParseVLM,
		contracts.RouteParseArch:   contracts.RouteParseArch,
		contracts.RouteParseDB:     contracts.RouteParseDB,
		contracts.RouteChunk:       contracts.RouteChunk,
	}
	got := contracts.ParserQueues()
	if len(got) != len(want) {
		t.Fatalf("ParserQueues size = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("ParserQueues[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// TestParserQueuesIsolated confirms each call returns a fresh, independent map.
func TestParserQueuesIsolated(t *testing.T) {
	a := contracts.ParserQueues()
	a[contracts.RouteChunk] = "mutated"
	b := contracts.ParserQueues()
	if b[contracts.RouteChunk] != contracts.RouteChunk {
		t.Fatalf("ParserQueues is not isolated: got %q", b[contracts.RouteChunk])
	}
}

// TestRouteForMIME exercises MIME and extension routing, including the fallback.
func TestRouteForMIME(t *testing.T) {
	cases := []struct {
		name, mime, file, want string
	}{
		{"zip mime", "application/zip", "bundle.bin", contracts.RouteParseArch},
		{"rar mime", "application/x-rar-compressed", "a.dat", contracts.RouteParseArch},
		{"7z mime", "application/x-7z-compressed", "a.dat", contracts.RouteParseArch},
		{"tar mime", "application/x-tar", "a.dat", contracts.RouteParseArch},
		{"gzip mime", "application/gzip", "a.dat", contracts.RouteParseArch},
		{"octet zip ext", "application/octet-stream", "a.zip", contracts.RouteParseArch},
		{"empty mime tgz ext", "", "a.tgz", contracts.RouteParseArch},
		{"octet tar ext", "application/octet-stream", "a.tar", contracts.RouteParseArch},
		{"pdf mime", "application/pdf", "doc.pdf", contracts.RouteParsePDF},
		{"pdf substring", "application/x-pdf", "doc.bin", contracts.RouteParsePDF},
		{"rfc822 mime", "message/rfc822", "mail.bin", contracts.RouteParseEmail},
		{"eml ext", "application/octet-stream", "mail.eml", contracts.RouteParseEmail},
		{"msg ext", "application/octet-stream", "mail.msg", contracts.RouteParseEmail},
		{"image png", "image/png", "scan.png", contracts.RouteParseOCR},
		{"image jpeg", "image/jpeg", "scan.jpg", contracts.RouteParseOCR},
		{"docx office", "application/vnd.openxmlformats-officedocument", "doc.docx", contracts.RouteParseOffice},
		{"sqlite mime", "application/vnd.sqlite3", "data.bin", contracts.RouteParseDB},
		{"sqlite x- mime", "application/x-sqlite3", "data", contracts.RouteParseDB},
		{"octet sqlite ext", "application/octet-stream", "corpus.sqlite", contracts.RouteParseDB},
		{"octet db ext", "application/octet-stream", "corpus.db", contracts.RouteParseDB},
		{"db ext but known mime not sqlite", "application/pdf", "weird.db", contracts.RouteParsePDF},
		{"unknown binary fallback", "application/octet-stream", "data.bin", contracts.RouteParseOffice},
		{"empty everything fallback", "", "", contracts.RouteParseOffice},
		{"uppercase mime normalised", "APPLICATION/PDF", "DOC.PDF", contracts.RouteParsePDF},
		{"zip ext but known mime not archive", "application/pdf", "weird.zip", contracts.RouteParsePDF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contracts.RouteForMIME(tc.mime, tc.file); got != tc.want {
				t.Fatalf("RouteForMIME(%q, %q) = %q, want %q", tc.mime, tc.file, got, tc.want)
			}
		})
	}
}

// TestContractConstants pins the exchange, queue and status string values.
func TestContractConstants(t *testing.T) {
	pairs := []struct {
		got, want string
	}{
		{contracts.ExchangeIngestion, "ingestion"},
		{contracts.ExchangeEvents, "events"},
		{contracts.ExchangeDLX, "ingestion.dlx"},
		{contracts.QueueDead, "dead"},
		{contracts.StatusUploaded, "uploaded"},
		{contracts.StatusQueued, "queued"},
		{contracts.StatusParsing, "parsing"},
		{contracts.StatusParsed, "parsed"},
		{contracts.StatusChunking, "chunking"},
		{contracts.StatusIndexed, "indexed"},
		{contracts.StatusFailed, "failed"},
	}
	for _, p := range pairs {
		if p.got != p.want {
			t.Errorf("constant = %q, want %q", p.got, p.want)
		}
	}
}
