package pdfdoc

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// booklet returns a fresh reader over the eight-page corpus booklet. Fresh per
// call: a PageSource holds an io.ReadSeeker, and two tests sharing one would
// seek each other apart.
func booklet(t *testing.T) io.ReadSeeker {
	t.Helper()
	b, ok := corpus.ByName("booklet")
	if !ok {
		t.Fatal("the corpus has no booklet document")
	}
	return bytes.NewReader(b)
}

func mixedDoc(t *testing.T) io.ReadSeeker {
	t.Helper()
	b, ok := corpus.ByName("mixed")
	if !ok {
		t.Fatal("the corpus has no mixed document")
	}
	return bytes.NewReader(b)
}

// inheritedCropBoxDoc is a two-page document whose /Pages node carries the
// /CropBox on behalf of both leaves, which ISO 32000-1 table 30 permits and no
// corpus document exercises. Page 1 paints a rectangle so it is not empty.
//
// It is written by hand rather than assembled through this package, because the
// shape under test is one nothing in this package produces.
func inheritedCropBoxDoc(llx, lly, urx, ury int) []byte {
	body := "0 0 1 rg 120 120 100 100 re f\n"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2"+
			" /MediaBox [0 0 612 792] /CropBox [%d %d %d %d] >>", llx, lly, urx, ury),
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
		"<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
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

// linkToPageTwoDoc is a two-page document whose page 1 carries a /Link
// annotation destinationing page 2. No corpus document has one, and it is the
// shape that makes a page reachable from something other than the page tree.
func linkToPageTwoDoc() []byte {
	one := "0 0 1 rg 10 10 50 50 re f\n"
	// The marker is a content-stream COMMENT, so page 2 needs no font resource
	// and the document validates. A /F1 with no /Resources /Font entry is
	// refused by pdfcpu's validator, and that would fail the fixture rather
	// than the behaviour under test.
	two := "%PAGETWOMARKER\n0 1 0 rg 20 20 30 30 re f\n"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [7 0 R] >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(one), one),
		"<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(two), two),
		"<< /Type /Annot /Subtype /Link /Rect [10 10 60 60] /Border [0 0 0]" +
			" /Dest [5 0 R /Fit] >>",
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

// titledDoc is a two-page document carrying an information dictionary.
func titledDoc(title string) []byte {
	one := "0 0 1 rg 10 10 50 50 re f\n"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(one), one),
		"<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
		fmt.Sprintf("<< /Title (%s) /Author (Ada) /Subject (Subj) /Keywords (kw)"+
			" /Producer (fixture) >>", title),
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
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, len(objs), start)
	return buf.Bytes()
}

// infoOf returns the built document's information dictionary entries.
func infoOf(t *testing.T, out []byte) types.Dict {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(out), defaultConfig())
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if ctx.XRefTable.Info == nil {
		return nil
	}
	d, err := ctx.XRefTable.DereferenceDict(*ctx.XRefTable.Info)
	if err != nil {
		t.Fatalf("/Info: %v", err)
	}
	return d
}

// TestBuildFromPagesCarriesTheInfoDictOfASingleSource covers the entries that
// describe the DOCUMENT rather than its pages. byblos stores provenance in the
// information dictionary (provenance.go:284, :324), and pdfcpu's fresh context
// sets xRefTable.Info = nil, so a build that said nothing here would silently
// discard the title, the author and the provenance of every export.
func TestBuildFromPagesCarriesTheInfoDictOfASingleSource(t *testing.T) {
	// ONE reader, named twice. That is how a caller says "these two pages come
	// from the same document"; two readers over identical bytes are two sources
	// as far as this API is concerned, and the next test relies on that.
	src := bytes.NewReader(titledDoc("A Title"))
	_, out := build(t, []PageSource{
		{Source: src, Page: 2},
		{Source: src, Page: 1},
	})
	info := infoOf(t, out)
	for key, want := range map[string]string{
		"Title": "A Title", "Author": "Ada", "Subject": "Subj", "Keywords": "kw",
	} {
		v, ok := info[key].(types.StringLiteral)
		if !ok {
			t.Errorf("the built document's /Info has no /%s", key)
			continue
		}
		if v.Value() != want {
			t.Errorf("/Info /%s is %q, want %q", key, v.Value(), want)
		}
	}
	// /Producer names what WROTE these bytes, and that is byblos, not the
	// source's producer. Carrying it would misattribute the file.
	if v, ok := info["Producer"].(types.StringLiteral); ok && v.Value() == "fixture" {
		t.Error("/Producer was carried from the source; it describes the writer, not the content")
	}
}

