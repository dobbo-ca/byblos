package pdfdoc

// The two checks stamp_test.go's own comment (in the byblos package) promises
// live here rather than there: they need pdfcpu internals, and arch_test.go
// forbids any package but this one from importing pdfcpu, including from a
// test file.
//
//   - A1: the embedded /FontFile2 stream, decoded straight out of the written
//     document, is byte-for-byte the Program AddFontResource was given. This
//     is the check TestStampTextLayerFontLoadsCleanlyInPoppler cannot make:
//     poppler is quiet whether the font is present-and-correct, corrupt, or
//     entirely absent (a missing /FontFile2 makes it silently substitute
//     base-14 Helvetica), so an oracle-only suite cannot tell "embedded
//     correctly" from "not embedded at all".
//   - A3: the written document validates cleanly under pdfcpu itself, in both
//     strict and relaxed mode. /FontFile2 is OPTIONAL by pdfcpu's own
//     validator (validate/font.go), so this does not stand in for A1 -- a
//     document with FontFile2 dropped entirely still validates clean. It
//     does catch a FontFile2 that is present but missing /Length1, and a
//     font dict missing /Widths, both of which pdfcpu's validator treats as
//     required once the parent entry exists.

import (
	"bytes"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func testFont(program []byte) TrueTypeFont {
	widths := make([]int, 95)
	for i := range widths {
		widths[i] = 600
	}
	return TrueTypeFont{
		BaseFont:    "BbyblosTestFont",
		Program:     program,
		FirstChar:   32,
		Widths:      widths,
		Flags:       32,
		FontBBox:    [4]int{0, 0, 0, 0},
		ItalicAngle: 0,
		Ascent:      800,
		Descent:     -200,
		CapHeight:   700,
		StemV:       80,
	}
}

// A1: the FontFile2 stream this document ends up with, decoded, is exactly
// the Program bytes given to AddFontResource -- not "poppler didn't
// complain", which a missing, truncated, or substituted font also achieves.
func TestAddFontResourceEmbedsFontFile2Hermetically(t *testing.T) {
	program := bytes.Repeat([]byte("glyphless-sfnt-bytes;"), 37) // arbitrary, deterministic, non-trivial length

	d := openCorpus(t, "scan")
	name, err := d.AddFontResource(1, testFont(program))
	if err != nil {
		t.Fatalf("AddFontResource: %v", err)
	}
	// AppendContent so the font is actually referenced: Page()'s own read
	// path consolidates resources down to what the page's content stream
	// names (see AddFontResource's doc comment on consolidateRes), and an
	// unreferenced font would be legitimately pruned back out on re-open,
	// same as a real caller -- StampTextLayer never calls AddFontResource
	// without also writing content that uses the name it returns.
	if err := d.AppendContent(1, []byte("BT\n3 Tr\n/"+name+" 12 Tf\n72 700 Td\n(x) Tj\nET\n")); err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	out := writeDoc(t, d)
	after, err := Open(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-opening the written document: %v", err)
	}
	xt := after.(*doc).ctx.XRefTable

	p, err := after.Page(1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	fontObj, ok := after.(*doc).lookupResource(p.Scope, "Font", name)
	if !ok {
		t.Fatalf("font resource %q not found on page 1 after round-trip", name)
	}
	fontDict, err := xt.DereferenceDict(fontObj)
	if err != nil || fontDict == nil {
		t.Fatalf("dereference font dict: %v", err)
	}
	descrObj, found := fontDict.Find("FontDescriptor")
	if !found {
		t.Fatal("font dict has no /FontDescriptor")
	}
	descr, err := xt.DereferenceDict(descrObj)
	if err != nil || descr == nil {
		t.Fatalf("dereference /FontDescriptor: %v", err)
	}
	ffObj, found := descr.Find("FontFile2")
	if !found {
		t.Fatal("/FontDescriptor has no /FontFile2 -- the font was not embedded")
	}
	sd, _, err := xt.DereferenceStreamDict(ffObj)
	if err != nil || sd == nil {
		t.Fatalf("dereference /FontFile2 stream: %v", err)
	}
	if err := sd.Decode(); err != nil {
		t.Fatalf("decode /FontFile2: %v", err)
	}
	if !bytes.Equal(sd.Content, program) {
		t.Fatalf("/FontFile2 decoded to %d bytes; want %d bytes matching the given Program\ngot:  % x...\nwant: % x...",
			len(sd.Content), len(program), firstN(sd.Content, 24), firstN(program, 24))
	}

	// /Length1 lives on the FontFile2 stream dict itself (ISO 32000-1 table
	// 126), not on the FontDescriptor that points to it.
	length1, ok := sd.Dict.Find("Length1")
	if !ok {
		t.Fatal("/FontFile2 stream dict has no /Length1")
	}
	li, ok := length1.(types.Integer)
	if !ok {
		t.Fatalf("/Length1 is %T, not an integer", length1)
	}
	if li.Value() != len(program) {
		t.Errorf("/Length1 = %d, want %d (len(Program))", li.Value(), len(program))
	}
}

func firstN(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// A3: a document AddFontResource and AppendContent have touched validates
// cleanly under pdfcpu's own validator, in both strict and relaxed mode.
// Strict mode requires /Widths on a TrueType font dict and /Length1 on a
// present FontFile2 stream (validate/font.go) -- so this test, unlike A1,
// would catch /Widths or /Length1 being dropped while it would NOT catch
// /FontFile2 being dropped entirely (that entry is OPTIONAL by pdfcpu's own
// rules); A1 above is what pins that.
func TestStampedFontAndContentValidateCleanly(t *testing.T) {
	program := bytes.Repeat([]byte("glyphless-sfnt-bytes;"), 37)

	d := openCorpus(t, "scan")
	name, err := d.AddFontResource(1, testFont(program))
	if err != nil {
		t.Fatalf("AddFontResource: %v", err)
	}
	if err := d.AppendContent(1, []byte("BT\n3 Tr\n/"+name+" 12 Tf\n72 700 Td\n(hello) Tj\nET\n")); err != nil {
		t.Fatalf("AppendContent: %v", err)
	}
	out := writeDoc(t, d)

	strict := model.NewDefaultConfiguration()
	strict.ValidationMode = model.ValidationStrict
	if err := api.Validate(bytes.NewReader(out), strict); err != nil {
		t.Errorf("api.Validate(strict) = %v, want nil", err)
	}

	relaxed := model.NewDefaultConfiguration()
	relaxed.ValidationMode = model.ValidationRelaxed
	if err := api.Validate(bytes.NewReader(out), relaxed); err != nil {
		t.Errorf("api.Validate(relaxed) = %v, want nil", err)
	}
}
