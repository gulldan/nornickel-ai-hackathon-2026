package kgstore

import (
	"context"
	"testing"
	"time"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/jsonx"
)

// fakeKV is an in-memory JSONStore that round-trips through the JSON facade, so
// the test exercises real (un)marshalling of the edge list.
type fakeKV struct{ data map[string][]byte }

func (f *fakeKV) SetJSON(_ context.Context, key string, v any, _ time.Duration) error {
	b, err := jsonx.Marshal(v)
	if err != nil {
		return err
	}
	f.data[key] = b
	return nil
}

func (f *fakeKV) GetJSON(_ context.Context, key string, dest any) (bool, error) {
	b, ok := f.data[key]
	if !ok {
		return false, nil
	}
	return true, jsonx.Unmarshal(b, dest)
}

func edge(from, rel, to, hyp string) application.KGEdge {
	return application.KGEdge{
		FromType: "process", FromName: from, Relation: rel, ToType: "property", ToName: to,
		HypothesisID: hyp, KPIID: "k1",
	}
}

// A miss yields an empty (non-nil) edge list, and AddEdges then Edges round-trips.
func TestStore_RoundTrip(t *testing.T) {
	kv := &fakeKV{data: map[string][]byte{}}
	s := New(kv)

	got, err := s.Edges(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("Edges on miss: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("miss should yield empty non-nil slice, got %#v", got)
	}

	want := []application.KGEdge{
		edge("X", "affects_property", "Y", "A"),
		edge("Y", "supports_kpi", "Z", "B"),
	}
	if aerr := s.AddEdges(context.Background(), "owner-1", want); aerr != nil {
		t.Fatalf("AddEdges: %v", aerr)
	}
	if _, ok := kv.data[graphKeyPrefix+"owner-1"]; !ok {
		t.Fatalf("expected key %q to be written", graphKeyPrefix+"owner-1")
	}
	got, err = s.Edges(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("round-trip mismatch: want 2 edges, got %d (%+v)", len(got), got)
	}
}

// AddEdges de-duplicates against stored edges so re-generation never piles up
// duplicate triples.
func TestStore_AddEdgesDedups(t *testing.T) {
	kv := &fakeKV{data: map[string][]byte{}}
	s := New(kv)
	e := edge("X", "affects_property", "Y", "A")

	if err := s.AddEdges(context.Background(), "owner-1", []application.KGEdge{e}); err != nil {
		t.Fatalf("AddEdges #1: %v", err)
	}
	// Re-add the same edge plus one new one.
	if err := s.AddEdges(context.Background(), "owner-1", []application.KGEdge{
		e, edge("Y", "supports_kpi", "Z", "B"),
	}); err != nil {
		t.Fatalf("AddEdges #2: %v", err)
	}
	got, err := s.Edges(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 de-duplicated edges, got %d (%+v)", len(got), got)
	}
}

// AddEdges with nothing to add is a no-op (no write, no error).
func TestStore_AddEmptyNoOp(t *testing.T) {
	kv := &fakeKV{data: map[string][]byte{}}
	s := New(kv)
	if err := s.AddEdges(context.Background(), "owner-1", nil); err != nil {
		t.Fatalf("AddEdges(nil): %v", err)
	}
	if len(kv.data) != 0 {
		t.Fatalf("empty add must not write, got %d keys", len(kv.data))
	}
}