// TestBuildFromPagesDropsTheInfoDictWhenSourcesDisagree is the other half. With
// two sources there is no defensible answer to "whose title is this", and
// picking the first page's source would make the answer depend on the ORDER --
// so moving an imported page to the front would silently change the document's
// identity. Nothing is carried instead.
func TestBuildFromPagesDropsTheInfoDictWhenSourcesDisagree(t *testing.T) {
	a, b := titledDoc("First"), titledDoc("Second")
	_, out := build(t, []PageSource{
		{Source: bytes.NewReader(a), Page: 1},
		{Source: bytes.NewReader(b), Page: 1},
	})
	info := infoOf(t, out)
	if v, ok := info["Title"].(types.StringLiteral); ok {
		t.Errorf("the built document claims /Title %q, but its pages come from two documents", v.Value())
	}
}

// build runs BuildFromPages and returns a Doc over what it wrote, having first
// put the bytes through pdfcpu's validator.
//
// The validator is not decoration. This package's writer emits a page tree it
// assembled itself, and the failure it is most likely to produce -- a /Kids
// entry or a /Count that disagrees with what was written -- is one a reader
// tolerates and a validator names.
func build(t *testing.T, pages []PageSource) (Doc, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := BuildFromPages(&buf, pages); err != nil {
		t.Fatalf("BuildFromPages: %v", err)
	}
	out := buf.Bytes()
	if err := Validate(bytes.NewReader(out)); err != nil {
		t.Fatalf("the built document does not validate: %v", err)
	}
	assertNoDanglingRefs(t, out)
	d, err := Open(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("reopening the built document: %v", err)
	}
	return d, out
}

// assertNoDanglingRefs fails if any object in the written document names an
// object number the document does not define.
//
// It runs on EVERY build, because a dangling reference is the failure mode this
// writer is most likely to produce and the one least likely to be noticed. The
// migration walk reserves an output object number the moment it reaches a
// reference, so a reference it followed into material the writer then declined
// to write leaves a number behind with nothing at it. pdfcpu's own validator
// accepts such a file: ISO 32000-1 7.3.10 makes an undefined reference the null
// object, so a reader silently gets nothing where a destination or a resource
// should be.
func assertNoDanglingRefs(t *testing.T, out []byte) {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(out), defaultConfig())
	if err != nil {
		t.Fatalf("re-reading the built document: %v", err)
	}
	xt := ctx.XRefTable
	defined := map[int]bool{}
	for objNr, e := range xt.Table {
		if e != nil && !e.Free {
			defined[objNr] = true
		}
	}
	for objNr := range defined {
		if objNr == 0 {
			continue
		}
		o, err := xt.Dereference(types.IndirectRef{ObjectNumber: types.Integer(objNr)})
		if err != nil || o == nil {
			continue
		}
		for _, target := range refsOf(o) {
			if !defined[target] {
				t.Errorf("object %d references object %d, which the document does not define",
					objNr, target)
			}
		}
	}
}

// pageMarkers reports which booklet pages a built document's page n holds text
// from. Every booklet page paints "page N line 0" in rendering mode 3, so the
// marker identifies the SOURCE page independently of where it landed.
func pageMarkers(t *testing.T, d Doc, n int) []string {
	t.Helper()
	p, err := d.Page(n)
	if err != nil {
		t.Fatalf("page %d: %v", n, err)
	}
	var out []string
	for i := 1; i <= corpus.BookletPages; i++ {
		if bytes.Contains(p.Content, fmt.Appendf(nil, "page %d line 0", i)) {
			out = append(out, fmt.Sprintf("p%d", i))
		}
	}
	return out
}

