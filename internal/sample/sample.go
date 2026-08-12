// Package sample walks a directory of PDF documents and reports the POPULATION
// that a rate measured over that directory is a rate of.
//
// IT EXISTS BECAUSE THE POPULATION WAS NOT ONE NUMBER. Two independently
// verified measurement lanes reported the same pinned sample as 169,376 pages
// and as 169,034 pages, agreed exactly on three of its four corpora, and
// differed by 342 pages on the fourth. Neither lane was wrong and neither had
// made an arithmetic mistake. They had walked the sample through two different
// entry points, and neither entry point said what it counted, so the difference
// was invisible until someone put the two totals side by side (byb-wj2).
//
// # The three readability predicates, and which one the count uses
//
// A document or a page can fail to be read in three different ways. They are
// not the same predicate and they do not have the same answer:
//
//   - UNOPENABLE DOCUMENT. pdfdoc.Open refuses the file, so it has no page tree
//     and therefore no page count at all. It is OUT: it contributes zero pages,
//     and Population.Unopenable counts it separately so it never looks like a
//     document of zero pages. One document of the pinned sample is unopenable
//     (govdocs1/pdfs/700620.pdf, "xrefsection: missing trailer dict").
//
//   - PARTIALLY READ PAGE. The document opens, the page exists in its page
//     tree, and byblos cannot walk all of the page's content -- a content
//     stream that stops early, an inline image with no EI, a page dictionary
//     that will not resolve. It is IN. The page is on the paper whether or not
//     byblos read every operator on it, so removing it from the denominator
//     would flatter every rate divided by it. byblos.Inspect returns such a
//     page carrying a SeverityError diagnostic, and 7 pages of the pinned
//     sample are in this state.
//
//   - UNREADABLE DOCUMENT. A document that opens but holds at least one
//     partially read page. THIS IS NOT A PREDICATE THIS PACKAGE HAS, and that
//     is the whole point of the package. It was byblos.Inspect's behaviour
//     before byb-3jq: one page it could not walk cost the entire document, so
//     17 openable documents holding 342 pages fell out of the count. Those 342
//     pages are the whole of the disagreement byb-wj2 reconciled. Inspect no
//     longer does this, but nothing structurally prevents it coming back, which
//     is why TestPopulationAgreesWithInspect exists in the root package.
//
// # The definition
//
// Population.Pages is the sum of pdfdoc.Doc.PageCount over every document under
// the root that pdfdoc.Open accepts. Nothing about what byblos can do with a
// page changes whether the page is counted. That is deliberate: a population
// that shrinks when the reader gets worse, and grows when it gets better, is
// not a denominator, and byb-5kk is the record of what that costs -- a
// reassuring percentage over a population that had quietly dropped the pages it
// was failing on.
//
// # Why this is not built on byblos.Inspect
//
// Inspect would be the obvious way to count pages, and it is the way one of the
// two lanes did. It is deliberately not used here. Counting through pdfdoc gives
// the root package's TestPopulationAgreesWithInspect two INDEPENDENT derivations
// of the same number to compare; counting through Inspect would make that test
// compare Inspect with itself and pass over any regression at all.
package sample

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// Population is what one walk found. Files is every candidate path; Documents
// and Unopenable partition it; Pages is the count under the definition in the
// package comment.
//
// Documents is carried rather than left as Files-Unopenable so that a caller
// printing the three cannot print two of them from this and derive the third
// some other way -- which is the shape of the drift this package exists to end.
type Population struct {
	Files      int // every *.pdf path under the root, openable or not
	Documents  int // the ones pdfdoc.Open accepted
	Unopenable int // the ones it refused; they contribute no pages
	Pages      int // sum of PageCount over Documents
}

// Doc is one document, handed to Walk's callback.
//
// Doc and File are open for the duration of the callback and are closed after
// it returns, so a callback that wants to keep either must copy what it needs.
// File is positioned wherever pdfdoc.Open left it; a callback that reads from it
// must Seek first.
//
// Doc is nil exactly when Err is non-nil, and then the document is UNOPENABLE
// and Pages is 0. A callback that only wants readable documents checks Err.
type Doc struct {
	Path  string // the path as walked, absolute or relative as the root was
	Rel   string // Path relative to the root, for reporting
	Index int    // position in lexical order, so a concurrent callback can
	// slot its result and still emit in corpus order
	Pages int // this document's contribution to Population.Pages

	Doc  pdfdoc.Doc
	File *os.File
	Err  error
}

// DefaultJobs matches cmd/byblos-divert's own default and is here so a caller
// that has no opinion does not have to invent one. It is a small constant rather
// than NumCPU on purpose: these walks run beside whatever else is on the machine.
const DefaultJobs = 4

// Paths enumerates the PDF documents under root in lexical order.
//
// ORDER IS PART OF THE CONTRACT, because the use of these walks is diffing one
// build against another over the same directory. The filter is a case-insensitive
// .pdf extension and nothing else: no size floor, no magic-byte check, no attempt
// to skip a file that looks broken. A file that is not a PDF at all is a document
// pdfdoc.Open refuses, which is a fact the population reports rather than hides.
//
// A DIRECTORY IT CANNOT READ IS AN ERROR AND NOT AN EMPTY DIRECTORY. Every walk
// in this repository before this one wrote `if err != nil || e.IsDir() || ...
// { return nil }`, which discards WalkDir's error along with the entries it
// could not stat: an unreadable subdirectory makes the sample smaller and the
// walk still succeeds. That is survivable in a probe reporting a shape and not
// in the thing that defines a denominator, so it is refused here. A caller that
// genuinely wants a partial walk can catch the error and say so.
func Paths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// Walk opens every document under root and reports the population, calling fn
// for each document as it goes.
//
// fn RUNS ON A WORKER GOROUTINE and up to workers of them run at once, so fn
// must be safe for concurrent use and must not assume it sees documents in
// order. Doc.Index is the lexical position, which is what a caller that needs
// ordered output slots its per-document result into. fn may be nil.
//
// The population is tallied here rather than by fn, so a caller cannot report a
// count this package did not produce.
func Walk(root string, workers int, fn func(Doc)) (Population, error) {
	paths, err := Paths(root)
	if err != nil {
		return Population{}, err
	}
	if workers < 1 {
		workers = DefaultJobs
	}

	pages := make([]int, len(paths))
	opened := make([]bool, len(paths))

	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				pages[i], opened[i] = visit(root, paths[i], i, fn)
			}
		}()
	}
	for i := range paths {
		work <- i
	}
	close(work)
	wg.Wait()

	p := Population{Files: len(paths)}
	for i := range paths {
		if opened[i] {
			p.Documents++
			p.Pages += pages[i]
			continue
		}
		p.Unopenable++
	}
	return p, nil
}

// visit is one document's share of the walk. A file that will not even open is
// reported through the same Err field as one pdfcpu refuses, because from the
// population's point of view they are the same fact: no page tree, no pages.
func visit(root, path string, index int, fn func(Doc)) (pages int, opened bool) {
	d := Doc{Path: path, Rel: rel(root, path), Index: index}
	f, err := os.Open(path)
	if err != nil {
		d.Err = err
		call(fn, d)
		return 0, false
	}
	defer f.Close()
	d.File = f

	doc, err := pdfdoc.Open(f)
	if err != nil {
		d.Err = err
		call(fn, d)
		return 0, false
	}
	d.Doc = doc
	d.Pages = doc.PageCount()
	call(fn, d)
	return d.Pages, true
}

func call(fn func(Doc), d Doc) {
	if fn != nil {
		fn(d)
	}
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}
