// Command byblos-annots measures how often an annotation that paints is lost
// because ExtractPageRaster returned the page's raster without it.
//
// byb-b1.3 removed the coverage gate for a lone raster, on the argument that
// nothing in the content stream can mark the page outside the placement. That
// argument is sound and says nothing about annotations, which are not in the
// content stream at all. This tool measures the gap the argument leaves.
//
// Three numbers, narrowing, and they answer different questions:
//
//   - ExtractedWithAnnot: pages Byblos extracted that carry an annotation which
//     paints. Every one of them loses that ink, because the returned raster is
//     the content stream's image and the annotation is not in it. This is the
//     silent loss, and most of it predates byb-b1.3.
//   - ExtractedNotCovering: the subset whose raster fell short of the page box.
//     Before byb-b1.3 these diverted, so their annotations were safe by
//     accident. This is what that change newly exposed.
//   - ExtractedOutside: the subset whose ink lands outside the raster
//     altogether, in the blank strip byb-b1.3's argument says nothing can mark.
//
// No appearance stream is rendered. That is a renderer, and design spec section
// 2 puts it out of scope; this reads dictionaries only.
//
//	byblos-annots [-jsonl out.jsonl] <dir>
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// tol mirrors paintTolerancePt, not coverTolerancePt. The raster box here is
// the exact float box, so the only slack needed is for arithmetic; coverage's
// 1.0pt allowance is about where the page edge is, which is a different
// question.
const tol = 1e-3

type annotRow struct {
	Subtype string     `json:"subtype"`
	Bucket  string     `json:"bucket"` // "" when it paints
	Paints  bool       `json:"paints"`
	Rect    [4]float64 `json:"rect"`
	Outside bool       `json:"outside"` // escapes the returned raster box
	OffPage bool       `json:"off_page"`
	HasOC   bool       `json:"has_oc"`
	Flags   int        `json:"flags"`
}

type pageRow struct {
	File       string     `json:"file"`
	Page       int        `json:"page"`
	Outcome    string     `json:"outcome"`
	Reason     string     `json:"reason,omitempty"`
	CoversPage bool       `json:"covers_page"`
	BoxExact   bool       `json:"box_exact"`
	RasterBox  [4]float64 `json:"raster_box"`
	PageBox    [4]float64 `json:"page_box"`
	Annots     []annotRow `json:"annots,omitempty"`
}

type summary struct {
	Files, FilesUnopenable      int
	Pages                       int
	Extracted, Diverted, Failed uint64
	Reasons                     map[string]uint64
	// The headline. Extracted pages carrying a painting annotation, and the
	// subset whose ink escapes the raster that was returned.
	ExtractedWithAnnot int
	// ExtractedNotCovering is the byb-b1.3 delta: a page carrying painting ink
	// that the old coverage gate would have diverted. Not a subset of
	// ExtractedOutside — the annotation can sit well inside the raster and
	// still be new exposure, because before b1.3 the whole page diverted.
	ExtractedNotCovering int
	ExtractedOutside     int
	// Annotations whose Rect is outside the CropBox entirely. A viewer shows
	// nothing there either, so these are held apart from the headline.
	OffPage int
	// Pages whose only painting candidate depends on a viewer synthesising an
	// appearance. The uncertainty band around the headline.
	ExtractedMaybe                 int
	DivertedWithAnnot              int
	SubtypeHistogram               map[string]int
	BucketHistogram                map[string]int
	HasOC, NoView                  int
	PagesBoxExact, PagesBoxMissing int
	// Pages that crashed the process from inside pdfcpu rather than returning
	// an error. Not a divert and not a failure: byblos has no outcome for them.
	PagesPanicked int
}

