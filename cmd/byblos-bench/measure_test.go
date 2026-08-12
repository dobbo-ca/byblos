package main

import (
	"errors"
	"testing"

	"github.com/dobbo-ca/byblos/internal/bench"
	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestMeasureRecordsDeterministicMetrics pins the property the committed
// baseline depends on: two measurements of the same capability over the same
// document agree exactly on every metric except latency.
func TestMeasureRecordsDeterministicMetrics(t *testing.T) {
	doc, ok := corpus.ByName("scan")
	if !ok {
		t.Fatal("corpus.ByName(\"scan\") not found")
	}

	first, err := measure("jbig2-generic", "scan", doc, 0)
	if err != nil {
		t.Fatalf("first measure: %v", err)
	}
	second, err := measure("jbig2-generic", "scan", doc, 0)
	if err != nil {
		t.Fatalf("second measure: %v", err)
	}

	if first.OutBytes != second.OutBytes {
		t.Errorf("OutBytes differs across runs: %d then %d", first.OutBytes, second.OutBytes)
	}
	if first.OutBytes == 0 {
		t.Error("OutBytes is 0; jbig2-generic must produce an encoded stream")
	}
	if first.AllocBytes != second.AllocBytes {
		t.Errorf("AllocBytes differs across runs: %d then %d", first.AllocBytes, second.AllocBytes)
	}
}

// TestMeasureTimesOnlyWhenAsked pins that repetitions are spent only on the
// capabilities a diff touches (spec section 5.3).
func TestMeasureTimesOnlyWhenAsked(t *testing.T) {
	doc, _ := corpus.ByName("scan")

	untimed, err := measure("inspect", "scan", doc, 0)
	if err != nil {
		t.Fatalf("untimed: %v", err)
	}
	if len(untimed.WallNS) != 0 {
		t.Errorf("recorded %d timings with reps 0", len(untimed.WallNS))
	}
	if _, ok := untimed.Value(bench.MetricLatency); ok {
		t.Error("an untimed sample reports a latency")
	}

	timed, err := measure("inspect", "scan", doc, 3)
	if err != nil {
		t.Fatalf("timed: %v", err)
	}
	if len(timed.WallNS) != 3 {
		t.Errorf("recorded %d timings with reps 3", len(timed.WallNS))
	}
}

// TestMeasureReportsIneligibleRatherThanFailing pins that an ineligible
// document is a skip, so one born-digital PDF cannot redden a whole run.
func TestMeasureReportsIneligibleRatherThanFailing(t *testing.T) {
	doc, ok := corpus.ByName("born-digital")
	if !ok {
		t.Fatal("corpus.ByName(\"born-digital\") not found")
	}
	_, err := measure("jbig2-generic", "born-digital", doc, 0)
	if err == nil {
		t.Fatal("born-digital measured for jbig2-generic; want ineligible")
	}
	if !errorsIsIneligible(err) {
		t.Fatalf("got %v, want ErrIneligible", err)
	}
}

func errorsIsIneligible(err error) bool {
	return err != nil && errors.Is(err, bench.ErrIneligible)
}
