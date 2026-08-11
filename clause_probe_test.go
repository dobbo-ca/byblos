package byblos

// Measurement probe for byb-e04: re-measure the clause breakdown of the
// "vector-paint" divert on top of byb-7aq and byb-62t.
//
// It is a test rather than a cmd/ binary because everything it measures is
// unexported package state -- classify, paintsHidden, inkHidden, opaqueCover,
// paintTolerancePt. A binary would have to re-implement classify's arm order
// and the paint loop, and that duplicate would drift from the thing being
// measured, which is the whole risk this probe exists to avoid.
//
// It skips unless BYBLOS_CLAUSE_CORPUS is set, so `go test ./...` is unaffected.
//
//	BYBLOS_CLAUSE_CORPUS=<dir> BYBLOS_CLAUSE_OUT=<file> BYBLOS_JOBS=6 \
//	  go test -run TestClauseBreakdownProbe -v -count=1 -timeout 60m .
//
// It replays the shipped decision path exactly rather than approximating it:
//
//   - A page whose content stream fails to walk is a FAILURE in extractPage
//     (extract.go:335-339 checks inspectPage's error before classify), not a
//     divert. It is skipped here for the same reason.
//   - "Held only by the paint arm" re-runs the REAL classify against a scan whose
//     Paints are nil. paintsHidden then passes vacuously, so arm 4 is inert while
//     every other arm runs on the real data. This is exact because Scan.Paints is
//     read in exactly one non-test place in the tree, extract.go:545.
//   - The clause tally covers the DECIDING paint alone -- the first marking paint
//     that no raster hides -- because that is the paint paintsHidden returns
//     false on. Candidates rejected while examining a paint that turned out to be
//     hidden say nothing about why the page diverted.
//
// READ req_tol_fill, NOT THE G ROW, to scope a tolerance change. The clause
// column describes the deciding paint only, and 39 of the 325 held-only G pages
// are held by a DIFFERENT unhidden paint that no tolerance reaches -- the page is
// labelled by a paint a tolerance would fix while a later one still escapes.
// Scoping off the G row over-states the opportunity by those 39.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// clauseOrder is byb-b1.9's recorded precedence, "opacity -> image dict ->
// paint order -> geometry". It is NOT inkHidden's code order, which tests paint
// order first (extract.go:857). The full tally is emitted alongside the label so
// a different PAGE-level precedence can be re-derived from the TSV without
// re-sweeping; measured, that re-derivation moves the pages column (G 506 -> 553
// under a G-first rule) and leaves the held-only column untouched at 325,
// because every held-only page has exactly one non-zero letter.
//
// A different PER-CANDIDATE order cannot be re-derived from the tally, because
// candFail stops at the first reason a candidate fails and a candidate failing
// on two grounds is counted once. Testing geometry first instead would move four
// held-only pages out of C/D/F into G, which overstates what a tolerance can
// reach; the strict order here is the conservative one.
var clauseOrder = []string{"A", "C", "D", "M", "U", "F", "G"}

const noTolerance = math.MaxFloat64

// strokePaintOps mirrors internal/content's unexported strokingOps
// (walk.go:526), which this package cannot reach. A fill-only tolerance is by
// definition not offered to these, so an unhidden paint carrying one of them
// needs a tolerance no fill-only rule can give it.
var strokePaintOps = map[string]bool{
	"S": true, "s": true, "B": true, "B*": true, "b": true, "b*": true,
}

// candFail reports the first reason img cannot hide ink at paint order order,
// in inkHidden's own evaluation order, and the four overhangs when it got as far
// as the geometry test. It returns "" when img does hide the ink.
//
// The labels are byb-e04's: A a lowered /ca or /CA, C an /ImageMask stencil,
// D an /SMask, M a /Mask, U an image dictionary that would not resolve, F no
// raster painted after the ink, G ink outside the raster's box.
func candFail(ink content.Box, order int, img content.Placement, info func(int) (pdfdoc.ImageInfo, bool)) (string, [4]float64) {
	var ov [4]float64
	if img.Index < order {
		return "F", ov
	}
	if !img.Opaque {
		return "A", ov
	}
	d, ok := info(img.ID)
	if !ok {
		return "U", ov
	}
	switch {
	case d.ImageMask:
		return "C", ov
	case d.SMask:
		return "D", ov
	case d.Mask:
		return "M", ov
	}
	// Positive means the ink escapes that side of the raster.
	ov = [4]float64{
		img.Box.LLX - ink.LLX, // left
		img.Box.LLY - ink.LLY, // bottom
		ink.URX - img.Box.URX, // right
		ink.URY - img.Box.URY, // top
	}
	if maxOv(ov) <= paintTolerancePt {
		return "", ov
	}
	return "G", ov
}