// TestBuildFromPagesTakesOnePage is the smallest whole build: one page, one
// source, no reordering. It is still the full object-graph migration -- the page
// dict, its content stream, its raster and its font all have to arrive.
func TestBuildFromPagesTakesOnePage(t *testing.T) {
	d, out := build(t, []PageSource{{Source: booklet(t), Page: 3}})

	if got := d.PageCount(); got != 1 {
		t.Fatalf("built document has %d pages, want 1", got)
	}
	if got := pageMarkers(t, d, 1); len(got) != 1 || got[0] != "p3" {
		t.Errorf("page 1 holds text from %v, want [p3]", got)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if p.MediaBox.URX != corpus.PageWidthPt || p.MediaBox.URY != corpus.PageHeightPt {
		t.Errorf("page 1 MediaBox is %+v, want %gx%g", p.MediaBox,
			float64(corpus.PageWidthPt), float64(corpus.PageHeightPt))
	}
	// The page's raster must have come across too, resolvable through the
	// page's own resource scope.
	if _, ok := d.XObject(p.Scope, "Im0"); !ok {
		t.Error("page 1 cannot resolve its /Im0 raster: the image XObject did not migrate")
	}
	t.Logf("one page of the eight-page booklet: %d bytes", len(out))
}

// TestBuildFromPagesReordersAndDeletes is the whole edit vocabulary against one
// source: delete is omitting a page, reorder is ordering the sequence, and a
// page named twice appears twice.
func TestBuildFromPagesReordersAndDeletes(t *testing.T) {
	src := booklet(t)
	d, _ := build(t, []PageSource{
		{Source: src, Page: 8},
		{Source: src, Page: 2},
		{Source: src, Page: 8},
	})
	if got := d.PageCount(); got != 3 {
		t.Fatalf("built document has %d pages, want 3", got)
	}
	want := [][]string{{"p8"}, {"p2"}, {"p8"}}
	for i, w := range want {
		if got := pageMarkers(t, d, i+1); len(got) != 1 || got[0] != w[0] {
			t.Errorf("page %d holds text from %v, want %v", i+1, got, w)
		}
	}
}

// TestBuildFromPagesImportsFromASecondSource is the operation no pdfcpu v0.13.0
// entry point offers at all: page N of document B at position k of document A.
func TestBuildFromPagesImportsFromASecondSource(t *testing.T) {
	// Booklet page 1 is deliberately not used here: it is the one page of that
	// fixture that paints no text at all, so it could not tell an imported page
	// apart from a booklet page.
	book, other := booklet(t), mixedDoc(t)
	d, _ := build(t, []PageSource{
		{Source: book, Page: 3},
		{Source: other, Page: 2},
		{Source: book, Page: 5},
	})
	if got := d.PageCount(); got != 3 {
		t.Fatalf("built document has %d pages, want 3", got)
	}
	if got := pageMarkers(t, d, 1); len(got) != 1 || got[0] != "p3" {
		t.Errorf("page 1 holds text from %v, want [p3]", got)
	}
	if got := pageMarkers(t, d, 3); len(got) != 1 || got[0] != "p5" {
		t.Errorf("page 3 holds text from %v, want [p5]", got)
	}
	// The imported page is the 'mixed' document's scan page, so it must resolve
	// its own raster through its own resources rather than the booklet's.
	p, err := d.Page(2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(pageMarkers(t, d, 2)) != 0 {
		t.Errorf("page 2 holds booklet text; the imported page took the wrong content stream")
	}
	if _, ok := d.XObject(p.Scope, "Im0"); !ok {
		t.Error("the imported page cannot resolve its raster: its resources did not migrate")
	}
}

// TestBuildFromPagesAppliesAbsoluteRotation pins that Rotate REPLACES the source
// page's own value and is never added to it. Kleio redelivers a job at least
// once (ocr.go:55-60), so an additive rotation would turn a retry into a defect
// that compounds on each delivery.
func TestBuildFromPagesAppliesAbsoluteRotation(t *testing.T) {
	rotated, ok := corpus.ByName("scan-rotated") // one page, declaring /Rotate 90
	if !ok {
		t.Fatal("the corpus has no scan-rotated document")
	}
	for _, want := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprintf("rotate %d", want), func(t *testing.T) {
			d, _ := build(t, []PageSource{
				{Source: bytes.NewReader(rotated), Page: 1, Rotate: want},
			})
			p, err := d.Page(1)
			if err != nil {
				t.Fatalf("page 1: %v", err)
			}
			if p.Rotate != want {
				t.Errorf("built page reports /Rotate %d, want %d "+
					"(the source declares 90, so an additive rotation would give %d)",
					p.Rotate, want, (90+want)%360)
			}
		})
	}
}

