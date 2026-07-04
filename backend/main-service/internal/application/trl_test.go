package application

import "testing"

func mkVerdicts(met ...bool) []trlLevelVerdict {
	out := make([]trlLevelVerdict, 0, len(met))
	for i, m := range met {
		out = append(out, trlLevelVerdict{Level: i + 1, Met: m})
	}
	return out
}

func TestGateTRL(t *testing.T) {
	cases := []struct {
		name string
		in   []trlLevelVerdict
		want int
	}{
		{"all met", mkVerdicts(true, true, true, true, true, true, true, true, true), 9},
		{"gap at 4 stops climb", mkVerdicts(true, true, true, false, true, true, true, true, true), 3},
		{"none met", mkVerdicts(false, false, false, false, false, false, false, false, false), 0},
		{"only 1-2", mkVerdicts(true, true, false, false, false, false, false, false, false), 2},
		{"out of order input", []trlLevelVerdict{{Level: 3, Met: true}, {Level: 1, Met: true}, {Level: 2, Met: true}}, 3},
	}
	for _, c := range cases {
		if got := gateTRL(c.in); got != c.want {
			t.Errorf("%s: gateTRL = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestUGTRubricEmbedded(t *testing.T) {
	rubric := loadUGT()
	if len(rubric.Levels) != 9 {
		t.Fatalf("expected 9 УГТ levels, got %d", len(rubric.Levels))
	}
	for i, l := range rubric.Levels {
		if l.Level != i+1 {
			t.Errorf("levels out of order at index %d: level=%d", i, l.Level)
		}
		if l.Name == "" || len(l.Criteria) == 0 {
			t.Errorf("level %d missing name/criteria", l.Level)
		}
	}
	if ugtLevelByNumber(4).Name == "" {
		t.Error("ugtLevelByNumber(4) returned empty")
	}
}
