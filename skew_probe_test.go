package byblos

// Measurement probe for byb-16j.1: how crooked is the pinned sample, and is any
// of G5 -- straightening a page -- worth building at all.
//
// THE BEAD GATES A WHOLE TREE ON THIS NUMBER and says to close the parent on the
// measurement if the answer is no, the way byb-cdf was closed. Nothing is built
// before it runs.
//
// # It measures TWO different angles and must never add them up by accident
//
// PLACEMENT SKEW is the raster's own paint matrix. byblos already has it --
// ImageRef.Placement is the CTM, and skewDegrees (extract.go) is the production
// function that reads an angle off it -- so this half costs no decoding at all.
//
// CONTENT SKEW is the angle of the marks INSIDE the raster. A scanner that fed
// the page crooked writes an axis-aligned placement of a rotated raster: the
// placement angle is zero and the page is still crooked. Nothing in byblos can
// see it; internal/skew is the instrument built for this census, and its own
// calibration tests run on every `go test ./...`.
//
// THEY ARE DIFFERENT COLUMNS AND THE ROW CARRIES BOTH. byb-divert measured four
// pages of govdocs1/005393.pdf by hand and found the opposite of the bead's
// hypothesis on that file -- the stored raster is the RAW skewed scan and the
// CTM carries the deskew -- so the two angles are not independent on every
// producer and a census that reported only their sum could not have found that.
//
// # The decisive column, and why it is |content skew| and not anything else
//
// maxSkewDeg (extract.go) is 2.0, and placementReason diverts any placement
// whose axes lie further than that from the page's. Straightening a page by the
// LOSSLESS content-matrix route turns the content by the negative of the angle
// the reader sees, which leaves the placement sitting at exactly the CONTENT
// skew that was taken out. So:
//
//	a page whose content skew exceeds maxSkewDeg cannot take the lossless
//	route -- byblos would stop recognising its own output -- and must be
//	resampled instead.
//
// That is the population byb-16j's re-scoping turns on, and it is one column:
// the share of |content_deg| over 2.0.
//
// # The keystone half
//
// The bead asks for pages whose four detected page-edge corners are NOT a
// parallelogram. Most archive scans are cropped to the page and have no visible
// edge, so internal/skew applies the same test to the TEXT BLOCK: after the page
// is turned back by its own line angle, a parallelogram has parallel sides and
// parallel lines. skew.Estimate.Converge is the first, BandSpread the second,
// and a keystone needs only one of them to be non-zero. Both are upper bounds --
// see internal/skew's doc comment for what neither can see.
//
// # Two deviations from how probes are written here, both deliberate
//
// IT WRITES ROWS AS IT GOES. Every other probe in this repository creates its
// output file only after the walk returns (clause_probe_test.go,
// jbig2_symbol_probe_test.go), so a run that is killed, cancelled or hits
// -timeout produces zero bytes. This census decodes far more than the JBIG2 one
// did, and a run long enough to be worth doing is long enough to be interrupted.
// Rows therefore carry doc_index and are sorted afterwards rather than emitted
// in order:
//
//	sort -t$'\t' -k1,1n -k3,3n rows.tsv
//
// IT ASKS THE SHIPPED PRIMITIVE FOR THE PIXELS. extractPage is what
// ExtractPageRaster is, minus the re-parse of the document per page, and the
// bead's question -- "how many scan-shaped pages ExtractPageRaster can reduce to
// ONE raster" -- is a question about that function and not about a copy of it. A
// probe-local copy of the codec dispatch would answer a question about the copy.
// The cost is that the page is walked twice, once here for the divert reason
// that extractPage does not return and once inside it; the walk is a small
// fraction of a decode.
//
//	BYBLOS_SKEW_CORPUS=<dir> BYBLOS_SKEW_OUT=<file> BYBLOS_JOBS=6 \
//	  go test -run TestSkewCensus -v -count=1 -timeout 240m .

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/dobbo-ca/byblos/internal/sample"
	"github.com/dobbo-ca/byblos/internal/skew"
)

