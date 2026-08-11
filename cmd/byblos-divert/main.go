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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/dobbo-ca/byblos"
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

// sweepResult is what one walk observed. lines are the per-page diagnostics —
// the same text the serial version wrote to stderr as it went — in lexical path
// order.
type sweepResult struct {
	files, pages, unreadable int
	lines                    []string
}

// sweep walks root and extracts every page of every PDF, with workers files in
// flight at once.
//
// ORDER IS PART OF THE CONTRACT. The whole use of this tool is diffing one
// build against another over the same corpus, so output must depend on the
// corpus and not on which worker finished first. Each file's lines are gathered
// into its own slot and concatenated in path order at the end; nothing is
// printed from a worker. Buffering the lot is affordable — a full govdocs1 run
// is about 9.6 MB of lines.
//
// The counters byblos keeps are package-level and already mutex-guarded
// (stats.go), so they need nothing here.
func sweep(root string, workers int) (sweepResult, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return sweepResult{}, err
	}
	sort.Strings(paths)

	if workers < 1 {
		workers = 1
	}
	per := make([][]string, len(paths))
	pages := make([]int, len(paths))
	unreadable := make([]int, len(paths))

	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				per[i], pages[i], unreadable[i] = sweepFile(paths[i])
			}
		}()
	}
	for i := range paths {
		work <- i
	}
	close(work)
	wg.Wait()

	out := sweepResult{files: len(paths)}
	for i := range paths {
		out.pages += pages[i]
		out.unreadable += unreadable[i]
		out.lines = append(out.lines, per[i]...)
	}
	return out, nil
}

// sweepFile is one document's share of the walk. It touches nothing shared
// except byblos's own counters, so it is safe to run many at once.
func sweepFile(path string) (lines []string, pages, unreadable int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 1
	}
	defer f.Close()
	infos, err := byblos.Inspect(f)
	if err != nil {
		return []string{fmt.Sprintf("inspect %s: %v", path, err)}, 0, 1
	}
	for _, pi := range infos {
		pages++
		if _, err := f.Seek(0, 0); err != nil {
			return lines, pages, unreadable
		}
		if _, err := byblos.ExtractPageRaster(f, pi.Index); err != nil {
			lines = append(lines, fmt.Sprintf("%s page %d: %v", path, pi.Index, err))
		}
	}
	return lines, pages, unreadable
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
			Files, Pages, Unreadable int
			byblos.ExtractCounters
			DivertRate    float64
			UnhandledRate float64
		}{res.files, res.pages, res.unreadable, c, c.DivertRate(), c.UnhandledRate()})
		return
	}

	fmt.Printf("files       %d\n", res.files)
	fmt.Printf("unreadable  %d\n", res.unreadable)
	fmt.Printf("pages       %d\n", c.Attempted)
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