func maxOv(o [4]float64) float64 {
	m := o[0]
	for _, v := range o[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// pageProbe is one diverted page's row.
type pageProbe struct {
	path       string
	page       int
	reason     string
	reasonNP   string // classify with the paint arm inert; "" means it would extract
	clause     string
	tally      map[string]int
	reqTol     float64 // noTolerance when no tolerance can rescue the page
	reqTolFill float64 // the same, for a tolerance offered only to fills
	bindOp     string
	bindOv     [4]float64
	marking    int // marking paints on the page
	unhidden   int // marking paints no raster hides
	strokes    int // of those, how many carry a stroking operator
	decode     string
}

func (r pageProbe) line() string {
	tol := "inf"
	ovs := []string{"-", "-", "-", "-"}
	if r.reqTol < noTolerance {
		tol = strconv.FormatFloat(r.reqTol, 'f', 4, 64)
		for i, v := range r.bindOv {
			ovs[i] = strconv.FormatFloat(v, 'f', 4, 64)
		}
	}
	tolFill := "inf"
	if r.reqTolFill < noTolerance {
		tolFill = strconv.FormatFloat(r.reqTolFill, 'f', 4, 64)
	}
	np := r.reasonNP
	if np == "" {
		np = "EXTRACTS"
	}
	return strings.Join([]string{
		r.path,
		strconv.Itoa(r.page),
		r.reason,
		np,
		r.clause,
		fmt.Sprintf("A=%d,C=%d,D=%d,M=%d,U=%d,F=%d,G=%d",
			r.tally["A"], r.tally["C"], r.tally["D"], r.tally["M"], r.tally["U"], r.tally["F"], r.tally["G"]),
		tol,
		tolFill,
		r.bindOp,
		ovs[0], ovs[1], ovs[2], ovs[3],
		strconv.Itoa(r.marking),
		strconv.Itoa(r.unhidden),
		strconv.Itoa(r.strokes),
		r.decode,
	}, "\t")
}

// probeVectorPaint fills in the clause columns for a page classify called
// "vector-paint". It walks the same paints paintsHidden walks, in the same
// order.
func probeVectorPaint(r *pageProbe, s *content.Scan, info func(int) (pdfdoc.ImageInfo, bool)) {
	r.clause = "?"
	r.bindOp = "-"
	decided := false
	for _, pt := range s.Paints {
		ink, marks := pt.Ink()
		if !marks {
			continue
		}
		r.marking++
		tally := map[string]int{}
		best := noTolerance
		var bestOv [4]float64
		hidden := false
		for _, img := range s.Images {
			fr, ov := candFail(ink, pt.Index, img, info)
			if fr == "" {
				hidden = true
				break
			}
			tally[fr]++
			// Only a candidate that reached the geometry test can be rescued by a
			// larger tolerance; take the one that misses by least.
			if fr == "G" {
				if m := maxOv(ov); m < best {
					best, bestOv = m, ov
				}
			}
		}
		if hidden {
			continue
		}
		r.unhidden++
		// paintsHidden returns false on the FIRST unhidden paint, so that paint is
		// the one that decided the page.
		if !decided {
			decided = true
			r.tally = tally
			for _, c := range clauseOrder {
				if tally[c] > 0 {
					r.clause = c
					break
				}
			}
		}
		// The page needs EVERY unhidden paint brought inside a raster, so the
		// tolerance it requires is the largest any of them needs. A paint with no
		// geometric candidate at all needs an infinite one: no tolerance rescues
		// this page.
		if best > r.reqTol {
			r.reqTol, r.bindOp, r.bindOv = best, pt.Op, bestOv
		}
		// A fill-only tolerance is not offered to a stroke, so an unhidden stroke
		// keeps the page diverted whatever the number is set to.
		needFill := best
		if strokePaintOps[pt.Op] {
			r.strokes++
			needFill = noTolerance
		}
		if needFill > r.reqTolFill {
			r.reqTolFill = needFill
		}
	}
	if r.tally == nil {
		r.tally = map[string]int{}
	}
}

// decodeCheck reports whether the page's chosen raster actually decodes, i.e.
// whether removing the paint arm would really yield a page rather than move the
// divert to a codec. It replicates extract.go:348-401 and nothing else.
func decodeCheck(d pdfdoc.Doc, placement content.Placement) string {
	data, fileType, err := d.RawImage(placement.ID)
	if err != nil {
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			return "unsupported-codec"
		}
		return "read-failure"
	}
	switch fileType {
	case "jbig2":
		if _, err := decodeJBIG2Placement(data, d.ImageInfo, placement.ID); err != nil {
			return "unsupported-codec-jbig2"
		}
	case "jpx":
		return "unsupported-codec-jpx"
	default:
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return "unsupported-codec-" + fileType
		}
	}
	return "OK"
}