// TestBuildFromPagesPushesAnInheritedCropBoxDown is the loss pdfcpu's own path
// takes and byblos must not. A /Pages node may legally carry /CropBox on behalf
// of its descendants; the output's page tree is a NEW node that carries nothing,
// so an attribute left on the old node is simply gone.
//
// MEASURED against pdfcpu: on this shape api.Collect reports (0,0)-(612,792)
// for a page byblos reads as (100,100)-(300,400). addPages bakes Resources,
// Parent, MediaBox and conditionally Rotate onto the migrated leaf, and never
// CropBox.
func TestBuildFromPagesPushesAnInheritedCropBoxDown(t *testing.T) {
	data := inheritedCropBoxDoc(100, 100, 300, 400)
	in, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the fixture does not open: %v", err)
	}
	before, err := in.Page(1)
	if err != nil {
		t.Fatalf("fixture page 1: %v", err)
	}
	if before.CropBox != (Rect{100, 100, 300, 400}) {
		t.Fatalf("the fixture does not exercise an inherited /CropBox: page 1 reads %+v", before.CropBox)
	}

	d, _ := build(t, []PageSource{{Source: bytes.NewReader(data), Page: 1}})
	after, err := d.Page(1)
	if err != nil {
		t.Fatalf("built page 1: %v", err)
	}
	if after.CropBox != before.CropBox {
		t.Errorf("built page CropBox is %+v, want %+v: the inherited attribute was not pushed down",
			after.CropBox, before.CropBox)
	}
}

// countObjects reports how many objects of the written document satisfy match.
// It reads the bytes back rather than inspecting the builder's own state, so a
// migration that produced an object the writer then dropped is counted honestly.
func countObjects(t *testing.T, out []byte, match func(types.Dict) bool) int {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(out), defaultConfig())
	if err != nil {
		t.Fatalf("re-reading the built document: %v", err)
	}
	xt := ctx.XRefTable
	n := 0
	for objNr, e := range xt.Table {
		if objNr == 0 || e == nil || e.Free {
			continue
		}
		o, err := xt.Dereference(types.IndirectRef{ObjectNumber: types.Integer(objNr)})
		if err != nil || o == nil {
			continue
		}
		var d types.Dict
		switch v := o.(type) {
		case types.Dict:
			d = v
		case types.StreamDict:
			d = v.Dict
		case *types.StreamDict:
			d = v.Dict
		default:
			continue
		}
		if match(d) {
			n++
		}
	}
	return n
}

func isType(name string) func(types.Dict) bool {
	return func(d types.Dict) bool {
		t := d.Type()
		return t != nil && *t == name
	}
}

func isImage(d types.Dict) bool {
	s := d.Subtype()
	return s != nil && *s == "Image"
}

