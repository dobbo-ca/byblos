package main

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/bench"
	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestPrepareIsMeasuredAndCostsMoreThanTheEncode is byb-om7.12's whole point.
//
// jbig2-generic's Prepare is rasterOf then Sauvola and its Run is the encode.
// Before this, Prepare was uncounted setup, so a candidate could cut extraction
// or binarisation by an order of magnitude and score +0.000. The assertion is
// the direction of the inequality, not a ratio: the measured figures on
// bench-v1 were 14x the latency and 352x the memory, and pinning either number
// would make this a change-detector rather than a check.
func TestPrepareIsMeasuredAndCostsMoreThanTheEncode(t *testing.T) {
	doc, ok := corpus.ByName("scan")
	if !ok {
		t.Fatal("corpus.ByName(\"scan\") not found")
	}

	samples, err := measure("jbig2-generic", "scan", doc, 0)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want the Run and the Prepare", len(samples))
	}
	run, prepare := samples[0], samples[1]

	if want := "jbig2-generic" + bench.PrepareSuffix; prepare.Capability != want {
		t.Errorf("prepare sample capability = %q, want %q", prepare.Capability, want)
	}
	if run.Capability != "jbig2-generic" {
		t.Errorf("run sample capability = %q, want the bare capability", run.Capability)
	}
	if prepare.Doc != run.Doc {
		t.Errorf("prepare doc %q, run doc %q; both describe one document", prepare.Doc, run.Doc)
	}

	if prepare.AllocBytes <= 0 {
		t.Fatalf("prepare allocated %d bytes; extraction and Sauvola cannot be free",
			prepare.AllocBytes)
	}
	if prepare.AllocBytes <= run.AllocBytes {
		t.Errorf("prepare allocated %d and the encode %d; byb-om7.12 measured prepare as the "+
			"larger cost, so scoring the encode alone is what hid it",
			prepare.AllocBytes, run.AllocBytes)
	}

	// A Prepare sample has no artifact, so it must drop out of the size axis
	// rather than post a 100% size win against a Run that does produce bytes.
	if prepare.OutBytes != 0 {
		t.Errorf("prepare reported %d out bytes; Prepare returns a prepared input, not an artifact",
			prepare.OutBytes)
	}
	if v, ok := prepare.Value(bench.MetricSize); ok && v != 0 {
		t.Errorf("prepare reports size %v; it must be 0 so Score skips the pair", v)
	}
	if run.OutBytes <= 0 {
		t.Errorf("run produced %d bytes; the jbig2 encode must produce a stream", run.OutBytes)
	}
}

// TestPrepareSuffixCannotCollideWithACapability pins that the pseudo-capability
// namespace stays disjoint from the real one. A capability containing the
// suffix would make a Prepare pair indistinguishable from a Run pair in the
// baseline's totals, which are keyed on the string alone.
func TestPrepareSuffixCannotCollideWithACapability(t *testing.T) {
	for _, tg := range bench.Targets {
		if strings.Contains(tg.Capability, bench.PrepareSuffix) {
			t.Errorf("capability %q contains %q, so its Prepare pair would collide with it",
				tg.Capability, bench.PrepareSuffix)
		}
	}
}