// skewRow is one page. EVERY PREDICATE IS ON THE SAME ROW, measured in one pass,
// because two lanes measuring two predicates over one corpus is how byb-wj2
// happened: the only way to be sure the placement column and the content column
// describe the same page is for one walk to have produced both.
//
// Sentinels are explicit and are never zero: 0 degrees of skew is a real and
// common measurement, so "not measured" has to look different from "measured as
// straight". Angles that were not measured are NaN and print as "-".
type skewRow struct {
	docIndex int
	rel      string
	page     int

	// The page, before any decoding.
	nImages   int
	textChars int
	reason    string // classify's verdict; "" means one page-covering raster
	pageW     int
	pageH     int

	// Placement skew, from the paint matrix alone.
	//
	// topSkew is byblos's OWN predicate -- skewDegrees, which drops signs and
	// takes the worse of the two axes, because that is what placementReason
	// diverts on. maxSkew is the same over every placement on the page, which
	// differs on a stacked page.
	//
	// topAngle is the SIGNED angle atan2(b, a) of the same placement, and it is a
	// separate column rather than a sign bit on topSkew because the two answer
	// different questions. Only the signed one can be added to the content angle,
	// and that sum is what a reader sees as crooked.
	topSkew  float64
	topAngle float64
	maxSkew  float64
	mirror   bool

	// What byblos did with the page.
	extractOK  bool
	extractErr string
	filter     string
	bitonal    bool
	rasterW    int
	rasterH    int
	covers     bool

	// Content skew, from the pixels.
	est skew.Estimate
}

// nan is the "not measured" angle. Written as "-" so a column of angles cannot
// be summed by accident.
var nan = math.NaN()

