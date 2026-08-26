package pdfdoc

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"strings"
	"testing"

	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
)

// corpusDoc, not doc: `doc` is already the unexported struct type in
// pdfdoc.go, and shadowing it here is a compile error.
func corpusDoc(t *testing.T, name string) []byte {
	t.Helper()
	data, ok := corpus.ByName(name)
	if !ok {
		t.Fatalf("corpus document %q not found", name)
	}
	return data
}

func open(t *testing.T, name string) Doc {
	t.Helper()
	d, err := Open(bytes.NewReader(corpusDoc(t, name)))
	if err != nil {
		t.Fatalf("Open(%q) error = %v", name, err)
	}
	return d
}

// image0 resolves /Im0 on a page and returns its id, so the RawImage tests do
// not each repeat the four-line dance.
func image0(t *testing.T, d Doc, page int) int {
	t.Helper()
	p, err := d.Page(page)
	if err != nil {
		t.Fatalf("Page(%d) error = %v", page, err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok {
		t.Fatalf("page %d: XObject(scope, \"Im0\") not found", page)
	}
	if !xo.Image {
		t.Fatalf("page %d: Im0 did not resolve as an image", page)
	}
	return xo.ID
}

func TestOpenReadsPageGeometry(t *testing.T) {
	d := open(t, "scan")
	if got := d.PageCount(); got != 1 {
		t.Fatalf("PageCount() = %d; want 1", got)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	want := Rect{0, 0, corpus.PageWidthPt, corpus.PageHeightPt}
	if p.MediaBox != want {
		t.Errorf("MediaBox = %+v; want %+v", p.MediaBox, want)
	}
	// A page with no /CropBox reports the MediaBox, so callers never special-case it.
	if p.CropBox != want {
		t.Errorf("CropBox = %+v; want %+v (the MediaBox)", p.CropBox, want)
	}
	if p.Rotate != 0 {
		t.Errorf("Rotate = %d; want 0", p.Rotate)
	}
	if !strings.Contains(string(p.Content), "/Im0 Do") {
		t.Errorf("Content = %q; want it to contain \"/Im0 Do\"", p.Content)
	}
}

func TestOpenReadsRotate(t *testing.T) {
	p, err := open(t, "scan-rotated").Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	if p.Rotate != 90 {
		t.Errorf("Rotate = %d; want 90", p.Rotate)
	}
}

func TestOpenMultiPage(t *testing.T) {
	d := open(t, "mixed")
	if got := d.PageCount(); got != 2 {
		t.Fatalf("PageCount() = %d; want 2", got)
	}
	p1, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	if !strings.Contains(string(p1.Content), "Tj") {
		t.Errorf("page 1 content = %q; want the born-digital text", p1.Content)
	}
	p2, err := d.Page(2)
	if err != nil {
		t.Fatalf("Page(2) error = %v", err)
	}
	if !strings.Contains(string(p2.Content), "/Im0 Do") {
		t.Errorf("page 2 content = %q; want the scan", p2.Content)
	}
}

// A /Pages node may hold its /Kids as an indirect reference, and Google Books
// PDF Converter output does. pdfcpu reads /Kids with types.Dict.ArrayEntry,
// which does not dereference, so such a node looks childless and PageDict
// returns it — no /MediaBox, no /Contents — as the dictionary of every page.
// Open repairs the tree; without that, both pages here fail with
// "no dictionary or no MediaBox". See byb-5kk.
func TestPageTreeWithIndirectKids(t *testing.T) {
	d := open(t, "indirect-kids")
	if got := d.PageCount(); got != 2 {
		t.Fatalf("PageCount() = %d; want 2", got)
	}
	want := Rect{0, 0, corpus.PageWidthPt, corpus.PageHeightPt}
	for n := 1; n <= 2; n++ {
		p, err := d.Page(n)
		if err != nil {
			t.Fatalf("Page(%d) error = %v", n, err)
		}
		if p.MediaBox != want {
			t.Errorf("page %d: MediaBox = %+v; want %+v", n, p.MediaBox, want)
		}
		if !strings.Contains(string(p.Content), "/Im0 Do") {
			t.Errorf("page %d: Content = %q; want it to contain \"/Im0 Do\"", n, p.Content)
		}
		// The page's own /Resources is an indirect reference too, so a repair
		// that fixed only /Kids would still resolve nothing here.
		if _, ok := d.XObject(p.Scope, "Im0"); !ok {
			t.Errorf("page %d: Im0 does not resolve in the page scope", n)
		}
	}
	// Each page must resolve its own raster: returning the same dictionary for
	// every page is exactly the bug, and it would pass every assertion above if
	// the two pages shared an image.
	if id1, id2 := image0(t, d, 1), image0(t, d, 2); id1 == id2 {
		t.Errorf("both pages resolved to image id %d; want one image object each", id1)
	}
}

func TestPageOutOfRange(t *testing.T) {
	d := open(t, "scan")
	for _, n := range []int{0, 2, -1} {
		if _, err := d.Page(n); err == nil {
			t.Errorf("Page(%d) on a 1-page document: want an error, got nil", n)
		}
	}
}

func TestXObjectResolvesAnImage(t *testing.T) {
	d := open(t, "scan")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok {
		t.Fatal("XObject(scope, \"Im0\") not found")
	}
	if !xo.Image {
		t.Fatal("Im0 did not resolve as an image")
	}
	info, ok := d.ImageInfo(xo.ID)
	if !ok {
		t.Fatalf("ImageInfo(%d) not found", xo.ID)
	}
	if info.Width != corpus.ScanImageW || info.Height != corpus.ScanImageH {
		t.Errorf("image dims = %dx%d; want %dx%d",
			info.Width, info.Height, corpus.ScanImageW, corpus.ScanImageH)
	}
	if info.BPC != 8 {
		t.Errorf("BPC = %d; want 8", info.BPC)
	}
	if info.ImageMask {
		t.Error("ImageMask = true; want false")
	}
}

// A Form XObject must come back decoded, with its own /Matrix and a scope
// handle that resolves the form's own resources.
func TestXObjectResolvesAFormWithItsOwnScope(t *testing.T) {
	d := open(t, "scan-in-form")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	fm, ok := d.XObject(p.Scope, "Fm0")
	if !ok {
		t.Fatal("XObject(scope, \"Fm0\") not found")
	}
	if fm.Image {
		t.Fatal("Fm0 resolved as an image; want a form")
	}
	if !strings.Contains(string(fm.Content), "/Im0 Do") {
		t.Errorf("form content = %q; want it decoded and containing \"/Im0 Do\"", fm.Content)
	}
	if fm.Matrix != content.Identity {
		t.Errorf("form Matrix = %v; want identity", fm.Matrix)
	}
	if fm.Scope == p.Scope {
		t.Error("the form reused the page's scope; it declares its own /Resources")
	}
	if _, ok := d.XObject(fm.Scope, "Im0"); !ok {
		t.Error("Im0 does not resolve in the form's scope")
	}
	// The page's own scope must NOT see the form's Im0.
	if _, ok := d.XObject(p.Scope, "Im0"); ok {
		t.Error("Im0 leaked into the page scope")
	}
}

// A form without its own /Resources inherits the enclosing scope
// (ISO 32000-1 section 8.10.2). overlay-text's form declares only /Font, so
// the image must still resolve through the page's resources.
func TestFormWithoutXObjectResourcesFallsBack(t *testing.T) {
	d := open(t, "overlay-text")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	fm, ok := d.XObject(p.Scope, "Fm0")
	if !ok {
		t.Fatal("Fm0 not found")
	}
	if _, ok := d.XObject(fm.Scope, "Im0"); !ok {
		t.Error("Im0 does not resolve from the form's scope; resource fallback is missing")
	}
}

func TestXObjectMissingName(t *testing.T) {
	d := open(t, "scan")
	p, _ := d.Page(1)
	if _, ok := d.XObject(p.Scope, "Nope"); ok {
		t.Error("XObject(scope, \"Nope\") reported found")
	}
	if _, ok := d.XObject(9999, "Im0"); ok {
		t.Error("XObject with an out-of-range scope reported found")
	}
}

func TestRawImageReturnsDecodableBytes(t *testing.T) {
	d := open(t, "scan")
	data, ft, err := d.RawImage(image0(t, d, 1))
	if err != nil {
		t.Fatalf("RawImage() error = %v", err)
	}
	// pdfcpu re-renders a Flate-compressed image to PNG.
	if ft != "png" {
		t.Errorf("fileType = %q; want \"png\"", ft)
	}
	// Assert the bytes actually decode. Checking only len(data) != 0 would pass
	// on five kilobytes of garbage.
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("image.Decode() error = %v", err)
	}
	if format != "png" {
		t.Errorf("decoded format = %q; want \"png\"", format)
	}
	if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
		t.Errorf("decoded %dx%d; want %dx%d", b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
	}
}

// The regression guard for the dedup trap. Both pages of dup-raster hold the
// same raster bytes as two distinct objects; pdfcpu's optimize pass collapses
// them, so any path that asks pdfcpu which objects are on page 2 gets page 1's
// answer. Resolving through the page's own resources must not.
func TestRawImageIsPerPageWhenRastersAreIdentical(t *testing.T) {
	d := open(t, "dup-raster")
	id1, id2 := image0(t, d, 1), image0(t, d, 2)
	if id1 == id2 {
		t.Fatalf("both pages resolved to id %d; the corpus document is meant to use two objects", id1)
	}
	for page, id := range map[int]int{1: id1, 2: id2} {
		data, ft, err := d.RawImage(id)
		if err != nil {
			t.Fatalf("page %d: RawImage(%d) error = %v", page, id, err)
		}
		if ft != "png" {
			t.Errorf("page %d: fileType = %q; want \"png\"", page, ft)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("page %d: image.Decode() error = %v", page, err)
		}
		if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
			t.Errorf("page %d: decoded %dx%d; want %dx%d",
				page, b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
		}
	}
}

// pdfcpu hands JBIG2 back as opaque bytes with FileType "jbig2" and no error.
// That is not a failure at this layer — it is a real payload in a codec this
// package does not claim to render — so RawImage reports it faithfully and
// extract.go decides what to do.
func TestRawImageReportsJBIG2AsIs(t *testing.T) {
	d := open(t, "jbig2")
	data, ft, err := d.RawImage(image0(t, d, 1))
	if err != nil {
		t.Fatalf("RawImage() error = %v", err)
	}
	if ft != "jbig2" {
		t.Errorf("fileType = %q; want \"jbig2\"", ft)
	}
	if len(data) == 0 {
		t.Error("RawImage() returned no bytes for the JBIG2 stream")
	}
}

// The bitonal flag B2 selects on has to come from the dictionary, because
// pdfcpu reports zero for Bpc on this path.
func TestImageInfoReadsOneBitPerComponent(t *testing.T) {
	d := open(t, "jbig2")
	info, ok := d.ImageInfo(image0(t, d, 1))
	if !ok {
		t.Fatal("ImageInfo() not found")
	}
	if info.BPC != 1 {
		t.Errorf("BPC = %d; want 1", info.BPC)
	}
	if info.Width != corpus.ScanImageW || info.Height != corpus.ScanImageH {
		t.Errorf("dims = %dx%d; want %dx%d",
			info.Width, info.Height, corpus.ScanImageW, corpus.ScanImageH)
	}
}

// Whether the upper layer of a stacked page is opaque decides whether it can be
// said to hide the layer below, and /ca lives in an /ExtGState rather than in
// the image dictionary.
func TestExtGStateOpaque(t *testing.T) {
	d := open(t, "stacked-alpha")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	if d.ExtGStateOpaque(p.Scope, "GS0") {
		t.Error("ExtGStateOpaque(GS0) = true; the state sets /ca 0.5")
	}
	// A name the document does not declare has not been shown to be opaque, and
	// guessing that it is would be the one direction that loses content.
	if d.ExtGStateOpaque(p.Scope, "Nope") {
		t.Error("ExtGStateOpaque(undeclared name) = true; want the conservative answer")
	}
	// A document with no /ExtGState at all must not report its placements as
	// transparent, or every clean scan would divert.
	scan := open(t, "scan")
	sp, err := scan.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	if s, err := content.Walk(context.Background(), sp.Content, sp.Scope, scan); err != nil {
		t.Fatalf("Walk() error = %v", err)
	} else if len(s.Images) != 1 || !s.Images[0].Opaque {
		t.Errorf("Images = %+v; want one opaque placement", s.Images)
	}
}

// Font resolves a /Font resource name to the stable id content.Walk records
// on each TextShow (byb-lez.6); byb-lez.5 keys its font reads by it.
func TestFontResolvesToAStableID(t *testing.T) {
	d := open(t, "born-digital")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	id, ok := d.Font(p.Scope, "F1")
	if !ok {
		t.Fatal("Font(F1) not resolved; the fixture declares it")
	}
	if again, _ := d.Font(p.Scope, "F1"); again != id {
		t.Errorf("Font(F1) = %d then %d; want a stable id", id, again)
	}
	if _, ok := d.Font(p.Scope, "Nope"); ok {
		t.Error("Font(undeclared name) resolved; want false")
	}
	s, err := content.Walk(context.Background(), p.Content, p.Scope, d)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) == 0 {
		t.Fatal("TextShows empty; the fixture shows text")
	}
	if s.TextShows[0].FontID != id {
		t.Errorf("TextShows[0].FontID = %d; want %d", s.TextShows[0].FontID, id)
	}
}

func TestImageInfoReportsTransparencyEntries(t *testing.T) {
	d := open(t, "stacked-smask")
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	for name, want := range map[string]bool{"Im1": true, "Im0": false} {
		xo, ok := d.XObject(p.Scope, name)
		if !ok {
			t.Fatalf("XObject(scope, %q) not found", name)
		}
		info, ok := d.ImageInfo(xo.ID)
		if !ok {
			t.Fatalf("ImageInfo for %s not found", name)
		}
		if info.SMask != want {
			t.Errorf("%s: SMask = %v; want %v", name, info.SMask, want)
		}
		if info.Mask {
			t.Errorf("%s: Mask = true; the document declares none", name)
		}
	}
}

// ImageInfo.DecodeArray is what byb-e7n needs and ImageInfo.Decode alone could
// never supply: /Decode [1 0] on a bilevel raster inverts it and /Decode [0 1]
// is the default and changes nothing, and presence cannot tell those apart.
//
// The two fields say different things and BOTH are load-bearing. Decode is
// "the entry is there", which is what optimize.go's eligibility test wants —
// an entry it cannot read is still an entry it must not drop. DecodeArray is
// "and these are its numbers", nil when there are none to have. So the
// unreadable case below is the sharp one: /Decode present in a shape this
// cannot turn into floats must leave Decode true and DecodeArray nil, or a
// caller reading only DecodeArray would treat a remap it does not understand
// as no remap at all.
func TestImageInfoReadsTheDecodeArray(t *testing.T) {
	const raw = "<< /Type /XObject /Subtype /Image /Width 4 /Height 4 " +
		"/BitsPerComponent 1 /ColorSpace /DeviceGray /Filter /JBIG2Decode /Length 4 >>\n" +
		"stream\n\x00\x01\x02\x03\nendstream"
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << " +
			"/Absent 4 0 R /Default 5 0 R /Inverted 6 0 R /Float 7 0 R " +
			"/Indirect 8 0 R /Unreadable 10 0 R >> >> >>",
		raw,
		strings.Replace(raw, "/Length 4", "/Decode [0 1] /Length 4", 1),
		strings.Replace(raw, "/Length 4", "/Decode [1 0] /Length 4", 1),
		strings.Replace(raw, "/Length 4", "/Decode [1.0 0.0] /Length 4", 1),
		strings.Replace(raw, "/Length 4", "/Decode 9 0 R /Length 4", 1),
		"[1 0]",
		strings.Replace(raw, "/Length 4", "/Decode [/Bogus 0] /Length 4", 1),
	})

	d, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	for _, tc := range []struct {
		name    string
		present bool
		want    []float64
	}{
		{"Absent", false, nil},
		{"Default", true, []float64{0, 1}},
		{"Inverted", true, []float64{1, 0}},
		{"Float", true, []float64{1, 0}},
		{"Indirect", true, []float64{1, 0}},
		{"Unreadable", true, nil},
	} {
		xo, ok := d.XObject(p.Scope, tc.name)
		if !ok || !xo.Image {
			t.Fatalf("XObject(%q) not resolved as an image; the fixture is broken, "+
				"not the behaviour under test", tc.name)
		}
		info, ok := d.ImageInfo(xo.ID)
		if !ok {
			t.Fatalf("ImageInfo for %q not found", tc.name)
		}
		if info.Decode != tc.present {
			t.Errorf("ImageInfo(%q).Decode = %v; want %v", tc.name, info.Decode, tc.present)
		}
		if !slices.Equal(info.DecodeArray, tc.want) {
			t.Errorf("ImageInfo(%q).DecodeArray = %v; want %v", tc.name, info.DecodeArray, tc.want)
		}
	}
}