// TestBuildFromPagesMigratesASharedResourceOnce is the reason sources are opened
// once and objects are memoized by (source, object number). Booklet pages 2
// upward share ONE body font object; migrating it per page would multiply every
// shared font, raster and colour space by the number of pages that use it, which
// is exactly the duplication that made the reject per-page split cost up to
// 12.82x.
func TestBuildFromPagesMigratesASharedResourceOnce(t *testing.T) {
	src := booklet(t)
	_, out := build(t, []PageSource{
		{Source: src, Page: 2},
		{Source: src, Page: 3},
		{Source: src, Page: 4},
	})
	if got := countObjects(t, out, isType("Font")); got != 1 {
		t.Errorf("the built document holds %d font objects, want 1: "+
			"pages 2, 3 and 4 all name the same body font", got)
	}
	// And the rasters are genuinely distinct, so the count above is dedup and
	// not a migration that lost objects.
	if got := countObjects(t, out, isImage); got != 3 {
		t.Errorf("the built document holds %d image objects, want 3", got)
	}
}

// TestBuildFromPagesTakingOnePageTwiceSharesItsContent is the same memo seen
// from the other side: the page DICTIONARY is duplicated, because two entries
// are two pages and may carry two rotations, but everything under it is shared.
func TestBuildFromPagesTakingOnePageTwiceSharesItsContent(t *testing.T) {
	src := booklet(t)
	_, out := build(t, []PageSource{
		{Source: src, Page: 4},
		{Source: src, Page: 4, Rotate: 90},
	})
	if got := countObjects(t, out, isType("Page")); got != 2 {
		t.Errorf("the built document holds %d page dictionaries, want 2", got)
	}
	if got := countObjects(t, out, isImage); got != 1 {
		t.Errorf("the built document holds %d image objects, want 1: "+
			"both pages are the same source page and share its raster", got)
	}
}

// TestBuildFromPagesDoesNotResurrectAPageThroughAnAnnotation is the trap that
// makes /Parent not the only back-reference worth worrying about. A link
// annotation's /Dest names a PAGE, so migrating a page's annotations naively
// pulls the destination page's dictionary, its content stream and its resources
// into a document that has no room for them -- an invisible copy of the page the
// caller deleted.
func TestBuildFromPagesDoesNotResurrectAPageThroughAnAnnotation(t *testing.T) {
	data := linkToPageTwoDoc()
	in, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the fixture does not open: %v", err)
	}
	annots, err := in.Annots(1)
	if err != nil || len(annots) != 1 {
		t.Fatalf("the fixture does not exercise a link annotation: %d annots, err %v",
			len(annots), err)
	}

	_, out := build(t, []PageSource{{Source: bytes.NewReader(data), Page: 1}})
	if got := countObjects(t, out, isType("Page")); got != 1 {
		t.Errorf("the built document holds %d page dictionaries, want 1: "+
			"the link annotation dragged its destination page back in", got)
	}
	if bytes.Contains(out, []byte("PAGETWOMARKER")) {
		t.Error("page two's content stream is in the built document")
	}
}

// TestBuildFromPagesKeepsALinkBetweenTwoExportedPages is the other half of the
// rule. Stopping the walk at a page must not mean severing every link: when the
// destination page IS in the build, the annotation has to name the output page
// it became, not the source page it came from.
func TestBuildFromPagesKeepsALinkBetweenTwoExportedPages(t *testing.T) {
	data := linkToPageTwoDoc()
	src := bytes.NewReader(data)
	// Reversed on purpose, so a destination that was merely copied through
	// would land on the wrong page.
	d, out := build(t, []PageSource{{Source: src, Page: 2}, {Source: src, Page: 1}})

	annots, err := d.Annots(2)
	if err != nil {
		t.Fatalf("built page 2 annots: %v", err)
	}
	if len(annots) != 1 {
		t.Fatalf("built page 2 has %d annotations, want 1", len(annots))
	}
	if got := countObjects(t, out, isType("Page")); got != 2 {
		t.Fatalf("the built document holds %d page dictionaries, want 2", got)
	}

	ctx, err := api.ReadContext(bytes.NewReader(out), defaultConfig())
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	xt := ctx.XRefTable
	kids, err := xt.DereferenceArray(mustPagesDict(t, xt)["Kids"])
	if err != nil {
		t.Fatalf("/Kids: %v", err)
	}
	wantPage1, _ := indirectRefOf(kids[0]) // the output's FIRST page, which is source page 2

	dest := destOfFirstAnnot(t, xt, kids[1])
	if len(dest) == 0 {
		t.Fatal("the migrated annotation lost its /Dest entirely")
	}
	got, ok := indirectRefOf(dest[0])
	if !ok {
		t.Fatalf("the migrated /Dest names %T, not a page", dest[0])
	}
	if got.ObjectNumber != wantPage1.ObjectNumber {
		t.Errorf("the migrated /Dest names object %v; the exported destination page is %v",
			got.ObjectNumber, wantPage1.ObjectNumber)
	}
}

