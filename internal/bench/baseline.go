package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrStaleBaseline reports a committed baseline that cannot be compared
// against the current head. The workflow's response is to measure the base
// live, never to compare anyway.
var ErrStaleBaseline = errors.New("bench: baseline does not match the current run")

// Fingerprint is what a baseline's numbers depend on. Every field must match
// for the stored metrics to be usable -- design spec section 5.3.
type Fingerprint struct {
	BenchSetSHA256 string `json:"bench_set_sha256"`
	HarnessSHA256  string `json:"harness_sha256"`
	Commit         string `json:"commit"`
	GoVersion      string `json:"go_version"`
	GOOSGOARCH     string `json:"goos_goarch"`

	// Runner is recorded for the reader and is deliberately NOT checked. No
	// absolute duration is stored, so a different runner does not invalidate
	// the file.
	Runner string `json:"runner,omitempty"`
}

// Baseline is the committed comparison target.
//
// Totals carry the five deterministic metrics per capability. Shares carry
// every metric including latency, because a share is a ratio between
// capabilities rather than an absolute figure, and survives being carried to a
// different machine in a way milliseconds do not.
type Baseline struct {
	Fingerprint Fingerprint                   `json:"fingerprint"`
	Totals      map[Metric]map[string]float64 `json:"totals"`
	Shares      map[Metric]map[string]float64 `json:"shares"`

	// Spreads is the percentage band each (metric, capability) moved across
	// repeated runs of the same commit. A head delta inside that band is noise
	// and scores zero -- see BaselineFromRuns and Score.
	//
	// An absent entry means a spread was never measured, which Score treats as
	// zero tolerance. That is the safe direction: it scores a real difference
	// rather than silently forgiving one.
	Spreads map[Metric]map[string]float64 `json:"spreads,omitempty"`
}

// BaselineFrom reduces a run to per-capability totals and shares.
func BaselineFrom(r Run, f Fingerprint) Baseline {
	return BaselineFromRuns([]Run{r}, f)
}

// BaselineFromRuns reduces N repeated runs of the SAME commit to totals, shares
// and observed spreads.
//
// Repetition is what makes Spreads meaningful, and Spreads is why this exists.
// The design assumed every metric except latency was exactly reproducible.
// Measured over three runs of the generated corpus that turned out to be true
// only for the capabilities byblos encodes itself -- jbig2-generic, build-pdf
// and quantize-png are byte-identical run to run -- while the three that pass
// through pdfcpu's writer drift by up to 0.11% on output size, and total
// allocation drifts on all nine by up to 0.28%.
//
// Rather than pretend otherwise, the baseline records how much each
// (capability, metric) actually moved when nothing changed, and Score treats a
// delta inside that band as noise. That is the same rule design spec section 6
// already applies to latency, generalised to every metric now that latency is
// known not to be the only unstable one.
//
// Totals are taken from the FIRST run, not an average, so a baseline stays a
// set of real observed numbers rather than a synthetic mean no run produced.
func BaselineFromRuns(runs []Run, f Fingerprint) Baseline {
	b := Baseline{
		Fingerprint: f,
		Totals:      make(map[Metric]map[string]float64),
		Shares:      make(map[Metric]map[string]float64),
		Spreads:     make(map[Metric]map[string]float64),
	}
	if len(runs) == 0 {
		return b
	}

	for m := range MetricWeights {
		// perRun[i][capability] is run i's total for this metric.
		perRun := make([]map[string]float64, len(runs))
		for i, r := range runs {
			totals := make(map[string]float64)
			for _, s := range r.Samples {
				v, ok := s.Value(m)
				if !ok {
					continue
				}
				totals[s.Capability] += v
			}
			perRun[i] = totals
		}

		totals := perRun[0]
		var grand float64
		for _, v := range totals {
			grand += v
		}
		if m.Deterministic() && len(totals) > 0 {
			b.Totals[m] = totals
		}

		if len(runs) > 1 {
			spreads := make(map[string]float64)
			for capability, first := range totals {
				lo, hi := first, first
				for _, totalsN := range perRun[1:] {
					v, ok := totalsN[capability]
					if !ok {
						continue
					}
					lo, hi = min(lo, v), max(hi, v)
				}
				if hi > 0 {
					spreads[capability] = (hi - lo) / hi * 100
				}
			}
			if len(spreads) > 0 {
				b.Spreads[m] = spreads
			}
		}

		if grand == 0 {
			continue
		}
		shares := make(map[string]float64, len(totals))
		for capability, v := range totals {
			shares[capability] = v / grand
		}
		b.Shares[m] = shares
	}
	return b
}

// Validate applies the four checks of design spec section 5.3.
//
// isAncestor reports whether the baseline's commit is an ancestor of the
// current merge base. It is a parameter rather than a git call so this package
// stays testable without a repository.
func (b Baseline) Validate(current Fingerprint, isAncestor func(commit string) bool) error {
	switch {
	case b.Fingerprint.BenchSetSHA256 != current.BenchSetSHA256:
		return fmt.Errorf("%w: bench set %s, baseline measured on %s",
			ErrStaleBaseline, current.BenchSetSHA256, b.Fingerprint.BenchSetSHA256)
	case b.Fingerprint.HarnessSHA256 != current.HarnessSHA256:
		return fmt.Errorf("%w: the harness changed since the baseline was measured", ErrStaleBaseline)
	case b.Fingerprint.GoVersion != current.GoVersion:
		return fmt.Errorf("%w: go %s, baseline measured on %s",
			ErrStaleBaseline, current.GoVersion, b.Fingerprint.GoVersion)
	case b.Fingerprint.GOOSGOARCH != current.GOOSGOARCH:
		return fmt.Errorf("%w: platform %s, baseline measured on %s",
			ErrStaleBaseline, current.GOOSGOARCH, b.Fingerprint.GOOSGOARCH)
	case !isAncestor(b.Fingerprint.Commit):
		return fmt.Errorf("%w: baseline commit %s is not an ancestor of this merge base",
			ErrStaleBaseline, b.Fingerprint.Commit)
	}
	return nil
}

// LoadBaseline reads a baseline from disk.
func LoadBaseline(path string) (Baseline, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Baseline{}, err
	}
	var b Baseline
	if err := json.Unmarshal(body, &b); err != nil {
		return Baseline{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return b, nil
}
