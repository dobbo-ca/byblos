package bench

import (
	"errors"
	"strings"
	"testing"
)

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

// TestLatencyScoresNonZeroWithFreshBase proves that latency contributes to the
// score when a fresh base run is provided, and zero when it is not.
func TestLatencyScoresNonZeroWithFreshBase(t *testing.T) {
	// Baseline from multiple runs, so it has latency spreads.
	base := BaselineFromRuns([]Run{
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1000, WallNS: []int64{100_000_000}}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1000, WallNS: []int64{105_000_000}}}},
		{Samples: []Sample{{Capability: "linearize", OutBytes: 1000, WallNS: []int64{102_000_000}}}},
	}, fixtureFingerprint())

	if _, ok := base.Totals[MetricLatency]; ok {
		t.Fatal("baseline has MetricLatency in Totals; this test requires it absent")
	}
	if _, ok := base.Spreads[MetricLatency]; !ok {
		t.Fatal("baseline missing MetricLatency spreads; this test requires them present")
	}

	// Head is 20% faster (100ms -> 80ms), outside the noise band.
	head := Run{Samples: []Sample{
		{Capability: "linearize", OutBytes: 1000, WallNS: []int64{80_000_000}},
	}}

	// Without fresh base: latency contributes zero and is tracked as unscored.
	withoutFresh := Score(base, head)
	var latencyContribution float64
	for _, f := range withoutFresh.Findings {
		if f.Metric == MetricLatency {
			latencyContribution += f.Contribution
		}
	}
	if latencyContribution != 0 {
		t.Errorf("latency contributed %v without fresh base, want 0", latencyContribution)
	}
	var foundUnscored bool
	for _, u := range withoutFresh.UnscoredPairs {
		if u.Metric == MetricLatency && u.Reason == "no_baseline_value" {
			foundUnscored = true
			break
		}
	}
	if !foundUnscored {
		t.Error("latency not tracked in UnscoredPairs without fresh base")
	}

	// With fresh base: latency scores non-zero (baseline provides spread, fresh
	// base provides value).
	freshBase := Run{Samples: []Sample{
		{Capability: "linearize", OutBytes: 1000, WallNS: []int64{100_000_000}},
	}}
	withFresh := Score(base, head, freshBase)
	latencyContribution = 0
	for _, f := range withFresh.Findings {
		if f.Metric == MetricLatency {
			latencyContribution += f.Contribution
		}
	}
	if latencyContribution <= 0 {
		t.Errorf("latency contributed %v with fresh base, want positive", latencyContribution)
	}
	for _, u := range withFresh.UnscoredPairs {
		if u.Metric == MetricLatency {
			t.Errorf("latency still in UnscoredPairs with fresh base: %+v", u)
		}
	}
}

// TestFreshBasePreferredOnlyForCoveredCapabilities proves that the fresh run
// is used only for the capabilities it measured, and stored Totals still serve
// the others.
func TestFreshBasePreferredOnlyForCoveredCapabilities(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600},
		{Capability: "quantize-png", OutBytes: 300},
	}}, fixtureFingerprint())

	// Fresh base measures only jbig2-generic at a different value.
	freshBase := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 650},
	}}

	head := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 650}, // matches fresh base
		{Capability: "quantize-png", OutBytes: 300},  // matches stored Totals
	}}

	got := Score(base, head, freshBase)
	for _, f := range got.Findings {
		switch f.Capability {
		case "jbig2-generic":
			// Fresh base (650) was used, so delta is 0.
			if f.Base != 650 {
				t.Errorf("jbig2-generic base = %v, want 650 from fresh run", f.Base)
			}
			if f.DeltaPercent != 0 {
				t.Errorf("jbig2-generic delta = %v, want 0 (head matches fresh base)", f.DeltaPercent)
			}
		case "quantize-png":
			// Stored Totals (300) was used, so delta is 0.
			if f.Base != 300 {
				t.Errorf("quantize-png base = %v, want 300 from stored Totals", f.Base)
			}
			if f.DeltaPercent != 0 {
				t.Errorf("quantize-png delta = %v, want 0 (head matches stored Totals)", f.DeltaPercent)
			}
		}
	}
}

