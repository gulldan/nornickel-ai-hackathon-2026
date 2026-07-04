package scoringstore

import (
	"context"
	"testing"
	"time"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/jsonx"
)

// fakeKV is an in-memory JSONStore that round-trips through the JSON facade, so
// the test exercises real (un)marshalling of ScoringWeights.
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

func TestStore_DefaultsOnMiss(t *testing.T) {
	s := New(&fakeKV{data: map[string][]byte{}})
	got, err := s.Get(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || *got != application.DefaultWeights() {
		t.Fatalf("miss should yield default weights, got %+v", got)
	}
}

func TestStore_RoundTrip(t *testing.T) {
	kv := &fakeKV{data: map[string][]byte{}}
	s := New(kv)
	want := application.ScoringWeights{
		KPIFit: 0.4, Evidence: 0.1, Novelty: 0.1, Value: 0.2, RiskInv: 0.1, TRLFit: 0.1,
	}
	if err := s.Set(context.Background(), "owner-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok := kv.data[weightsKeyPrefix+"owner-1"]; !ok {
		t.Fatalf("expected key %q to be written", weightsKeyPrefix+"owner-1")
	}
	got, err := s.Get(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || *got != want {
		t.Fatalf("round-trip mismatch: want %+v, got %+v", want, got)
	}
}
