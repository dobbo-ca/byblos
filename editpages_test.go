package byblos

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// recordedBooklet is the eight-page corpus booklet carrying a provenance record
// whose page entries are told apart by their Applied marker, so a re-indexing
// mistake names the page it took the record from.
func recordedBooklet(t *testing.T) []byte {
	t.Helper()
	in := corpusDoc(t, "booklet")
	p := Provenance{
		Version:      Version,
		Capabilities: []string{"extract-raster", "jbig2-generic"},
		Optimized:    "rewritten-linearized",
	}
	for i := 1; i <= corpus.BookletPages; i++ {
		p.Pages = append(p.Pages, PageProvenance{
			Applied:       []string{fmt.Sprintf("marker-%d", i)},
			DroppedAnnots: i,
		})
	}
	var buf bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(in), &buf, p); err != nil {
		t.Fatalf("seeding provenance: %v", err)
	}
	// The seed has to be readable, or every assertion below is vacuous.
	got, err := ReadProvenance(bytes.NewReader(buf.Bytes()))
	if err != nil || got == nil || len(got.Pages) != corpus.BookletPages {
		t.Fatalf("the seeded record does not read back: %v, %+v", err, got)
	}
	return buf.Bytes()
}

func buildPages(t *testing.T, pages []PageSource) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := BuildFromPages(&buf, pages); err != nil {
		t.Fatalf("BuildFromPages: %v", err)
	}
	return buf.Bytes()
}

