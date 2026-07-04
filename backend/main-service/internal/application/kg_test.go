package application

import "testing"

// deriveEdges must turn a filled materials genItem into the expected typed
// triples: material→process, process→property (via mechanism when distinct),
// property→KPI, and the causal-chain stages into a leads_to path.
func TestDeriveEdges_FromFilledItem(t *testing.T) {
	item := genItem{
		MaterialSystem:          "сплав Al2O3",
		ProcessChange:           "плазменное напыление",
		MicrostructureMechanism: "измельчение зерна",
		TargetProperty:          "трещиностойкость",
		CausalChain: []causalStep{
			{Stage: "состав", Change: "ввести Al2O3"},
			{Stage: "процесс", Change: "напыление"},
			{Stage: "свойство", Change: "рост трещиностойкости"},
		},
	}
	edges := deriveEdges(item, "kpi-1", "hyp-1")

	want := map[string]KGEdge{
		"material->process": {
			FromType: nodeMaterial, FromName: "сплав Al2O3", Relation: relProcessChanges,
			ToType: nodeProcess, ToName: "плазменное напыление", HypothesisID: "hyp-1", KPIID: "kpi-1",
		},
		"process->mechanism": {
			FromType: nodeProcess, FromName: "плазменное напыление", Relation: relAffectsProperty,
			ToType: nodeMechanism, ToName: "измельчение зерна", HypothesisID: "hyp-1", KPIID: "kpi-1",
		},
		"mechanism->property": {
			FromType: nodeMechanism, FromName: "измельчение зерна", Relation: relAffectsProperty,
			ToType: nodeProperty, ToName: "трещиностойкость", HypothesisID: "hyp-1", KPIID: "kpi-1",
		},
		"property->kpi": {
			FromType: nodeProperty, FromName: "трещиностойкость", Relation: relSupportsKPI,
			ToType: nodeKPI, ToName: "kpi:kpi-1", HypothesisID: "hyp-1", KPIID: "kpi-1",
		},
	}
	for name, w := range want {
		if !containsEdge(edges, w) {
			t.Fatalf("missing expected edge %s: %+v\ngot: %+v", name, w, edges)
		}
	}
	// The causal chain (3 stages) yields 2 leads_to edges.
	leads := 0
	for _, e := range edges {
		if e.Relation == relLeadsTo {
			leads++
		}
	}
	if leads != 2 {
		t.Fatalf("expected 2 leads_to edges from a 3-stage chain, got %d", leads)
	}
}

// A sparsely-filled item yields no broken edges: an empty process means no
// material→process or process→property edge is produced.
func TestDeriveEdges_SkipsEmptyEnds(t *testing.T) {
	edges := deriveEdges(genItem{MaterialSystem: "сталь", TargetProperty: "прочность"}, "", "hyp-x")
	for _, e := range edges {
		if e.FromName == "" || e.ToName == "" {
			t.Fatalf("derived an edge with an empty end: %+v", e)
		}
		if e.Relation == relProcessChanges {
			t.Fatalf("no process ⇒ no process_changes edge, got %+v", e)
		}
	}
	// No KPI id ⇒ no supports_kpi edge (the KPI node would be unnamed).
	for _, e := range edges {
		if e.Relation == relSupportsKPI {
			t.Fatalf("blank kpi id ⇒ no supports_kpi edge, got %+v", e)
		}
	}
}

// findBridges must pair a process→property edge with a property→KPI edge from a
// DIFFERENT hypothesis on the shared property, and never bridge within one source.
func TestFindBridges_PairsAcrossHypotheses(t *testing.T) {
	edges := []KGEdge{
		// Work A: process X improves property Y.
		{FromType: nodeProcess, FromName: "процесс X", Relation: relAffectsProperty,
			ToType: nodeProperty, ToName: "свойство Y", HypothesisID: "A", KPIID: "k1"},
		// Work B: property Y supports KPI Z.
		{FromType: nodeProperty, FromName: "свойство Y", Relation: relSupportsKPI,
			ToType: nodeKPI, ToName: "KPI Z", HypothesisID: "B", KPIID: "k1"},
		// Work A also links Y→KPI itself: must NOT bridge to its own process edge.
		{FromType: nodeProperty, FromName: "свойство Y", Relation: relSupportsKPI,
			ToType: nodeKPI, ToName: "KPI Z", HypothesisID: "A", KPIID: "k1"},
	}
	bridges := findBridges(edges, "k1", "")
	if len(bridges) != 1 {
		t.Fatalf("expected exactly 1 cross-hypothesis bridge, got %d: %+v", len(bridges), bridges)
	}
	b := bridges[0]
	if b.Process != "процесс X" || b.Property != "свойство Y" || b.KPI != "KPI Z" {
		t.Fatalf("unexpected bridge content: %+v", b)
	}
	if b.FromHypothesis != "A" || b.ViaHypothesis != "B" {
		t.Fatalf("bridge must cross A→B, got from=%s via=%s", b.FromHypothesis, b.ViaHypothesis)
	}
	if b.Statement == "" {
		t.Fatal("bridge statement must be populated")
	}
}

// findBridges is defensive: an empty or single-source graph yields no bridges.
func TestFindBridges_EmptyGraph(t *testing.T) {
	if got := findBridges(nil, "k1", ""); len(got) != 0 {
		t.Fatalf("empty graph ⇒ no bridges, got %d", len(got))
	}
	only := []KGEdge{
		{FromType: nodeProcess, FromName: "p", Relation: relAffectsProperty,
			ToType: nodeProperty, ToName: "y", HypothesisID: "A"},
		{FromType: nodeProperty, FromName: "y", Relation: relSupportsKPI,
			ToType: nodeKPI, ToName: "z", HypothesisID: "A"},
	}
	if got := findBridges(only, "k1", ""); len(got) != 0 {
		t.Fatalf("single-source graph ⇒ no cross bridge, got %d", len(got))
	}
}

func containsEdge(edges []KGEdge, want KGEdge) bool {
	for _, e := range edges {
		if e == want {
			return true
		}
	}
	return false
}