func mustPagesDict(t *testing.T, xt *model.XRefTable) types.Dict {
	t.Helper()
	root, err := xt.Pages()
	if err != nil || root == nil {
		t.Fatalf("page tree: %v", err)
	}
	d, err := xt.DereferenceDict(*root)
	if err != nil || d == nil {
		t.Fatalf("page tree dict: %v", err)
	}
	return d
}

func destOfFirstAnnot(t *testing.T, xt *model.XRefTable, pageRef types.Object) types.Array {
	t.Helper()
	pd, err := xt.DereferenceDict(pageRef)
	if err != nil || pd == nil {
		t.Fatalf("page dict: %v", err)
	}
	annots, err := xt.DereferenceArray(pd["Annots"])
	if err != nil || len(annots) == 0 {
		t.Fatalf("the built page carries no annotations: %v", err)
	}
	ad, err := xt.DereferenceDict(annots[0])
	if err != nil || ad == nil {
		t.Fatalf("annotation dict: %v", err)
	}
	dest, err := xt.DereferenceArray(ad["Dest"])
	if err != nil {
		t.Fatalf("/Dest: %v", err)
	}
	return dest
}

// TestBuildFromPagesLeavesTheSourceUnmodified pins that a build is a read of its
// sources. The migration pushes inherited attributes down on the SOURCE context
// to do its work, which mutates parsed dictionaries in memory; nothing may reach
// the caller's bytes.
func TestBuildFromPagesLeavesTheSourceUnmodified(t *testing.T) {
	original, ok := corpus.ByName("booklet")
	if !ok {
		t.Fatal("the corpus has no booklet document")
	}
	given := make([]byte, len(original))
	copy(given, original)

	src := bytes.NewReader(given)
	if _, out := build(t, []PageSource{{Source: src, Page: 2}}); len(out) == 0 {
		t.Fatal("the build wrote nothing")
	}
	if !bytes.Equal(given, original) {
		t.Error("BuildFromPages modified the bytes behind its source")
	}
}

// TestBuildFromPagesRefusesAnEncryptedSource is the refusal Linearize already
// makes for the same reason (linearize.go:75-81), and it is not a nicety.
// pdfcpu decrypts strings and streams on READ, so a migration carries plaintext
// into an output that has no /Encrypt dictionary -- silently stripping the
// encryption from a document somebody chose to encrypt. Re-emitting under the
// source's /Encrypt would instead produce a file that opens as garbage. Neither
// is byblos's call to make without being asked.
func TestBuildFromPagesRefusesAnEncryptedSource(t *testing.T) {
	// An EMPTY user password, which is the case that matters. A document
	// encrypted with a user password is refused earlier, by Open, because
	// pdfcpu cannot read it at all -- so it was never the hazard. The common
	// shape is owner-password-only: the document opens with no password and
	// still carries an /Encrypt dictionary stating what may be done with it.
	conf := defaultConfig()
	conf.UserPW, conf.OwnerPW = "", "o"
	var enc bytes.Buffer
	if err := api.Encrypt(bytes.NewReader(corpusMust(t, "mixed")), &enc, conf); err != nil {
		t.Fatalf("building the encrypted fixture: %v", err)
	}
	// The fixture has to be encrypted AND openable without a password, or this
	// proves nothing about the path under test.
	ctx, err := api.ReadContext(bytes.NewReader(enc.Bytes()), defaultConfig())
	if err != nil {
		t.Fatalf("the fixture does not open without a password: %v", err)
	}
	if ctx.XRefTable.Encrypt == nil {
		t.Fatal("the fixture carries no /Encrypt dictionary, so it is not encrypted")
	}

	var buf bytes.Buffer
	err = BuildFromPages(&buf, []PageSource{{Source: bytes.NewReader(enc.Bytes()), Page: 1}})
	if err == nil {
		t.Fatalf("BuildFromPages accepted an encrypted source and wrote %d bytes", buf.Len())
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error %q does not say the document is encrypted", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused build wrote %d bytes", buf.Len())
	}
}

