package pdfdoc

import (
	"bytes"
	"errors"
	"image"
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
	if s, err := content.Walk(sp.Content, sp.Scope, scan); err != nil {
		t.Fatalf("Walk() error = %v", err)
	} else if len(s.Images) != 1 || !s.Images[0].Opaque {
		t.Errorf("Images = %+v; want one opaque placement", s.Images)
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
