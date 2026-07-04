package application

import (
	"testing"

	"github.com/example/chunk-splitter/internal/domain"
	commonv1 "github.com/example/chunk-splitter/internal/platform/genproto/common/v1"
)

func TestWorkbookAnchorMetadataFromSourceURI(t *testing.T) {
	line := "source_uri=flotation.xlsx#sheet=Flotation&range=A1%3AC3&row_start=1&row_end=3" +
		"&columns=sample&columns=Ni%20loss%20%28%25%29"
	text := "### Sheet: Flotation, range A1:C3\n" +
		line + "\n" +
		"| source_uri | sample | Ni loss (%) |\n"

	uri, ok := sourceURIFromLine(line)
	if !ok {
		t.Fatalf("source URI not parsed from %q", line)
	}
	if uri != "flotation.xlsx#sheet=Flotation&range=A1%3AC3&row_start=1&row_end=3&columns=sample&columns=Ni%20loss%20%28%25%29" {
		t.Fatalf("source URI = %q", uri)
	}
	parsed := metadataFromSourceURI(uri)
	if parsed["sheet"] != "Flotation" {
		t.Fatalf("metadataFromSourceURI sheet = %q (all=%v)", parsed["sheet"], parsed)
	}

	meta := workbookAnchorMetadata(text)
	want := map[string]string{
		"source_uri": "flotation.xlsx#sheet=Flotation&range=A1%3AC3&row_start=1&row_end=3&columns=sample&columns=Ni%20loss%20%28%25%29",
		"sheet":      "Flotation",
		"range":      "A1:C3",
		"row_start":  "1",
		"row_end":    "3",
		"columns":    "sample|Ni loss (%)",
		"block_id":   "Flotation!A1:C3",
	}
	for key, value := range want {
		if got := meta[key]; got != value {
			t.Fatalf("metadata[%q] = %q, want %q (all=%v)", key, got, value, meta)
		}
	}
}

func TestBuildBatchCarriesWorkbookAnchorMetadata(t *testing.T) {
	text := "source_uri=steel.xlsx#sheet=matminer&range=A2%3AC2&columns=formula&columns=yield%20strength\n" +
		"| steel.xlsx#sheet=matminer&range=A2%3AC2 | Fe | 2411.5 |"
	evt := &commonv1.DocumentParsed{DocumentId: "doc-xlsx", OwnerId: "u1", Filename: "steel.xlsx"}
	points, docs := buildBatch(
		evt,
		"",
		false,
		[]domain.Chunk{{Index: 0, Text: text}},
		0,
		[][2]int{{0, 0}},
		0,
		docStructure{},
		nil,
		[][]float32{{0.1}},
	)
	if got := points[0].Metadata["sheet"]; got != "matminer" {
		t.Fatalf("point sheet metadata = %q, want matminer", got)
	}
	if got := points[0].Metadata["row_start"]; got != "2" {
		t.Fatalf("point row_start metadata = %q, want 2", got)
	}
	if got := docs[0].Metadata["columns"]; got != "formula|yield strength" {
		t.Fatalf("search doc columns metadata = %q", got)
	}
}

func TestBuildBatchCarriesWorkbookRowEvidenceMetadata(t *testing.T) {
	text := "source_uri=steel.xlsx#sheet=matminer&range=A3%3AC3&row_start=3&row_end=3" +
		"&columns=formula&columns=yield%20strength&columns=tensile%20strength\n" +
		"block_type=table_row\n" +
		"row=3 | formula=Fe0.623 | yield strength=1123.1 | tensile strength=1929.2"
	evt := &commonv1.DocumentParsed{DocumentId: "doc-xlsx", OwnerId: "u1", Filename: "steel.xlsx"}
	points, docs := buildBatch(
		evt,
		"",
		false,
		[]domain.Chunk{{Index: 0, Text: text}},
		0,
		[][2]int{{0, 0}},
		0,
		docStructure{},
		nil,
		[][]float32{{0.1}},
	)
	if got := points[0].Metadata["sheet"]; got != "matminer" {
		t.Fatalf("point sheet metadata = %q, want matminer", got)
	}
	if got := points[0].Metadata["row_start"]; got != "3" {
		t.Fatalf("point row_start metadata = %q, want 3", got)
	}
	if got := points[0].Metadata["row_end"]; got != "3" {
		t.Fatalf("point row_end metadata = %q, want 3", got)
	}
	if got := docs[0].Metadata["columns"]; got != "formula|yield strength|tensile strength" {
		t.Fatalf("search doc columns metadata = %q", got)
	}
}

func TestBuildBatchUsesWorkbookAnchorIndexWhenChunkLacksSourceURI(t *testing.T) {
	full := "source_uri=steel.xlsx#sheet=matminer&range=A2%3AC2&columns=formula&columns=yield%20strength\n" +
		"Fe | 2411.5 | 2473.5"
	chunkText := "Fe | 2411.5 | 2473.5"
	charStart := len([]rune("source_uri=steel.xlsx#sheet=matminer&range=A2%3AC2&columns=formula&columns=yield%20strength\n"))
	evt := &commonv1.DocumentParsed{DocumentId: "doc-xlsx", OwnerId: "u1", Filename: "steel.xlsx"}
	points, _ := buildBatch(
		evt,
		"",
		false,
		[]domain.Chunk{{Index: 0, Text: chunkText}},
		0,
		[][2]int{{charStart, charStart + len([]rune(chunkText))}},
		0,
		docStructure{},
		parseWorkbookAnchorIndex(full),
		[][]float32{{0.1}},
	)
	if got := points[0].Metadata["sheet"]; got != "matminer" {
		t.Fatalf("point sheet metadata = %q, want matminer", got)
	}
	if got := points[0].Metadata["row_start"]; got != "2" {
		t.Fatalf("point row_start metadata = %q, want 2", got)
	}
}

func TestWorkbookAnchorIndexAppliesAnchorInsideChunkSpan(t *testing.T) {
	full := "### Sheet: citrine_mpea, range A1:W26\n" +
		"source_uri=citrine_mpea.xlsx#sheet=citrine_mpea&range=A1%3AW26&row_start=1&row_end=26&columns=FORMULA\n" +
		"| FORMULA | PROPERTY: Microstructure |\n"
	meta := parseWorkbookAnchorIndex(full).metadataAt(0, len([]rune(full)))
	if got := meta["sheet"]; got != "citrine_mpea" {
		t.Fatalf("sheet = %q, want citrine_mpea (all=%v)", got, meta)
	}
	if got := meta["row_start"]; got != "1" {
		t.Fatalf("row_start = %q, want 1", got)
	}
}

func TestWorkbookAnchorIndexPrefersNewAnchorInsideChunkSpan(t *testing.T) {
	first := "source_uri=steel.xlsx#sheet=old&range=A2%3AC2&row_start=2&row_end=2\nold row\n"
	second := "source_uri=steel.xlsx#sheet=new&range=A3%3AC3&row_start=3&row_end=3\nnew row\n"
	full := first + second

	charStart := len([]rune(first)) - len([]rune("old row\n"))
	meta := parseWorkbookAnchorIndex(full).metadataAt(charStart, len([]rune(full)))

	if got := meta["sheet"]; got != "new" {
		t.Fatalf("sheet = %q, want new (all=%v)", got, meta)
	}
	if got := meta["row_start"]; got != "3" {
		t.Fatalf("row_start = %q, want 3 (all=%v)", got, meta)
	}
}
