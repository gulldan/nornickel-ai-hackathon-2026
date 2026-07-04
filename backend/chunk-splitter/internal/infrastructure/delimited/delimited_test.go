package delimited

import (
	"strings"
	"testing"
)

func TestTransform_CommaCSVRowAnchors(t *testing.T) {
	input := "formula,yield strength,tensile strength\nFe0.620C0.000953,2411.5,2473.5\n"
	got, ok := Transform("steel.csv", "text/plain; charset=utf-8", input)
	if !ok {
		t.Fatal("Transform should recognize .csv")
	}
	for _, want := range []string{
		"source_uri=steel.csv#rows=2&columns=formula&columns=yield+strength&columns=tensile+strength",
		"row=2",
		"formula=Fe0.620C0.000953",
		"yield strength=2411.5",
		"tensile strength=2473.5",
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("rendered CSV missing %q:\n%s", want, got.Text)
		}
	}
}

func TestTransform_SemicolonSepAndBOM(t *testing.T) {
	input := "\ufeffsep=;\nname;loss,%\nT-001;12,4\n"
	got, ok := Transform("assay.csv", "text/csv", input)
	if !ok {
		t.Fatal("Transform should recognize sep=; CSV")
	}
	if got.Delimiter != ';' {
		t.Fatalf("delimiter = %q, want ';'", got.Delimiter)
	}
	if !strings.Contains(got.Text, "loss,%=12,4") {
		t.Fatalf("decimal comma value not preserved:\n%s", got.Text)
	}
}

func TestTransform_SemicolonDecimalCommaHeuristic(t *testing.T) {
	input := "name;loss,%\nT-001;12,4\nT-002;10,1\n"
	got, ok := Transform("assay.csv", "text/csv", input)
	if !ok {
		t.Fatal("Transform should recognize semicolon CSV")
	}
	if got.Delimiter != ';' {
		t.Fatalf("delimiter = %q, want ';'\n%s", got.Delimiter, got.Text)
	}
	if !strings.Contains(got.Text, "name=T-001") || !strings.Contains(got.Text, "loss,%=12,4") {
		t.Fatalf("semicolon decimal comma row was not preserved:\n%s", got.Text)
	}
}

func TestTransform_TSVQuotedMultilineAndDuplicateHeaders(t *testing.T) {
	input := "sample\tvalue\t" + "value\t\nT-001\t\"quoted\tinside\"\t\"line 1\nline 2\"\textra\n"
	got, ok := Transform("assay.tsv", "text/plain", input)
	if !ok {
		t.Fatal("Transform should recognize .tsv")
	}
	for _, want := range []string{
		"sample=T-001",
		"value=quoted inside",
		"value_2=line 1 line 2",
		"unnamed_4=extra",
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("rendered TSV missing %q:\n%s", want, got.Text)
		}
	}
}

func TestTransform_DuplicateHeadersDoNotCollideWithExistingSuffix(t *testing.T) {
	got, ok := Transform("collide.csv", "text/csv", "a,a,a_2\n1,2,3\n")
	if !ok {
		t.Fatal("Transform should tolerate duplicate headers")
	}
	for _, want := range []string{"a=1", "a_2=2", "a_2_2=3"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("rendered CSV missing %q:\n%s", want, got.Text)
		}
	}
}

func TestTransform_RaggedRowsExtendHeaders(t *testing.T) {
	got, ok := Transform("ragged.csv", "text/csv", "a,b\n1,2,3\n")
	if !ok {
		t.Fatal("Transform should tolerate ragged rows")
	}
	if !strings.Contains(got.Text, "extra_3=3") {
		t.Fatalf("extra column missing:\n%s", got.Text)
	}
}

func TestTransform_RaggedRowsDoNotCollideWithExistingExtraHeader(t *testing.T) {
	got, ok := Transform("ragged.csv", "text/csv", "id,extra_3\nA,old,new\n")
	if !ok {
		t.Fatal("Transform should tolerate ragged rows")
	}
	for _, want := range []string{"extra_3=old", "extra_3_2=new"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("rendered CSV missing %q:\n%s", want, got.Text)
		}
	}
}

func TestTransform_WideRowsRepeatAnchorsPerSegment(t *testing.T) {
	headers := make([]string, 0, 24)
	values := make([]string, 0, 24)
	for i := 1; i <= 24; i++ {
		headers = append(headers, "c"+string(rune('A'+i-1)))
		values = append(values, strings.Repeat(string(rune('a'+(i%20))), 80))
	}
	values[23] = "target-value"
	got, ok := Transform("wide.csv", "text/csv", strings.Join(headers, ",")+"\n"+strings.Join(values, ",")+"\n")
	if !ok {
		t.Fatal("Transform should recognize wide CSV")
	}
	lines := strings.Split(strings.TrimSpace(got.Text), "\n")
	anchoredDataLines := 0
	targetAnchored := false
	for _, line := range lines {
		if !strings.Contains(line, "row=2") {
			continue
		}
		anchoredDataLines++
		if !strings.Contains(line, "source_uri=wide.csv#rows=2") {
			t.Fatalf("row segment without source_uri anchor:\n%s", line)
		}
		if strings.Contains(line, "target-value") {
			targetAnchored = true
		}
	}
	if anchoredDataLines < 2 {
		t.Fatalf("wide row should be split into multiple anchored lines:\n%s", got.Text)
	}
	if !targetAnchored {
		t.Fatalf("target value not found in anchored segment:\n%s", got.Text)
	}
}

func TestTransformWithLimit_CapsRenderedOutputAtRowBoundary(t *testing.T) {
	var input strings.Builder
	input.WriteString("sample,value\n")
	for range 100 {
		input.WriteString("T-")
		input.WriteString(strings.Repeat("x", 8))
		input.WriteString(",")
		input.WriteString(strings.Repeat("y", 64))
		input.WriteByte('\n')
	}
	got, ok := TransformWithLimit("limited.csv", "text/csv", input.String(), 512)
	if !ok {
		t.Fatal("TransformWithLimit should keep at least one row")
	}
	if !got.Truncated {
		t.Fatal("TransformWithLimit should report truncation")
	}
	if len(got.Text) > 512 {
		t.Fatalf("rendered text length = %d, want <= 512", len(got.Text))
	}
	if !strings.HasSuffix(got.Text, "\n") {
		t.Fatalf("rendered text should stop at a row boundary:\n%q", got.Text)
	}
}

func TestTransform_RejectsPlainText(t *testing.T) {
	if _, ok := Transform("notes.txt", "text/plain", "hello\nworld\n"); ok {
		t.Fatal("plain text should not be transformed")
	}
	if _, ok := Transform("bad.csv", "text/csv", "hello\nworld\n"); ok {
		t.Fatal("single-column CSV-like text should not be transformed")
	}
}
