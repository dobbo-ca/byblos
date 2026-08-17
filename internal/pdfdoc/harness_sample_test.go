package pdfdoc_test

// The BYBLOS_SAMPLE harness for byb-yul.6.
//
// EXTERNAL TEST PACKAGE, DELIBERATELY. This file imports internal/sample,
// which imports internal/pdfdoc -- a plain `package pdfdoc` test file
// importing internal/sample back would be an import cycle. arch_test.go's
// pdfcpu-import guard still covers this file: it keys on ImportPath, and
// this directory's ImportPath (internal/pdfdoc) is already the one package
// allowed to import pdfcpu, regardless of which of its test files --
// internal or external -- is the one doing it.
//
// GATED ON BYBLOS_SAMPLE, and built as a standalone binary rather than run
// with `go test`, because it is slow: 4,899 documents built twice (full
// reverse, drop-middle) is measured at 7 to 12 minutes, well past the point
// a backgrounded `go test` process is silently killed (~15 minutes is the
// budget for the whole package's test binary, not this one test). Build it
// once and run the binary directly:
//
//	go test -c -o pdfdoc.harness ./internal/pdfdoc
//	BYBLOS_SAMPLE=/path/to/sample BYBLOS_JOBS=6 \
//	  nohup ./pdfdoc.harness -test.run TestSampleBuildFromPages -test.v \
//	  > harness.log 2>&1 &
//
// BASELINE, measured at HEAD before byb-yul.6 (api.WriteContext as the
// writer): 4,899 of 5,063 multi-page documents built per sequence, 164
// refused per sequence, 11 documents with a dangling reference. AFTER: the
// same 4,899 built, the same 164 refused, ZERO dangling.
//
// api.Validate failures (badValidate) were claimed COUNT- and IDENTITY-equal
// to HEAD by the implementer's report, but that claim was written before the
// full-sample run was launched, and the harness as first shipped could not
// have re-derived it: BYBLOS_SAMPLE_OUT wrote only dangling-reference lines,
// never which documents failed Validate, so "the same paths, not just the
// same total" was unobtainable from this file's own output (found in
// adversarial review; the review's own out-of-band harness confirmed the
// IDENTITY claim true on the numbers it happened to have written down, but
// this file could not reproduce that check itself). Every result line below
// now carries its Validate outcome too, so a re-run's BYBLOS_SAMPLE_OUT can
// actually be diffed against a prior one for both dangling and badValidate,
// by path and sequence.
import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/dobbo-ca/byblos/internal/sample"
)

// seqReverse and seqDropMiddle are the two sequences the pinned-sample
// measurement in buildpages.go's package comment ran: every page in reverse
// order, and every page except the middle one.
func seqReverse(n int) []int {
	out := make([]int, 0, n)
	for i := n; i >= 1; i-- {
		out = append(out, i)
	}
	return out
}

func seqDropMiddle(n int) []int {
	mid := (n + 1) / 2
	out := make([]int, 0, n-1)
	for i := 1; i <= n; i++ {
		if i == mid {
			continue
		}
		out = append(out, i)
	}
	return out
}

// sampleResult is one (document, sequence) build's outcome.
type sampleResult struct {
	path        string
	seq         string
	built       bool
	dangling    []pdfdoc.DanglingRef
	badValidate bool
}

func TestSampleBuildFromPages(t *testing.T) {
	root := os.Getenv("BYBLOS_SAMPLE")
	if root == "" {
		t.Skip("no BYBLOS_SAMPLE")
	}
	workers := sample.DefaultJobs
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && v > 0 {
		workers = v
	}

	paths, err := sample.Paths(root)
	if err != nil {
		t.Fatalf("sample.Paths: %v", err)
	}
	t.Logf("files=%d workers=%d", len(paths), workers)

	var (
		mu                                           sync.Mutex
		results                                      []sampleResult
		multi, built, refused, dangling, badValidate int
	)

	one := func(path string) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		d, err := pdfdoc.Open(f)
		if err != nil {
			return
		}
		n := d.PageCount()
		if n < 2 {
			return
		}
		mu.Lock()
		multi++
		mu.Unlock()

		for _, seq := range []struct {
			name string
			pp   []int
		}{{"reverse", seqReverse(n)}, {"dropmid", seqDropMiddle(n)}} {
			if _, err := f.Seek(0, 0); err != nil {
				return
			}
			src := make([]pdfdoc.PageSource, 0, len(seq.pp))
			for _, p := range seq.pp {
				src = append(src, pdfdoc.PageSource{Source: f, Page: p})
			}
			var buf bytes.Buffer
			if err := pdfdoc.BuildFromPages(&buf, src); err != nil {
				mu.Lock()
				refused++
				results = append(results, sampleResult{path: path, seq: seq.name, built: false})
				mu.Unlock()
				continue
			}
			out := buf.Bytes()
			hits, herr := pdfdoc.DanglingRefs(out)
			verr := pdfdoc.Validate(bytes.NewReader(out))
			mu.Lock()
			built++
			r := sampleResult{path: path, seq: seq.name, built: true, badValidate: verr != nil}
			if herr == nil && len(hits) > 0 {
				dangling++
				r.dangling = hits
			}
			if verr != nil {
				badValidate++
			}
			// One result per built (path, seq), whether or not it dangled or
			// failed Validate, so BYBLOS_SAMPLE_OUT can record every outcome
			// -- not just the dangling ones -- and a re-run's output is
			// diffable against a prior one by path+seq for both.
			results = append(results, r)
			mu.Unlock()
		}
	}

	work := make(chan string)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				func() {
					defer func() { recover() }()
					one(p)
				}()
			}
		}()
	}
	for _, p := range paths {
		work <- p
	}
	close(work)
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		if results[i].path != results[j].path {
			return results[i].path < results[j].path
		}
		return results[i].seq < results[j].seq
	})

	if outPath := os.Getenv("BYBLOS_SAMPLE_OUT"); outPath != "" {
		var lines []string
		for _, r := range results {
			if len(r.dangling) == 0 && !r.badValidate {
				continue
			}
			var refs []string
			for _, h := range r.dangling {
				refs = append(refs, h.String())
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%d\t%t\t%s",
				r.path, r.seq, len(r.dangling), r.badValidate, strings.Join(refs, " ")))
		}
		_ = os.WriteFile(outPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}

	t.Logf("multi=%d built=%d refused=%d dangling=%d badValidate=%d", multi, built, refused, dangling, badValidate)
	for _, r := range results {
		if len(r.dangling) == 0 {
			continue
		}
		t.Errorf("%s %s: %d dangling refs: %v", r.path, r.seq, len(r.dangling), r.dangling)
	}
}
