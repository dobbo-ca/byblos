package byblos

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// byb-b5.1's field shape (PageProvenance.Geometry) has no production writer
// yet -- every []PageProvenance{...} literal in this package is hand-built in
// a _test.go, and Optimize's fresh-Provenance branch deliberately leaves Pages
// nil (optimize.go). RecordExtraction is that writer: it runs extraction over
// every page of a document and returns a ready-to-write Provenance.
//
// TestRecordExtractionRecordsUnroundedGeometry is the test the whole design
// exists for: PageRaster.Bounds/.Page are image.Rectangle, built through
// round() (inspect.go:92-100), so a writer sourcing Geometry from a PageRaster
// would report {0 0 568 792} for this page, not {0 0 568.3708 791.7616}. If
// this assertion still passed against rounded ints, it would be vacuous; it
// does not, because 568.3708 != 568 and 791.7616 != 792.
func TestRecordExtractionRecordsUnroundedGeometry(t *testing.T) {
	doc := corpusDoc(t, "scan-natural-dpi")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	pg := got.Pages[0]
	if pg.Geometry == nil {
		t.Fatal("Pages[0].Geometry = nil; want a measured geometry")
	}
	wantRaster := [4]float64{0, 0, 568.3708, 791.7616}
	if pg.Geometry.RasterBox != wantRaster {
		t.Errorf("RasterBox = %v; want %v (unrounded -- NOT {0 0 568 792})",
			pg.Geometry.RasterBox, wantRaster)
	}
	wantPage := [4]float64{0, 0, 612, 792}
	if pg.Geometry.PageBox != wantPage {
		t.Errorf("PageBox = %v; want %v", pg.Geometry.PageBox, wantPage)
	}
	if pg.Geometry.CoversPage() {
		t.Error("CoversPage() = true; want false (43.6pt short, byb-b1.3)")
	}
}

// A diverted page has nothing to measure: classify never returns a placement
// index for it, so it must record the divert class and no Geometry at all.
func TestRecordExtractionDivertedPageHasNoGeometry(t *testing.T) {
	doc := corpusDoc(t, "tiled")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	pg := got.Pages[0]
	if pg.Diverted != "not-single-raster" {
		t.Errorf("Diverted = %q; want %q", pg.Diverted, "not-single-raster")
	}
	if pg.Geometry != nil {
		t.Errorf("Geometry = %+v; want nil for a diverted page", pg.Geometry)
	}
}

// jbig2 is diverted through the codec path, which must map through
// divertClass the same as ExtractPageRaster's callers see it, and also carry
// no Geometry.
func TestRecordExtractionDivertedCodecPageHasNoGeometry(t *testing.T) {
	doc := corpusDoc(t, "jbig2")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	pg := got.Pages[0]
	if pg.Diverted != "unsupported-codec-jbig2" {
		t.Errorf("Diverted = %q; want %q", pg.Diverted, "unsupported-codec-jbig2")
	}
	if pg.Geometry != nil {
		t.Errorf("Geometry = %+v; want nil for a diverted page", pg.Geometry)
	}
}

// Pages must be exactly one entry per page, in page order, with no page
// skipped or merged -- PageProvenance carries no page number of its own, so
// index i must describe page i+1.
func TestRecordExtractionIsOneEntryPerPageInOrder(t *testing.T) {
	doc := corpusDoc(t, "mixed")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 2 {
		t.Fatalf("len(Pages) = %d; want 2", len(got.Pages))
	}
	if got.Pages[0].Diverted != "not-single-raster" {
		t.Errorf("Pages[0].Diverted = %q; want %q (born-digital page)",
			got.Pages[0].Diverted, "not-single-raster")
	}
	if got.Pages[0].Geometry != nil {
		t.Errorf("Pages[0].Geometry = %+v; want nil (diverted page)", got.Pages[0].Geometry)
	}
	if got.Pages[1].Diverted != "" {
		t.Errorf("Pages[1].Diverted = %q; want \"\" (scan page)", got.Pages[1].Diverted)
	}
	if got.Pages[1].Geometry == nil {
		t.Fatal("Pages[1].Geometry = nil; want a measured geometry (scan page)")
	}
}

// RecordExtraction genuinely performs extraction and nothing else -- it must
// not claim Capabilities it did not exercise (optimize.go:134-149's rule,
// mirrored here for the new writer). "inspect" is deliberately omitted even
// though inspectPage runs internally: Inspect itself was never called.
func TestRecordExtractionClaimsOnlyExtractRaster(t *testing.T) {
	doc := corpusDoc(t, "scan")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "extract-raster" {
		t.Errorf("Capabilities = %v; want [extract-raster]", got.Capabilities)
	}
}

