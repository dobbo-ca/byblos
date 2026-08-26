package bench

import (
	"errors"
	"testing"
)

func fixtureFingerprint() Fingerprint {
	return Fingerprint{
		BenchSetSHA256: "aaa",
		HarnessSHA256:  "bbb",
		Commit:         "ccc",
		GoVersion:      "go1.26.4",
		GOOSGOARCH:     "linux/amd64",
	}
}

func fixtureBaseline() Baseline {
	return Baseline{Fingerprint: fixtureFingerprint()}
}

func alwaysAncestor(string) bool { return true }
func neverAncestor(string) bool  { return false }

func TestBaselineValidateAcceptsAnExactMatch(t *testing.T) {
	if err := fixtureBaseline().Validate(fixtureFingerprint(), alwaysAncestor); err != nil {
		t.Fatalf("Validate rejected an exact match: %v", err)
	}
}

// TestBaselineValidateRejectsEachMismatch walks the four checks of design spec
// section 5.3 one at a time. Table-driven so a new check cannot be added
// without a case.
func TestBaselineValidateRejectsEachMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*Fingerprint){
		"bench set":  func(f *Fingerprint) { f.BenchSetSHA256 = "different" },
		"harness":    func(f *Fingerprint) { f.HarnessSHA256 = "different" },
		"go version": func(f *Fingerprint) { f.GoVersion = "go1.27.0" },
		"platform":   func(f *Fingerprint) { f.GOOSGOARCH = "darwin/arm64" },
	} {
		t.Run(name, func(t *testing.T) {
			current := fixtureFingerprint()
			mutate(&current)
			err := fixtureBaseline().Validate(current, alwaysAncestor)
			if !errors.Is(err, ErrStaleBaseline) {
				t.Fatalf("got %v, want ErrStaleBaseline", err)
			}
		})
	}
}

// TestBaselineValidateRejectsADivergedCommit is the fourth check: a baseline
// measured on a line this branch did not descend from is not this branch's
// base, however well its other fields match.
func TestBaselineValidateRejectsADivergedCommit(t *testing.T) {
	err := fixtureBaseline().Validate(fixtureFingerprint(), neverAncestor)
	if !errors.Is(err, ErrStaleBaseline) {
		t.Fatalf("got %v, want ErrStaleBaseline", err)
	}
}

// TestSharesSumToOne pins the property that makes a score readable as a
// weighted percentage: for any metric, the capability shares total 1.
func TestSharesSumToOne(t *testing.T) {
	run := Run{Samples: []Sample{
		{Capability: "jbig2-generic", OutBytes: 600, AllocBytes: 10},
		{Capability: "quantize-png", OutBytes: 300, AllocBytes: 20},
		{Capability: "build-pdf", OutBytes: 100, AllocBytes: 70},
	}}
	b := BaselineFrom(run, fixtureFingerprint())

	for _, m := range []Metric{MetricSize, MetricMemory} {
		var sum float64
		for _, share := range b.Shares[m] {
			sum += share
		}
		if sum < 0.9999 || sum > 1.0001 {
			t.Errorf("%s shares sum to %v, want 1.0", m, sum)
		}
	}
	if got := b.Shares[MetricSize]["jbig2-generic"]; got < 0.5999 || got > 0.6001 {
		t.Errorf("jbig2-generic size share = %v, want 0.6", got)
	}
}

// TestSharesOfAnAllZeroMetricAreEmpty pins that a metric no capability
// registers -- disk on a machine with no /proc/self/io -- yields no shares
// rather than a division by zero.
func TestSharesOfAnAllZeroMetricAreEmpty(t *testing.T) {
	run := Run{Samples: []Sample{{Capability: "inspect", OutBytes: 0}}}
	b := BaselineFrom(run, fixtureFingerprint())
	if len(b.Shares[MetricSize]) != 0 {
		t.Errorf("got %d size shares from an all-zero metric, want 0", len(b.Shares[MetricSize]))
	}
}
