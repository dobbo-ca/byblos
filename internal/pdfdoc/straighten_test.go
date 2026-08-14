package pdfdoc

// Step 4 of the straighten design (docs/superpowers/specs/2026-08-14-straighten-design.md
// sections 2-4): the contract and the rotation.

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestStraightenIsLossless pins that the underlying raster is carried byte
// for byte: WrapContent never decodes or rewrites an existing stream, so the
// image XObject's own encoded bytes must be identical before and after.
func TestStraightenIsLossless(t *testing.T) {
	orig := open(t, "scan")
	origID := image0(t, orig, 1)
	origRaw := orig.(*doc).streams[origID].Raw
	if len(origRaw) == 0 {
		t.Fatal("precondition: the source raster stream is empty")
	}

	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: 1.7}},
	})
	gotID := image0(t, d, 1)
	gotRaw := d.(*doc).streams[gotID].Raw

	if !bytes.Equal(origRaw, gotRaw) {
		t.Errorf("the straightened raster stream differs from the source's: got %d bytes, want %d bytes identical",
			len(gotRaw), len(origRaw))
	}
}

// wrapMatrix reads the six numbers of the "before" wrapper stream's `cm`
// operator that applyStraighten wrote onto page n.
func wrapMatrix(t *testing.T, d Doc, n int) matrix {
	t.Helper()
	pd, xt := pageDict(t, d, n)
	arr, ok := pd["Contents"].(types.Array)
	if !ok || len(arr) == 0 {
		t.Fatalf("page %d /Contents is %T, want a non-empty types.Array", n, pd["Contents"])
	}
	ref, ok := arr[0].(types.IndirectRef)
	if !ok {
		t.Fatalf("page %d /Contents[0] is %T, not types.IndirectRef", n, arr[0])
	}
	ops := string(decodedStream(t, xt, ref))
	var m matrix
	i := strings.Index(ops, "\n")
	if i < 0 || strings.TrimSpace(ops[:i]) != "q" {
		t.Fatalf("page %d wrapper stream = %q, want it to start with a lone \"q\" line", n, ops)
	}
	if _, err := fmt.Sscanf(ops[i+1:], "%g %g %g %g %g %g cm", &m.a, &m.b, &m.c, &m.d, &m.e, &m.f); err != nil {
		t.Fatalf("page %d wrapper stream = %q, want a \"cm\" line: %v", n, ops, err)
	}
	return m
}

// TestStraightenRotatesAboutThePageCentre refutes origin-rotation
// explicitly: the CropBox centre must be a fixed point of the matrix
// WrapContent is given, and -- since the corpus scan fixture's centre is not
// the origin -- the translation terms must be nonzero. Rotating about the
// origin was measured to push the raster off the page edge (design spec
// section 3).
func TestStraightenRotatesAboutThePageCentre(t *testing.T) {
	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: 90}},
	})
	m := wrapMatrix(t, d, 1)

	const cx, cy = 306, 396 // half of the 612x792 corpus page
	gotX := cx*m.a + cy*m.c + m.e
	gotY := cx*m.b + cy*m.d + m.f
	if math.Abs(gotX-cx) > 1e-6 || math.Abs(gotY-cy) > 1e-6 {
		t.Errorf("the page centre (%v,%v) maps to (%v,%v); it must be a fixed point of the rotation",
			cx, cy, gotX, gotY)
	}
	if math.Abs(m.e) < 1 && math.Abs(m.f) < 1 {
		t.Errorf("matrix = %+v has a near-zero translation; an origin-anchored rotation would too, "+
			"and the page centre here is (306,396), not the origin", m)
	}
}

// TestStraightenComposesWithRotate pins that /Rotate and the straighten
// angle are independent: /Rotate passes through unchanged (it is a display
// attribute the viewer applies after the content, design spec section 3),
// and the content matrix rotates by exactly the angle asked for regardless
// of what /Rotate says.
func TestStraightenComposesWithRotate(t *testing.T) {
	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Rotate: 90, Straighten: &StraightenSpec{Deg: 0.6}},
	})
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	if p.Rotate != 90 {
		t.Errorf("/Rotate = %d, want 90 unchanged by the straighten angle", p.Rotate)
	}
	m := wrapMatrix(t, d, 1)
	if got := math.Atan2(m.b, m.a) * 180 / math.Pi; math.Abs(got-0.6) > 1e-6 {
		t.Errorf("the content matrix's angle = %v, want 0.6 -- /Rotate must not compose into it", got)
	}
}