func main() {
	jsonlPath := ""
	args := os.Args[1:]
	if len(args) > 1 && args[0] == "-jsonl" {
		jsonlPath, args = args[1], args[2:]
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: byblos-annots [-jsonl out.jsonl] <dir>")
		os.Exit(2)
	}

	var jsonl io.WriteCloser = nopCloser{io.Discard}
	if jsonlPath != "" {
		f, err := os.Create(jsonlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "byblos-annots:", err)
			os.Exit(1)
		}
		jsonl = f
	}
	defer jsonl.Close()
	enc := json.NewEncoder(jsonl)

	s := summary{
		Reasons:          map[string]uint64{},
		SubtypeHistogram: map[string]int{},
		BucketHistogram:  map[string]int{},
	}
	root := args[0]
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		s.Files++
		scanFile(path, root, &s, enc)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-annots:", err)
		os.Exit(1)
	}

	c := byblos.ExtractStats()
	s.Pages = int(c.Attempted)
	s.Extracted, s.Diverted, s.Failed = c.Extracted, c.Diverted, c.Failed
	for k, v := range c.Reasons {
		s.Reasons[k] = v
	}
	report(s)
}

func scanFile(path, root string, s *summary, enc *json.Encoder) {
	f, err := os.Open(path)
	if err != nil {
		s.FilesUnopenable++
		return
	}
	defer f.Close()

	// pdfdoc.Open directly, not byblos.Inspect: Inspect returns an error for
	// the whole file on the first page it cannot walk, which would drop every
	// good page in the file — and those are exactly the pages this measures.
	d, err := pdfdoc.Open(f)
	if err != nil {
		s.FilesUnopenable++
		fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
		return
	}
	rel, _ := filepath.Rel(root, path)

	for n := 1; n <= d.PageCount(); n++ {
		scanPage(f, d, rel, n, s, enc)
	}
}

// scanPage measures one page and survives a panic from below.
//
// pdfcpu parses the page's content stream inside PageDict, and its TJ handling
// indexes an empty slice on a malformed array, so a single damaged page kills
// the process from inside pdfdoc.Page. pdfcpu's own fault.Catch recovers only
// its own panic type, which this is not. Recovering here is not a fix — any
// caller of byblos.Inspect or byblos.ExtractPageRaster on such a file crashes
// the same way, which is byb-avp — it is what lets a measurement over
// thousands of real files report the damage instead of dying on it.
func scanPage(f io.ReadSeeker, d pdfdoc.Doc, rel string, n int, s *summary, enc *json.Encoder) {
	defer func() {
		if r := recover(); r != nil {
			s.PagesPanicked++
			fmt.Fprintf(os.Stderr, "PANIC %s page %d: %v\n", rel, n, r)
		}
	}()

	row := pageRow{File: rel, Page: n}

	// Re-walk per page so one bad page does not cost its siblings. The raster
	// is the last placement, which is the candidate classify takes.
	if p, perr := d.Page(n); perr == nil {
		row.PageBox = [4]float64{p.CropBox.LLX, p.CropBox.LLY, p.CropBox.URX, p.CropBox.URY}
		if sc, werr := content.Walk(p.Content, p.Scope, d); werr == nil && len(sc.Images) > 0 {
			b := sc.Images[len(sc.Images)-1].Box
			row.RasterBox = [4]float64{b.LLX, b.LLY, b.URX, b.URY}
			row.BoxExact = true
		}
	}
	if row.BoxExact {
		s.PagesBoxExact++
	} else {
		s.PagesBoxMissing++
	}

	annots, _ := d.Annots(n)

	// The outcome comes from diffing the package counters rather than parsing
	// an error string: stats.go guarantees exactly one of
	// Extracted/Diverted/Failed moves per attempt, and on a divert exactly one
	// Reasons key moves with it.
	before := byblos.ExtractStats()
	if _, serr := f.Seek(0, 0); serr != nil {
		return
	}
	pr, _ := byblos.ExtractPageRaster(f, n)
	row.Outcome, row.Reason = diffStats(before, byblos.ExtractStats())
	if pr != nil {
		row.CoversPage = pr.CoversPage()
	}

	tallyPage(&row, annots, s)
	_ = enc.Encode(row)
}

