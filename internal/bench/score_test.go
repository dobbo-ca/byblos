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
