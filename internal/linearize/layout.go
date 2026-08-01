package linearize

import (
	"fmt"
	"slices"
)

// The Annex F object partition and renumbering.
//
// F.3.1 divides a linearized file into eleven parts and constrains both the
// physical order of the objects and their NUMBERING. Byblos emits parts 1-9 and
// 11; part 10, the overflow hint stream, is never written (qpdf does not write
// one either, and it has not been necessary since PDF 1.4).
//
//	part 1   header
//	part 2   linearization parameter dictionary
//	part 3   first-page cross-reference section and trailer
//	part 4   catalog and other document-level objects
//	part 5   primary hint stream
//	part 6   first-page section
//	part 7   remaining pages, in page order
//	part 8   objects shared between later pages but not used by the first page
//	part 9   everything else: the page tree, the info dictionary, thumbnails
//	part 11  main cross-reference section and trailer
//
// The numbering is two contiguous groups: parts 7, 8 and 9 take 1..k, and parts
// 2, 4, 5 and 6 take the numbers after that. Part 6 being one ascending run
// starting at the first page's object number is what lets the front
// cross-reference section be a single subsection and the page-offset hint table
// address a page by "the next N object numbers".

// Graph is a document's object graph, with no PDF parser attached. Every object
// number in it is the ORIGINAL number from the input document.
//
// Refs holds the outgoing references of every object that must survive, so its
// key set is also the set of objects to be written. A page dictionary's /Parent
// and /Thumb are deliberately absent from its entry: /Parent points back at the
// page tree, which Annex F.3.10 puts in part 9, and a thumbnail is not needed to
// display the page. Both would otherwise drag part-9 material into the
// first-page section. They stay in the key set because they are still reachable
// and still get written -- they just do not steer the partition.
type Graph struct {
	Refs     map[int][]int
	Pages    []int // page object numbers, in page order
	PageTree []int // every /Pages node, root first
	Catalog  int
	Info     int // 0 when the document has none

	// OpenDoc holds the direct targets of the catalog keys a reader consults
	// before it can display anything: /ViewerPreferences, /PageMode, /Threads,
	// /OpenAction and /AcroForm. They belong to part 4.
	OpenDoc []int

	// Outlines is the catalog's /Outlines target, and UseOutlines records
	// whether /PageMode is /UseOutlines. An outline tree that will be shown
	// immediately belongs to the first-page section; one that will not belongs
	// to part 9 (F.3.8).
	Outlines    int
	UseOutlines bool

	// Other holds the direct targets of every remaining catalog key, plus the
	// info dictionary. An object reachable from one of these is document-level
	// material that no single page owns, so it may not be filed under a page
	// even when exactly one page also refers to it.
	Other []int
}

// Plan is the layout decision. Every object number in it is the NEW number.
type Plan struct {
	// Renumber maps original object number to new, over exactly Graph.Refs's
	// key set.
	Renumber map[int]int

	Part4 []int   // catalog first
	Part6 []int   // Part6[0] is the first page's dictionary, i.e. /O
	Part7 [][]int // one group per page 2..N; each group's page dictionary first
	Part8 []int
	Part9 []int

	// PageShared[i] holds, for page i, the indices into the shared-object hint
	// table (Part6 followed by Part8) of the shared objects that page uses.
	// PageShared[0] is always empty: Table F.4 defines the first page's shared
	// objects implicitly.
	PageShared [][]int

	// Outlines holds the outline tree's objects in placement order, the
	// /Outlines root first. They sit at the end of part 6 or the end of part 9
	// -- and they are also members of whichever of those slices they went into,
	// so this is an index, not a fourth part. It is empty when the document has
	// no outline tree, and non-empty means the primary hint stream must carry
	// an outline hint table: the reference implementation computes one whether
	// the outlines went to part 6 or part 9, and warns "incorrect object count
	// in outline hint table" when the file does not have one.
	Outlines []int

	LinDict int // the linearization parameter dictionary's object number
	Hint    int // the primary hint stream's object number
	Catalog int
	Info    int // 0 when the document has none
	Size    int // trailer /Size for the whole file: every object plus object 0
	NPages  int
}

