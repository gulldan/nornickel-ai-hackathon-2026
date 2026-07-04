// Knowledge graph (P2.1): a typed, owner-scoped graph of triples mined from the
// structured fields of every generated hypothesis (no extra LLM call). Nodes are
// {type, name} where type ∈ {material, process, property, kpi, mechanism}; edges
// are typed relations carrying provenance (hypothesis_id, kpi_id). The graph lets
// the factory find CROSS-hypothesis bridges: a process that improved one property
// in work A, recombined with a property→KPI link from work B, becomes a candidate
// research direction the corpus has not yet connected.
//
// The backing store is the application-layer port GraphStore; the concrete
// adapter is valkey-backed (internal/infrastructure/kgstore) — pragmatic for a
// board-sized portfolio. A Postgres/graph-DB backing is the production follow-up.

package application

import (
	"context"
	"sort"
	"strings"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// Knowledge-graph node types.
const (
	nodeMaterial  = "material"
	nodeProcess   = "process"
	nodeProperty  = "property"
	nodeKPI       = "kpi"
	nodeMechanism = "mechanism"
)

// Knowledge-graph edge relations.
const (
	relProcessChanges  = "process_changes"  // material/composition → process
	relAffectsProperty = "affects_property" // process → property/microstructure
	relSupportsKPI     = "supports_kpi"     // property → KPI
	relLeadsTo         = "leads_to"         // causal-chain stage → next stage
)

// kgMaxBridges bounds how many bridge candidates a single request returns, so a
// large graph never floods the UI.
const kgMaxBridges = 12

// KGEdge is one typed triple with provenance. Nodes are carried inline (type +
// name on each end) so the store stays a flat, append-only edge list.
type KGEdge struct {
	FromType     string `json:"from_type"`
	FromName     string `json:"from_name"`
	Relation     string `json:"relation"`
	ToType       string `json:"to_type"`
	ToName       string `json:"to_name"`
	HypothesisID string `json:"hypothesis_id"`
	KPIID        string `json:"kpi_id"`
}

// GraphStore persists the owner's knowledge-graph edges. It is the
// application-layer port; the concrete adapter (kgstore) is valkey-backed and a
// nil store is a safe no-op so the factory degrades gracefully when valkey is
// absent.
type GraphStore interface {
	AddEdges(ctx context.Context, ownerID string, edges []KGEdge) error
	Edges(ctx context.Context, ownerID string) ([]KGEdge, error)
}

// deriveEdges turns one generated hypothesis's structured fields into typed
// edges. It is pure (no I/O) and best-effort: every end is trimmed, empty ends
// are skipped, so a sparsely-filled item simply yields fewer edges. The mapping:
//   - material/composition → process            (process_changes)
//   - process → property/microstructure         (affects_property)
//   - property → KPI                             (supports_kpi)
//   - each causal-chain stage → the next stage   (leads_to)
func deriveEdges(item genItem, kpiID, hypothesisID string) []KGEdge {
	material := compactText(firstNonEmpty(item.MaterialSystem, item.CompositionChange))
	process := compactText(item.ProcessChange)
	property := compactText(firstNonEmpty(item.TargetProperty, item.MicrostructureMechanism))
	mechanism := compactText(item.MicrostructureMechanism)

	edges := make([]KGEdge, 0, 8)
	add := func(ft, fn, rel, tt, tn string) {
		fn, tn = compactText(fn), compactText(tn)
		if fn == "" || tn == "" {
			return
		}
		edges = append(edges, KGEdge{
			FromType: ft, FromName: fn, Relation: rel, ToType: tt, ToName: tn,
			HypothesisID: hypothesisID, KPIID: kpiID,
		})
	}

	add(nodeMaterial, material, relProcessChanges, nodeProcess, process)
	// process → property (mechanism, when distinct, is the property's microstructural cause)
	if mechanism != "" && mechanism != property {
		add(nodeProcess, process, relAffectsProperty, nodeMechanism, mechanism)
		add(nodeMechanism, mechanism, relAffectsProperty, nodeProperty, item.TargetProperty)
	} else {
		add(nodeProcess, process, relAffectsProperty, nodeProperty, property)
	}
	add(nodeProperty, property, relSupportsKPI, nodeKPI, kpiTargetName(kpiID))

	// Causal chain: link each non-empty stage label to the next, typed by the
	// materials-science stage vocabulary (состав/процесс/микроструктура/свойство/KPI).
	stages := make([]causalStep, 0, len(item.CausalChain))
	for _, st := range item.CausalChain {
		if compactText(st.Stage) != "" {
			stages = append(stages, st)
		}
	}
	for i := 0; i+1 < len(stages); i++ {
		add(causalStageType(stages[i].Stage), stages[i].Stage,
			relLeadsTo, causalStageType(stages[i+1].Stage), stages[i+1].Stage)
	}
	return edges
}

// kpiTargetName names the KPI node. The KPI's metric/title is not on the genItem,
// so the id anchors the node; the bridge-finder rewrites it to the metric when it
// has the KPI in hand. A blank id yields "" so the supports_kpi edge is skipped.
func kpiTargetName(kpiID string) string {
	if kpiID == "" {
		return ""
	}
	return "kpi:" + kpiID
}

// causalStageType maps a Russian causal-chain stage label to a node type, so the
// leads_to path is typed consistently with the rest of the graph.
func causalStageType(stage string) string {
	s := strings.ToLower(compactText(stage))
	switch {
	case strings.Contains(s, "состав"):
		return nodeMaterial
	case strings.Contains(s, "процесс") || strings.Contains(s, "режим"):
		return nodeProcess
	case strings.Contains(s, "микроструктур") || strings.Contains(s, "механизм"):
		return nodeMechanism
	case strings.Contains(s, "kpi") || strings.Contains(s, "показател"):
		return nodeKPI
	case strings.Contains(s, "свойств"):
		return nodeProperty
	default:
		return nodeProperty
	}
}

// firstNonEmpty returns the first argument whose compacted form is non-empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if compactText(v) != "" {
			return v
		}
	}
	return ""
}

