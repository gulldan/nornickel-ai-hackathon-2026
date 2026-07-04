package delimited

// DataRow is one logical CSV/TSV data record. Number is 1-based and counts the
// header as row 1, so the first data record is row 2.
type DataRow struct {
	Number int
	Cells  []string
}

// TransformResult describes a successful delimited-text normalization.
type TransformResult struct {
	Text      string
	Rows      int
	Columns   int
	Delimiter rune
	Truncated bool
}
