package application

import (
	"encoding/json"
	"testing"
)

// parseExperimentPlan must extract the plan object from noisy model output, and
// mergeExperimentPlan must store it under detail.experiment_plan while preserving
// the other detail fields (e.g. a prior competitors block).
func TestExperimentPlan_ParseAndMergePreservesDetail(t *testing.T) {
	answer := "Вот план:\n```json\n" +
		`{"experiment_type":"новый сплав",` +
		`"sections":[{"title":"Подготовка шихты","purpose":"зафиксировать состав",` +
		`"items":["взвесить элементы"]},{"title":"","purpose":"","items":[]}],` +
		`"materials":["Al2O3","связующее"],` +
		`"process_parameters":[{"name":"температура","range":"800-1200 °C"},{"name":"","range":""}],` +
		`"characterization_methods":["SEM","XRD"],"test_methods":["tensile test"],` +
		`"controls":["базовый образец"],"success_criteria":["+15% к трещиностойкости"],` +
		`"estimated_cost":"средняя","estimated_time":"weeks","risks":["растрескивание"]}` +
		"\n```"
	plan, err := parseExperimentPlan(answer)
	if err != nil {
		t.Fatalf("parseExperimentPlan: %v", err)
	}

	prior := `{"competitors":{"summary":"s"},"material_system":"сплав"}`
	merged := mergeExperimentPlan(prior, plan, "model-x")

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(merged), &m); err != nil {
		t.Fatalf("merged detail must be valid JSON: %v", err)
	}
	for _, key := range []string{"competitors", "material_system", "experiment_plan"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("detail.%s must be present after merge", key)
		}
	}

	var ep struct {
		ExperimentType string              `json:"experiment_type"`
		Sections       []map[string]any    `json:"sections"`
		Materials      []string            `json:"materials"`
		ProcessParams  []map[string]string `json:"process_parameters"`
		EstimatedCost  string              `json:"estimated_cost"`
		EstimatedTime  string              `json:"estimated_time"`
		Model          string              `json:"model"`
	}
	if err := json.Unmarshal(m["experiment_plan"], &ep); err != nil {
		t.Fatalf("experiment_plan must parse: %v", err)
	}
	if len(ep.Materials) != 2 {
		t.Fatalf("expected 2 materials, got %d", len(ep.Materials))
	}
	if ep.ExperimentType != "new_alloy" {
		t.Fatalf("experiment_type must normalise to new_alloy, got %q", ep.ExperimentType)
	}
	if len(ep.Sections) != 1 {
		t.Fatalf("empty section must be dropped, got %d", len(ep.Sections))
	}
	if len(ep.ProcessParams) != 1 {
		t.Fatalf("empty process param must be dropped, got %d", len(ep.ProcessParams))
	}
	if ep.EstimatedCost != "medium" {
		t.Fatalf("Russian «средняя» must normalise to medium, got %q", ep.EstimatedCost)
	}
	if ep.EstimatedTime != "weeks" {
		t.Fatalf("estimated_time = %q, want weeks", ep.EstimatedTime)
	}
	if ep.Model != "model-x" {
		t.Fatalf("plan must record the model, got %q", ep.Model)
	}
}

// The cost/time normalisers map known labels (and Russian variants) and drop the
// rest to "".
func TestNormalizeCostAndTime(t *testing.T) {
	costs := map[string]string{"low": "low", "Высокий": "high", "средне": "medium", "огромная": ""}
	for in, want := range costs {
		if got := normalizeCostLevel(in); got != want {
			t.Fatalf("normalizeCostLevel(%q) = %q, want %q", in, got, want)
		}
	}
	times := map[string]string{"days": "days", "Недели": "weeks", "месяц": "months", "вечность": ""}
	for in, want := range times {
		if got := normalizeTimeScale(in); got != want {
			t.Fatalf("normalizeTimeScale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExperimentDomainSpecForText(t *testing.T) {
	cases := map[string]string{
		"жаропрочный никелевый сплав после плавки":      "new_alloy",
		"катодный материал для sodium ion battery cell": "battery_material",
		"защитное покрытие для снижения коррозии":       "coating_corrosion",
		"новый режим бурения и обработки отверстий":     "process_route",
		"повышение извлечения меди при флотации":        "ore_beneficiation",
		"выщелачивание цинкового огарка после обжига":   "metallurgy_process",
	}
	for text, want := range cases {
		got := experimentDomainSpecForText(text)
		if got.Type != want {
			t.Fatalf("experimentDomainSpecForText(%q) = %q, want %q", text, got.Type, want)
		}
		if len(got.Sections) == 0 {
			t.Fatalf("domain spec %q must define sections", got.Type)
		}
	}
}
