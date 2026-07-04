package settingsstore

import (
	"context"
	"testing"
	"time"

	"github.com/example/main-service/internal/application"
	"github.com/example/main-service/internal/platform/jsonx"
)

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
	want := application.DefaultHypothesisRuntimeSettings()
	if got == nil || *got != want {
		t.Fatalf("miss should yield defaults, got %+v", got)
	}
}

func TestStore_RoundTripAndClamp(t *testing.T) {
	kv := &fakeKV{data: map[string][]byte{}}
	s := New(kv)
	in := application.HypothesisRuntimeSettings{
		DefaultGenerateCount:   20,
		ClusterGenerateCount:   2,
		DirectionGenerateCount: 4,
		GenerationTimeoutSec:   240,
		ReadyTRLMin:            3,
		ReadyScoreMin:          60,
		RiskScoreMin:           80,
		GraphDirectionLimit:    30,
	}
	if err := s.Set(context.Background(), "owner-1", in); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok := kv.data[settingsKeyPrefix+"owner-1"]; !ok {
		t.Fatalf("expected key %q to be written", settingsKeyPrefix+"owner-1")
	}
	got, err := s.Get(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := application.NormalizeHypothesisRuntimeSettings(in)
	if got == nil || *got != want {
		t.Fatalf("round-trip mismatch: want %+v, got %+v", want, got)
	}
}
