package bench

import (
	"strings"
	"testing"
)

// procIOFixture is a verbatim /proc/self/io from a Linux 6.x kernel. Kept
// literal rather than synthesised so the parser is tested against the real
// field order and spacing.
const procIOFixture = `rchar: 2814624
wchar: 178
syscr: 91
syscw: 4
read_bytes: 0
write_bytes: 8192
cancelled_write_bytes: 0
`

func TestParseProcIO(t *testing.T) {
	got, ok := parseProcIO(strings.NewReader(procIOFixture))
	if !ok {
		t.Fatal("parseProcIO reported the fixture unusable")
	}
	if got.WChar != 178 {
		t.Errorf("WChar = %d, want 178", got.WChar)
	}
	if got.SysCR != 91 {
		t.Errorf("SysCR = %d, want 91", got.SysCR)
	}
	if got.SysCW != 4 {
		t.Errorf("SysCW = %d, want 4", got.SysCW)
	}
}

// TestParseProcIOIncomplete pins the rule from design spec section 5.1: a
// partial read is reported as unusable, never as zero. Zero would look like a
// capability that touched no disk, which is a claim, not an absence.
func TestParseProcIOIncomplete(t *testing.T) {
	for name, body := range map[string]string{
		"empty":       "",
		"no syscw":    "rchar: 10\nwchar: 20\nsyscr: 3\n",
		"no wchar":    "rchar: 10\nsyscr: 3\nsyscw: 4\n",
		"unparseable": "wchar: not-a-number\nsyscr: 3\nsyscw: 4\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseProcIO(strings.NewReader(body)); ok {
				t.Error("parseProcIO accepted an incomplete file")
			}
		})
	}
}

func TestPeakRSSReportsItsUnit(t *testing.T) {
	v, unit := PeakRSS()
	if v <= 0 {
		t.Errorf("peakRSS = %d, want a positive value", v)
	}
	if unit != "KiB" && unit != "bytes" {
		t.Errorf("peakRSS unit = %q, want KiB or bytes", unit)
	}
}
