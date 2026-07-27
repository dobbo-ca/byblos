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

func TestDivertRateWithNoAttempts(t *testing.T) {
	ResetExtractStats()
	if got := ExtractStats().DivertRate(); got != 0 {
		t.Errorf("DivertRate() with no attempts = %v; want 0", got)
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