// TestUnchangedHeadFailsWithFreshBase proves that a fresh base run of the SAME
// commit as head still fails, not scoring jitter as a win. This is the trap the
// repo fell into: a baseline from three runs scored against an unmeasured
// fourth run of the same commit passed. The fresh-base path must not reopen it.
func TestUnchangedHeadFailsWithFreshBase(t *testing.T) {
	// Baseline from three runs, with spreads recorded. Includes latency with
	// realistic same-commit jitter: build-pdf measured 25.6-30.4ms (15.59%)
	// across four runs.
	runs := []Run{
		{Samples: []Sample{{Capability: "build-pdf", OutBytes: 1000,
			WallNS: []int64{25_600_000, 26_000_000, 25_800_000}}}},
		{Samples: []Sample{{Capability: "build-pdf", OutBytes: 1002,
			WallNS: []int64{27_100_000, 26_900_000, 27_000_000}}}},
		{Samples: []Sample{{Capability: "build-pdf", OutBytes: 999,
			WallNS: []int64{30_400_000, 30_100_000, 30_200_000}}}},
	}
	base := BaselineFromRuns(runs, fixtureFingerprint())

	// Fresh base is the fourth run at 28ms (within [25.6, 30.4]).
	freshBase := Run{Samples: []Sample{{Capability: "build-pdf", OutBytes: 1001,
		WallNS: []int64{28_000_000, 28_200_000, 27_900_000}}}}
	// Head is slightly faster at 27ms. Delta is -3.57%, which is noise (spread
	// 15.59% x margin 3 = 47.7%), but without noise suppression would score as
	// a positive contribution.
	head := Run{Samples: []Sample{{Capability: "build-pdf", OutBytes: 1001,
		WallNS: []int64{27_000_000}}}}

	got := Score(base, head, freshBase)
	if got.Score >= MinimumScore {
		t.Errorf("Score = %v, want < %v: an unchanged head must not pass", got.Score, MinimumScore)
	}
	if got.Pass {
		t.Error("an unchanged head passed with a fresh base run")
	}
}

// TestStaleHarnessRefusedEvenWithFreshBase proves that a baseline whose harness
// sha differs is still refused by Validate, even when a fresh base run is
// provided. A fresh base recovers same-runner latency and touched-capability
// re-measurement, but it does not repair a comparison between different harness
// versions.
func TestStaleHarnessRefusedEvenWithFreshBase(t *testing.T) {
	base := scoringBaseline()
	base.Fingerprint.HarnessSHA256 = "old-harness-sha"

	current := fixtureFingerprint()
	current.HarnessSHA256 = "new-harness-sha"

	err := base.Validate(current, func(commit string) bool { return true })
	if err == nil {
		t.Fatal("Validate did not refuse a stale harness sha")
	}
	if !errors.Is(err, ErrStaleBaseline) {
		t.Errorf("got error %v, want ErrStaleBaseline", err)
	}
	// The message must be actionable.
	if !strings.Contains(err.Error(), "harness changed") {
		t.Errorf("error message does not mention harness: %v", err)
	}
}

// TestStaleBenchSetRefusedEvenWithFreshBase proves that a baseline whose bench
// set sha differs is still refused, even with a fresh base run. A fresh base
// does not repair a comparison between different document sets.
func TestStaleBenchSetRefusedEvenWithFreshBase(t *testing.T) {
	base := scoringBaseline()
	base.Fingerprint.BenchSetSHA256 = "old-set-sha"

	current := fixtureFingerprint()
	current.BenchSetSHA256 = "new-set-sha"

	err := base.Validate(current, func(commit string) bool { return true })
	if err == nil {
		t.Fatal("Validate did not refuse a stale bench set sha")
	}
	if !errors.Is(err, ErrStaleBaseline) {
		t.Errorf("got error %v, want ErrStaleBaseline", err)
	}
	if !strings.Contains(err.Error(), "bench set") {
		t.Errorf("error message does not mention bench set: %v", err)
	}
}

// TestFreshBaseLatencyWithSpreadUsesNoiseMargin proves that when the baseline
// has a measured spread for latency (from multiple runs), the fresh-base path
// uses it with NoiseMargin like any other metric.
func TestFreshBaseLatencyWithSpreadUsesNoiseMargin(t *testing.T) {
	runs := []Run{
		{Samples: []Sample{{Capability: "linearize", WallNS: []int64{10000000}}}},
		{Samples: []Sample{{Capability: "linearize", WallNS: []int64{10100000}}}},
	}
	base := BaselineFromRuns(runs, fixtureFingerprint())
	spread := base.Spreads[MetricLatency]["linearize"] // ~1%

	freshBase := Run{Samples: []Sample{{Capability: "linearize", WallNS: []int64{10000000, 10010000, 9990000}}}}
	// Head is inside spread*NoiseMargin.
	head := Run{Samples: []Sample{{Capability: "linearize", WallNS: []int64{10050000}}}}

	got := Score(base, head, freshBase)
	for _, f := range got.Findings {
		if f.Metric == MetricLatency {
			if !f.Noise {
				t.Errorf("latency delta %.2f%% was not marked as noise despite spread %.2f%% and margin %v",
					abs(f.DeltaPercent), spread, NoiseMargin)
			}
		}
	}
}

