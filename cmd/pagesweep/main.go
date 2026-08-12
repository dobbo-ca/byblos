// Command pagesweep records what every page of every document under a root
// decodes to, so two builds can be diffed line for line.
//
//	pagesweep [-j N] -out FILE /path/to/pdfs
//
// One line per page, TSV, in lexical path order whatever the worker count:
//
//	rel <TAB> page <TAB> contentBytes <TAB> error
//
// A document pdfdoc.Open refuses contributes ONE line with page 0 and its open
// error, because it has no pages to report.
//
// contentBytes is len(Page.Content) -- the concatenated, decoded content
// streams. It is recorded for every page and not only for failures, because the
// regression this exists to measure is a page that still succeeds and returns
// FEWER bytes than before. An error-only diff would call that no change.
//
// This is a measurement tool for byb-3iw, not a shipped command.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/dobbo-ca/byblos/internal/sample"
)

func main() {
	jobs := flag.Int("j", sample.DefaultJobs, "documents to open at once")
	out := flag.String("out", "", "file to write the TSV to")
	flag.Parse()
	if flag.NArg() != 1 || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: pagesweep [-j N] -out FILE DIR")
		os.Exit(2)
	}

	paths, err := sample.Paths(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pagesweep:", err)
		os.Exit(1)
	}
	lines := make([][]string, len(paths))
	var mu sync.Mutex

	pop, err := sample.Walk(flag.Arg(0), *jobs, func(d sample.Doc) {
		var got []string
		if d.Err != nil {
			got = []string{row(d.Rel, 0, -1, d.Err)}
		} else {
			for n := 1; n <= d.Doc.PageCount(); n++ {
				p, err := d.Doc.Page(n)
				size := -1
				if err == nil {
					size = len(p.Content)
				}
				got = append(got, row(d.Rel, n, size, err))
			}
		}
		mu.Lock()
		lines[d.Index] = got
		mu.Unlock()
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pagesweep:", err)
		os.Exit(1)
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pagesweep:", err)
		os.Exit(1)
	}
	w := bufio.NewWriter(f)
	for _, doc := range lines {
		for _, l := range doc {
			fmt.Fprintln(w, l)
		}
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "pagesweep:", err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "pagesweep:", err)
		os.Exit(1)
	}
	fmt.Printf("files %d documents %d unopenable %d pages %d\n",
		pop.Files, pop.Documents, pop.Unopenable, pop.Pages)
}

// row keeps the error on one line and stripped of the absolute path, so two
// machines produce the same bytes for the same finding.
func row(rel string, page, size int, err error) string {
	msg := ""
	if err != nil {
		msg = strings.Join(strings.Fields(err.Error()), " ")
	}
	return fmt.Sprintf("%s\t%d\t%d\t%s", rel, page, size, msg)
}
