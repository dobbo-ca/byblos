package bench_test

import (
	"errors"
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/bench"
	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestEveryTargetHasACase pins that the map and the dispatch table cannot
// drift apart. A target with no case is a capability that silently measures
// nothing.
func TestEveryTargetHasACase(t *testing.T) {
	for _, tg := range bench.Targets {
		if _, ok := bench.CaseFor(tg.Capability); !ok {
			t.Errorf("target %q has no case in case.go", tg.Capability)
		}
	}
}

// TestCasesRunOnTheGeneratedCorpus exercises every case against the in-repo
// scan fixture, so the dispatch table is proven without needing the bench set
// downloaded. It asserts only that a case either produces a result or reports
// itself ineligible -- never that it panics or returns an unclassified error.
func TestCasesRunOnTheGeneratedCorpus(t *testing.T) {
	doc, ok := corpus.ByName("scan")
	if !ok {
		t.Fatal("corpus.ByName(\"scan\") not found")
	}
	for _, tg := range bench.Targets {
		t.Run(tg.Capability, func(t *testing.T) {
			c, ok := bench.CaseFor(tg.Capability)
			if !ok {
				t.Fatalf("no case for %q", tg.Capability)
			}
			in, err := c.Prepare(doc)
			if errors.Is(err, bench.ErrIneligible) {
				t.Skipf("scan is ineligible for %s", tg.Capability)
			}
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if _, err := c.Run(in); err != nil && !errors.Is(err, bench.ErrIneligible) {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}

// TestJPEGQualityIsPinned pins design spec section 3.3: the lossy pass runs at
// a fixed quality so a size win can never come from discarding more image.
func TestJPEGQualityIsPinned(t *testing.T) {
	if bench.JPEGQuality != 75 {
		t.Errorf("JPEGQuality = %d, want 75", bench.JPEGQuality)
	}
	var _ byblos.OptimizeOptions // the constant is only meaningful against this type
}