func tallyPage(row *pageRow, annots []pdfdoc.Annot, s *summary) {
	var painting, outside, maybe int
	for _, a := range annots {
		bucket := a.Reason()
		ar := annotRow{
			Subtype: a.Subtype, Bucket: bucket, Paints: bucket == "",
			Rect:  [4]float64{a.Rect.LLX, a.Rect.LLY, a.Rect.URX, a.Rect.URY},
			HasOC: a.HasOC, Flags: a.Flags,
		}
		s.BucketHistogram[bucketName(bucket)]++
		if a.HasOC {
			s.HasOC++
		}
		if bucket == "noview-print-only" {
			s.NoView++
		}
		if bucket == "no-ap-viewer-synthesised" {
			maybe++
		}
		if bucket == "" {
			painting++
			s.SubtypeHistogram[a.Subtype]++
			ar.OffPage = a.HasRect && escapes(a.Rect, row.PageBox)
			if row.BoxExact {
				ar.Outside = a.HasRect && escapes(a.Rect, row.RasterBox)
			}
			if ar.OffPage {
				s.OffPage++
			} else if ar.Outside {
				outside++
			}
		}
		row.Annots = append(row.Annots, ar)
	}

	switch row.Outcome {
	case "extracted":
		if painting > 0 {
			s.ExtractedWithAnnot++
			if !row.CoversPage {
				s.ExtractedNotCovering++
			}
		}
		if outside > 0 {
			s.ExtractedOutside++
		}
		if painting == 0 && maybe > 0 {
			s.ExtractedMaybe++
		}
	case "diverted":
		if painting > 0 {
			s.DivertedWithAnnot++
		}
	}
}

// escapes reports whether r reaches outside box on any edge.
func escapes(r pdfdoc.Rect, box [4]float64) bool {
	return r.LLX < box[0]-tol || r.LLY < box[1]-tol ||
		r.URX > box[2]+tol || r.URY > box[3]+tol
}

func bucketName(b string) string {
	if b == "" {
		return "PAINTS"
	}
	return b
}

func diffStats(a, b byblos.ExtractCounters) (string, string) {
	switch {
	case b.Extracted > a.Extracted:
		return "extracted", ""
	case b.Failed > a.Failed:
		return "failed", ""
	case b.Diverted > a.Diverted:
		for k, v := range b.Reasons {
			if v > a.Reasons[k] {
				return "diverted", k
			}
		}
		return "diverted", "?"
	}
	return "none", ""
}

func report(s summary) {
	fmt.Printf("files              %d  (unopenable %d)\n", s.Files, s.FilesUnopenable)
	fmt.Printf("pages              %d\n", s.Pages)
	fmt.Printf("  extracted        %d\n", s.Extracted)
	fmt.Printf("  diverted         %d\n", s.Diverted)
	fmt.Printf("  failed           %d\n", s.Failed)
	fmt.Printf("box exact/missing  %d / %d\n", s.PagesBoxExact, s.PagesBoxMissing)
	fmt.Printf("panicked           %d   (byb-avp; no outcome recorded)\n", s.PagesPanicked)
	fmt.Println()
	fmt.Printf("EXTRACTED pages with a painting annotation   %d   <-- silent loss\n", s.ExtractedWithAnnot)
	fmt.Printf("  ...whose raster did not cover the page     %d   <-- byb-b1.3 exposure\n", s.ExtractedNotCovering)
	fmt.Printf("  ...whose ink escapes the returned raster   %d   <-- the uncovered strip\n", s.ExtractedOutside)
	fmt.Printf("  uncertainty band (viewer-synthesised only) %d\n", s.ExtractedMaybe)
	fmt.Printf("annotations entirely off the CropBox         %d\n", s.OffPage)
	fmt.Printf("DIVERTED pages with a painting annotation    %d   (already safe)\n", s.DivertedWithAnnot)
	fmt.Printf("annotations with /OC                         %d\n", s.HasOC)
	printHist("painting annotations by subtype", s.SubtypeHistogram)
	printHist("all annotations by bucket", s.BucketHistogram)
	if len(s.Reasons) > 0 {
		fmt.Println("divert reasons:")
		keys := make([]string, 0, len(s.Reasons))
		for k := range s.Reasons {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return s.Reasons[keys[i]] > s.Reasons[keys[j]] })
		for _, k := range keys {
			fmt.Printf("  %-24s %d\n", k, s.Reasons[k])
		}
	}
}

func printHist(title string, h map[string]int) {
	if len(h) == 0 {
		return
	}
	fmt.Println(title + ":")
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if h[keys[i]] != h[keys[j]] {
			return h[keys[i]] > h[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Printf("  %-28s %d\n", k, h[k])
	}
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
