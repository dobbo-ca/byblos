package pdfdoc

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The corpus documents carry one annotation and it is a hidden, zero-area
// /Square with no /AP, so they exercise none of the reads below. These build
// PDFs literally instead: every case here is a way a dictionary read can be
// wrong, and most of them fail in the direction that turns an annotation which
// paints into one that does not, which is silent data loss rather than a
// visible error.

// annotPDF builds a one-page document whose /Annots entry is annotsEntry and
// whose extra objects start at object 5. The page is 612x792 with no content.
func annotPDF(annotsEntry string, extra ...string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources 4 0 R /Annots %s >>", annotsEntry),
		"<< >>",
	}
	return buildPDF(append(objs, extra...))
}

// buildPDF writes objects 1..len(bodies) with a correct xref table. A body may
// contain "stream"/"endstream"; its /Length must already be right.
func buildPDF(bodies []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(bodies))
	for i, b := range bodies {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, b)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(bodies)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(bodies)+1, start)
	return buf.Bytes()
}

// formStream is a minimal appearance stream: a form XObject painting nothing.
// Whether it deposits ink is a renderer's question and out of scope; that it
// resolves to a stream is the whole test.
const formStream = "<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Length 0 >>\nstream\n\nendstream"

func annotsOf(t *testing.T, data []byte) []Annot {
	t.Helper()
	d, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	a, err := d.Annots(1)
	if err != nil {
		t.Fatalf("Annots(1) error = %v", err)
	}
	return a
}

// An indirect /Annots is legal and types.Dict.ArrayEntry reports it as absent.
// That is the same trap normalizePageTree exists to repair for /Kids, and here
// it would report a page carrying a stamp as carrying nothing at all.
func TestAnnotsReadsAnIndirectArray(t *testing.T) {
	data := annotPDF("5 0 R",
		"[6 0 R]",
		"<< /Type /Annot /Subtype /Stamp /Rect [10 10 100 100] /AP << /N 7 0 R >> >>",
		formStream)

	got := annotsOf(t, data)
	if len(got) != 1 {
		t.Fatalf("got %d annotations through an indirect /Annots; want 1", len(got))
	}
	if !got[0].HasAPN {
		t.Errorf("HasAPN = false; want true")
	}
	if !got[0].Paints() {
		t.Errorf("Paints() = false (%s); want true", got[0].Reason())
	}
}

// An indirect /F read with types.Dict.IntEntry comes back 0, which clears the
// Hidden bit and turns an annotation a viewer never draws into one this
// measurement counts as painting. That is the wrong direction: it inflates the
// headline with ink that does not exist.
func TestAnnotsReadsAnIndirectFlagsEntry(t *testing.T) {
	data := annotPDF("[5 0 R]",
		"<< /Type /Annot /Subtype /Stamp /Rect [10 10 100 100] /F 6 0 R /AP << /N 7 0 R >> >>",
		"2",
		formStream)

	got := annotsOf(t, data)
	if len(got) != 1 {
		t.Fatalf("got %d annotations; want 1", len(got))
	}
	if got[0].Flags != 2 {
		t.Errorf("Flags = %d through an indirect /F; want 2", got[0].Flags)
	}
	if r := got[0].Reason(); r != "hidden" {
		t.Errorf("Reason() = %q; want %q", r, "hidden")
	}
}

// An object listed in /Annots but free in the xref dereferences to a nil
// dictionary and a nil error, so checking err alone walks into a nil map.
func TestAnnotsSkipsADanglingReference(t *testing.T) {
	data := annotPDF("[99 0 R 5 0 R]",
		"<< /Type /Annot /Subtype /Stamp /Rect [10 10 100 100] /AP << /N 6 0 R >> >>",
		formStream)

	got := annotsOf(t, data)
	if len(got) != 1 {
		t.Fatalf("got %d annotations past a dangling reference; want 1", len(got))
	}
	if !got[0].Paints() {
		t.Errorf("Paints() = false (%s); want true", got[0].Reason())
	}
}