func fdeg(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func fbool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// line is the TSV row. No header, matching the other probes; the field order is
// the struct order and is documented here because it is the only place it is.
//
//	doc_index rel page nimg textchars reason page_w page_h top_skew top_angle max_skew
//	mirror extract filter bitonal raster_w raster_h covers ink_frac ink_points
//	content_deg alt_deg conf est_ok band0 band1 band2 band_spread converge
//	left_deg right_deg edge_resid edges_ok railed err
func (r skewRow) line() string {
	e := r.est
	deg, alt := nan, nan
	if e.OK {
		deg, alt = e.Deg, e.AltDeg
	}
	spread := nan
	if e.BandSpread >= 0 {
		spread = e.BandSpread
	}
	conv, left, right, resid := nan, nan, nan, nan
	if e.EdgesOK {
		conv, left, right, resid = e.Converge, e.LeftDeg, e.RightDeg, e.EdgeResid
	}
	band := func(i int) string {
		if !e.BandOK[i] {
			return "-"
		}
		return strconv.FormatFloat(e.BandDeg[i], 'f', 4, 64)
	}
	reason := r.reason
	if reason == "" {
		reason = "EXTRACTS"
	}
	return strings.Join([]string{
		strconv.Itoa(r.docIndex), r.rel, strconv.Itoa(r.page),
		strconv.Itoa(r.nImages), strconv.Itoa(r.textChars), reason,
		strconv.Itoa(r.pageW), strconv.Itoa(r.pageH),
		fdeg(r.topSkew), fdeg(r.topAngle), fdeg(r.maxSkew), fbool(r.mirror),
		fbool(r.extractOK), r.filter, fbool(r.bitonal),
		strconv.Itoa(r.rasterW), strconv.Itoa(r.rasterH), fbool(r.covers),
		strconv.FormatFloat(e.InkFrac, 'f', 5, 64), strconv.Itoa(e.InkPoints),
		fdeg(deg), fdeg(alt), strconv.FormatFloat(e.Confidence, 'f', 4, 64),
		fbool(e.OK),
		band(0), band(1), band(2), fdeg(spread),
		fdeg(conv), fdeg(left), fdeg(right), fdeg(resid), fbool(e.EdgesOK),
		fbool(e.Railed),
		strings.ReplaceAll(r.extractErr, "\t", " "),
	}, "\t")
}

// skewCensusDoc measures every page of one already-open document.
//
// The document was opened by sample.Walk to count its pages, and its pages are
// reached through that same handle. Counting them again here is exactly what let
// two lanes publish two different populations (byb-wj2).
func skewCensusDoc(doc sample.Doc) []skewRow {
	d := doc.Doc
	ctx := context.Background()
	rows := make([]skewRow, 0, d.PageCount())
	for n := 1; n <= d.PageCount(); n++ {
		r := skewRow{docIndex: doc.Index, rel: doc.Rel, page: n,
			topSkew: nan, topAngle: nan, maxSkew: nan}
		p, err := d.Page(n)
		if err != nil {
			r.reason = "page-unreadable"
			r.extractErr = err.Error()
			rows = append(rows, r)
			continue
		}
		r.pageW = int(math.Round(p.CropBox.URX - p.CropBox.LLX))
		r.pageH = int(math.Round(p.CropBox.URY - p.CropBox.LLY))

		s, walkErr := content.Walk(ctx, p.Content, p.Scope, d)
		if s == nil {
			r.reason = "walk-failed"
			if walkErr != nil {
				r.extractErr = walkErr.Error()
			}
			rows = append(rows, r)
			continue
		}
		r.textChars, r.nImages = s.TextChars, len(s.Images)

		// THE PLACEMENT HALF, and it costs nothing. skewDegrees is byblos's own
		// function, not a copy of it, so this column and the divert decision
		// cannot drift apart.
		if len(s.Images) > 0 {
			top := s.Images[len(s.Images)-1]
			r.topSkew = skewDegrees(top.CTM)
			r.topAngle = math.Atan2(top.CTM[1], top.CTM[0]) * 180 / math.Pi
			r.mirror = top.CTM[0] <= 0 || top.CTM[3] <= 0
			worst := 0.0
			for _, pl := range s.Images {
				worst = max(worst, skewDegrees(pl.CTM))
			}
			r.maxSkew = worst
		}

		_, r.reason = classify(p.CropBox, s, d.ImageInfo)
		if walkErr != nil && r.reason == "" {
			// A page byblos read only part of. It is IN the population
			// (internal/sample's second predicate) and its numbers are wrong
			// LOW, so it is named rather than dropped.
			r.reason = "partial-walk"
		}
		if r.reason != "" {
			rows = append(rows, r)
			continue
		}

		// THE CONTENT HALF. Only a page byblos reduces to one raster has one,
		// and that population is itself an answer the bead asked for.
		pr, _, err := extractPage(ctx, d, n)
		if err != nil || pr == nil || pr.Image == nil {
			r.reason = "decode-failed"
			if err != nil {
				r.extractErr = err.Error()
			}
			rows = append(rows, r)
			continue
		}
		r.extractOK, r.bitonal, r.covers = true, pr.Bitonal, pr.CoversPage()
		b := pr.Image.Bounds()
		r.rasterW, r.rasterH = b.Dx(), b.Dy()
		if info, ok := d.ImageInfo(pr.ObjNr); ok {
			r.filter = info.Filter
		}
		if r.filter == "" {
			r.filter = "none"
		}
		r.est = skew.Measure(pr.Image, skew.Options{Bitonal: pr.Bitonal})
		rows = append(rows, r)
	}
	return rows
}

func TestSkewCensus(t *testing.T) {
	root := os.Getenv("BYBLOS_SKEW_CORPUS")
	if root == "" {
		t.Skip("set BYBLOS_SKEW_CORPUS to a directory of PDFs to run the byb-16j.1 census")
	}
	outPath := os.Getenv("BYBLOS_SKEW_OUT")
	if outPath == "" {
		t.Fatal("set BYBLOS_SKEW_OUT to the TSV to write")
	}
	workers := 6
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && v > 0 {
		workers = v
	}

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	defer out.Close()
	w := bufio.NewWriterSize(out, 1<<20)
	// One mutex over the writer rather than a channel and a writer goroutine:
	// the work is seconds per document and the contention is nothing, while a
	// goroutine would need its own shutdown path to guarantee the last flush.
	var mu sync.Mutex
	var pages, extracted, measured int
	flushEvery := 200
	sinceFlush := 0

	start := time.Now()
	pop, err := sample.Walk(root, workers, func(d sample.Doc) {
		if d.Err != nil {
			return
		}
		rows := skewCensusDoc(d)
		mu.Lock()
		defer mu.Unlock()
		for _, r := range rows {
			fmt.Fprintln(w, r.line())
			pages++
			if r.extractOK {
				extracted++
			}
			if r.est.OK {
				measured++
			}
		}
		sinceFlush++
		if sinceFlush >= flushEvery {
			_ = w.Flush()
			sinceFlush = 0
			t.Logf("%s  docs-flushed pages=%d extracted=%d measured=%d",
				time.Since(start).Round(time.Second), pages, extracted, measured)
		}
	})
	mu.Lock()
	_ = w.Flush()
	mu.Unlock()
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// The denominator is internal/sample's, never this probe's.
	t.Logf("files=%d unopenable=%d documents=%d pages=%d",
		pop.Files, pop.Unopenable, pop.Documents, pop.Pages)
	t.Logf("rows=%d extracted=%d content-measured=%d elapsed=%s",
		pages, extracted, measured, time.Since(start).Round(time.Second))
	if pages != pop.Pages {
		// Not a failure: a document whose page dictionary will not resolve still
		// emits a row, so these should agree exactly and a disagreement is worth
		// seeing rather than hiding.
		t.Logf("NOTE: emitted %d rows for a population of %d pages", pages, pop.Pages)
	}
}

// TestSkewInstrumentAgainstMeasuredDeskew checks internal/skew against the one
// piece of real-world ground truth this project has.
//
// byb-divert opened govdocs1/005393.pdf by hand and established two things about
// it: the placement angles run from -1.09 to +0.85 degrees with a median of
// 0.13, and -- verified by projection-variance on pages 14, 20, 100 and 148 --
// THE STORED RASTER IS THE RAW SKEWED SCAN while the CTM carries the deskew. So
// on that file the content angle must be the NEGATIVE of the placement angle,
// and the two must cancel: the page reads straight.
//
// IT IS A STRONG CHECK BECAUSE THE TWO SIDES SHARE NOTHING. The placement angle
// is six numbers out of a content stream and no pixel is decoded to get it; the
// content angle is a projection profile over decoded ink and no dictionary is
// read to get it. Every other test of this instrument runs on a page I drew, and
// a drawn page cannot contain a case I did not think of.
//
// The file is in the pinned sample as anchors/pdfs/005393.pdf -- it is an ANCHOR
// precisely because it was characterised by hand.
//
//	BYBLOS_SKEW_ANCHOR=~/work/dobbo-ca/.byblos-sample/anchors/pdfs/005393.pdf \
//	  go test -run TestSkewInstrumentAgainstMeasuredDeskew -v -count=1 .
func TestSkewInstrumentAgainstMeasuredDeskew(t *testing.T) {
	path := os.Getenv("BYBLOS_SKEW_ANCHOR")
	if path == "" {
		t.Skip("set BYBLOS_SKEW_ANCHOR to 005393.pdf to check the instrument " +
			"against byb-divert's hand measurement")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	d, err := pdfdoc.Open(f)
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	ctx := context.Background()

	var sums []float64
	var minA, maxA = math.Inf(1), math.Inf(-1)
	for n := 1; n <= d.PageCount(); n++ {
		p, err := d.Page(n)
		if err != nil {
			continue
		}
		s, werr := content.Walk(ctx, p.Content, p.Scope, d)
		if werr != nil || s == nil || len(s.Images) == 0 {
			continue
		}
		if _, reason := classify(p.CropBox, s, d.ImageInfo); reason != "" {
			continue
		}
		pr, _, err := extractPage(ctx, d, n)
		if err != nil || pr == nil || pr.Image == nil {
			continue
		}
		est := skew.Measure(pr.Image, skew.Options{Bitonal: pr.Bitonal})
		if !est.OK {
			continue
		}
		m := s.Images[len(s.Images)-1].CTM
		place := math.Atan2(m[1], m[0]) * 180 / math.Pi
		minA, maxA = math.Min(minA, place), math.Max(maxA, place)
		sums = append(sums, place+est.Deg)
	}
	if len(sums) < 100 {
		t.Fatalf("only %d pages measured; expected the file's 159", len(sums))
	}

	// byb-divert's range, reproduced from the matrices alone. If this drifts,
	// the file is not the file that was measured and nothing below means
	// anything.
	if minA < -1.15 || minA > -1.00 || maxA < 0.80 || maxA > 0.95 {
		t.Errorf("placement angles span [%+.4f, %+.4f]; byb-divert measured "+
			"[-1.09, +0.85] over this file", minA, maxA)
	}

	// THE CLAIM. Content cancels placement, so the page reads straight. The
	// tolerance is a quarter of a degree on the mean, which is far tighter than
	// the 1.09 degrees of skew being cancelled -- an instrument reporting noise
	// would not cancel anything.
	var tot, worst float64
	for _, v := range sums {
		tot += math.Abs(v)
		worst = math.Max(worst, math.Abs(v))
	}
	mean := tot / float64(len(sums))
	if mean > 0.25 {
		t.Errorf("placement + content is %.4f deg on average over %d pages; "+
			"byb-divert established the CTM deskews this file's raw raster, so "+
			"they must cancel", mean, len(sums))
	}
	t.Logf("%d pages: placement in [%+.4f, %+.4f], mean |placement+content| "+
		"%.4f, worst %.4f", len(sums), minA, maxA, mean, worst)
}
