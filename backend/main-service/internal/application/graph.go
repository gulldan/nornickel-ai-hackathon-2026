// Evidence graph for the Hypothesis Factory: the data behind the
// "check against the knowledge base" view. Every hypothesis and every source
// document it cites become nodes; each piece of evidence becomes a typed edge
// (supports / contradicts / context). Hypotheses are linked
// implicitly through the document nodes they share. This is intentionally
// separate from the typed knowledge graph in kg.go (material/process/property/KPI
// triples used to suggest bridge directions). Built from existing list/evidence
// reads (no new query), which is fine for board-sized portfolios.

package application

import (
	"context"
	"encoding/json"

	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
)

// graphMaxHypotheses caps how many hypotheses the graph spans (a backstop, not a
// real-portfolio limit). A dedicated join query is the later optimisation.
const graphMaxHypotheses = 1000

// GraphNode is one node: a hypothesis or a cited source document.
type GraphNode struct {
	ID      string // hypothesis id, or document id ("file:<name>" when id absent)
	Kind    string // "hypothesis" | "document"
	Label   string
	Status  string // hypotheses: lifecycle status
	TRL     *int32 // hypotheses: readiness 1..9
	Verdict string // hypotheses: corpus-check verdict, if checked
	Degree  int    // documents: how many hypotheses cite it
	KPIID   string // hypotheses: bound KPI (lets the UI filter the graph by goal)
}

// GraphEdge is an evidence link: hypothesis → document, classed by evidence stance.
type GraphEdge struct {
	Source  string // hypothesis id
	Target  string // document node id
	Class   string // supports | contradicts | context
	Score   *float64
	ChunkID string // cited chunk (lets the UI open the source at the right place)
}

// HypothesisGraph is the owner's full citation graph.
type HypothesisGraph struct {
	Nodes []GraphNode
	Edges []GraphEdge
}

// Graph builds the owner's citation graph from their hypotheses and evidence.
func (s *HypothesisService) Graph(ctx context.Context, ownerID string) (*HypothesisGraph, error) {
	hyps, err := s.cat.ListHypotheses(ctx, &dbv1.ListHypothesesRequest{
		OwnerId: ownerID, Limit: graphMaxHypotheses,
	})
	if err != nil {
		return nil, err
	}

	g := &HypothesisGraph{}
	docDegree := make(map[string]int)
	docLabel := make(map[string]string)
	for _, h := range hyps {
		g.Nodes = append(g.Nodes, GraphNode{
			ID: h.GetId(), Kind: "hypothesis", Label: h.GetTitle(),
			Status: h.GetStatus(), TRL: h.Trl, Verdict: checkVerdict(h.GetAssessment()),
			KPIID: h.GetKpiId(),
		})
		evs, eerr := s.cat.ListHypothesisEvidence(ctx, h.GetId())
		if eerr != nil {
			continue // a hypothesis with unreadable evidence still shows as a node
		}
		for _, e := range evs {
			docID := e.GetDocumentId()
			if docID == "" {
				if e.GetFilename() == "" {
					continue // nothing to anchor the node on
				}
				docID = "file:" + e.GetFilename() // group by filename when id is absent
			}
			if _, seen := docLabel[docID]; !seen {
				docLabel[docID] = e.GetFilename()
			}
			docDegree[docID]++
			score := e.Score
			g.Edges = append(g.Edges, GraphEdge{
				Source: h.GetId(), Target: docID, Class: e.GetStance(),
				Score: score, ChunkID: e.GetChunkId(),
			})
		}
	}
	for id, deg := range docDegree {
		label := docLabel[id]
		if label == "" {
			label = id
		}
		g.Nodes = append(g.Nodes, GraphNode{ID: id, Kind: "document", Label: label, Degree: deg})
	}
	return g, nil
}

// checkVerdict reads assessment.check.verdict (set by Verify), or "" if unchecked.
func checkVerdict(assessment string) string {
	if assessment == "" {
		return ""
	}
	var m struct {
		Check struct {
			Verdict string `json:"verdict"`
		} `json:"check"`
	}
	if err := json.Unmarshal([]byte(assessment), &m); err != nil {
		return ""
	}
	return m.Check.Verdict
}
