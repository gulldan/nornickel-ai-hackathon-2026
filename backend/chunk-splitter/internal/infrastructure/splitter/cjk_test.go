package splitter

import (
	"strings"
	"testing"
)

// Китайские заголовки секций распознаются: голые, с нумерацией и с CJK-двоеточием.
func TestSectionHeading_Chinese(t *testing.T) {
	for in, want := range map[string]string{
		"引言": secIntroduction, "1. 结论": secConclusion, "参考文献：": secReferences,
	} {
		if got := sectionHeading(in); got != want {
			t.Fatalf("sectionHeading(%q) = %q, want %q", in, got, want)
		}
	}
}

// Китайский текст без пробелов режется по концам предложений, не посреди фразы.
func TestSplit_ChineseSentences(t *testing.T) {
	sentence := strings.Repeat("锂离子电池正极材料的循环稳定性研究", 3) + "。"
	text := strings.Repeat(sentence, 6) // ~330 рун без единого пробела

	r := NewRecursive(120, 0)
	chunks := r.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c.Text)) > 120 {
			t.Fatalf("chunk %d exceeds budget: %d runes", i, len([]rune(c.Text)))
		}
		if !strings.HasSuffix(strings.TrimSpace(c.Text), "。") {
			t.Fatalf("chunk %d must end on a sentence boundary, got %q…", i, string([]rune(c.Text)[:20]))
		}
	}
}
