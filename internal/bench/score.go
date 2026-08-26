package bench

import (
	"fmt"
	"slices"
	"strings"
)

// RegressionCeiling is rule 1 of design spec section 6: any capability-and-
// metric pair worse than this percentage fails the candidate whatever the
// weighted sum says.
const RegressionCeiling = 10.0

// NoiseMargin multiplies the spread recorded in the baseline before a delta is
// judged to be noise.
//
// It exists because a min-to-max band taken over N runs UNDER-estimates the
// true band: three runs cannot show the widest excursion a number makes. That
// was not a theory. A baseline built from three runs of the generated corpus,
// scored against an unmeasured fourth run of the same commit, produced +0.001
// and PASSED -- the exact failure this floor exists to stop. A 3x margin
// covered every excursion observed since.
const NoiseMargin = 3.0

// AbsoluteNoiseFloorNS is the smallest absolute latency delta (nanoseconds)
// that can be scored. build-pdf measured 25.6ms with a 15.59% spread across
// four same-commit runs (bench-v1, linux/arm64, golang:1.26.4, -reps 3). That
// is ~4ms of jitter on the FASTEST capability, where scheduler noise is the
// largest fraction of total time. A relative-only rule scores that 4ms as
// meaningful when it is not.
const AbsoluteNoiseFloorNS = 4_000_000

// AbsoluteNoiseFloorBytes is the smallest absolute byte delta that can be
// scored. Disk counters at single-digit bytes (wchar [0,8,8,0] across four
// runs) measure Go runtime incidentals, not byblos behaviour, and must not be
// expressed as percentages.
const AbsoluteNoiseFloorBytes = 1024

// WideSpreadCeiling is the spread percentage above which a pair is effectively
// unscoreable. disk_bytes and write_iops showed 100% spreads (zero counters
// jittering) across four same-commit runs, meaning ANY delta is forgiven. That
// outcome is correct, but the reason must be visible: the pair is not being
// measured, not that a measurement happened and was within tolerance.
const WideSpreadCeiling = 50.0

// MinimumScore is the smallest weighted score that may pass.
//
// The per-pair noise floor above suppresses jitter one pair at a time; this
// guards the total, because many pairs each drifting below their own floor can
// still sum to a positive number. It is also the more honest gate: the routine
// exists to find material improvements, and a candidate that cannot clear this
// is not worth a maintainer reading it.
//
// Scale: an unchanged run scored +0.001 in testing. A 1% size win on
// jbig2-generic, which holds roughly a third of the size share, scores about
// +0.14. This threshold sits between them, nearer the noise.
const MinimumScore = 0.05

// Finding is one capability-and-metric comparison.
type Finding struct {
	Capability   string  `json:"capability"`
	Metric       Metric  `json:"metric"`
	Base         float64 `json:"base"`
	Head         float64 `json:"head"`
	DeltaPercent float64 `json:"delta_percent"` // negative is an improvement
	Contribution float64 `json:"contribution"`

	// Noise is true when the delta fell inside the spread this pair showed
	// across repeated runs of the baseline commit. Such a finding is printed
	// for the reader and contributes zero to the score.
	Noise bool `json:"noise,omitempty"`
}

// Result is one scored comparison.
type Result struct {
	Score         float64        `json:"score"`
	Pass          bool           `json:"pass"`
	Findings      []Finding      `json:"findings"`
	Ceilings      []Finding      `json:"ceilings"`
	UnscoredPairs []UnscoredPair `json:"unscored_pairs,omitempty"` // pairs that could not be scored and why
}

// UnscoredPair is one capability-and-metric pair that contributed nothing to
// the score, and the reason it was skipped.
type UnscoredPair struct {
	Capability string `json:"capability"`
	Metric     Metric `json:"metric"`
	Reason     string `json:"reason"` // "no_baseline_value", "insufficient_reps", "wide_spread"
}

