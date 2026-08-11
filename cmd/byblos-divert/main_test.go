package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/corpus"
)

func TestJobsPrefersFlagThenEnvThenDefault(t *testing.T) {
	tests := []struct {
		name string
		flag int
		env  string
		want int
	}{
		{"nothing set falls back to the default", 0, "", defaultJobs},
		{"the env var is read when no flag is given", 0, "7", 7},
		{"an explicit flag beats the env var", 3, "7", 3},
		{"a junk env var falls back rather than failing", 0, "banana", defaultJobs},
		{"a zero env var falls back", 0, "0", defaultJobs},
		{"a negative env var falls back", 0, "-4", defaultJobs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(jobsEnv, tt.env)
			if tt.env == "" {
				os.Unsetenv(jobsEnv)
			}
			if got := jobs(tt.flag); got != tt.want {
				t.Errorf("jobs(%d) with %s=%q = %d, want %d", tt.flag, jobsEnv, tt.env, got, tt.want)
			}
		})
	}
}

// TestSweepIsDeterministicAcrossWorkerCounts is the property that makes a
// parallel sweep usable at all: two runs must be diffable. A worker pool that
// printed as it went would interleave by completion order and two runs of the
// same corpus would differ from each other, which is exactly what these sweeps
// are for.
func TestSweepIsDeterministicAcrossWorkerCounts(t *testing.T) {
	dir := t.TempDir()
	for _, d := range corpus.All() {
		if err := os.WriteFile(filepath.Join(dir, d.Name+".pdf"), d.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(workers int) sweepResult {
		byblos.ResetExtractStats()
		got, err := sweep(dir, workers)
		if err != nil {
			t.Fatalf("sweep(%d workers): %v", workers, err)
		}
		return got
	}

	serial := run(1)
	if serial.files == 0 || serial.pages == 0 {
		t.Fatalf("sweep walked nothing: %+v", serial)
	}
	if len(serial.lines) == 0 {
		t.Fatal("no per-page lines; the corpus diverts pages, so this test would not " +
			"notice a reordering bug")
	}

	for _, workers := range []int{2, 4, 8} {
		got := run(workers)
		if got.files != serial.files || got.pages != serial.pages || got.unreadable != serial.unreadable {
			t.Errorf("%d workers: counts %+v, want %+v", workers, got, serial)
		}
		if len(got.lines) != len(serial.lines) {
			t.Fatalf("%d workers: %d lines, want %d", workers, len(got.lines), len(serial.lines))
		}
		for i := range got.lines {
			if got.lines[i] != serial.lines[i] {
				t.Fatalf("%d workers: line %d = %q, want %q", workers, i, got.lines[i], serial.lines[i])
			}
		}
	}
}

// TestSweepCountsAgreeWithTheCounters guards the summary half: the counters are
// package-level and shared, so a parallel sweep that lost an increment would
// still produce perfectly ordered lines.
func TestSweepCountsAgreeWithTheCounters(t *testing.T) {
	dir := t.TempDir()
	for _, d := range corpus.All() {
		if err := os.WriteFile(filepath.Join(dir, d.Name+".pdf"), d.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	byblos.ResetExtractStats()
	got, err := sweep(dir, 8)
	if err != nil {
		t.Fatal(err)
	}
	c := byblos.ExtractStats()
	if int(c.Attempted) != got.pages {
		t.Errorf("Attempted = %d, but the sweep walked %d pages", c.Attempted, got.pages)
	}
	if c.Extracted+c.Diverted+c.Failed != c.Attempted {
		t.Errorf("counters do not reconcile: %d extracted + %d diverted + %d failed != %d attempted",
			c.Extracted, c.Diverted, c.Failed, c.Attempted)
	}
}