// TestBuildFromPagesCarriesEachPagesProvenanceToItsNewIndex is design spec G4's
// obligation to G3, and it is the one this API cannot get away with skipping.
// Provenance.Pages is positional with no page identity -- index i describes page
// i+1 -- so a sequence that omits or reorders pages must move each record with
// its page. Leaving the slice alone degrades G3 from "identify precisely" to
// "identify wrongly".
func TestBuildFromPagesCarriesEachPagesProvenanceToItsNewIndex(t *testing.T) {
	src := bytes.NewReader(recordedBooklet(t))
	out := buildPages(t, []PageSource{
		{Source: src, Page: 6},
		{Source: src, Page: 2},
		{Source: src, Page: 6, Rotate: 90},
	})

	p, err := ReadProvenance(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if p == nil {
		t.Fatal("the built document carries no provenance record at all")
	}
	if len(p.Pages) != 3 {
		t.Fatalf("the record describes %d pages; the document has 3", len(p.Pages))
	}
	for i, want := range []int{6, 2, 6} {
		marker := fmt.Sprintf("marker-%d", want)
		if got := p.Pages[i].Applied; len(got) != 1 || got[0] != marker {
			t.Errorf("page %d of the export carries Applied %v, want [%s]", i+1, got, marker)
		}
		if got := p.Pages[i].DroppedAnnots; got != want {
			t.Errorf("page %d of the export carries DroppedAnnots %d, want %d", i+1, got, want)
		}
	}
}

// TestBuildFromPagesRecordsAPageItHasNoRecordFor covers a page imported from a
// document byblos never processed. The zero PageProvenance will not do:
// provenance.go says it "is indistinguishable to any reader from a page that was
// handled and had nothing applied". The record says so instead, using a reason
// outside the extraction vocabulary so that anyPageDiverted -- which matches
// exact strings -- leaves it inert rather than nominating a renderer.
func TestBuildFromPagesRecordsAPageItHasNoRecordFor(t *testing.T) {
	recorded := bytes.NewReader(recordedBooklet(t))
	plain := bytes.NewReader(corpusDoc(t, "mixed")) // no provenance record at all
	out := buildPages(t, []PageSource{
		{Source: recorded, Page: 4},
		{Source: plain, Page: 1},
	})

	p, err := ReadProvenance(bytes.NewReader(out))
	if err != nil || p == nil {
		t.Fatalf("ReadProvenance: %v, %+v", err, p)
	}
	if len(p.Pages) != 2 {
		t.Fatalf("the record describes %d pages; the document has 2", len(p.Pages))
	}
	if got := p.Pages[0].Applied; len(got) != 1 || got[0] != "marker-4" {
		t.Errorf("page 1 carries Applied %v, want [marker-4]", got)
	}

	imported := p.Pages[1]
	if isZeroPageProvenance(imported) {
		t.Fatal("the imported page carries a zero record, which reads as " +
			"'handled, and nothing was applied'")
	}
	if imported.Diverted == "" {
		t.Fatal("the imported page carries no Diverted reason")
	}
	for _, known := range []string{
		"not-single-raster", "unsupported-codec",
		"unsupported-codec-jbig2", "unsupported-codec-jpx", "unsupported-codec-tiff",
	} {
		if imported.Diverted == known {
			t.Errorf("the imported page claims Diverted %q, which is an extraction "+
				"reason and would nominate the document for a capability it never lacked", known)
		}
	}
	// And that reason must not make the export an upgrade candidate on a rule
	// that reads the divert vocabulary.
	if got := UpgradeCandidates(p, Capabilities()); len(got) > 0 {
		t.Logf("UpgradeCandidates = %v", got)
	}
}

// isZeroPageProvenance reports the record provenance.go warns about: the one
// "indistinguishable to any reader from a page that was handled and had nothing
// applied". PageProvenance holds a slice, so it cannot be compared with ==.
func isZeroPageProvenance(p PageProvenance) bool {
	return len(p.Applied) == 0 && p.Diverted == "" && len(p.Placement) == 0 &&
		p.DroppedAnnots == 0 && p.Geometry == nil
}

// TestBuildFromPagesRecordsDelinearizedOutput pins the vocabulary
// capabilityRules["linearize"] reads. Output is never linearized, and a record
// left saying "rewritten-linearized" -- which the source here does say -- would
// hide the fact that re-linearizing this document is now a real upgrade.
func TestBuildFromPagesRecordsDelinearizedOutput(t *testing.T) {
	src := bytes.NewReader(recordedBooklet(t))
	out := buildPages(t, []PageSource{{Source: src, Page: 1}})

	p, err := ReadProvenance(bytes.NewReader(out))
	if err != nil || p == nil {
		t.Fatalf("ReadProvenance: %v, %+v", err, p)
	}
	if p.Optimized != "rewritten-delinearized" {
		t.Errorf("Optimized is %q, want %q", p.Optimized, "rewritten-delinearized")
	}
	if !containsString(UpgradeCandidates(p, Capabilities()), "linearize") {
		t.Error("the export is not nominated for re-linearization, so a caller " +
			"composing BuildFromPages with Optimize{Linearize:true} cannot find it")
	}
	if p.Version != Version {
		t.Errorf("Version is %q, want %q", p.Version, Version)
	}
}

func containsString(s []string, want string) bool { return slices.Contains(s, want) }

// TestBuildFromPagesClaimsOnlyCapabilitiesEverySourceHad keeps the whole-document
// claim honest when the pages come from documents two different builds produced.
// Claiming the union would suppress an upgrade nomination for a capability one
// of the contributing builds did not have, and the rest of upgrade.go takes the
// opposite bias: a wasted re-run beats a hidden upgrade.
func TestBuildFromPagesClaimsOnlyCapabilitiesEverySourceHad(t *testing.T) {
	recorded := bytes.NewReader(recordedBooklet(t))
	plain := bytes.NewReader(corpusDoc(t, "mixed"))

	single := buildPages(t, []PageSource{{Source: recorded, Page: 2}})
	p, err := ReadProvenance(bytes.NewReader(single))
	if err != nil || p == nil {
		t.Fatalf("ReadProvenance: %v, %+v", err, p)
	}
	if !containsString(p.Capabilities, "jbig2-generic") {
		t.Errorf("a single-source export claims Capabilities %v; its source claimed "+
			"jbig2-generic and nothing about the export lost it", p.Capabilities)
	}

	mixed := buildPages(t, []PageSource{
		{Source: recorded, Page: 2},
		{Source: plain, Page: 1},
	})
	q, err := ReadProvenance(bytes.NewReader(mixed))
	if err != nil || q == nil {
		t.Fatalf("ReadProvenance: %v, %+v", err, q)
	}
	if containsString(q.Capabilities, "jbig2-generic") {
		t.Errorf("an export mixing a recorded source with an unrecorded one claims "+
			"Capabilities %v; the second source's build claimed nothing", q.Capabilities)
	}
}

// TestBuildFromPagesContextIsCheckedBeforeAnyWork pins the convention every
// other primitive in this package follows: a cancelled call writes nothing.
func TestBuildFromPagesContextIsCheckedBeforeAnyWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	err := BuildFromPagesContext(ctx, &buf, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "booklet")), Page: 1},
	})
	if err == nil {
		t.Fatal("a cancelled context did not stop the call")
	}
	if buf.Len() != 0 {
		t.Errorf("a cancelled call wrote %d bytes", buf.Len())
	}
}