func TestRawImageUnknownID(t *testing.T) {
	d := open(t, "scan")
	if _, _, err := d.RawImage(99999); err == nil {
		t.Fatal("RawImage() for an id that was never resolved: want an error, got nil")
	}
	// ErrUnsupportedCodec is reserved for a real image in a codec pdfcpu will
	// not render; an unknown id is a caller mistake and must not be confused
	// with it.
	_, _, err := d.RawImage(99999)
	if errors.Is(err, ErrUnsupportedCodec) {
		t.Errorf("error = %v; want a plain lookup error, not ErrUnsupportedCodec", err)
	}
}

// A truncated file must produce a clean error, never a panic and never a
// plausible-looking parse.
func TestOpenMalformedReturnsAnError(t *testing.T) {
	if _, err := Open(bytes.NewReader(corpusDoc(t, "malformed"))); err == nil {
		t.Fatal("Open(malformed): want an error, got nil")
	}
}

// A page that carries no /Contents is legal and empty, not an error.
func TestPageWithoutContentsIsEmptyNotAnError(t *testing.T) {
	// A minimal one-page document with no /Contents entry.
	src := "%PDF-1.7\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 300] >>\nendobj\n"
	var xref strings.Builder
	offs := []int{
		strings.Index(src, "1 0 obj"),
		strings.Index(src, "2 0 obj"),
		strings.Index(src, "3 0 obj"),
	}
	start := len(src)
	xref.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, o := range offs {
		xref.WriteString(pad10(o) + " 00000 n \n")
	}
	xref.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n" + itoa(start) + "\n%%EOF\n")

	d, err := Open(strings.NewReader(src + xref.String()))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) on a contentless page: want no error, got %v", err)
	}
	if len(p.Content) != 0 {
		t.Errorf("Content = %q; want empty", p.Content)
	}
	if want := (Rect{0, 0, 200, 300}); p.MediaBox != want {
		t.Errorf("MediaBox = %+v; want %+v", p.MediaBox, want)
	}
}

