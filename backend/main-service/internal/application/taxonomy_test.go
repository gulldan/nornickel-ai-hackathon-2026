package application

import "testing"

func TestTaxonomiesEmbedded(t *testing.T) {
	if n := len(loadTax(grntiRaw)); n == 0 {
		t.Error("grnti.json embedded empty")
	}
	if n := len(loadTax(vakRaw)); n == 0 {
		t.Error("vak.json embedded empty")
	}
	if n := len(loadTax(asjcRaw)); n == 0 {
		t.Error("asjc.json embedded empty")
	}
}

func TestValidateDropsUnknownAndDedups(t *testing.T) {
	idx := map[string]string{"31": "Химия"}
	got := validate([]taxEntry{
		{Code: "31", Name: "wrong-name"},
		{Code: "99", Name: "hallucinated"},
		{Code: "31", Name: "dup"},
	}, idx)
	if len(got) != 1 {
		t.Fatalf("expected 1 valid entry, got %d (%+v)", len(got), got)
	}
	if got[0].Code != "31" || got[0].Name != "Химия" {
		t.Errorf("expected canonical name, got %+v", got[0])
	}
}

func TestWithResearchTagKeepsOneResearchType(t *testing.T) {
	got := withResearchTag([]string{"практическое", "композит", "композит"}, "теоретическое исследование")
	if len(got) != 2 {
		t.Fatalf("expected research tag plus deduped domain tag, got %#v", got)
	}
	if got[0] != researchTagTheoretical || got[1] != "композит" {
		t.Fatalf("unexpected tags: %#v", got)
	}
}

func TestInferResearchTag(t *testing.T) {
	if got := inferResearchTag(true, "внедрение на производстве снизило аварийность"); got != researchTagPractical {
		t.Fatalf("expected practical, got %q", got)
	}
	if got := inferResearchTag(true, "численная модель и расчёт параметров"); got != researchTagTheoretical {
		t.Fatalf("expected theoretical, got %q", got)
	}
}