// Score compares a head run against a baseline. An optional fresh base run,
// measured on the same runner as head, may be supplied to score non-
// deterministic metrics (latency). Variadic for backwards compatibility: the
// existing two-argument form still works.
//
// A metric absent from either side is SKIPPED, never treated as zero. Zero is
// the reading for "this capability wrote nothing", and scoring an absent
// counter as zero would post a 100% improvement for a machine that simply
// cannot measure it.
func Score(base Baseline, head Run, freshBase ...Run) Result {
	var freshBaseTotals map[Metric]map[string]float64
	if len(freshBase) > 0 {
		freshBaseTotals = totalsOf(freshBase[0])
	}

	headTotals := totalsOf(head)

	var res Result
	for metric, weight := range MetricWeights {
		shares := base.Shares[metric]
		for capability, share := range shares {
			baseValue, haveTotals := base.Totals[metric][capability]
			useFreshBase := false

			// If a fresh base run is provided and measured this pair, prefer
			// it over the stored Totals. This makes latency scoreable (it is
			// not in Totals because Metric.Deterministic() returns false for
			// MetricLatency, and baseline.go line 106 only writes to Totals
			// when m.Deterministic() is true).
			if freshBaseTotals != nil {
				if v, ok := freshBaseTotals[metric][capability]; ok && v > 0 {
					baseValue, haveTotals = v, true
					useFreshBase = true
				}
			}

			if !haveTotals || baseValue == 0 {
				if metric == MetricLatency && share > 0 {
					res.UnscoredPairs = append(res.UnscoredPairs, UnscoredPair{
						Capability: capability,
						Metric:     metric,
						Reason:     "no_baseline_value",
					})
				}
				continue
			}
			headValue, ok := headTotals[metric][capability]
			if !ok {
				continue
			}

			// A spread wider than WideSpreadCeiling means the metric is
			// measuring noise, not byblos. Four same-commit runs showed
			// disk_bytes 100% (wchar [0,8,8,0]), write_iops 100% (syscw
			// [0,1,1,0]). Track these separately so they surface as "not
			// measured" rather than "measured and within tolerance".
			if spread, ok := base.Spreads[metric][capability]; ok && spread > WideSpreadCeiling {
				res.UnscoredPairs = append(res.UnscoredPairs, UnscoredPair{
					Capability: capability,
					Metric:     metric,
					Reason:     "wide_spread",
				})
				continue
			}

			delta := (headValue - baseValue) / baseValue * 100
			deltaNS := headValue - baseValue
			deltaBytes := headValue - baseValue

			override := 1.0
			if t, ok := TargetFor(capability); ok {
				override = t.Override
			}

			f := Finding{
				Capability:   capability,
				Metric:       metric,
				Base:         baseValue,
				Head:         headValue,
				DeltaPercent: delta,
				Contribution: weight * share * override * -delta,
			}

			// Absolute floors for metrics where single-digit differences are
			// runtime noise, not meaningful deltas.
			if metric == MetricLatency && abs(deltaNS) < AbsoluteNoiseFloorNS {
				f.Noise = true
				f.Contribution = 0
			} else if (metric == MetricDiskBytes || metric == MetricWriteIOPS || metric == MetricReadIOPS) &&
				abs(deltaBytes) < AbsoluteNoiseFloorBytes {
				f.Noise = true
				f.Contribution = 0
			} else if spread, ok := base.Spreads[metric][capability]; ok && abs(delta) <= spread*NoiseMargin {
				// Recorded spread from baseline.
				f.Noise = true
				f.Contribution = 0
			} else if useFreshBase && metric == MetricLatency {
				// Fresh-base latency: use the baseline's spread if it has one
				// (from multiple runs that measure run-to-run variation), not
				// rep-to-rep spread within the fresh base run (which shares warm
				// caches and under-estimates true jitter). If the baseline has no
				// latency spread, refuse rather than guess.
				//
				// Measured run-to-run spreads (bench-v1, linux/arm64, -reps 3):
				//   build-pdf        25.6ms   15.59%
				//   jbig2-generic    73.4ms    8.63%
				//   linearize      2570.8ms    7.71%
				//   text-layer     1362.6ms    7.57%
				//   downsample      324.0ms    7.02%
				//   extract-raster 1095.5ms    6.64%
				//   inspect         231.8ms    5.10%
				//   quantize-png   1586.8ms    4.67%
				//   jpeg-recompress 19088ms    3.80%
				//
				// Under load (same setup, load avg 40-71): inspect reached 52.87%,
				// so the band varies by an order of magnitude with contention.
				if spread, ok := base.Spreads[metric][capability]; ok && abs(delta) <= spread*NoiseMargin {
					f.Noise = true
					f.Contribution = 0
				} else if _, ok := base.Spreads[metric][capability]; !ok {
					// No baseline spread for latency; a single fresh base run
					// cannot bound run-to-run jitter. Refuse rather than guess.
					res.UnscoredPairs = append(res.UnscoredPairs, UnscoredPair{
						Capability: capability,
						Metric:     metric,
						Reason:     "no_baseline_spread",
					})
					f.Contribution = 0
				}
			}

			res.Findings = append(res.Findings, f)
			res.Score += f.Contribution

			// The ceiling is checked against the real delta, but only if the
			// delta is outside the noise band. Latency spreads are 3.80% to
			// 15.59% (52.87% under load), and disk counters jitter by 100%, so
			// a pair can be both noisy AND above the 10% ceiling. A pair inside
			// its noise band cannot breach the ceiling; a pair whose band is
			// wider than the ceiling is unscoreable, already tracked above.
			if !f.Noise && delta > RegressionCeiling {
				res.Ceilings = append(res.Ceilings, f)
			}
		}
	}

	slices.SortFunc(res.Findings, func(a, b Finding) int {
		if a.Contribution != b.Contribution {
			if a.Contribution < b.Contribution {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Capability, b.Capability)
	})

	res.Pass = res.Score >= MinimumScore && len(res.Ceilings) == 0
	return res
}

// totalsOf reduces a run to per-metric, per-capability totals, the same shape
// Baseline.Totals holds.
func totalsOf(r Run) map[Metric]map[string]float64 {
	out := make(map[Metric]map[string]float64)
	for m := range MetricWeights {
		totals := make(map[string]float64)
		for _, s := range r.Samples {
			v, ok := s.Value(m)
			if !ok {
				continue
			}
			totals[s.Capability] += v
		}
		if len(totals) > 0 {
			out[m] = totals
		}
	}
	return out
}

// Markdown renders the result as the table the workflow posts.
func (r Result) Markdown() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Pass {
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "**%s** — weighted score `%+.3f` (pass needs `%+.2f`)\n\n",
		verdict, r.Score, MinimumScore)

	if len(r.UnscoredPairs) > 0 {
		b.WriteString("> **Not scored:**\n")
		for _, u := range r.UnscoredPairs {
			switch u.Reason {
			case "no_baseline_value":
				fmt.Fprintf(&b, "> - `%s` %s: baseline has no absolute duration (spec 5.3), use `-base-run`\n",
					u.Capability, u.Metric)
			case "no_baseline_spread":
				fmt.Fprintf(&b, "> - `%s` %s: baseline has no run-to-run spread, cannot bound jitter\n",
					u.Capability, u.Metric)
			case "wide_spread":
				fmt.Fprintf(&b, "> - `%s` %s: spread >%.0f%%, measuring noise not byblos\n",
					u.Capability, u.Metric, WideSpreadCeiling)
			}
		}
		b.WriteString("\n")
	}

	if len(r.Ceilings) > 0 {
		fmt.Fprintf(&b, "> Regression ceiling breached (>%.0f%%):\n>\n", RegressionCeiling)
		for _, f := range r.Ceilings {
			fmt.Fprintf(&b, "> - `%s` %s %+.1f%%\n", f.Capability, f.Metric, f.DeltaPercent)
		}
		b.WriteString("\n")
	}

	b.WriteString("| capability | metric | base | head | delta | contribution |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|\n")
	for _, f := range r.Findings {
		contribution := fmt.Sprintf("%+.4f", f.Contribution)
		if f.Noise {
			contribution = "noise"
		}
		fmt.Fprintf(&b, "| %s | %s | %.0f | %.0f | %+.2f%% | %s |\n",
			f.Capability, f.Metric, f.Base, f.Head, f.DeltaPercent, contribution)
	}
	return b.String()
}

// abs is math.Abs without the import, kept local because it is the only
// floating-point helper this file needs.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
