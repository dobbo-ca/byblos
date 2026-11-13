package byblos

import (
	"slices"
	"testing"
)

// The capability string is what UpgradeCandidates keys on (design spec section
// 6), so it is API surface and must not drift.
func TestCapabilityStringIsStable(t *testing.T) {
	if CapabilityJBIG2Generic != "jbig2-generic" {
		t.Errorf("CapabilityJBIG2Generic = %q; want %q", CapabilityJBIG2Generic, "jbig2-generic")
	}
	if slices.Contains(Capabilities(), "jbig2-symbol") {
		t.Error("Capabilities() advertises jbig2-symbol, which this build does not implement")
	}
	if !slices.Contains(Capabilities(), CapabilityJBIG2Generic) {
		t.Errorf("Capabilities() = %v; want it to contain %q", Capabilities(), CapabilityJBIG2Generic)
	}
}
