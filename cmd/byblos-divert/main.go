// Command byblos-divert measures how often ExtractPageRaster declines a page.
//
// Design spec section 2 makes the entire scope of Byblos conditional on that
// case being rare, and FUTURE.md makes a PDF renderer conditional on this
// number. Run it over a real archive sample before anyone argues from
// intuition.
//
// Read the unhandled rate, not the divert rate: a page Byblos cannot read at
// all is not a page it handled, and only the former counts both.
//
//	byblos-divert [-json] [-j N] /path/to/pdfs
//
// Files are swept in parallel; -j sets how many at once, or BYBLOS_JOBS does.
// Per-page lines are emitted in lexical path order whatever the worker count,
// so two runs stay diffable — see sweep.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/sample"
)

// defaultJobs is deliberately a small constant rather than NumCPU. These sweeps
// run for tens of minutes beside whatever else is on the machine, and a default
// that claims every core makes the box unusable for the length of a govdocs1
// run. Ask for more with -j when the machine is yours.
const defaultJobs = 4

const jobsEnv = "BYBLOS_JOBS"

// jobs resolves the worker count: an explicit -j wins, then BYBLOS_JOBS, then
// defaultJobs. Anything unparseable or non-positive falls back rather than
// failing — a mistyped environment variable should not stop a two-hour sweep
// that was going to be fine at the default.
func jobs(flagVal int) int {
	if flagVal > 0 {
		return flagVal
	}
	if n, err := strconv.Atoi(os.Getenv(jobsEnv)); err == nil && n > 0 {
		return n
	}
	return defaultJobs
}

// sweepResult is what one walk observed: the population, and the per-page
// diagnostics — the same text the serial version wrote to stderr as it went —
// in lexical path order.
//
// THE POPULATION IS internal/sample's AND NOT THIS TOOL'S. It used to be
// counted here, and counting it here is what byb-wj2 had to reconcile: this
// tool called a document that Inspect refused "unreadable" and gave it zero
// pages, while the measurement probes counted the same document's pages off its
// page tree. The two definitions differed by 342 pages over the pinned sample
// and neither said so. sample.Walk now decides, so a lane that walks with this
// tool and a lane that walks with the package inherit one answer.
type sweepResult struct {
	pop   sample.Population
	lines []string
}

// sweep walks root and extracts every page of every PDF, with workers files in
// flight at once.
//
// ORDER IS PART OF THE CONTRACT. The whole use of this tool is diffing one
// build against another over the same corpus, so output must depend on the
// corpus and not on which worker finished first. Each file's lines are gathered
// into its own slot by sample.Doc.Index and concatenated in path order at the
// end; nothing is printed from a worker. Buffering the lot is affordable — a
// full govdocs1 run is about 9.6 MB of lines.
//
// The counters byblos keeps are package-level and already mutex-guarded
// (stats.go), so they need nothing here.
func sweep(root string, workers int) (sweepResult, error) {
	paths, err := sample.Paths(root)
	if err != nil {
		return sweepResult{}, err
	}
	per := make([][]string, len(paths))

	pop, err := sample.Walk(root, workers, func(d sample.Doc) {
		per[d.Index] = sweepDoc(d)
	})
	if err != nil {
		return sweepResult{}, err
	}

	out := sweepResult{pop: pop}
	for i := range per {
		out.lines = append(out.lines, per[i]...)
	}
	return out, nil
}

// sweepDoc is one document's share of the walk. It touches nothing shared
// except byblos's own counters, so it is safe to run many at once.
//
// It extracts the pages sample.Walk counted, and it reaches them through the
// page indices Inspect reports rather than through 1..Pages, because
// ExtractPageRaster is indexed by PageInfo.Index. A document whose page tree
// sample.Walk could count but that Inspect will not read at all is reported and
// contributes no extraction attempt — and it is NOT removed from the
// population, which is the whole of byb-wj2.
func sweepDoc(d sample.Doc) (lines []string) {
	if d.Err != nil {
		return []string{fmt.Sprintf("open %s: %v", d.Path, d.Err)}
	}
	if _, err := d.File.Seek(0, 0); err != nil {
		return []string{fmt.Sprintf("seek %s: %v", d.Path, err)}
	}
	infos, err := byblos.Inspect(d.File)
	if err != nil {
		return []string{fmt.Sprintf("inspect %s: %v", d.Path, err)}
	}
	for _, pi := range infos {
		if _, err := d.File.Seek(0, 0); err != nil {
			return lines
		}
		if _, err := byblos.ExtractPageRaster(d.File, pi.Index); err != nil {
			lines = append(lines, fmt.Sprintf("%s page %d: %v", d.Path, pi.Index, err))
		}
	}
	return lines
}

func main() {
	jsonOut := flag.Bool("json", false, "emit the summary as JSON")
	j := flag.Int("j", 0, "files to sweep in parallel (default "+
		strconv.Itoa(defaultJobs)+", or $"+jobsEnv+")")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: byblos-divert [-json] [-j N] <dir>")
		os.Exit(2)
	}

	res, err := sweep(flag.Arg(0), jobs(*j))
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-divert:", err)
		os.Exit(1)
	}
	for _, line := range res.lines {
		fmt.Fprintln(os.Stderr, line)
	}

	c := byblos.ExtractStats()
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Files, Documents, Unopenable, Pages int
			byblos.ExtractCounters
			DivertRate    float64
			UnhandledRate float64
		}{res.pop.Files, res.pop.Documents, res.pop.Unopenable, res.pop.Pages,
			c, c.DivertRate(), c.UnhandledRate()})
		return
	}

	// Files, documents and pages are the population (internal/sample); attempted
	// and below are what this run did with it. They are printed apart because
	// conflating them is how byb-wj2 happened: "pages" used to mean whichever of
	// the two the reader assumed.
	fmt.Printf("files       %d\n", res.pop.Files)
	fmt.Printf("unopenable  %d\n", res.pop.Unopenable)
	fmt.Printf("documents   %d\n", res.pop.Documents)
	fmt.Printf("pages       %d\n", res.pop.Pages)
	fmt.Printf("attempted   %d\n", c.Attempted)
	fmt.Printf("extracted   %d\n", c.Extracted)
	// Of the extracted pages, those whose raster does not fill the page box.
	// byb-b1.3 retired the "not-page-covering" reason, and these are the pages
	// it used to name: ordinary scans placed at their natural resolution. They
	// are worth watching anyway, because a page that is 3% raster is a page
	// whose scanner did something strange, and nothing else reports it.
	fmt.Printf("  partial   %d\n", c.Partial)
	fmt.Printf("diverted    %d  (%.2f%%)\n", c.Diverted, 100*c.DivertRate())
	fmt.Printf("failed      %d\n", c.Failed)
	// The headline number. Reporting the divert rate alone is what let byb-5kk
	// hide 1,266 unreadable pages behind a reassuring percentage.
	fmt.Printf("unhandled   %d  (%.2f%%)\n", c.Diverted+c.Failed, 100*c.UnhandledRate())
	if len(c.Reasons) > 0 {
		fmt.Println("reasons:")
		keys := make([]string, 0, len(c.Reasons))
		for k := range c.Reasons {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if c.Reasons[keys[i]] != c.Reasons[keys[j]] {
				return c.Reasons[keys[i]] > c.Reasons[keys[j]]
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			fmt.Printf("  %-20s %d\n", k, c.Reasons[k])
		}
	}
}
