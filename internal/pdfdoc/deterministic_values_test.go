package pdfdoc_test

// The VALUE half of byb-c53's determinism contract (the byte-compare half
// lives in the root package's deterministic_test.go): the pinned dates and
// the preserved /ID element 0 now sit inside a compressed object stream, so
// asserting them takes a pdfcpu re-read — which only this import path may do
// (TestOnlyPdfdocImportsPdfcpu). Byte-equality alone is structurally blind
// here: a writer that clobbered the input's /ID element 0 or dates with any
// OTHER deterministic value would still pass two-runs-compare.

import (
	"bytes"
	"fmt"
	"image"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pinnedDateWant is the documented constant deterministic.go pins
// CreationDate/ModDate to when the input document carried none.
const pinnedDateWant = "D:20000101000000+00'00'"

// reRead parses data with pdfcpu, independently of the writer under test.
func reRead(t *testing.T, data []byte) *model.Context {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(data), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("re-read output: %v", err)
	}
	return ctx
}

// infoDatesOf returns data's Info-dictionary CreationDate and ModDate, "" for
// absent.
func infoDatesOf(t *testing.T, data []byte) (creation, mod string) {
	t.Helper()
	ctx := reRead(t, data)
	if ctx.Info == nil {
		return "", ""
	}
	d, err := ctx.DereferenceDict(*ctx.Info)
	if err != nil || d == nil {
		t.Fatalf("output Info dictionary: %v", err)
	}
	get := func(key string) string {
		o, _ := ctx.Dereference(d[key])
		if s, ok := o.(types.StringLiteral); ok {
			return s.Value()
		}
		return ""
	}
	return get("CreationDate"), get("ModDate")
}

// idElementZero returns data's trailer /ID element 0 as uppercase hex, "" for
// absent.
func idElementZero(t *testing.T, data []byte) string {
	t.Helper()
	ctx := reRead(t, data)
	if len(ctx.ID) == 0 {
		return ""
	}
	switch v := ctx.ID[0].(type) {
	case types.HexLiteral:
		return strings.ToUpper(v.Value())
	case types.StringLiteral:
		return strings.ToUpper(fmt.Sprintf("%X", v.Value()))
	}
	return ""
}

func stamped(t *testing.T, base []byte) []byte {
	t.Helper()
	tl := byblos.TextLayer{Pages: [][]byblos.PositionedWord{
		{{Text: "byb-c53", Bounds: image.Rect(72, 700, 140, 712)}},
	}}
	var out bytes.Buffer
	if err := byblos.StampTextLayer(&out, bytes.NewReader(base), tl); err != nil {
		t.Fatalf("StampTextLayer: %v", err)
	}
	return out.Bytes()
}

// fixturePDF is a self-contained one-page document with an Info dictionary
// carrying the given dates and, when id is non-empty, a trailer /ID with id
// as both elements — so the preserve-the-input's-own-values half of the pin
// has fixtures to bite on (the corpus generators write neither).
func fixturePDF(t *testing.T, creation, mod, id string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	var offs []int
	obj := func(body string) {
		offs = append(offs, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << >> >>")
	obj("<< /Length 0 >>\nstream\n\nendstream")
	obj(fmt.Sprintf("<< /CreationDate (%s) /ModDate (%s) >>", creation, mod))
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, off := range offs {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R", len(offs)+1, len(offs))
	if id != "" {
		fmt.Fprintf(&buf, " /ID [<%s> <%s>]", id, id)
	}
	fmt.Fprintf(&buf, " >>\nstartxref\n%d\n%%%%EOF\n", start)
	return buf.Bytes()
}

func TestWritePinsMissingDatesToTheConstant(t *testing.T) {
	scan, ok := corpus.ByName("scan")
	if !ok {
		t.Fatal("corpus document scan not found")
	}
	// The input has no Info dictionary, so both dates must be the constant --
	// not whatever second the test happened to run in.
	creation, mod := infoDatesOf(t, stamped(t, scan))
	if creation != pinnedDateWant || mod != pinnedDateWant {
		t.Errorf("dates = (%q, %q); want both pinned to %s", creation, mod, pinnedDateWant)
	}
}

func TestWriteKeepsTheInputDocumentsDates(t *testing.T) {
	base := fixturePDF(t, "D:20190102030405+01'00'", "D:20200607080910-05'00'", "")
	creation, mod := infoDatesOf(t, stamped(t, base))
	if creation != "D:20190102030405+01'00'" {
		t.Errorf("CreationDate = %q; the input's did not survive the write", creation)
	}
	if mod != "D:20200607080910-05'00'" {
		t.Errorf("ModDate = %q; the input's did not survive the write", mod)
	}
}

func TestWriteKeepsTheInputDocumentsIDElementZero(t *testing.T) {
	// ISO 32000-1 14.4: element 0 is the document's permanent identity, set
	// at creation and kept by every rewrite; only element 1 changes.
	const id = "4DB48EDAE40A66E4C5E4F2328B8EB183"
	base := fixturePDF(t, "D:20190102030405+01'00'", "D:20190102030405+01'00'", id)
	if got := idElementZero(t, stamped(t, base)); got != id {
		t.Errorf("/ID element 0 = %q; want the input's own %q", got, id)
	}
}
