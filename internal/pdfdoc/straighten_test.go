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
