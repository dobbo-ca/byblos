package byblos

import (
	"bytes"
	"sync"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestConcurrentInspectAndExtractAreRaceFree drives the two read entry points
// from several goroutines at once, which is what any parallel corpus sweep does
// (cmd/byblos-divert -j).
//
// RUN IT IN ISOLATION AND UNDER -race OR IT PROVES NOTHING. The race it guards
// is in pdfcpu's LAZY GLOBAL configuration init, which happens on the first
// pdfdoc.Open in the process and never again. In a full package run some earlier
// test has already triggered that init, so this test cannot reach the racing
// code at all and passes for the wrong reason. Measured 2026-08-10: 10 data
// races when run alone with -race against bf31e3f, ZERO when run as part of
// `go test -race .` for the whole package. CI runs it alone; see ci.yml.
//
//	go test -race -run '^TestConcurrentInspectAndExtractAreRaceFree$' .
func TestConcurrentInspectAndExtractAreRaceFree(t *testing.T) {
	docs := corpus.All()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, d := range docs {
				infos, err := Inspect(bytes.NewReader(d.Data))
				if err != nil {
					continue
				}
				for _, pi := range infos {
					// The result is not the point; reaching pdfdoc.Open from
					// several goroutines at once is.
					_, _ = ExtractPageRaster(bytes.NewReader(d.Data), pi.Index)
				}
			}
		}()
	}
	wg.Wait()
}