// ISO 32000-1 7.7.3.3 says the CropBox is to be intersected with the
// MediaBox, and poppler does that. A CropBox that overhangs the MediaBox
// must not enlarge the page's reported bounds beyond it. See byb-wtp.
func TestCropBoxIsClampedToMediaBox(t *testing.T) {
	src := "%PDF-1.7\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/CropBox [-100 -100 900 1000] >>\nendobj\n"
	var xref strings.Builder
	offs := []int{
		strings.Index(src, "1 0 obj"),
		strings.Index(src, "2 0 obj"),
		strings.Index(src, "3 0 obj"),
	}
	start := len(src)
	xref.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, o := range offs {
		xref.WriteString(pad10(o) + " 00000 n \n")
	}
	xref.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n" + itoa(start) + "\n%%EOF\n")

	d, err := Open(strings.NewReader(src + xref.String()))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	want := Rect{0, 0, 612, 792}
	if p.MediaBox != want {
		t.Errorf("MediaBox = %+v; want %+v", p.MediaBox, want)
	}
	if p.CropBox != want {
		t.Errorf("CropBox = %+v; want %+v (clamped to MediaBox)", p.CropBox, want)
	}
}

// A CropBox that falls entirely outside the MediaBox has no intersection
// with it. poppler falls back to the MediaBox rather than reporting an
// empty page. See byb-wtp.
func TestCropBoxOutsideMediaBoxFallsBackToMediaBox(t *testing.T) {
	src := "%PDF-1.7\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/CropBox [700 900 1000 1200] >>\nendobj\n"
	var xref strings.Builder
	offs := []int{
		strings.Index(src, "1 0 obj"),
		strings.Index(src, "2 0 obj"),
		strings.Index(src, "3 0 obj"),
	}
	start := len(src)
	xref.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, o := range offs {
		xref.WriteString(pad10(o) + " 00000 n \n")
	}
	xref.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n" + itoa(start) + "\n%%EOF\n")

	d, err := Open(strings.NewReader(src + xref.String()))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	want := Rect{0, 0, 612, 792}
	if p.CropBox != want {
		t.Errorf("CropBox = %+v; want %+v (MediaBox fallback)", p.CropBox, want)
	}
}