// PlanLayout partitions g and assigns new object numbers.
func PlanLayout(g Graph) (Plan, error) {
	if len(g.Pages) == 0 {
		return Plan{}, fmt.Errorf("linearize: the document has no pages")
	}
	if g.Catalog == 0 {
		return Plan{}, fmt.Errorf("linearize: the document has no catalog")
	}
	for _, n := range g.Pages {
		if _, ok := g.Refs[n]; !ok {
			return Plan{}, fmt.Errorf("linearize: page object %d is not in the graph", n)
		}
	}
	if _, ok := g.Refs[g.Catalog]; !ok {
		return Plan{}, fmt.Errorf("linearize: catalog object %d is not in the graph", g.Catalog)
	}

	// A closure never walks into a page dictionary or a page-tree node it did
	// not start from. Without that rule an outline item's /Dest, or a /Next
	// link, pulls a later page's objects into the first-page section -- and it
	// is how the reference implementation behaves (qpdf's updateObjectMaps
	// returns as soon as it reaches a /Page node that is not the top of the
	// walk).
	barrier := map[int]bool{}
	for _, n := range g.Pages {
		barrier[n] = true
	}
	for _, n := range g.PageTree {
		barrier[n] = true
	}

	firstPage := g.closure(g.Pages[0], barrier)
	otherPages := make([]map[int]bool, len(g.Pages))
	pageCount := map[int]int{} // object -> how many of pages 2..N reach it
	for i := 1; i < len(g.Pages); i++ {
		otherPages[i] = g.closure(g.Pages[i], barrier)
		for n := range otherPages[i] {
			pageCount[n]++
		}
	}
	openDoc := g.closureOf(g.OpenDoc, barrier)
	others := g.closureOf(g.Other, barrier)
	var outlines map[int]bool
	if g.Outlines != 0 {
		outlines = g.closure(g.Outlines, barrier)
	}

	var p Plan
	p.NPages = len(g.Pages)
	part7 := make([][]int, len(g.Pages)-1)
	var outlineGroup []int

	// The cascade, first match wins. It mirrors the reference implementation's
	// (QPDF_linearization.cc, calculateLinearizationData): an object reachable
	// from the first page goes to part 6 whether or not later pages also use
	// it, but an object reachable from exactly one LATER page is only that
	// page's private property if no document-level key reaches it too.
	for n := range g.Refs {
		switch {
		case n == g.Catalog:
			p.Part4 = append(p.Part4, n)
		case outlines[n]:
			outlineGroup = append(outlineGroup, n)
		case openDoc[n]:
			p.Part4 = append(p.Part4, n)
		case firstPage[n]:
			p.Part6 = append(p.Part6, n)
		case pageCount[n] == 1 && !others[n]:
			for i := 1; i < len(g.Pages); i++ {
				if otherPages[i][n] {
					part7[i-1] = append(part7[i-1], n)
					break
				}
			}
		case pageCount[n] > 1:
			p.Part8 = append(p.Part8, n)
		default:
			p.Part9 = append(p.Part9, n)
		}
	}

	// Ordering inside each part. The leading object of each is fixed by Annex
	// F; everything else is sorted by original number so the output is
	// deterministic for a given input.
	p.Part4 = lead(g.Catalog, p.Part4)
	p.Part6 = lead(g.Pages[0], p.Part6)
	for i := range part7 {
		part7[i] = lead(g.Pages[i+1], part7[i])
	}
	p.Part7 = part7
	slices.Sort(p.Part8)
	p.Part9 = leadAll(g.PageTree, p.Part9)

	// F.3.8: the outline tree goes in the first-page section when /PageMode is
	// /UseOutlines, because the reader has to draw it before it can show
	// anything, and in part 9 otherwise. Either way it is placed as one
	// contiguous run with the /Outlines root at its head, which is what makes
	// the outline hint table's single (object, offset, count, length) tuple
	// describe it.
	outlineGroup = lead(g.Outlines, outlineGroup)
	if g.UseOutlines {
		p.Part6 = append(p.Part6, outlineGroup...)
	} else {
		p.Part9 = append(p.Part9, outlineGroup...)
	}

	// Numbering, F.3.1. Parts 7, 8 and 9 take 1..k in emission order; the
	// first group follows, in the order the file physically holds it apart from
	// the hint stream, which the reference implementation also numbers before
	// part 6.
	p.Renumber = make(map[int]int, len(g.Refs))
	next := 1
	assign := func(ns []int) {
		for _, n := range ns {
			p.Renumber[n] = next
			next++
		}
	}
	for _, grp := range p.Part7 {
		assign(grp)
	}
	assign(p.Part8)
	assign(p.Part9)
	p.LinDict = next
	next++
	assign(p.Part4)
	p.Hint = next
	next++
	assign(p.Part6)
	p.Size = next

	if len(p.Renumber) != len(g.Refs) {
		return Plan{}, fmt.Errorf("linearize: partitioned %d objects out of %d",
			len(p.Renumber), len(g.Refs))
	}

	// The shared-object hint table is part 6 followed by part 8, and a page's
	// shared identifiers are indices into that concatenation.
	sharedIdx := make(map[int]int, len(p.Part6)+len(p.Part8))
	for i, n := range p.Part6 {
		sharedIdx[n] = i
	}
	for i, n := range p.Part8 {
		sharedIdx[n] = len(p.Part6) + i
	}
	p.PageShared = make([][]int, len(g.Pages))
	p.PageShared[0] = nil
	for i := 1; i < len(g.Pages); i++ {
		var ids []int
		for n := range otherPages[i] {
			if k, ok := sharedIdx[n]; ok {
				ids = append(ids, k)
			}
		}
		slices.Sort(ids)
		p.PageShared[i] = ids
	}

	p.Catalog = p.Renumber[g.Catalog]
	if g.Info != 0 {
		p.Info = p.Renumber[g.Info]
	}
	// Renumber the parts in place so callers never see original numbers.
	p.Part4 = renumberAll(p.Renumber, p.Part4)
	p.Part6 = renumberAll(p.Renumber, p.Part6)
	for i := range p.Part7 {
		p.Part7[i] = renumberAll(p.Renumber, p.Part7[i])
	}
	p.Part8 = renumberAll(p.Renumber, p.Part8)
	p.Part9 = renumberAll(p.Renumber, p.Part9)
	p.Outlines = renumberAll(p.Renumber, outlineGroup)
	return p, nil
}