func probeFile(path string, decode bool) (rows []pageProbe, pages int, skipped int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0
	}
	defer f.Close()
	d, err := pdfdoc.Open(f)
	if err != nil {
		return nil, 0, 0
	}
	ctx := context.Background()
	for n := 1; n <= d.PageCount(); n++ {
		pages++
		p, err := d.Page(n)
		if err != nil {
			// extractPage returns this page's error before classify too
			// (extract.go:330-334), so it is a failure and not a divert.
			skipped++
			continue
		}
		s, walkErr := content.Walk(ctx, p.Content, p.Scope, d)
		if walkErr != nil || s == nil {
			// extractPage checks inspectPage's error before classify, so this page
			// is a failure and never reaches a divert reason at all.
			skipped++
			continue
		}
		idx, reason := classify(p.CropBox, s, d.ImageInfo)
		_ = idx
		if reason == "" {
			continue
		}
		r := pageProbe{path: path, page: n, reason: reason, clause: "-", bindOp: "-", decode: "-", tally: map[string]int{}}
		r.reqTol = 0
		if reason == "vector-paint" {
			noPaint := *s
			noPaint.Paints = nil
			idxNP, reasonNP := classify(p.CropBox, &noPaint, d.ImageInfo)
			r.reasonNP = reasonNP
			probeVectorPaint(&r, s, d.ImageInfo)
			if decode && reasonNP == "" {
				r.decode = decodeCheck(d, noPaint.Images[idxNP])
			}
		} else {
			r.reasonNP = reason
			r.reqTol = noTolerance
			r.reqTolFill = noTolerance
		}
		rows = append(rows, r)
	}
	return rows, pages, skipped
}

func TestClauseBreakdownProbe(t *testing.T) {
	root := os.Getenv("BYBLOS_CLAUSE_CORPUS")
	if root == "" {
		t.Skip("set BYBLOS_CLAUSE_CORPUS to a directory of PDFs to run the byb-e04 clause probe")
	}
	outPath := os.Getenv("BYBLOS_CLAUSE_OUT")
	if outPath == "" {
		t.Fatal("set BYBLOS_CLAUSE_OUT to the TSV to write")
	}
	decode := os.Getenv("BYBLOS_CLAUSE_DECODE") != "0"

	var paths []string
	if err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatalf("no PDFs under %s", root)
	}

	workers := 6
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && v > 0 {
		workers = v
	}

	// Slot-indexed like cmd/byblos-divert's sweep: every worker writes its own
	// file's rows into its own slot, so output is in lexical path order whatever
	// the worker count.
	per := make([][]pageProbe, len(paths))
	pages := make([]int, len(paths))
	fails := make([]int, len(paths))
	start := time.Now()
	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				per[i], pages[i], fails[i] = probeFile(paths[i], decode)
			}
		}()
	}
	for i := range paths {
		work <- i
	}
	close(work)
	wg.Wait()
	elapsed := time.Since(start)

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	defer out.Close()
	var totalPages, totalFails, rows, vp, heldOnly int
	for i := range paths {
		totalPages += pages[i]
		totalFails += fails[i]
		for _, r := range per[i] {
			rows++
			if r.reason == "vector-paint" {
				vp++
				if r.reasonNP == "" {
					heldOnly++
				}
			}
			if _, err := fmt.Fprintln(out, r.line()); err != nil {
				t.Fatalf("write %s: %v", outPath, err)
			}
		}
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", outPath, err)
	}
	t.Logf("files=%d pages=%d pre-classify-skips=%d diverts=%d vector-paint=%d held-only=%d elapsed=%v",
		len(paths), totalPages, totalFails, rows, vp, heldOnly, elapsed)
}