// A page whose /Contents stream decodes to zero bytes is blank, not broken.
// The key is there, the stream is there, the stream is well-formed, and it
// inflates to nothing — a duplex scanner's back side. pdfcpu's PageContent
// reports it with model.ErrNoContent, and taking that for a failure is what
// byb-uxb measured costing 3% of a 200-document govdocs1 sample: the whole
// file, over a page with nothing on it. See byb-cqs.
func TestPageWhoseContentsDecodesToNothingIsBlankNotAnError(t *testing.T) {
	d := open(t, "blank-page")
	if got := d.PageCount(); got != 2 {
		t.Fatalf("PageCount() = %d; want 2", got)
	}
	p, err := d.Page(2)
	if err != nil {
		t.Fatalf("Page(2) on a blank page: want no error, got %v", err)
	}
	if len(p.Content) != 0 {
		t.Errorf("Content = %q; want empty", p.Content)
	}
	// A blank page still has geometry. Returning it is the whole difference
	// between "this page is empty" and "this page is unreadable".
	want := Rect{0, 0, corpus.PageWidthPt, corpus.PageHeightPt}
	if p.MediaBox != want {
		t.Errorf("MediaBox = %+v; want %+v", p.MediaBox, want)
	}
	if p.CropBox != want {
		t.Errorf("CropBox = %+v; want %+v", p.CropBox, want)
	}
	// And the page that is NOT blank must still read. This is the assertion
	// that measures the bug's real cost: one blank page took the document.
	p1, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) alongside a blank page: want no error, got %v", err)
	}
	if !strings.Contains(string(p1.Content), "/Im0 Do") {
		t.Errorf("page 1 Content = %q; want the scan", p1.Content)
	}
}