// closure walks g.Refs from start, refusing to enter any barrier object other
// than start itself.
func (g Graph) closure(start int, barrier map[int]bool) map[int]bool {
	seen := map[int]bool{}
	if _, ok := g.Refs[start]; !ok {
		return seen
	}
	stack := []int{start}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		if _, ok := g.Refs[n]; !ok {
			continue
		}
		if n != start && barrier[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, g.Refs[n]...)
	}
	return seen
}

// closureOf is closure over several seeds, with no exemption: a seed that is
// itself a page or a page-tree node is not entered.
func (g Graph) closureOf(seeds []int, barrier map[int]bool) map[int]bool {
	out := map[int]bool{}
	for _, s := range seeds {
		if barrier[s] {
			continue
		}
		for n := range g.closure(s, barrier) {
			out[n] = true
		}
	}
	return out
}

// lead sorts ns and moves first to the front, if it is there at all.
func lead(first int, ns []int) []int {
	slices.Sort(ns)
	if i := slices.Index(ns, first); i > 0 {
		ns = slices.Insert(slices.Delete(slices.Clone(ns), i, i+1), 0, first)
	}
	return ns
}

// leadAll sorts ns and moves every member of firsts, in order, to the front.
func leadAll(firsts, ns []int) []int {
	slices.Sort(ns)
	var head []int
	for _, f := range firsts {
		if i := slices.Index(ns, f); i >= 0 {
			head = append(head, f)
			ns = slices.Delete(slices.Clone(ns), i, i+1)
		}
	}
	return append(head, ns...)
}

func renumberAll(m map[int]int, ns []int) []int {
	out := make([]int, len(ns))
	for i, n := range ns {
		out[i] = m[n]
	}
	return out
}
