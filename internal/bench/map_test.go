package bench_test

import (
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/bench"
)

// TestEveryCapabilityHasATarget is the enrolment gate of design spec section
// 3.1. It is deliberately the same shape as TestEveryCapabilityHasARule in
// upgrade_test.go: a capability appended to buildCapabilities without a
// benchmark target must redden the suite rather than be silently unmeasured.
func TestEveryCapabilityHasATarget(t *testing.T) {
	for _, c := range byblos.Capabilities() {
		if _, ok := bench.TargetFor(c); !ok {
			t.Errorf("capability %q has no benchmark target in internal/bench/map.go", c)
		}
	}
}

// TestEveryTargetNamesAKnownCapability is the other direction: a target for a
// capability string that no longer exists is dead weight that would silently
// contribute zero to every score.
func TestEveryTargetNamesAKnownCapability(t *testing.T) {
	known := make(map[string]bool)
	for _, c := range byblos.Capabilities() {
		known[c] = true
	}
	for _, tg := range bench.Targets {
		if !known[tg.Capability] {
			t.Errorf("target %q names no capability in buildCapabilities", tg.Capability)
		}
	}
}

// TestOverrideRequiresReason pins design spec section 3.2: a hand multiplier is
// permitted, an unexplained one is not.
func TestOverrideRequiresReason(t *testing.T) {
	for _, tg := range bench.Targets {
		switch {
		case tg.Override == 0:
			t.Errorf("target %q has Override 0; use 1.0 for no override", tg.Capability)
		case tg.Override != 1.0 && tg.Why == "":
			t.Errorf("target %q overrides its weight to %v with no Why", tg.Capability, tg.Override)
		case tg.Override == 1.0 && tg.Why != "":
			t.Errorf("target %q has a Why but no override", tg.Capability)
		}
	}
}

func TestNoDuplicateTargets(t *testing.T) {
	seen := make(map[string]bool)
	for _, tg := range bench.Targets {
		if seen[tg.Capability] {
			t.Errorf("capability %q has more than one target", tg.Capability)
		}
		seen[tg.Capability] = true
	}
}