// A document byblos cannot even open must fail without returning a partial
// record.
func TestRecordExtractionFailsWhenTheDocumentWontOpen(t *testing.T) {
	doc := corpusDoc(t, "malformed")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err == nil {
		t.Fatal("RecordExtraction() error = nil; want an error for a document that won't open")
	}
	if !reflect.DeepEqual(got, Provenance{}) {
		t.Errorf("RecordExtraction() = %+v; want the zero Provenance", got)
	}
}

// RecordExtraction must not destroy a record the document already carries: a
// prior Optimize(Linearize:true) run's Optimized marker and Capabilities must
// survive, the same way Optimize itself preserves whatever RecordExtraction
// wrote (optimize.go). Without this, RecordExtraction-after-Optimize would
// erase "rewritten-linearized" and a later reprocessing run would falsely
// nominate "linearize" for a file byblos itself linearized.
func TestRecordExtractionPreservesAnExistingRecord(t *testing.T) {
	doc := corpusDoc(t, "scan")

	var withLinearize bytes.Buffer
	err := Optimize(&withLinearize, bytes.NewReader(doc), OptimizeOptions{Linearize: true})
	if err != nil {
		t.Fatalf("Optimize() error = %v", err)
	}

	got, err := RecordExtraction(bytes.NewReader(withLinearize.Bytes()))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if got.Optimized != "rewritten-linearized" {
		t.Errorf("Optimized = %q; want %q (preserved from Optimize)", got.Optimized, "rewritten-linearized")
	}
	if !slices.Contains(got.Capabilities, "extract-raster") {
		t.Errorf("Capabilities = %v; want it to still contain %q", got.Capabilities, "extract-raster")
	}
	candidates := UpgradeCandidates(&got, []string{"linearize"})
	if slices.Contains(candidates, "linearize") {
		t.Errorf("UpgradeCandidates() = %v; must not nominate %q for a file RecordExtraction just found already linearized", candidates, "linearize")
	}
}

// A /CropBox whose corners are named in the diagonally opposite order (legal
// under ISO 32000-1 7.9.5) must still be recorded canonically -- [0 0 612 792],
// not the inverted [612 792 0 0] pdfcpu's RectForArray stores verbatim -- so
// PageGeometry.CoversPage agrees with PageRaster.CoversPage for the same page.
func TestRecordExtractionNormalizesAReversedCropBox(t *testing.T) {
	doc := corpusDoc(t, "scan-reversed-cropbox")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 || got.Pages[0].Geometry == nil {
		t.Fatalf("Pages = %+v; want one page with a Geometry", got.Pages)
	}
	want := [4]float64{0, 0, 612, 792}
	if got.Pages[0].Geometry.PageBox != want {
		t.Errorf("PageBox = %v; want %v (canonicalized, not [612 792 0 0])", got.Pages[0].Geometry.PageBox, want)
	}
	if !got.Pages[0].Geometry.CoversPage() {
		t.Error("CoversPage() = false; want true (matches PageRaster.CoversPage for the same page)")
	}
}

// A successfully extracted page must carry exactly Applied: ["extract-raster"]
// -- neither over-claiming a per-page capability it never exercised (the same
// over-claim TestRecordExtractionClaimsOnlyExtractRaster's comment argues
// against, one layer down from the document-level Capabilities) nor omitting
// it, which would be indistinguishable from a correct record to anything that
// only checks its length or presence.
func TestRecordExtractionPageAppliedIsExactlyExtractRaster(t *testing.T) {
	doc := corpusDoc(t, "scan")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	want := []string{"extract-raster"}
	if !reflect.DeepEqual(got.Pages[0].Applied, want) {
		t.Errorf("Pages[0].Applied = %v; want %v", got.Pages[0].Applied, want)
	}
}

// RecordExtraction must stamp the current build's Version and a fresh
// ProcessedAt, not leave either at its zero value.
func TestRecordExtractionStampsVersionAndProcessedAt(t *testing.T) {
	before := time.Now()
	doc := corpusDoc(t, "scan")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if got.Version != Version {
		t.Errorf("Version = %q; want %q", got.Version, Version)
	}
	if got.ProcessedAt.Before(before) || got.ProcessedAt.After(time.Now()) {
		t.Errorf("ProcessedAt = %v; want between %v and now", got.ProcessedAt, before)
	}
}

