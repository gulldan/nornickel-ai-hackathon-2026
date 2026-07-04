package splitter

import (
	"testing"
)

// initials is the real "И. И. Иванов" sample (periods break the single-letter
// tokens, so CollapseTracking must leave it identical). It is a constant rather
// than an inline literal only so the repeated "И." token does not trip the
// dupword linter on a value that is genuine test data.
const initials = "И." + " " + "И." + " Иванов"

// hasTrackedRun reports whether s still contains a run of >= minTrackedRun
// single-letter tokens joined by single-whitespace gaps — i.e. whether any
// letter-spacing survived. It reuses the production tokenizer so the assertion
// tracks exactly what CollapseTracking acts on.
func hasTrackedRun(s string) bool {
	tokens, gaps := tokenize(s)
	for i := 0; i < len(tokens); {
		if n := trackedRunLength(tokens, gaps, i); n >= minTrackedRun {
			return true
		} else if n > 1 {
			i += n
		} else {
			i++
		}
	}
	return false
}

func TestCollapseTracking_Table(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "clean single tracked word",
			in:   "М е т о д о л о г и я",
			want: "Методология",
		},
		{
			name: "normal text unchanged",
			in:   "кот и пёс гуляли вместе",
			want: "кот и пёс гуляли вместе",
		},
		{
			name: "initials not collapsed (periods break single-letter tokens)",
			in:   initials,
			want: initials,
		},
		{
			name: "short spaced run below threshold unchanged",
			in:   "a b c d",
			want: "a b c d",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "exactly threshold letters collapse",
			in:   "а б в г д",
			want: "абвгд",
		},
		{
			name: "four letters below threshold unchanged",
			in:   "а б в г",
			want: "а б в г",
		},
		{
			name: "double-space gap still glues (PDF tracking uses 1-2 ws)",
			in:   "а б в г д  е ж з и к",
			want: "абвгдежзик",
		},
		{
			name: "gap over the connector limit (3 ws) ends the run",
			in:   "а б в г д   е ж з и к",
			want: "абвгд   ежзик",
		},
		{
			name: "paragraph break preserved around collapsed run",
			in:   "слово\n\nа б в г д е\n\nещё",
			want: "слово\n\nабвгде\n\nещё",
		},
		{
			name: "newline-joined run collapses",
			in:   "т е х н о л о г и я",
			want: "технология",
		},
		{
			name: "leading and trailing whitespace preserved",
			in:   "  а б в г д  ",
			want: "  абвгд  ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CollapseTracking(tc.in); got != tc.want {
				t.Fatalf("CollapseTracking(%q)\n  got  = %q\n  want = %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCollapseTracking_RealMixedSample uses the real corpus sample where tracking
// is interleaved with multi-letter fragments and mixed 1-2 space/newline gaps.
// Word boundaries inside the tracked text use the SAME 1-2 ws gaps as the letter
// spacing, so they are unrecoverable and the run collapses to one blob. We assert
// the dominant fix — NO single-letter tracked run survives (the embedding/search
// killer is gone) — and that the real words are still present as substrings.
func TestCollapseTracking_RealMixedSample(t *testing.T) {
	in := "в М\nе т\nо д\nо л\nо г\nи я и\n т е\nх н\nо л\nо г\nи я"
	got := CollapseTracking(in)

	if hasTrackedRun(got) {
		t.Fatalf("tracking should be gone, but a >=%d single-letter run survives: %q", minTrackedRun, got)
	}
	for _, sub := range []string{"етодологи", "ехнологи"} {
		if indexString(got, sub) < 0 {
			t.Fatalf("expected substring %q in result %q", sub, got)
		}
	}
}

func TestCollapseTracking_Idempotent(t *testing.T) {
	inputs := []string{
		"М е т о д о л о г и я",
		"в М\nе т\nо д\nо л\nо г\nи я и\n т е\nх н\nо л\nо г\nи я",
		"кот и пёс гуляли вместе",
		initials,
		"a b c d",
		"слово\n\nа б в г д е\n\nещё",
		"  а б в г д  ",
	}
	for _, in := range inputs {
		once := CollapseTracking(in)
		twice := CollapseTracking(once)
		if once != twice {
			t.Fatalf("not idempotent for %q:\n  f(x)    = %q\n  f(f(x)) = %q", in, once, twice)
		}
	}
}

func indexString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