// TestStraightenValidates pins that api.Validate accepts the straightened
// output (build already runs it) and that the page box is untouched, per
// design spec section 4: "the page box does not change".
func TestStraightenValidates(t *testing.T) {
	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: 7}},
	})
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	want := Rect{0, 0, 612, 792}
	if p.MediaBox != want {
		t.Errorf("MediaBox = %+v, want %+v unchanged", p.MediaBox, want)
	}
	if p.CropBox != want {
		t.Errorf("CropBox = %+v, want %+v unchanged", p.CropBox, want)
	}
}

// TestStraightenRefusesACrop pins validate's two new rows: a non-nil Crop
// (not implemented in this version) and a non-finite Deg, which would write
// a `cm` of non-finite numbers.
func TestStraightenRefusesACrop(t *testing.T) {
	crop := [4]float64{0, 0, 100, 100}
	for _, tc := range []struct {
		name string
		spec StraightenSpec
		want string
	}{
		{"a crop", StraightenSpec{Deg: 1, Crop: &crop}, "crop"},
		{"a NaN angle", StraightenSpec{Deg: math.NaN()}, "finite"},
		{"a positive infinite angle", StraightenSpec{Deg: math.Inf(1)}, "finite"},
		{"a negative infinite angle", StraightenSpec{Deg: math.Inf(-1)}, "finite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := BuildFromPages(&buf, []PageSource{
				{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &tc.spec},
			})
			if err == nil {
				t.Fatalf("BuildFromPages accepted %s and wrote %d bytes", tc.name, buf.Len())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if buf.Len() != 0 {
				t.Errorf("a refused build wrote %d bytes; it must write none", buf.Len())
			}
		})
	}
}

// TestStraightenDoesNotShareAWrappedContentsArrayAcrossOutputPages pins the
// "IndirectRef to an array" /Contents shape against a caller who names the
// SAME source page twice, each with a different straighten angle. migrate
// (buildpages.go) memoizes an object by its source object number, so both
// output page dicts end up pointing at the SAME migrated array object; if
// WrapContent extends that array object in place (as wrapIndirectContents
// used to), page 1's wrap and page 2's wrap both land in the one shared
// array, and BOTH output pages end up wearing both wrappers.
func TestStraightenDoesNotShareAWrappedContentsArrayAcrossOutputPages(t *testing.T) {
	src := openCorpus(t, "scan")
	orig := []byte("q 306 0 0 396 0 0 cm /Im0 Do Q\n")
	s1 := newStream(t, src.(*doc).ctx.XRefTable, orig)
	setContentsIndirectArray(t, src, 1, s1)
	srcBytes := writeDoc(t, src)
	r := bytes.NewReader(srcBytes)

	d, _ := build(t, []PageSource{
		{Source: r, Page: 1, Straighten: &StraightenSpec{Deg: 2}},
		{Source: r, Page: 1, Straighten: &StraightenSpec{Deg: 5}},
	})

	for n, wantDeg := range map[int]float64{1: 2, 2: 5} {
		pd, xt := pageDict(t, d, n)
		ref, ok := pd["Contents"].(types.IndirectRef)
		if !ok {
			t.Fatalf("page %d /Contents is %T, want types.IndirectRef (to an array)", n, pd["Contents"])
		}
		target, err := xt.Dereference(ref)
		if err != nil {
			t.Fatalf("page %d dereference /Contents: %v", n, err)
		}
		arr, ok := target.(types.Array)
		if !ok {
			t.Fatalf("page %d /Contents resolves to %T, want types.Array", n, target)
		}
		if len(arr) != 3 {
			t.Fatalf("page %d /Contents has %d entries, want 3 (before, original, after) -- "+
				"a length outside that means it picked up the OTHER page's wrapper too", n, len(arr))
		}
		wrapperRef, ok := arr[0].(types.IndirectRef)
		if !ok {
			t.Fatalf("page %d /Contents[0] is %T, not types.IndirectRef", n, arr[0])
		}
		ops := string(decodedStream(t, xt, wrapperRef))
		var m matrix
		i := strings.Index(ops, "\n")
		if i < 0 || strings.TrimSpace(ops[:i]) != "q" {
			t.Fatalf("page %d wrapper stream = %q, want it to start with a lone \"q\" line", n, ops)
		}
		if _, err := fmt.Sscanf(ops[i+1:], "%g %g %g %g %g %g cm", &m.a, &m.b, &m.c, &m.d, &m.e, &m.f); err != nil {
			t.Fatalf("page %d wrapper stream = %q, want a \"cm\" line: %v", n, ops, err)
		}
		if got := math.Atan2(m.b, m.a) * 180 / math.Pi; math.Abs(got-wantDeg) > 1e-6 {
			t.Errorf("page %d wrapper angle = %v, want %v", n, got, wantDeg)
		}
	}
}

