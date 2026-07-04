package application

import "testing"

// TestWithHeader pins the contextual-header contract: the breadcrumb is built
// from the non-empty title/section, and the RAW chunk is returned whenever the
// breadcrumb would be empty or the feature is disabled — the property that keeps
// the Qdrant payload.text untouched while only the embed/BM25 text gains context.
func TestWithHeader(t *testing.T) {
	const chunk = "The body of the chunk."
	for _, tc := range []struct {
		name           string
		enabled        bool
		title, section string
		want           string
	}{
		{"disabled returns raw", false, "Title", "Methods", chunk},
		{"title and section", true, "Title", "Methods", "Title — Methods\n\n" + chunk},
		{"title only", true, "Title", "", "Title\n\n" + chunk},
		{"section only", true, "", "Methods", "Methods\n\n" + chunk},
		{"both empty returns raw", true, "", "", chunk},
		{"blank parts return raw", true, "  ", "\t", chunk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withHeader(tc.enabled, tc.title, tc.section, chunk); got != tc.want {
				t.Fatalf("withHeader(%v, %q, %q) = %q, want %q", tc.enabled, tc.title, tc.section, got, tc.want)
			}
		})
	}
}