// The other half of the same decision, and the one that makes it a decision:
// a content stream that did not decode is NOT a blank page and must still be
// an error.
//
// pdfcpu gives this package no help here. Under the relaxed validation mode
// Open uses, decodeContentStream swallows a "flate: corrupt input before
// offset" failure and leaves the stream empty, so PageContent reports a
// shredded page with the SAME model.ErrNoContent it reports a blank one with.
// A fix that keyed on that sentinel alone would trade a false failure for a
// silent one: page 2 would come back as valid, empty, and wrong.
// Both /Contents shapes are covered because both reach this seam identically
// and each has its own way of being read wrong: the array form is what a
// reader that stops at the first entry, or that never expands the array at
// all, reports blank while the damage sits in the second stream.
func TestPageWithACorruptContentStreamIsStillAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"one stream", corpus.CorruptContentStream()},
		{"array of streams", corpus.CorruptContentStreamInArray()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Open(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			p, err := d.Page(2)
			if err == nil {
				t.Fatalf("Page(2) on a corrupt content stream: want an error, got Content = %q", p.Content)
			}
			if !strings.Contains(err.Error(), "page 2 content") {
				t.Errorf("error = %v; want it to name page 2's content", err)
			}
			// The failure must be confined to the damaged page: this fixture
			// differs from the blank-page one in nine bytes, so a
			// document-wide rejection here would mean the fix had simply moved
			// the over-reaction.
			if _, err := d.Page(1); err != nil {
				t.Errorf("Page(1) error = %v; want the undamaged page to still read", err)
			}
		})
	}
}

