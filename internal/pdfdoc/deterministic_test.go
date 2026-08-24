package pdfdoc

import "testing"

// pinDate's fallback branches never see a corpus fixture (every corpus
// document feeds it "" or a well-formed date), and a failure there degrades
// to writing whatever string came in — so the guarantee that EVERY input
// yields a fixed-length, deterministic date needs its own table.
func TestPinDateFallsBackToTheConstant(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", pinnedDate},
		{"garbage", pinnedDate},
		{"D:20190102030405+01'00'", "D:20190102030405+01'00'"}, // already normalized
		{"D:20190102030405Z", "D:20190102030405+00'00'"},       // normalized, offset preserved as UTC
		{"D:2019", "D:20190101000000+00'00'"},                  // partial dates are legal (ISO 32000-1 7.9.4)
	} {
		got := pinDate(tc.in)
		if got != tc.want {
			t.Errorf("pinDate(%q) = %q; want %q", tc.in, got, tc.want)
		}
		if len(got) != len(pinnedDate) {
			t.Errorf("pinDate(%q) renders at %d bytes; every pin must be %d", tc.in, len(got), len(pinnedDate))
		}
	}
}