// BridgeCandidate is a cross-hypothesis research direction synthesised from the
// graph: a process→property link from one source recombined with a property→KPI
// link from a DIFFERENT source. It is returned to the UI as a draft direction
// (not necessarily persisted).
type BridgeCandidate struct {
	Statement      string   `json:"statement"`
	Process        string   `json:"process"`
	Property       string   `json:"property"`
	KPI            string   `json:"kpi"`
	FromHypothesis string   `json:"from_hypothesis"` // source of the process→property link
	ViaHypothesis  string   `json:"via_hypothesis"`  // source of the property→KPI link
	Sources        []string `json:"sources"`
}

// kpiLink is a property→KPI edge indexed by its property for the bridge-finder.
type kpiLink struct {
	kpiName string
	hypID   string
	kpiID   string
}

// findBridges pairs every process→property edge with a property→KPI edge from a
// DIFFERENT hypothesis on a shared (case-folded) property, producing bridge
// directions the corpus has not yet connected. It is fully defensive: an empty or
// tiny graph yields an empty slice (never nil-panics), duplicates are collapsed,
// and the result is bounded by kgMaxBridges. kpiLabel, when non-empty, names KPI
// nodes whose id matches kpiID (so the statement reads the metric, not "kpi:…").
func findBridges(edges []KGEdge, kpiID, kpiLabel string) []BridgeCandidate {
	supportsByProp := indexKPILinks(edges)
	out := make([]BridgeCandidate, 0, kgMaxBridges)
	seen := make(map[string]struct{})
	for _, pe := range edges {
		if pe.Relation != relAffectsProperty || pe.ToType != nodeProperty {
			continue
		}
		process, property := compactText(pe.FromName), compactText(pe.ToName)
		if process == "" || property == "" {
			continue
		}
		links := supportsByProp[strings.ToLower(property)]
		out = appendBridges(out, seen, pe.HypothesisID, process, property, links, kpiID, kpiLabel)
		if len(out) >= kgMaxBridges {
			return out[:kgMaxBridges]
		}
	}
	return out
}

// indexKPILinks groups property→KPI edges by the (case-folded) property they
// support, so the bridge-finder can look up KPI links for a property in O(1).
func indexKPILinks(edges []KGEdge) map[string][]kpiLink {
	byProp := make(map[string][]kpiLink)
	for _, e := range edges {
		if e.Relation != relSupportsKPI || compactText(e.FromName) == "" || compactText(e.ToName) == "" {
			continue
		}
		key := strings.ToLower(compactText(e.FromName))
		byProp[key] = append(byProp[key], kpiLink{kpiName: e.ToName, hypID: e.HypothesisID, kpiID: e.KPIID})
	}
	return byProp
}

