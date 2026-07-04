package application

import (
	"testing"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// The confidence split must separate "how sure we are of the verdict" from "how
// much we now believe the hypothesis", and must never leave a stale high
// confidence when the corpus could not decide.
func TestConfidenceSplit(t *testing.T) {
	ev := []*commonv1.HypothesisEvidence{evDoc("supports", "a"), evDoc("contradicts", "b")}
	refuted := confidenceSplit(verdictRefuted, f64(0.9), ev)
	if refuted.VerdictConf < 0.89 {
		t.Fatalf("refuted must keep a high verdict confidence, got %.3f", refuted.VerdictConf)
	}
	if refuted.Belief > 0.2 {
		t.Fatalf("refuted ⇒ low belief, got %.3f", refuted.Belief)
	}
	if refuted.Unverified {
		t.Fatal("refuted is a decision, not unverified")
	}

	insufficient := confidenceSplit(verdictInsufficient, f64(0.8), nil)
	if !insufficient.Unverified {
		t.Fatal("insufficient must flag the card unverified")
	}
	if insufficient.Belief > 0.3 {
		t.Fatalf("insufficient must not inherit a high belief, got %.3f", insufficient.Belief)
	}
}

// A grounded contradiction downgrades a clean verdict to "mixed".
func TestReconcileVerdict(t *testing.T) {
	supportingOnly := []*commonv1.HypothesisEvidence{evDoc("supports", "a")}
	if got := reconcileVerdict(verdictSupported, supportingOnly); got != verdictSupported {
		t.Fatalf("no contradiction ⇒ keep supported, got %s", got)
	}
	withContra := []*commonv1.HypothesisEvidence{evDoc("supports", "a"), evDoc("contradicts", "b")}
	if got := reconcileVerdict(verdictSupported, withContra); got != verdictMixed {
		t.Fatalf("grounded contradiction ⇒ mixed, got %s", got)
	}
}
