package byblos

import (
	"bytes"
	"math"
	"testing"
)

// corpusDoc is declared in inspect_test.go (Task 9); this file needs no direct
// import of internal/corpus.

// These tests mutate package-level counters, so they must not run in parallel
// with anything that calls ExtractPageRaster.
func TestExtractStatsCountsOutcomes(t *testing.T) {
	ResetExtractStats()

	scan := corpusDoc(t, "scan")
	tiled := corpusDoc(t, "tiled")
	born := corpusDoc(t, "born-digital")
	bad := corpusDoc(t, "malformed")

	if _, err := ExtractPageRaster(bytes.NewReader(scan), 1); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(tiled), 1); err == nil {
		t.Fatal("tiled: want an error")
	}
	if _, err := ExtractPageRaster(bytes.NewReader(born), 1); err == nil {
		t.Fatal("born-digital: want an error")
	}
	if _, err := ExtractPageRaster(bytes.NewReader(bad), 1); err == nil {
		t.Fatal("malformed: want an error")
	}

	c := ExtractStats()
	if c.Attempted != 4 {
		t.Errorf("Attempted = %d; want 4", c.Attempted)
	}
	if c.Extracted != 1 {
		t.Errorf("Extracted = %d; want 1", c.Extracted)
	}
	if c.Diverted != 2 {
		t.Errorf("Diverted = %d; want 2", c.Diverted)
	}
	if c.Failed != 1 {
		t.Errorf("Failed = %d; want 1 (the malformed file is a failure, not a divert)", c.Failed)
	}
	if c.Reasons["multiple-images"] != 1 {
		t.Errorf("Reasons[multiple-images] = %d; want 1", c.Reasons["multiple-images"])
	}
	if c.Reasons["no-image"] != 1 {
		t.Errorf("Reasons[no-image] = %d; want 1", c.Reasons["no-image"])
	}
	if got, want := c.DivertRate(), 0.5; math.Abs(got-want) > 1e-9 {
		t.Errorf("DivertRate() = %v; want %v", got, want)
	}
	if got, want := c.UnhandledRate(), 0.75; math.Abs(got-want) > 1e-9 {
		t.Errorf("UnhandledRate() = %v; want %v (2 diverted and 1 failed of 4)", got, want)
	}
}

// The metric half of byb-5kk. A whole document class that cannot be read at all
// must move the number the design tells operators to watch. DivertRate does not
// see it — that is deliberate, failures are a different problem from declines —
// and it is why UnhandledRate exists.
func TestUnhandledRateSeesFailuresThatDivertRateDoesNot(t *testing.T) {
	ResetExtractStats()
	bad := corpusDoc(t, "malformed")
	for i := 0; i < 3; i++ {
		if _, err := ExtractPageRaster(bytes.NewReader(bad), 1); err == nil {
			t.Fatal("malformed: want an error")
		}
	}
	c := ExtractStats()
	if c.Failed != 3 || c.Diverted != 0 {
		t.Fatalf("Failed = %d, Diverted = %d; want 3 and 0", c.Failed, c.Diverted)
	}
	if got := c.DivertRate(); got != 0 {
		t.Errorf("DivertRate() = %v; want 0: no page was read and declined", got)
	}
	if got, want := c.UnhandledRate(), 1.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("UnhandledRate() = %v; want %v: not one page produced a raster", got, want)
	}
}

// Extracted + Diverted + Failed == Attempted is what makes UnhandledRate free of
// blind spots. A new outcome that forgets to increment one of them breaks here.
func TestEveryAttemptLandsInExactlyOneOutcome(t *testing.T) {
	ResetExtractStats()
	for _, name := range []string{"scan", "tiled", "born-digital", "jbig2", "indirect-kids", "malformed"} {
		data := corpusDoc(t, name)
		_, _ = ExtractPageRaster(bytes.NewReader(data), 1)
	}
	c := ExtractStats()
	if got := c.Extracted + c.Diverted + c.Failed; got != c.Attempted {
		t.Errorf("Extracted+Diverted+Failed = %d; Attempted = %d", got, c.Attempted)
	}
}

// byb-b1.3 retired the "not-page-covering" divert reason: a raster that does not
// fill the page box now extracts. That removed 132 measured pages from the
// instrumentation entirely — they moved from a named divert reason into the
// undifferentiated Extracted total. Partial is what keeps them countable, and
// counting them is how the next byblos-divert run checks the decision.
func TestExtractStatsCountsPartialPages(t *testing.T) {
	ResetExtractStats()
	if _, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan")), 1); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-natural-dpi")), 1); err != nil {
		t.Fatalf("scan-natural-dpi: %v", err)
	}
	c := ExtractStats()
	if c.Extracted != 2 {
		t.Errorf("Extracted = %d; want 2", c.Extracted)
	}
	if c.Partial != 1 {
		t.Errorf("Partial = %d; want 1 (the natural-DPI page only, not the page-covering scan)", c.Partial)
	}
}

// Partial counts a subset of Extracted, so a page that diverted or failed must
// never reach it — and it must not disturb the invariant that the three
// outcomes sum to Attempted.
func TestPartialCountsOnlyExtractedPages(t *testing.T) {
	ResetExtractStats()
	if _, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "tiled")), 1); err == nil {
		t.Fatal("tiled: want a divert")
	}
	if _, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "malformed")), 1); err == nil {
		t.Fatal("malformed: want a failure")
	}
	c := ExtractStats()
	if c.Partial != 0 {
		t.Errorf("Partial = %d; want 0 — one divert and one failure produced no raster at all", c.Partial)
	}
	if c.Extracted+c.Diverted+c.Failed != c.Attempted {
		t.Errorf("Extracted+Diverted+Failed = %d; want Attempted = %d",
			c.Extracted+c.Diverted+c.Failed, c.Attempted)
	}
}

func TestExtractStatsSnapshotIsACopy(t *testing.T) {
	ResetExtractStats()
	data := corpusDoc(t, "tiled")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); err == nil {
		t.Fatal("want an error")
	}
	c := ExtractStats()
	c.Reasons["multiple-images"] = 999
	c.Attempted = 999
	if got := ExtractStats(); got.Reasons["multiple-images"] != 1 || got.Attempted != 1 {
		t.Error("mutating a snapshot changed the package counters")
	}
}

func TestRatesWithNoAttempts(t *testing.T) {
	ResetExtractStats()
	c := ExtractStats()
	if got := c.DivertRate(); got != 0 {
		t.Errorf("DivertRate() with no attempts = %v; want 0", got)
	}
	if got := c.UnhandledRate(); got != 0 {
		t.Errorf("UnhandledRate() with no attempts = %v; want 0", got)
	}
}

func TestResetExtractStats(t *testing.T) {
	ResetExtractStats()
	data := corpusDoc(t, "scan")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); err != nil {
		t.Fatal(err)
	}
	ResetExtractStats()
	c := ExtractStats()
	if c.Attempted != 0 || c.Extracted != 0 || len(c.Reasons) != 0 {
		t.Errorf("after ResetExtractStats: %+v; want zero", c)
	}
}
