package main

import (
	"strings"
	"testing"
)

// TestBaselineRefusesAnEmptyFingerprintField pins the guard that cost a
// measured baseline: a fingerprint field left unflagged is written as the empty
// string, Baseline.Validate compares it, and the file is then stale against
// every run. The subcommand must refuse rather than write such a file.
func TestBaselineRefusesAnEmptyFingerprintField(t *testing.T) {
	out := t.TempDir() + "/baseline.json"
	full := map[string]string{
		"-runs":     "a.json,b.json,c.json",
		"-out":      out,
		"-commit":   "cccc",
		"-benchset": "bbbb",
		"-harness":  "hhhh",
	}

	for _, omit := range []string{"-commit", "-benchset", "-harness"} {
		var args []string
		for flag, value := range full {
			if flag == omit {
				continue
			}
			args = append(args, flag, value)
		}
		err := cmdBaseline(args)
		if err == nil {
			t.Fatalf("cmdBaseline wrote a baseline with %s unset", omit)
		}
		if !strings.Contains(err.Error(), omit) {
			t.Errorf("omitting %s: error does not name the flag: %v", omit, err)
		}
	}
}