// A page byblos opens the document fine but cannot read AT ALL must abort the
// whole call, from inside the per-page loop: there is no "failed" value in
// PageProvenance's vocabulary, and the two ways to carry on are both worse --
// skipping the page shifts every later page's index, and appending a zero
// PageProvenance is indistinguishable from a page that was handled and had
// nothing applied. This is distinct from
// TestRecordExtractionFailsWhenTheDocumentWontOpen: "mixed-page2-unreadable"
// opens cleanly (unlike "malformed", which fails inside pdfdoc.Open before the
// page loop ever runs) and fails only when the per-page loop reaches page 2,
// so this exercises RecordExtraction's own abort branch rather than a
// pdfdoc.Open pass-through.
func TestRecordExtractionAbortsOnAnUnreadablePage(t *testing.T) {
	doc := corpus.MixedPageTwoUnreadable()

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err == nil {
		t.Fatal("RecordExtraction() error = nil; want an error for an unreadable page")
	}
	const wantPrefix = "byblos: provenance: page 2:"
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error = %q; want it to start with %q", err.Error(), wantPrefix)
	}
	if !reflect.DeepEqual(got, Provenance{}) {
		t.Errorf("RecordExtraction() = %+v; want the zero Provenance", got)
	}
}

// The acceptance test for "B5 writes it": RecordExtraction's record must
// round-trip through a real PDF's Info dictionary, geometry intact, via the
// existing WriteProvenance/ReadProvenance pair.
func TestRecordExtractionRoundTripsThroughThePDF(t *testing.T) {
	doc := corpusDoc(t, "scan-natural-dpi")

	rec, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}

	var out bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(doc), &out, rec); err != nil {
		t.Fatalf("WriteProvenance() error = %v", err)
	}
	got, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance() error = %v", err)
	}
	if got == nil {
		t.Fatal("ReadProvenance() = nil; want the record just written")
	}
	if len(got.Pages) != 1 || got.Pages[0].Geometry == nil {
		t.Fatalf("Pages = %+v; want one page with a Geometry", got.Pages)
	}
	wantRaster := [4]float64{0, 0, 568.3708, 791.7616}
	if got.Pages[0].Geometry.RasterBox != wantRaster {
		t.Errorf("round-tripped RasterBox = %v; want %v",
			got.Pages[0].Geometry.RasterBox, wantRaster)
	}
}

// scan-deskewed carries a residual affine that must survive into
// PageProvenance.Placement, exactly as ExtractPageRaster's own callers would
// have to reconstruct it by hand today. The values must be the corpus
// document's own CTM (corpus.DeskewPlacement), not merely six numbers of any
// value -- and an axis-aligned page (byb-b1.2's "almost every page") must
// come back with Placement still empty, not six zeros standing in for it.
func TestRecordExtractionRecordsTheResidualAffine(t *testing.T) {
	doc := corpusDoc(t, "scan-deskewed")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	want := corpus.DeskewPlacement[:]
	if !reflect.DeepEqual(got.Pages[0].Placement, want) {
		t.Fatalf("Placement = %v; want %v (corpus.DeskewPlacement)", got.Pages[0].Placement, want)
	}

	axisAligned, err := RecordExtraction(bytes.NewReader(corpusDoc(t, "scan")))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(axisAligned.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(axisAligned.Pages))
	}
	if len(axisAligned.Pages[0].Placement) != 0 {
		t.Errorf("Placement = %v; want empty for an axis-aligned placement", axisAligned.Pages[0].Placement)
	}
}

// A page whose stamp annotation paints outside the stored raster must come
// back with DroppedAnnots > 0, which is what makes "render" a candidate
// (upgrade.go) -- closing the loop from a real document to that rule, which
// otherwise only has hand-built fixtures (upgrade_test.go).
func TestRecordExtractionNominatesRenderForADroppedAnnot(t *testing.T) {
	doc := corpusDoc(t, "scan-stamped")

	got, err := RecordExtraction(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(got.Pages) != 1 || got.Pages[0].DroppedAnnots == 0 {
		t.Fatalf("Pages = %+v; want DroppedAnnots > 0", got.Pages)
	}
	candidates := UpgradeCandidates(&got, []string{"extract-raster", "inspect", "render"})
	found := false
	for _, c := range candidates {
		if c == "render" {
			found = true
		}
	}
	if !found {
		t.Errorf("UpgradeCandidates() = %v; want it to contain %q", candidates, "render")
	}
}

// RecordExtraction attempts every page, so it must perturb ExtractStats the
// same way a manual per-page ExtractPageRaster loop would: each page lands in
// exactly one of Extracted/Diverted/Failed, all summing to Attempted.
func TestRecordExtractionCountsEveryPageOnce(t *testing.T) {
	ResetExtractStats()
	doc := corpusDoc(t, "mixed")

	if _, err := RecordExtraction(bytes.NewReader(doc)); err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}

	c := ExtractStats()
	if c.Attempted != 2 {
		t.Errorf("Attempted = %d; want 2", c.Attempted)
	}
	if got := c.Extracted + c.Diverted + c.Failed; got != c.Attempted {
		t.Errorf("Extracted+Diverted+Failed = %d; Attempted = %d", got, c.Attempted)
	}
}