// pdfcpu's RectForArray indexes a[0]..a[3] with no length check, so a short
// /Rect panics rather than diverting. A measurement that dies on one malformed
// page in a 5,000-file archive is not a measurement.
func TestAnnotsMalformedRectDoesNotPanic(t *testing.T) {
	for _, rect := range []string{"[10 10]", "[]", "[10 10 100]", "(not an array)"} {
		t.Run(rect, func(t *testing.T) {
			data := annotPDF("[5 0 R]",
				fmt.Sprintf("<< /Type /Annot /Subtype /Stamp /Rect %s /AP << /N 6 0 R >> >>", rect),
				formStream)

			got := annotsOf(t, data)
			if len(got) != 1 {
				t.Fatalf("got %d annotations; want 1", len(got))
			}
			if got[0].HasRect {
				t.Errorf("HasRect = true for /Rect %s; want false", rect)
			}
			if r := got[0].Reason(); r != "no-rect" {
				t.Errorf("Reason() = %q; want %q", r, "no-rect")
			}
		})
	}
}

// ISO 32000-1 7.9.5 lets a rectangle name its corners in either order.
// Un-normalised, the reversed form reports negative area and zero-area-rect
// then discards a real annotation.
func TestAnnotsNormalisesAReversedRect(t *testing.T) {
	data := annotPDF("[5 0 R]",
		"<< /Type /Annot /Subtype /Stamp /Rect [100 200 10 20] /AP << /N 6 0 R >> >>",
		formStream)

	got := annotsOf(t, data)
	want := Rect{LLX: 10, LLY: 20, URX: 100, URY: 200}
	if got[0].Rect != want {
		t.Errorf("Rect = %+v; want %+v", got[0].Rect, want)
	}
	if !got[0].Paints() {
		t.Errorf("Paints() = false (%s); want true", got[0].Reason())
	}
}

// /AP /N is either the appearance stream or a dictionary of named states, and
// in the second case /AS says which one is current. A widget with an /Off
// default still paints when /AS selects a state that is a stream.
func TestAnnotsResolvesAnAppearanceState(t *testing.T) {
	data := annotPDF("[5 0 R]",
		"<< /Type /Annot /Subtype /Widget /Rect [10 10 100 100] /AS /On /AP << /N << /On 6 0 R /Off 7 0 R >> >> >>",
		formStream, formStream)

	got := annotsOf(t, data)
	if !got[0].HasAPN {
		t.Errorf("HasAPN = false for an /AS-selected state; want true")
	}
	if !got[0].Paints() {
		t.Errorf("Paints() = false (%s); want true", got[0].Reason())
	}
}

// An /AP whose /N names a state that is not present resolves to nothing. It is
// reported as having an appearance dictionary but no usable normal appearance,
// rather than being counted as ink.
func TestAnnotsAppearanceStateMissing(t *testing.T) {
	data := annotPDF("[5 0 R]",
		"<< /Type /Annot /Subtype /Widget /Rect [10 10 100 100] /AS /Missing /AP << /N << /On 6 0 R >> >> >>",
		formStream)

	got := annotsOf(t, data)
	if got[0].HasAPN {
		t.Errorf("HasAPN = true for an unresolvable /AS; want false")
	}
	if r := got[0].Reason(); r != "ap-without-n" {
		t.Errorf("Reason() = %q; want %q", r, "ap-without-n")
	}
}

// The ladder decides what counts as ink. Every entry that is not "" is a fact
// in the dictionary saying a viewer draws nothing.
func TestAnnotReasonLadder(t *testing.T) {
	rect := "/Rect [10 10 100 100]"
	ap := "/AP << /N 6 0 R >>"
	cases := []struct {
		name string
		dict string
		want string
	}{
		{"paints", "<< /Type /Annot /Subtype /Stamp " + rect + " " + ap + " >>", ""},
		{"no subtype", "<< /Type /Annot " + rect + " " + ap + " >>", "no-subtype"},
		{"popup", "<< /Type /Annot /Subtype /Popup " + rect + " " + ap + " >>", "popup"},
		{"hidden", "<< /Type /Annot /Subtype /Stamp /F 2 " + rect + " " + ap + " >>", "hidden"},
		{"noview", "<< /Type /Annot /Subtype /Stamp /F 32 " + rect + " " + ap + " >>", "noview-print-only"},
		{"zero area", "<< /Type /Annot /Subtype /Stamp /Rect [0 0 0 0] " + ap + " >>", "zero-area-rect"},
		{"no ap", "<< /Type /Annot /Subtype /Stamp " + rect + " >>", "no-ap"},
		{"viewer synthesised", "<< /Type /Annot /Subtype /Square " + rect + " >>", "no-ap-viewer-synthesised"},
		// Hidden outranks a present appearance: the flag is the viewer's
		// instruction and the stream is only what it would have drawn.
		{"hidden beats ap", "<< /Type /Annot /Subtype /Stamp /F 2 " + rect + " " + ap + " >>", "hidden"},
		// Bit 1 Invisible is scoped by Table 165 to annotations with no handler,
		// so on a /Stamp it says nothing and must not suppress the ink.
		{"invisible bit ignored", "<< /Type /Annot /Subtype /Stamp /F 1 " + rect + " " + ap + " >>", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := annotsOf(t, annotPDF("[5 0 R]", c.dict, formStream))
			if len(got) != 1 {
				t.Fatalf("got %d annotations; want 1", len(got))
			}
			if r := got[0].Reason(); r != c.want {
				t.Errorf("Reason() = %q; want %q", r, c.want)
			}
			if got[0].Paints() != (c.want == "") {
				t.Errorf("Paints() = %v; want %v", got[0].Paints(), c.want == "")
			}
		})
	}
}

