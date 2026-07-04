package application

import (
	"context"
	"strings"
	"unicode/utf8"
)

// excludedDirectionsMax bounds the free-form exclusion note so it never
// crowds out the actual instruction in the generation prompt.
const excludedDirectionsMax = 600

// HypothesisRuntimeSettings are owner-editable knobs for hypothesis factory UX
// and defaults. They intentionally stay small and numeric so the UI can explain
// every value and the server can clamp unsafe inputs deterministically.
// ExcludedDirections is the one free-form knob: доменные ограничения эксперта —
// направления, которые фабрика не должна предлагать.
type HypothesisRuntimeSettings struct {
	DefaultGenerateCount   int    `json:"default_generate_count"`
	ClusterGenerateCount   int    `json:"cluster_generate_count"`
	DirectionGenerateCount int    `json:"direction_generate_count"`
	GenerationTimeoutSec   int    `json:"generation_timeout_sec"`
	ReadyTRLMin            int    `json:"ready_trl_min"`
	ReadyScoreMin          int    `json:"ready_score_min"`
	RiskScoreMin           int    `json:"risk_score_min"`
	GraphDirectionLimit    int    `json:"graph_direction_limit"`
	DeepPostprocessEnabled bool   `json:"deep_postprocess_enabled"`
	ExcludedDirections     string `json:"excluded_directions"`
}

// DefaultHypothesisRuntimeSettings is the baseline used before an owner edits
// settings. Frontend defaults mirror this value and the API remains authoritative.
func DefaultHypothesisRuntimeSettings() HypothesisRuntimeSettings {
	return HypothesisRuntimeSettings{
		DefaultGenerateCount:   5,
		ClusterGenerateCount:   3,
		DirectionGenerateCount: 3,
		GenerationTimeoutSec:   300,
		ReadyTRLMin:            4,
		ReadyScoreMin:          55,
		RiskScoreMin:           70,
		GraphDirectionLimit:    5,
		DeepPostprocessEnabled: false,
	}
}

// RuntimeSettingsStore persists per-owner factory settings; a nil store or a
// miss means DefaultHypothesisRuntimeSettings applies.
type RuntimeSettingsStore interface {
	Get(ctx context.Context, ownerID string) (*HypothesisRuntimeSettings, error)
	Set(ctx context.Context, ownerID string, settings HypothesisRuntimeSettings) error
}

// NormalizeHypothesisRuntimeSettings clamps each value to the supported range,
// using the default when a value is omitted/zero or otherwise invalid.
func NormalizeHypothesisRuntimeSettings(in HypothesisRuntimeSettings) HypothesisRuntimeSettings {
	def := DefaultHypothesisRuntimeSettings()
	return HypothesisRuntimeSettings{
		DefaultGenerateCount:   clampIntSetting(in.DefaultGenerateCount, genMinCount, genMaxCount, def.DefaultGenerateCount),
		ClusterGenerateCount:   clampIntSetting(in.ClusterGenerateCount, genMinCount, genMaxCount, def.ClusterGenerateCount),
		DirectionGenerateCount: clampIntSetting(in.DirectionGenerateCount, 1, genMaxCount, def.DirectionGenerateCount),
		GenerationTimeoutSec:   clampIntSetting(in.GenerationTimeoutSec, 30, 600, def.GenerationTimeoutSec),
		ReadyTRLMin:            clampIntSetting(in.ReadyTRLMin, 1, 9, def.ReadyTRLMin),
		ReadyScoreMin:          clampIntRange(in.ReadyScoreMin, 0, 100),
		RiskScoreMin:           clampIntRange(in.RiskScoreMin, 0, 100),
		GraphDirectionLimit:    clampIntSetting(in.GraphDirectionLimit, 1, 20, def.GraphDirectionLimit),
		DeepPostprocessEnabled: in.DeepPostprocessEnabled,
		ExcludedDirections:     truncateRunes(strings.TrimSpace(in.ExcludedDirections), excludedDirectionsMax),
	}
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

func clampIntSetting(v, lo, hi, fallback int) int {
	if v == 0 {
		v = fallback
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampIntRange(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