func corpusMust(t *testing.T, name string) []byte {
	t.Helper()
	b, ok := corpus.ByName(name)
	if !ok {
		t.Fatalf("the corpus has no %q document", name)
	}
	return b
}

// TestBuildFromPagesRefusals covers the inputs that cannot produce a document a
// reader would accept. Every one of them is a caller mistake that pdfcpu itself
// accepts silently: api.InsertPages writes /Rotate 45 with a nil error and then
// refuses the file it wrote on re-read.
func TestBuildFromPagesRefusals(t *testing.T) {
	tests := []struct {
		name  string
		pages func(t *testing.T) []PageSource
		want  string
	}{
		{
			name:  "an empty page sequence",
			pages: func(*testing.T) []PageSource { return nil },
			want:  "no pages",
		},
		{
			name: "a page below the source's range",
			pages: func(t *testing.T) []PageSource {
				return []PageSource{{Source: booklet(t), Page: 0}}
			},
			want: "out of range",
		},
		{
			name: "a page past the source's range",
			pages: func(t *testing.T) []PageSource {
				return []PageSource{{Source: booklet(t), Page: 9}}
			},
			want: "out of range",
		},
		{
			name: "a negative rotation, which PageInfo.Rotate would have normalized away",
			pages: func(t *testing.T) []PageSource {
				return []PageSource{{Source: booklet(t), Page: 1, Rotate: -90}}
			},
			want: "rotation",
		},
		{
			name: "a rotation that is not a quarter turn",
			pages: func(t *testing.T) []PageSource {
				return []PageSource{{Source: booklet(t), Page: 1, Rotate: 45}}
			},
			want: "rotation",
		},
		{
			name: "a rotation of 360, which is a quarter-turn multiple and still not one of the four",
			pages: func(t *testing.T) []PageSource {
				return []PageSource{{Source: booklet(t), Page: 1, Rotate: 360}}
			},
			want: "rotation",
		},
		{
			name: "a nil source",
			pages: func(t *testing.T) []PageSource {
				return []PageSource{{Source: nil, Page: 1}}
			},
			want: "no source",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := BuildFromPages(&buf, tc.pages(t))
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

// TestBuildFromPagesRefusesAPageFromAnUnreadableSource keeps the refusal at the
// source rather than letting a half-built document reach the writer.
func TestBuildFromPagesRefusesAPageFromAnUnreadableSource(t *testing.T) {
	bad, ok := corpus.ByName("malformed")
	if !ok {
		t.Fatal("the corpus has no malformed document")
	}
	var buf bytes.Buffer
	err := BuildFromPages(&buf, []PageSource{
		{Source: booklet(t), Page: 1},
		{Source: bytes.NewReader(bad), Page: 1},
	})
	if err == nil {
		t.Fatalf("BuildFromPages accepted a malformed source and wrote %d bytes", buf.Len())
	}
	if buf.Len() != 0 {
		t.Errorf("a refused build wrote %d bytes; it must write none", buf.Len())
	}
}
