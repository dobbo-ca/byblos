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
	Score    float64   `json:"score"`
	Pass     bool      `json:"pass"`
	Findings []Finding `json:"findings"`
	Ceilings []Finding `json:"ceilings"`
}

// Score compares a head run against a baseline.
//
// A metric absent from either side is SKIPPED, never treated as zero. Zero is
// the reading for "this capability wrote nothing", and scoring an absent
// counter as zero would post a 100% improvement for a machine that simply
// cannot measure it.
func Score(base Baseline, head Run) Result {
	headTotals := totalsOf(head)

	var res Result
	for metric, weight := range MetricWeights {
		shares := base.Shares[metric]
		for capability, share := range shares {
			baseValue, ok := base.Totals[metric][capability]
			if !ok || baseValue == 0 {
				continue
			}
			headValue, ok := headTotals[metric][capability]
			if !ok {
				continue
			}

			delta := (headValue - baseValue) / baseValue * 100

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

			// A delta inside the band this pair moved when nothing changed is
			// noise, not a result. Byblos is not as deterministic as the design
			// assumed: output size drifts by up to 0.11% on the capabilities
			// that write through pdfcpu, and total allocation drifts on all of
			// them. Without this, a candidate that changed nothing would score
			// a small non-zero number and could pass on measurement jitter
			// alone, because Pass requires only Score > 0.
			if spread, ok := base.Spreads[metric][capability]; ok && abs(delta) <= spread*NoiseMargin {
				f.Noise = true
				f.Contribution = 0
			}

			res.Findings = append(res.Findings, f)
			res.Score += f.Contribution

			// The ceiling is checked against the real delta, not the
			// noise-suppressed contribution. A spread is fractions of a
			// percent and the ceiling is 10%, so nothing can be both.
			if delta > RegressionCeiling {
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