func pad10(n int) string {
	s := itoa(n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// --- byb-ged: pdfcpu's resource consolidation is off ------------------------
//
// Page and Annots call XRefTable.PageDict with consolidateRes=false. The two
// tests below are the two things that changes, and both were measured against
// poppler over the 4,840-document govdocs1 sample before the argument moved:
// 26 documents byblos refused and poppler read became 18, and the only two
// documents whose answer changed gained images poppler also reports.

// A page whose content names a colour space its /Resources does not declare
// must still be read.
//
// This is govdocs1/600140.pdf page 126 reduced to four objects (byb-ged). With
// consolidateRes=true, pdfcpu's consolidateResourceSubDict raises "missing
// required resource subdict: ColorSpace" and — because Inspect walks every
// page — the WHOLE 140-page document is lost. poppler reads it, and so do
// govdocs1/250237, 350691, 550327, 750403 and 900355, all on the same error.
//
// Byblos never resolves a page-level colour space at all: ImageInfo reads
// /ColorSpace off the image XObject's own dictionary, so this subdict's
// absence cannot change any number Inspect reports. Refusing the document was
// pure loss.
func TestPageToleratesAColorSpaceMissingFromResources(t *testing.T) {
	body := "/Cs6 cs 1 1 1 scn 0 0 100 100 re f"
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>",
		"<< /Length " + itoa(len(body)) + " >>\nstream\n" + body + "\nendstream",
	})

	d, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v; want the page read. A \"missing required "+
			"resource subdict\" here means consolidateRes is back on.", err)
	}
	// Assert the page is actually usable, not merely non-erroring: a nil-safe
	// pass that returned an empty page would prove nothing.
	if want := (Rect{0, 0, 612, 792}); p.MediaBox != want {
		t.Errorf("MediaBox = %v; want %v", p.MediaBox, want)
	}
	if string(p.Content) != body {
		t.Errorf("Content = %q; want %q", p.Content, body)
	}
	if _, err := d.Annots(1); err != nil {
		t.Errorf("Annots(1) error = %v; want nil (it reads the same page dict)", err)
	}
}

// An image a Form XObject paints, and the page's own content stream never
// names, must still resolve through the form's scope.
//
// consolidateRes=true does not only reject; it PRUNES the resource dict it
// returns down to the names pdfcpu found in the page's own content stream, and
// pdfcpu never descends into a form's content to do it (its own TODO on
// consolidateResourcesWithContent says so). /Im1 is therefore deleted from the
// page /XObject subdict that becomes Page.Scope, and the form — which has no
// /Resources of its own and so resolves through its parent scope — cannot find
// it. That is silent loss, not a failure: Inspect reports a page with one
// fewer image and no error at all.
//
// Measured on real files: govdocs1/500973.pdf page 1 reported 1 image where
// pdfimages lists 5 (objects 672, 674, 676, 678, 680), and 100877.pdf page 1
// reported 3 where pdfimages lists 4 (the missing one is object 245, 31x22).
// Both now agree with poppler exactly.
func TestPageResourcesAreNotPrunedToTheOwnContentStream(t *testing.T) {
	const form = "q 50 0 0 50 0 0 cm /Im1 Do Q"
	const page = "/Fm1 Do"
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /XObject << /Fm1 5 0 R /Im1 6 0 R >> >> /Contents 4 0 R >>",
		"<< /Length " + itoa(len(page)) + " >>\nstream\n" + page + "\nendstream",
		"<< /Type /XObject /Subtype /Form /BBox [0 0 50 50] /Length " + itoa(len(form)) +
			" >>\nstream\n" + form + "\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 4 /Height 4 /BitsPerComponent 8 " +
			"/ColorSpace /DeviceGray /Filter /ASCIIHexDecode /Length 33 >>\n" +
			"stream\n00112233445566778899aabbccddeeff>\nendstream",
	})

	d, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	fm, ok := d.XObject(p.Scope, "Fm1")
	if !ok {
		t.Fatal("XObject(page, Fm1) not found; the page's own content names it, so " +
			"this fixture is broken rather than the behaviour under test")
	}
	im, ok := d.XObject(fm.Scope, "Im1")
	if !ok {
		t.Fatal("XObject(form, Im1) not found: the page resource dict was pruned to " +
			"the page's own content stream, so the form cannot reach the image it " +
			"paints. This is the silent half of byb-ged.")
	}
	if !im.Image {
		t.Fatalf("XObject(form, Im1).Image = false; want an image XObject")
	}
	info, ok := d.ImageInfo(im.ID)
	if !ok {
		t.Fatalf("ImageInfo(%d) not found", im.ID)
	}
	if info.Width != 4 || info.Height != 4 {
		t.Errorf("ImageInfo = %dx%d; want 4x4 (resolved the wrong object)", info.Width, info.Height)
	}
}