// /Annots is not inheritable: ISO 32000-1 Table 30 lists only /Resources,
// /MediaBox, /CropBox and /Rotate. A page inheriting its parent's annotations
// would double-count every one of them against the page that really carries it.
func TestAnnotsAreNotInherited(t *testing.T) {
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /Annots [4 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
		"<< /Type /Annot /Subtype /Stamp /Rect [10 10 100 100] /AP << /N 5 0 R >> >>",
		formStream,
	})

	if got := annotsOf(t, data); len(got) != 0 {
		t.Errorf("page inherited %d annotations from /Pages; want 0", len(got))
	}
}

func TestAnnotsPageWithout(t *testing.T) {
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
	})

	if got := annotsOf(t, data); len(got) != 0 {
		t.Errorf("got %d annotations on a page with no /Annots; want 0", len(got))
	}
}

func TestAnnotsPageOutOfRange(t *testing.T) {
	d, err := Open(bytes.NewReader(annotPDF("[]")))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	for _, n := range []int{0, 2, -1} {
		if _, err := d.Annots(n); err == nil {
			t.Errorf("Annots(%d) error = nil; want out of range", n)
		} else if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("Annots(%d) error = %v; want out of range", n, err)
		}
	}
}

// The byb-b1.3 shape: a stamp sitting in the blank strip a natural-DPI raster
// leaves. The placement is ia-DTIC_ADA383635.pdf p40's, so this is the exact
// geometry classify now extracts without looking at annotations at all.
func TestAnnotsStampInTheNaturalDPIStrip(t *testing.T) {
	data := annotPDF("[5 0 R]",
		"<< /Type /Annot /Subtype /Stamp /Rect [575 100 605 200] /AP << /N 6 0 R >> >>",
		formStream)

	got := annotsOf(t, data)
	if !got[0].Paints() {
		t.Fatalf("Paints() = false (%s); want true", got[0].Reason())
	}
	// The raster reaches 568.3708; the annotation starts beyond it and the page
	// runs to 612.
	const rasterURX = 568.3708
	if got[0].Rect.LLX <= rasterURX {
		t.Errorf("Rect.LLX = %v; want beyond the raster edge %v", got[0].Rect.LLX, rasterURX)
	}
	if got[0].Rect.URX > 612 {
		t.Errorf("Rect.URX = %v; want within the page box 612", got[0].Rect.URX)
	}
}

// pdfcpu's skipTJ indexes s[0] in three places with no length check, so a TJ
// array the content stream ends inside walks off the end. PageDict parses
// content, which is why the crash arrives from what looks like a dictionary
// read, and pdfcpu's fault.Catch does not recover it.
//
// "[(a)" is the shortest trigger: the string literal is consumed, the loop
// comes round, and TrimLeftFunc leaves nothing to index. "[" alone does NOT do
// it — that path returns a clean errTJExpressionCorrupt — so the array has to
// contain something before it runs out.
//
// govdocs1/050734.pdf page 19 is the real file this reproduces: it killed a
// byblos-annots run over 4,840 files outright. Without the recover this test
// does not fail, it takes the whole test binary down with it. See byb-avp.
func TestMalformedTJIsAnErrorNotAPanic(t *testing.T) {
	body := "[(a)"
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
	})

	d, err := Open(bytes.NewReader(data))
	if err != nil {
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("Open error = %v; want ErrMalformed", err)
		}
		return // caught one level up, which is equally fine
	}
	if _, err := d.Page(1); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Page(1) error = %v; want ErrMalformed. If nil, the fixture no "+
			"longer reproduces the panic and this test is proving nothing.", err)
	}
}