// TestFreshBaseLatencyUsesBaselineSpread proves that the noise band comes from
// the baseline's run-to-run spreads, not from rep-to-rep variation.
func TestFreshBaseLatencyUsesBaselineSpread(t *testing.T) {
	// Baseline with latency spread from multiple runs.
	runs := []Run{
		{Samples: []Sample{{Capability: "build-pdf", WallNS: []int64{25_600_000}}}},
		{Samples: []Sample{{Capability: "build-pdf", WallNS: []int64{30_400_000}}}},
		{Samples: []Sample{{Capability: "build-pdf", WallNS: []int64{28_000_000}}}},
	}
	base := BaselineFromRuns(runs, fixtureFingerprint())
	spread := base.Spreads[MetricLatency]["build-pdf"] // 15.79%

	// Fresh base at 28ms, head at 27ms. Delta is 3.57%, well inside spread*margin.
	freshBase := Run{Samples: []Sample{{Capability: "build-pdf", WallNS: []int64{28_000_000}}}}
	head := Run{Samples: []Sample{{Capability: "build-pdf", WallNS: []int64{27_000_000}}}}

	got := Score(base, head, freshBase)
	for _, f := range got.Findings {
		if f.Metric == MetricLatency {
			if !f.Noise {
				t.Errorf("latency delta %.2f%% not marked as noise despite baseline spread %.2f%% and margin %v",
					abs(f.DeltaPercent), spread, NoiseMargin)
			}
		}
	}
}

// TestFreshBaseLatencyWithoutBaselineSpread proves that when the baseline has
// no latency spread (single-run baseline), latency is unscored even with a
// fresh base run, because a single fresh base run cannot bound run-to-run jitter.
func TestFreshBaseLatencyWithoutBaselineSpread(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "linearize", WallNS: []int64{100_000_000}},
	}}, fixtureFingerprint())
	if _, ok := base.Spreads[MetricLatency]; ok {
		t.Fatal("single-run baseline has latency spreads; this test requires none")
	}

	freshBase := Run{Samples: []Sample{{Capability: "linearize", WallNS: []int64{100_000_000}}}}
	head := Run{Samples: []Sample{{Capability: "linearize", WallNS: []int64{90_000_000}}}}

	got := Score(base, head, freshBase)
	var foundUnscored bool
	for _, u := range got.UnscoredPairs {
		if u.Capability == "linearize" && u.Metric == MetricLatency && u.Reason == "no_baseline_spread" {
			foundUnscored = true
			break
		}
	}
	if !foundUnscored {
		t.Errorf("latency with no baseline spread not tracked as unscored: %+v", got.UnscoredPairs)
	}
}

// TestAbsoluteNoiseFloorSuppressesSmallDeltas proves that absolute thresholds
// prevent single-digit byte or nanosecond differences from scoring.
func TestAbsoluteNoiseFloorSuppressesSmallDeltas(t *testing.T) {
	base := BaselineFrom(Run{Samples: []Sample{
		{Capability: "build-pdf", OutBytes: 1000, WChar: 8, DiskCounters: true},
	}}, fixtureFingerprint())

	// Head differs by 1ms (< 4ms floor) and 4 bytes (< 1024 floor).
	head := Run{Samples: []Sample{{Capability: "build-pdf", OutBytes: 1000,
		WChar: 12, DiskCounters: true, WallNS: []int64{25_600_000 + 1_000_000}}}}
	freshBase := Run{Samples: []Sample{{Capability: "build-pdf",
		WChar: 8, DiskCounters: true,
		WallNS: []int64{25_600_000, 25_700_000, 25_500_000}}}}

	got := Score(base, head, freshBase)
	for _, f := range got.Findings {
		switch f.Metric {
		case MetricLatency, MetricDiskBytes:
			if !f.Noise {
				t.Errorf("%s not marked as noise despite absolute delta below floor", f.Metric)
			}
		}
	}
}

