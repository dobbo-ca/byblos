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
//	byblos-divert /path/to/pdfs
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dobbo-ca/byblos"
)

func main() {
	jsonOut := false
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-json" {
		jsonOut = true
		args = args[1:]
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: byblos-divert [-json] <dir>")
		os.Exit(2)
	}

	var files, pages, unreadable int
	err := filepath.WalkDir(args[0], func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		files++
		f, err := os.Open(path)
		if err != nil {
			unreadable++
			return nil
		}
		defer f.Close()
		infos, err := byblos.Inspect(f)
		if err != nil {
			unreadable++
			fmt.Fprintf(os.Stderr, "inspect %s: %v\n", path, err)
			return nil
		}
		for _, pi := range infos {
			pages++
			if _, err := f.Seek(0, 0); err != nil {
				return nil
			}
			if _, err := byblos.ExtractPageRaster(f, pi.Index); err != nil {
				fmt.Fprintf(os.Stderr, "%s page %d: %v\n", path, pi.Index, err)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-divert:", err)
		os.Exit(1)
	}

	c := byblos.ExtractStats()
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Files, Pages, Unreadable int
			byblos.ExtractCounters
			DivertRate    float64
			UnhandledRate float64
		}{files, pages, unreadable, c, c.DivertRate(), c.UnhandledRate()})
		return
	}

	fmt.Printf("files       %d\n", files)
	fmt.Printf("unreadable  %d\n", unreadable)
	fmt.Printf("pages       %d\n", c.Attempted)
	fmt.Printf("extracted   %d\n", c.Extracted)
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