// ImageInfo.Filter is the declaration byb-dng needed to tell an image that is
// ALREADY JBIG2 from one that is merely bitonal, without decoding anything.
//
// The array case is the one that decides whether the number is right. A filter
// array is decode order (ISO 32000-1 section 7.4), so the image codec is the
// LAST entry; reading the first would report /Filter [/ASCII85Decode
// /JBIG2Decode] as ASCII85Decode and score an already-JBIG2 raster as work
// still to do. The absent case has to stay distinguishable too: an unencoded
// stream is not a JBIG2 one.
func TestImageInfoReportsTheDeclaredFilter(t *testing.T) {
	const raw = "<< /Type /XObject /Subtype /Image /Width 4 /Height 4 " +
		"/BitsPerComponent 1 /ColorSpace /DeviceGray "
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /XObject << /Name 4 0 R /Chain 5 0 R /None 6 0 R >> >> >>",
		raw + "/Filter /JBIG2Decode /Length 4 >>\nstream\n\x00\x01\x02\x03\nendstream",
		raw + "/Filter [/ASCII85Decode /JBIG2Decode] /Length 4 >>\nstream\n\x00\x01\x02\x03\nendstream",
		raw + "/Length 2 >>\nstream\n\x00\x0f\nendstream",
	})

	d, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page(1) error = %v", err)
	}
	for _, tc := range []struct{ name, want string }{
		{"Name", "JBIG2Decode"},
		{"Chain", "JBIG2Decode"},
		{"None", ""},
	} {
		xo, ok := d.XObject(p.Scope, tc.name)
		if !ok || !xo.Image {
			t.Fatalf("XObject(%q) not resolved as an image; the fixture is broken, "+
				"not the behaviour under test", tc.name)
		}
		info, ok := d.ImageInfo(xo.ID)
		if !ok {
			t.Fatalf("ImageInfo for %q not found", tc.name)
		}
		if info.Filter != tc.want {
			t.Errorf("ImageInfo(%q).Filter = %q; want %q", tc.name, info.Filter, tc.want)
		}
	}
}

// deflateN returns n zero bytes flate-compressed at max level: a small file
// on disk that decodes to n bytes in memory -- the shape of a font-program
// flate bomb (byb-utl).
func deflateN(t *testing.T, n int) []byte {
	t.Helper()
	var z bytes.Buffer
	w, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		t.Fatalf("zlib.NewWriterLevel: %v", err)
	}
	buf := make([]byte, 1<<20)
	for left := n; left > 0; {
		c := len(buf)
		if left < c {
			c = left
		}
		if _, err := w.Write(buf[:c]); err != nil {
			t.Fatalf("zlib write: %v", err)
		}
		left -= c
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return z.Bytes()
}

// fontBombPDF builds a one-page document whose /FontFile2 decodes to
// decodedBytes -- a flate bomb sized by the caller, so the test can sit both
// under and over maxFontProgramBytes.
func fontBombPDF(t *testing.T, decodedBytes int) []byte {
	t.Helper()
	bomb := deflateN(t, decodedBytes)
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		"<< /Length 16 >>\nstream\nBT /F1 12 Tf ET\nendstream",
		"<< /Type /Font /Subtype /TrueType /BaseFont /Recon /FirstChar 0 /LastChar 0 /Widths [500] /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /Recon /Flags 4 /ItalicAngle 0 /Ascent 700 /Descent -200 /CapHeight 700 /StemV 80 /FontBBox [0 0 1000 1000] /FontFile2 7 0 R >>",
		fmt.Sprintf("<< /Length %d /Length1 %d /Filter /FlateDecode >>\nstream\n%s\nendstream",
			len(bomb), decodedBytes, bomb),
	})
}

// TestRenderFontBoundsDecodedProgramSize pins byb-utl: a /FontFile2 whose
// declared /Length1 and whose actual flate-bomb decode both claim far more
// than maxFontProgramBytes must not make renderFont allocate anywhere near
// that much. TotalAlloc's ceiling here is generous (8x the budget) so the
// assertion is not flaky against GC/runtime overhead; reverting the guard
// (renderFont's sd.DecodeWithLimit back to sd.Decode) pushes the real delta
// to 512 MiB and this fails hard, not marginally.
func TestRenderFontBoundsDecodedProgramSize(t *testing.T) {
	const decoded = 128 << 20 // 128 MiB, far past maxFontProgramBytes (16 MiB)
	pdf := fontBombPDF(t, decoded)

	d, err := Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	rf, ok := d.RenderFont(p.Scope, "F1")
	runtime.ReadMemStats(&m1)

	if !ok {
		t.Fatal("RenderFont ok=false; a bad font program must degrade, not fail resolution")
	}
	if len(rf.Program) > maxFontProgramBytes {
		t.Errorf("len(Program) = %d; want <= maxFontProgramBytes (%d)", len(rf.Program), maxFontProgramBytes)
	}
	const ceiling = 8 * maxFontProgramBytes // generous: real unguarded delta is 512 MiB
	if delta := int64(m1.TotalAlloc - m0.TotalAlloc); delta > ceiling {
		t.Errorf("TotalAlloc delta = %d bytes (%.2f MiB); want <= %d bytes (%.2f MiB)",
			delta, float64(delta)/(1<<20), ceiling, float64(ceiling)/(1<<20))
	}
}