// TestStraightenIsIdempotentWhenContentEndsWithoutTrailingWhitespace pins the
// enforced-absolute redelivery promise (design spec section 2: "Re-running
// Deg = 3.2 against an export already carrying Straightened{Deg: 3.2}
// applies 0.0 and changes nothing") against a page whose last content token
// is NOT followed by whitespace -- the common real-world shape ("...Do Q"
// with no trailing newline). pdfcpu's PageContent concatenates /Contents
// array members with no separator, so the wrapper's trailing "Q" would fuse
// with that last token on read-back unless WrapContent itself guards the
// boundary it is the one introducing.
func TestStraightenIsIdempotentWhenContentEndsWithoutTrailingWhitespace(t *testing.T) {
	src := openCorpus(t, "scan")
	pd, xt := pageDict(t, src, 1)
	sd, err := xt.NewStreamDictForBuf([]byte("q 306 0 0 396 0 0 cm /Im0 Do Q")) // no trailing newline
	if err != nil {
		t.Fatalf("NewStreamDictForBuf: %v", err)
	}
	if err := sd.Encode(); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	ref, err := xt.IndRefForNewObject(*sd)
	if err != nil {
		t.Fatalf("IndRefForNewObject: %v", err)
	}
	pd["Contents"] = *ref
	first := writeDoc(t, src)

	firstDoc, _ := build(t, []PageSource{
		{Source: bytes.NewReader(first), Page: 1, Straighten: &StraightenSpec{Deg: 3.2}},
	})
	straightenedOnce := writeDoc(t, firstDoc)

	// Redelivery: straighten the ALREADY-straightened export again by the
	// same absolute angle. This must succeed -- it is exactly the second half
	// of TestStraightenOnAlreadyStraightenedSourceAppliesTheDifference's
	// scenario, applied at the pdfdoc layer where the delta is already 0 --
	// not fail with ErrUnbalancedContent because the wrapper's own "Q" fused
	// with the original content's un-terminated last token.
	build(t, []PageSource{
		{Source: bytes.NewReader(straightenedOnce), Page: 1, Straighten: &StraightenSpec{Deg: 0}},
	})
}

// mediaAndCropBoxDoc is a minimal one-page document whose MediaBox and
// CropBox are DIFFERENT rectangles, written by hand like blankPageDoc
// (contract_test.go) because this package's own writer has no path that
// produces a page whose two boxes disagree.
func mediaAndCropBoxDoc(mediaX0, mediaY0, cropX0, cropY0, cropX1, cropY1 float64) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /CropBox [%g %g %g %g] /Contents 4 0 R >>",
			mediaX0, mediaY0, cropX0, cropY0, cropX1, cropY1),
		"<< /Length 19 >>\nstream\nq 1 0 0 1 0 0 cm Q\nendstream",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)
	return buf.Bytes()
}

