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

// Finding is one capability-and-metric comparison.
type Finding struct {
	Capability   string  `json:"capability"`
	Metric       Metric  `json:"metric"`
	Base         float64 `json:"base"`
	Head         float64 `json:"head"`
	DeltaPercent float64 `json:"delta_percent"` // negative is an improvement
	Contribution float64 `json:"contribution"`
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
			res.Findings = append(res.Findings, f)
			res.Score += f.Contribution
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

	res.Pass = res.Score > 0 && len(res.Ceilings) == 0
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
	fmt.Fprintf(&b, "**%s** — weighted score `%+.3f`\n\n", verdict, r.Score)

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
		fmt.Fprintf(&b, "| %s | %s | %.0f | %.0f | %+.2f%% | %+.4f |\n",
			f.Capability, f.Metric, f.Base, f.Head, f.DeltaPercent, f.Contribution)
	}
	return b.String()
}