// aliasedFontPDF builds a one-page document whose /Resources /Font dict names
// n resource names (F0..Fn-1), all indirect references to the SAME font dict
// object -- the byb-rq3 shape: many Tf names aliasing one dict. That font
// dict's /FontDescriptor carries a /FontFile2 flate stream that decodes to
// decodedBytes.
func aliasedFontPDF(t *testing.T, n, decodedBytes int) []byte {
	t.Helper()
	bomb := deflateN(t, decodedBytes)
	var names strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&names, "/F%d 5 0 R ", i)
	}
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << %s>> >> /Contents 4 0 R >>", names.String()),
		"<< /Length 0 >>\nstream\n\nendstream",
		"<< /Type /Font /Subtype /TrueType /BaseFont /Recon /FirstChar 0 /LastChar 0 /Widths [500] /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /Recon /Flags 4 /ItalicAngle 0 /Ascent 700 /Descent -200 /CapHeight 700 /StemV 80 /FontBBox [0 0 1000 1000] /FontFile2 7 0 R >>",
		fmt.Sprintf("<< /Length %d /Length1 %d /Filter /FlateDecode >>\nstream\n%s\nendstream",
			len(bomb), decodedBytes, bomb),
	})
}

// TestRenderFontCachesByFontObject pins the third fact byb-utl and byb-rq3
// share: pdfdoc has no cache of its own, so a resource dict aliasing one
// font object under many Tf names (byb-rq3's exact shape) made every one of
// those names re-run sd.DecodeWithLimit's full decode. render.resolveFont's
// own budget cannot see this: a font with no /Widths or /W charges 0 bytes
// per name, so nothing on the caller side stopped it. RenderFont now
// memoizes by the font dict's object identity, so N names resolving to the
// SAME dict decode once, not N times.
//
// TotalAlloc's ceiling is generous (8x one decode) so the assertion is not
// flaky; reverting the cache (RenderFont calling d.renderFont directly)
// pushes the real delta to roughly 50x one decode and this fails hard.
func TestRenderFontCachesByFontObject(t *testing.T) {
	const decoded = 8 << 20 // 8 MiB: under maxFontProgramBytes, so every call succeeds
	const names = 50        // census MAX Tf names/page
	pdf := aliasedFontPDF(t, names, decoded)

	d, err := Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	for i := 0; i < names; i++ {
		rf, ok := d.RenderFont(p.Scope, fmt.Sprintf("F%d", i))
		if !ok {
			t.Fatalf("RenderFont(F%d) ok=false", i)
		}
		if len(rf.Program) != decoded {
			t.Fatalf("RenderFont(F%d) len(Program) = %d; want %d", i, len(rf.Program), decoded)
		}
	}
	runtime.ReadMemStats(&m1)

	const ceiling = 8 * decoded // generous: N re-decodes would cost ~50x this
	if delta := int64(m1.TotalAlloc - m0.TotalAlloc); delta > ceiling {
		t.Errorf("TotalAlloc delta over %d aliased names = %d bytes (%.2f MiB); want <= %d bytes (%.2f MiB) -- looks like every name re-decoded instead of hitting the font-object cache",
			names, delta, float64(delta)/(1<<20), ceiling, float64(ceiling)/(1<<20))
	}
}

// unfilteredFontPDF builds a one-page document whose /FontFile2 carries n
// literal bytes and NO /Filter -- the shape pdfcpu's own
// DecodeLengthWithLimit short-circuits (types/streamdict.go: "no filter ...
// nothing to decode"), returning sd.Raw untouched with maxDecodeBytes never
// consulted.
func unfilteredFontPDF(t *testing.T, n int) []byte {
	t.Helper()
	raw := bytes.Repeat([]byte{0x41}, n)
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
		"<< /Type /Font /Subtype /TrueType /BaseFont /Recon /FirstChar 0 /LastChar 0 /Widths [500] /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /Recon /Flags 4 /ItalicAngle 0 /Ascent 700 /Descent -200 /CapHeight 700 /StemV 80 /FontBBox [0 0 1000 1000] /FontFile2 7 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", n, raw),
	})
}

// TestRenderFontBoundsUnfilteredProgramSize pins the gap byb-utl's own
// DecodeWithLimit does not close: it is enforced inside pdfcpu's filter
// stages (types.StreamDict.decodeLength), so a stream with NO /Filter
// returns sd.Raw untouched, maxDecodeBytes never consulted
// (DecodeLengthWithLimit's "nothing to decode" short-circuit). A literal,
// uncompressed /FontFile2 bigger than maxFontProgramBytes must still be
// refused, not admitted 1:1.
func TestRenderFontBoundsUnfilteredProgramSize(t *testing.T) {
	pdf := unfilteredFontPDF(t, maxFontProgramBytes+1)

	d, err := Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}

	rf, ok := d.RenderFont(p.Scope, "F1")
	if !ok {
		t.Fatal("RenderFont ok=false; an oversized program must degrade, not fail resolution")
	}
	if len(rf.Program) > maxFontProgramBytes {
		t.Errorf("len(Program) = %d; want <= maxFontProgramBytes (%d), an unfiltered stream must be bounded too", len(rf.Program), maxFontProgramBytes)
	}
}