// TestStraightenAppliesToTheRequestedPageNotOnlyPage1 pins applyStraighten's
// page-number arithmetic (straighten.go's "n := i + 1") against a multi-page
// build where only the SECOND page asks for a straighten: page 1 must come
// out untouched and page 2 must carry the wrapper, not the other way round.
// Every other straighten fixture in this package builds a single page, so
// this arithmetic was otherwise never exercised.
func TestStraightenAppliesToTheRequestedPageNotOnlyPage1(t *testing.T) {
	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1},
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: 5}},
	})

	pd1, _ := pageDict(t, d, 1)
	if _, ok := pd1["Contents"].(types.Array); ok {
		t.Errorf("page 1 /Contents is a wrapped types.Array; page 1 asked for no straighten and must be untouched")
	}

	m := wrapMatrix(t, d, 2)
	if got := math.Atan2(m.b, m.a) * 180 / math.Pi; math.Abs(got-5) > 1e-6 {
		t.Errorf("page 2 wrapper angle = %v, want 5", got)
	}
}

// TestStraightenRotatesAboutTheCropBoxNotTheMediaBox pins design spec section
// 3's explicit choice of centre against a page whose CropBox and MediaBox
// actually differ. Every other straighten fixture in this package (including
// blankPageDoc) sets the two boxes equal, so they cannot tell CropBox-centred
// rotation apart from MediaBox-centred rotation; this one can.
func TestStraightenRotatesAboutTheCropBoxNotTheMediaBox(t *testing.T) {
	src := mediaAndCropBoxDoc(612, 792, 100, 100, 300, 400)
	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: 90}},
	})
	m := wrapMatrix(t, d, 1)

	const cropCx, cropCy = 200.0, 250.0 // centre of CropBox [100 100 300 400]
	gotX := cropCx*m.a + cropCy*m.c + m.e
	gotY := cropCx*m.b + cropCy*m.d + m.f
	if math.Abs(gotX-cropCx) > 1e-6 || math.Abs(gotY-cropCy) > 1e-6 {
		t.Errorf("the CropBox centre (%v,%v) maps to (%v,%v); it must be the fixed point, not the "+
			"MediaBox centre (306,396)", cropCx, cropCy, gotX, gotY)
	}
}

// TestStraightenBoundsUnderARotatedCTM is the clip trap named in the design
// spec's tests section: a page with an existing W/W* clip now has a rotated
// parent CTM, and content.Walk's Bounds must follow the rotation into the
// clip rather than reporting the clip's literal, un-rotated coordinates.
//
// scan-clipped-corner clips a full-page raster to `0 0 100 100 re W n`,
// BEFORE any `cm` inside the page's own content -- so once WrapContent
// prepends a 90-degree rotation about the 612x792 page's centre (306,396),
// the clip rectangle's corners are what get mapped through that rotation,
// not the raster's. 90 degrees is chosen because it keeps the expected
// numbers exact integers rather than needing a trig tolerance beyond
// floating-point noise.
func TestStraightenBoundsUnderARotatedCTM(t *testing.T) {
	d, _ := build(t, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan-clipped-corner")), Page: 1, Straighten: &StraightenSpec{Deg: 90}},
	})
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	scan, err := content.Walk(context.Background(), p.Content, p.Scope, d)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(scan.Images) != 1 {
		t.Fatalf("Images = %v, want exactly one placement", scan.Images)
	}

	// The un-rotated clip is [0,0]-[100,100]. Rotating it 90 degrees CCW
	// about (306,396) -- see rotationMatrix's doc comment for the matrix --
	// maps its four corners to a 100x100 square at [602,90]-[702,190].
	want := content.Box{LLX: 602, LLY: 90, URX: 702, URY: 190}
	got := scan.Images[0].Box
	const tol = 1e-3
	if math.Abs(got.LLX-want.LLX) > tol || math.Abs(got.LLY-want.LLY) > tol ||
		math.Abs(got.URX-want.URX) > tol || math.Abs(got.URY-want.URY) > tol {
		t.Errorf("Images[0].Box = %+v, want %+v (the clip rotated with the page, not left at "+
			"its literal [0,0]-[100,100] coordinates)", got, want)
	}
}