// appendBridges emits one bridge per cross-hypothesis (process→property, link)
// pairing, skipping same-source pairs and duplicates, and returns the grown slice.
func appendBridges(
	out []BridgeCandidate, seen map[string]struct{},
	fromHyp, process, property string, links []kpiLink, kpiID, kpiLabel string,
) []BridgeCandidate {
	for _, link := range links {
		if link.hypID == fromHyp {
			continue // a bridge must cross two different sources
		}
		kpiName := bridgeKPIName(link, kpiID, kpiLabel)
		dedup := strings.ToLower(process + "|" + property + "|" + kpiName)
		if _, dup := seen[dedup]; dup {
			continue
		}
		seen[dedup] = struct{}{}
		out = append(out, BridgeCandidate{
			Statement:      bridgeStatement(process, property, kpiName, fromHyp, link.hypID),
			Process:        process,
			Property:       property,
			KPI:            kpiName,
			FromHypothesis: fromHyp,
			ViaHypothesis:  link.hypID,
			Sources:        bridgeSources(fromHyp, link.hypID),
		})
		if len(out) >= kgMaxBridges {
			return out
		}
	}
	return out
}

// bridgeKPIName resolves the KPI node label: the requested KPI's title when the
// link points at it, otherwise the link's own name with the "kpi:" anchor
// stripped so the sentence stays readable.
func bridgeKPIName(link kpiLink, kpiID, kpiLabel string) string {
	if kpiLabel != "" && link.kpiID == kpiID {
		return kpiLabel
	}
	return strings.TrimPrefix(link.kpiName, "kpi:")
}

// bridgeStatement renders the templated Russian research direction.
func bridgeStatement(process, property, kpi, fromHyp, viaHyp string) string {
	var b strings.Builder
	b.WriteString("Применить процесс «")
	b.WriteString(process)
	b.WriteString("»")
	if fromHyp != "" {
		b.WriteString(" (из работы ")
		b.WriteString(shortID(fromHyp))
		b.WriteByte(')')
	}
	b.WriteString(" для улучшения свойства «")
	b.WriteString(property)
	b.WriteString("», влияющего на KPI «")
	b.WriteString(kpi)
	b.WriteString("»")
	if viaHyp != "" {
		b.WriteString(" (из работы ")
		b.WriteString(shortID(viaHyp))
		b.WriteByte(')')
	}
	b.WriteByte('.')
	return b.String()
}

// bridgeSources returns the de-duplicated, sorted hypothesis ids a bridge draws on.
func bridgeSources(ids ...string) []string {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// shortID renders a compact id for prompt/statement readability (first segment of
// a UUID, or the whole id when short).
func shortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// GraphHypotheses loads the owner's knowledge graph and synthesises cross-
// hypothesis bridge directions for a target KPI: a process→property link from one
// source recombined with a property→KPI link from another. It is fully defensive:
// no store, an empty/tiny graph, a forbidden KPI or any store error yields an
// empty (never nil) slice rather than an error, so a click on a fresh portfolio
// degrades to "no bridges yet" instead of failing. The candidates are draft
// directions and are NOT persisted here.
func (s *HypothesisService) GraphHypotheses(ctx context.Context, ownerID, kpiID string) ([]BridgeCandidate, error) {
	empty := []BridgeCandidate{}
	if s.graph == nil {
		return empty, nil
	}
	// Verify ownership and resolve the KPI label; a forbidden/missing KPI still
	// yields bridges (anchored by id), so the view never 404s on a transient miss.
	var kpiLabel string
	if kpi, err := s.GetKPI(ctx, ownerID, kpiID); err == nil {
		kpiLabel = kpiNodeLabel(kpi)
	}
	// The graph read is best-effort: a store error degrades to "no bridges" rather
	// than failing the click, so the error is intentionally discarded here.
	edges, _ := s.graph.Edges(ctx, ownerID)
	if len(edges) == 0 {
		return empty, nil
	}
	if bridges := findBridges(edges, kpiID, kpiLabel); len(bridges) > 0 {
		return bridges, nil
	}
	return empty, nil
}

// kpiNodeLabel is the human label for a KPI node: its title, with the metric
// appended when present, so a bridge statement reads naturally.
func kpiNodeLabel(kpi *commonv1.KPI) string {
	title := compactText(kpi.GetTitle())
	if m := compactText(kpi.GetMetric()); m != "" && title != "" {
		return title + " (" + m + ")"
	}
	return title
}