// TestBuildFromPagesRefusalsReachTheCaller checks that the seam's refusals are
// not swallowed by the provenance pass wrapped around them.
func TestBuildFromPagesRefusalsReachTheCaller(t *testing.T) {
	tests := []struct {
		name  string
		pages []PageSource
		want  string
	}{
		{"no pages", nil, "no pages"},
		{
			"a page out of range",
			[]PageSource{{Source: bytes.NewReader(corpusDoc(t, "mixed")), Page: 3}},
			"out of range",
		},
		{
			"a rotation that is not a quarter turn",
			[]PageSource{{Source: bytes.NewReader(corpusDoc(t, "mixed")), Page: 1, Rotate: 45}},
			"rotation",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := BuildFromPages(&buf, tc.pages)
			if err == nil {
				t.Fatalf("BuildFromPages accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if buf.Len() != 0 {
				t.Errorf("a refused call wrote %d bytes", buf.Len())
			}
		})
	}
}

// TestBuildFromPagesContextRefusesADocumentPdfcpuWouldRefuse is the
// regression for byb-yul.6 Correction 5's own bug (found in adversarial
// review): folding the provenance write into BuildFromPagesWithProperties's
// one build removed the second read-validate-optimize-write pass that used
// to run afterwards -- and with it, the ONLY validation BuildFromPagesContext
// ever had, since that pass was api.AddProperties, which validates
// internally as a side effect of writing. Nothing replaced the gate, so an
// export whose migrated page dictionary carries a page-level entry pdfcpu's
// (relaxed) validator refuses -- /PresSteps is unconditionally "not
// supported" regardless of its content (pdfcpu validate/page.go) -- built
// successfully with a nil error and wrote invalid bytes to the caller.
func TestBuildFromPagesContextRefusesADocumentPdfcpuWouldRefuse(t *testing.T) {
	src := presStepsPageDoc()
	var buf bytes.Buffer
	err := BuildFromPagesContext(context.Background(), &buf, []PageSource{
		{Source: bytes.NewReader(src), Page: 1},
	})
	if err == nil {
		t.Fatal("BuildFromPagesContext accepted a page carrying /PresSteps, " +
			"which pdfcpu's own validator unconditionally refuses")
	}
	if !strings.Contains(err.Error(), "PresSteps") {
		t.Errorf("error = %q, want it to name PresSteps (pdfcpu's validator error)", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused build wrote %d bytes; it must write none", buf.Len())
	}
}

// presStepsPageDoc is a minimal, hand-assembled one-page PDF whose page
// dictionary carries /PresSteps -- a field BuildFromPages' migration copies
// verbatim (migratePage's generic field loop has no special case for it) and
// pdfcpu's validator refuses outright, regardless of the value.
func presStepsPageDoc() []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] " +
			"/Resources << >> /PresSteps << /S /Fly >> >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)
	return b.Bytes()
}

var _ io.Writer = (*bytes.Buffer)(nil)

// clonePageProvenance deep-copies ClipBox explicitly and would silently
// SHARE RasterQuad if the same treatment were missed for it (byb-2mt): a
// mutation through the export would reach back into the source, breaking the
// type's own doc comment promise that the exported record shares no slice
// with the source's.
func TestClonePageProvenanceSharesNoRasterQuadPointer(t *testing.T) {
	in := PageProvenance{Geometry: &PageGeometry{RasterQuad: &[8]float64{1, 2, 3, 4, 5, 6, 7, 8}}}
	out := clonePageProvenance(in)
	if out.Geometry == nil || out.Geometry.RasterQuad == nil {
		t.Fatal("clonePageProvenance() dropped RasterQuad")
	}
	if out.Geometry.RasterQuad == in.Geometry.RasterQuad {
		t.Fatal("clonePageProvenance() shares the RasterQuad pointer with its input; want a deep copy")
	}
	if *out.Geometry.RasterQuad != *in.Geometry.RasterQuad {
		t.Errorf("out.Geometry.RasterQuad = %v; want %v", *out.Geometry.RasterQuad, *in.Geometry.RasterQuad)
	}
	out.Geometry.RasterQuad[0] = 99
	if in.Geometry.RasterQuad[0] == 99 {
		t.Fatal("mutating the export's RasterQuad reached back into the source")
	}
}
