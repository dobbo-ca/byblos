package bench

import "testing"

// scoringBaseline is three capabilities with known size shares: 0.6, 0.3, 0.1.
func scoringBaseline() Baseline {
	return BaselineFrom(Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 100},
	}}, fixtureFingerprint())
}

// TestScoreIsTheWeightedPercentImprovement pins the arithmetic of design spec
// section 6. jbig2-generic drops 600 -> 540, a 10% improvement on a capability
// holding 0.6 of the size share, with size weighted 0.40:
//
//	0.40 x 0.6 x 10 = 2.4
func TestScoreIsTheWeightedPercentImprovement(t *testing.T) {
	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 540},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 100},
	}}
	got := Score(scoringBaseline(), head)
	if got.Score < 2.3999 || got.Score > 2.4001 {
		t.Errorf("Score = %v, want 2.4", got.Score)
	}
	if !got.Pass {
		t.Error("a 10% size win on the dominant capability did not pass")
	}
}

// TestScoreChargesForARegression pins that a metric getting worse subtracts.
// build-pdf grows 100 -> 105, 5% worse on a 0.1 share: 0.40 x 0.1 x -5 = -0.2.
func TestScoreChargesForARegression(t *testing.T) {
	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 105},
	}}
	got := Score(scoringBaseline(), head)
	if got.Score > -0.1999 || got.Score < -0.2001 {
		t.Errorf("Score = %v, want -0.2", got.Score)
	}
	if got.Pass {
		t.Error("a net regression passed")
	}
}

// TestCeilingFailsRegardlessOfScore pins rule 1: a single metric worse than
// +10% fails even when the weighted sum is strongly positive.
func TestCeilingFailsRegardlessOfScore(t *testing.T) {
	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 300}, // -50%, a huge win
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 200}, // +100%, over the ceiling
	}}
	got := Score(scoringBaseline(), head)
	if got.Score <= 0 {
		t.Fatalf("Score = %v; this fixture must have a positive sum for the test to mean anything", got.Score)
	}
	if got.Pass {
		t.Error("a candidate over the regression ceiling passed")
	}
	if len(got.Ceilings) != 1 {
		t.Fatalf("got %d ceiling breaches, want 1", len(got.Ceilings))
	}
	if got.Ceilings[0].Capability != "build-pdf" {
		t.Errorf("ceiling breach on %q, want build-pdf", got.Ceilings[0].Capability)
	}
}

// TestUnmeasuredMetricIsSkippedNotZeroed pins that a metric absent from head --
// a disk counter on a machine with no /proc/self/io -- contributes nothing,
// rather than reading as a 100% improvement to zero.
func TestUnmeasuredMetricIsSkippedNotZeroed(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600, WChar: 4096, DiskCounters: true},
	}}, fixtureFingerprint())

	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600, DiskCounters: false},
	}}
	got := Score(base, head)
	if got.Score != 0 {
		t.Errorf("Score = %v, want 0: an unmeasured metric must not score", got.Score)
	}
}

func TestScoreOfAnIdenticalRunIsZero(t *testing.T) {
	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 100},
	}}
	if got := Score(scoringBaseline(), head); got.Score != 0 {
		t.Errorf("Score = %v, want 0 for an identical run", got.Score)
	}
}

// TestScoreOfExactlyZeroFails pins that Pass requires a net positive score: a
// purely neutral candidate has not proposed an improvement, and the routine
// exists to propose improvements.
func TestScoreOfExactlyZeroFails(t *testing.T) {
	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 100},
	}}
	got := Score(scoringBaseline(), head)
	if got.Pass {
		t.Error("a score of exactly 0 passed; Pass must require Score > 0")
	}
}

// TestRegressionCeilingBoundaryIsExclusive pins the wording of design spec
// section 6 rule 1, "worse than +10%": a delta of exactly +10% does not breach
// the ceiling.
func TestRegressionCeilingBoundaryIsExclusive(t *testing.T) {
	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 110}, // exactly +10%
	}}
	got := Score(scoringBaseline(), head)
	if len(got.Ceilings) != 0 {
		t.Errorf("got %d ceiling breaches at exactly +10%%, want 0", len(got.Ceilings))
	}
}

// TestScoreSkipsAZeroBaseline pins that a capability whose baseline reading is
// zero -- inspect and extract-raster read zero for size, spec section 3.3 --
// is skipped rather than dividing by zero.
func TestScoreSkipsAZeroBaseline(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "inspect", OutBytes: 0},
		{Capability: "jbig2-generic", OutBytes: 600},
	}}, fixtureFingerprint())

	head := Run{Samples: []Sample{
		{Capability: "inspect", OutBytes: 0},
		{Capability: "jbig2-generic", OutBytes: 600},
	}}
	got := Score(base, head)
	for _, f := range got.Findings {
		if f.Capability == "inspect" {
			t.Errorf("got a finding for a zero-baseline capability: %+v", f)
		}
	}
}

// TestScoreSumsSamplesAcrossDocuments pins that a capability's total is summed
// over every document in the corpus, not just the last one measured -- the
// corpus is more than one document per capability in production.
func TestScoreSumsSamplesAcrossDocuments(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "jbig2-generic", Doc: "a.pdf", OutBytes: 300},
		{Capability: "jbig2-generic", Doc: "b.pdf", OutBytes: 300},
	}}, fixtureFingerprint())

	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", Doc: "a.pdf", OutBytes: 270},
		{Capability: "jbig2-generic", Doc: "b.pdf", OutBytes: 270},
	}}
	got := Score(base, head)
	if len(got.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(got.Findings))
	}
	f := got.Findings[0]
	if f.Base != 600 || f.Head != 540 {
		t.Errorf("base/head = %v/%v, want 600/540 -- both documents must be summed", f.Base, f.Head)
	}
}

// TestOverrideMultipliesContribution pins design spec section 3.2: a target's
// hand multiplier must actually reach the score, not just be validated for
// having a reason.
func TestOverrideMultipliesContribution(t *testing.T) {
	old := Targets
	Targets = append([]Target{{
		Capability: "jbig2-generic",
		Override:   2.0,
		Why:        "test",
	}}, old...)
	t.Cleanup(func() { Targets = old })

	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 540},
		{Capability: "quantize-png", OutBytes: 300},
		{Capability: "build-pdf", OutBytes: 100},
	}}
	got := Score(scoringBaseline(), head)
	// Same fixture as TestScoreIsTheWeightedPercentImprovement, doubled by the
	// override: 0.40 x 0.6 x 10 x 2.0 = 4.8.
	if got.Score < 4.7999 || got.Score > 4.8001 {
		t.Errorf("Score = %v, want 4.8 with a 2.0 override applied", got.Score)
	}
}