// TestWideSpreadTrackedAsUnscored proves that a pair whose baseline spread is
// wider than WideSpreadCeiling is tracked as unscored, not silently forgiven.
func TestWideSpreadTrackedAsUnscored(t *testing.T) {
	// Baseline with 100% spread on disk_bytes: wchar goes from 100 to 0 and
	// back, simulating the runtime noise observed in practice.
	runs := []Run{
		{Samples: []Sample{{Capability: "inspect", WChar: 100, DiskCounters: true}}},
		{Samples: []Sample{{Capability: "inspect", WChar: 0, DiskCounters: true}}},
		{Samples: []Sample{{Capability: "inspect", WChar: 8, DiskCounters: true}}},
		{Samples: []Sample{{Capability: "inspect", WChar: 0, DiskCounters: true}}},
	}
	base := BaselineFromRuns(runs, fixtureFingerprint())
	spread := base.Spreads[MetricDiskBytes]["inspect"]
	if spread <= WideSpreadCeiling {
		t.Fatalf("spread = %.1f%%, want > %.0f%% for this test to mean anything", spread, WideSpreadCeiling)
	}

	head := Run{Samples: []Sample{{Capability: "inspect", WChar: 50, DiskCounters: true}}}
	got := Score(base, head)

	var foundUnscored bool
	for _, u := range got.UnscoredPairs {
		if u.Capability == "inspect" && u.Metric == MetricDiskBytes && u.Reason == "wide_spread" {
			foundUnscored = true
			break
		}
	}
	if !foundUnscored {
		t.Errorf("wide-spread pair not tracked as unscored: %+v", got.UnscoredPairs)
	}
}

// TestEndToEndUnchangedRunFails is the control byb-om7.8 step 6 requires:
// three runs of the same commit -> baseline, score an unchanged fourth run with
// a fresh base, assert Pass == false AND len(Ceilings) == 0. The second
// assertion proves the ceiling check respects noise bands.
func TestEndToEndUnchangedRunFails(t *testing.T) {
	// Three synthetic runs with realistic per-capability jitter from the quieter
	// table (build-pdf 15.59%, jbig2 8.63%, ... inspect 5.10%).
	runs := []Run{
		{Samples: []Sample{
			{Capability: "build-pdf", OutBytes: 10000, WallNS: []int64{25_600_000}},
			{Capability: "jbig2-generic", OutBytes: 5000, WallNS: []int64{73_400_000}},
			{Capability: "inspect", OutBytes: 0, WallNS: []int64{231_800_000}},
		}},
		{Samples: []Sample{
			{Capability: "build-pdf", OutBytes: 10000, WallNS: []int64{30_400_000}},
			{Capability: "jbig2-generic", OutBytes: 5000, WallNS: []int64{80_400_000}},
			{Capability: "inspect", OutBytes: 0, WallNS: []int64{244_200_000}},
		}},
		{Samples: []Sample{
			{Capability: "build-pdf", OutBytes: 10000, WallNS: []int64{28_000_000}},
			{Capability: "jbig2-generic", OutBytes: 5000, WallNS: []int64{75_000_000}},
			{Capability: "inspect", OutBytes: 0, WallNS: []int64{236_000_000}},
		}},
	}
	base := BaselineFromRuns(runs, fixtureFingerprint())

	// Fourth run, UNCHANGED commit, latencies drift within observed bands.
	freshBase := Run{Samples: []Sample{
		{Capability: "build-pdf", OutBytes: 10000, WallNS: []int64{29_000_000}},
		{Capability: "jbig2-generic", OutBytes: 5000, WallNS: []int64{78_000_000}},
		{Capability: "inspect", OutBytes: 0, WallNS: []int64{240_000_000}},
	}}
	head := Run{Samples: []Sample{
		{Capability: "build-pdf", OutBytes: 10000, WallNS: []int64{27_500_000}},
		{Capability: "jbig2-generic", OutBytes: 5000, WallNS: []int64{76_000_000}},
		{Capability: "inspect", OutBytes: 0, WallNS: []int64{243_000_000}},
	}}

	got := Score(base, head, freshBase)

	// The control: unchanged run must fail.
	if got.Pass {
		t.Error("an unchanged run passed")
	}
	if got.Score >= MinimumScore {
		t.Errorf("Score = %v, want < %v for unchanged run", got.Score, MinimumScore)
	}

	// The second gate: ceiling must not breach on noise-band deltas. Before the
	// fix, this produced 13 ceiling breaches on an unchanged commit.
	if len(got.Ceilings) != 0 {
		t.Errorf("got %d ceiling breaches on unchanged run, want 0:\n", len(got.Ceilings))
		for _, c := range got.Ceilings {
			t.Errorf("  %s %s %+.2f%%\n", c.Capability, c.Metric, c.DeltaPercent)
		}
	}
}
