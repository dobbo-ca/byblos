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

// TestSpreadsAreMeasuredFromRepeatedRuns pins that a baseline built from more
// than one run of the SAME commit records how much each pair moved when
// nothing changed.
func TestSpreadsAreMeasuredFromRepeatedRuns(t *testing.T) {
	runs := []Run{
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1000}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1002}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 999}}},
	}
	b := BaselineFromRuns(runs, fixtureFingerprint())

	got := b.Spreads[MetricSize]["linearize"]
	want := (1002.0 - 999.0) / 1002.0 * 100 // 0.2994%
	if got < want-0.0001 || got > want+0.0001 {
		t.Errorf("spread = %v, want %v", got, want)
	}
	if b.Totals[MetricSize]["linearize"] != 1000 {
		t.Errorf("total = %v, want the FIRST run's 1000, not an average",
			b.Totals[MetricSize]["linearize"])
	}
}

// TestDeltaInsideTheSpreadScoresZero is the whole point of the spread: a
// candidate that changed nothing must not pass on measurement jitter.
func TestDeltaInsideTheSpreadScoresZero(t *testing.T) {
	runs := []Run{
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1000}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1005}}},
	}
	b := BaselineFromRuns(runs, fixtureFingerprint())

	// 0.2% better, well inside the ~0.5% spread those two runs showed.
	head := Run{Samples: []Sample{{Capability: "linearize", OutBytes: 998}}}
	got := Score(b, head)

	if got.Score != 0 {
		t.Errorf("Score = %v, want 0: a delta inside the spread is noise", got.Score)
	}
	if got.Pass {
		t.Error("a candidate passed on noise")
	}
	if len(got.Findings) != 1 || !got.Findings[0].Noise {
		t.Errorf("the finding was not marked as noise: %+v", got.Findings)
	}
}

// TestDeltaOutsideTheSpreadStillScores pins that the floor suppresses jitter
// without suppressing a real win.
func TestDeltaOutsideTheSpreadStillScores(t *testing.T) {
	runs := []Run{
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1000}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1001}}},
	}
	b := BaselineFromRuns(runs, fixtureFingerprint())

	head := Run{Samples: []Sample{{Capability: "linearize", OutBytes: 900}}} // -10%
	got := Score(b, head)

	if got.Findings[0].Noise {
		t.Error("a 10% win was suppressed as noise")
	}
	if got.Score <= 0 {
		t.Errorf("Score = %v, want positive for a real 10%% size win", got.Score)
	}
}

// TestAbsentSpreadMeansZeroTolerance pins the safe direction: a pair with no
// measured spread scores its delta rather than being silently forgiven.
func TestAbsentSpreadMeansZeroTolerance(t *testing.T) {
	b := BaselineFrom(Run{Samples: []Sample{{Capability: "jbig2-generic", OutBytes: 1000}}},
		fixtureFingerprint())
	if len(b.Spreads) != 0 {
		t.Fatalf("a single-run baseline recorded spreads: %v", b.Spreads)
	}
	head := Run{Samples: []Sample{{Capability: "jbig2-generic", OutBytes: 999}}}
	got := Score(b, head)
	if got.Findings[0].Noise {
		t.Error("a pair with no measured spread was treated as noise")
	}
	if got.Score <= 0 {
		t.Errorf("Score = %v, want positive", got.Score)
	}
}

// TestATinyPositiveScoreCannotPass is the regression test for the failure this
// gate was added for: a baseline built from three runs, scored against an
// unmeasured fourth run of the SAME commit, produced +0.001 and passed.
//
// Many pairs each drifting below their own noise floor can still sum to a
// positive total, so suppressing jitter per pair is not sufficient on its own.
func TestATinyPositiveScoreCannotPass(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 1000000},
	}}, fixtureFingerprint())

	// 0.001% better: a real, non-noise delta, but far too small to act on.
	head := Run{Samples: []Sample{{Capability: "jbig2-generic", OutBytes: 999990}}}
	got := Score(base, head)

	if got.Score <= 0 {
		t.Fatalf("Score = %v; this fixture must be positive for the test to mean anything", got.Score)
	}
	if got.Score >= MinimumScore {
		t.Fatalf("Score = %v is above MinimumScore %v; pick a smaller delta", got.Score, MinimumScore)
	}
	if got.Pass {
		t.Errorf("a score of %v passed; MinimumScore is %v", got.Score, MinimumScore)
	}
}

// TestNoiseMarginWidensTheBand pins that the recorded spread is multiplied
// before it is believed, because a min-to-max band over N runs under-estimates
// the true one.
func TestNoiseMarginWidensTheBand(t *testing.T) {
	if NoiseMargin <= 1 {
		t.Fatalf("NoiseMargin = %v; a margin of 1 or less does not widen anything", NoiseMargin)
	}
	runs := []Run{
		{Samples: []Sample{{Capability: "linearize", OutBytes: 10000}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 10001}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 10000}}},
	}
	b := BaselineFromRuns(runs, fixtureFingerprint())
	spread := b.Spreads[MetricSize]["linearize"] // ~0.01%

	// A delta outside the raw spread but inside spread*NoiseMargin.
	head := Run{Samples: []Sample{{Capability: "linearize", OutBytes: 9998}}} // -0.02%
	got := Score(b, head)

	if abs(got.Findings[0].DeltaPercent) <= spread {
		t.Fatal("fixture delta is inside the raw spread; it must be outside for this test to bite")
	}
	if !got.Findings[0].Noise {
		t.Errorf("delta %.4f%% was scored despite a spread of %.4f%% and a margin of %v",
			got.Findings[0].DeltaPercent, spread, NoiseMargin)
	}
}
