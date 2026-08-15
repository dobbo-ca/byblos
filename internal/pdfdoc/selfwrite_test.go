package pdfdoc

// Unit tests for selfwrite.go's writeDocument, isolated from BuildFromPages'
// migration walk: each test builds its own tiny *model.XRefTable by hand,
// the same way pdfcpu.CreateXRefTableWithRootDict does, and inspects the
// exact bytes the writer produced.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// freshTable returns a catalog-only table: object 0 is the free head, object
// 1 is a bare << /Type /Catalog >>, /Info is nil -- exactly what
// buildContext starts every build from.
func freshTable(t *testing.T) *model.XRefTable {
	t.Helper()
	xt, err := pdfcpu.CreateXRefTableWithRootDict()
	if err != nil {
		t.Fatalf("CreateXRefTableWithRootDict: %v", err)
	}
	return xt
}

func TestWriteDocumentRefusesAReservedButUnfilledObject(t *testing.T) {
	xt := freshTable(t)
	if _, err := xt.InsertObject(nil); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	var buf bytes.Buffer
	err := writeDocument(&buf, xt)
	if err == nil {
		t.Fatal("writeDocument accepted a table with an unfilled object")
	}
	if !strings.Contains(err.Error(), "reserved but never filled") {
		t.Errorf("error = %q, want it to name the unfilled object", err)
	}
	// buildpages.go:102's promise, and the whole reason the audit runs
	// before the first byte: a refused build writes nothing.
	if buf.Len() != 0 {
		t.Errorf("a refused write produced %d bytes; it must write none", buf.Len())
	}
}

func TestWriteDocumentSizeAndRoot(t *testing.T) {
	xt := freshTable(t)
	var buf bytes.Buffer
	if err := writeDocument(&buf, xt); err != nil {
		t.Fatalf("writeDocument: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "/Size 2") {
		t.Errorf("trailer does not say /Size 2 (objects 0 and 1): %q", out)
	}
	if !strings.Contains(out, "/Root 1 0 R") {
		t.Errorf("trailer does not say /Root 1 0 R: %q", out)
	}
	if strings.Contains(out, "/Info") {
		t.Errorf("trailer names /Info, but the table has none: %q", out)
	}
}

func TestWriteDocumentInfoPresentWhenSet(t *testing.T) {
	xt := freshTable(t)
	nr, err := xt.InsertObject(types.Dict{"Title": types.StringLiteral("hi")})
	if err != nil {
		t.Fatalf("insert info dict: %v", err)
	}
	ref := types.NewIndirectRef(nr, 0)
	xt.Info = ref
	var buf bytes.Buffer
	if err := writeDocument(&buf, xt); err != nil {
		t.Fatalf("writeDocument: %v", err)
	}
	out := buf.String()
	want := "/Info 2 0 R"
	if !strings.Contains(out, want) {
		t.Errorf("trailer does not say %q: %q", want, out)
	}
}

// TestWriteDocumentSingleXrefSubsectionWithFreeHead pins the exact shape a
// mutation in Correction 3 targets: ONE "0 N" subsection covering every
// object from the free head to the highest object number, not one
// subsection per contiguous run.
func TestWriteDocumentSingleXrefSubsectionWithFreeHead(t *testing.T) {
	xt := freshTable(t)
	if _, err := xt.InsertObject(types.Dict{"A": types.Integer(1)}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var buf bytes.Buffer
	if err := writeDocument(&buf, xt); err != nil {
		t.Fatalf("writeDocument: %v", err)
	}
	out := buf.String()

	subsections := regexp.MustCompile(`(?m)^\d+ \d+$`).FindAllString(out, -1)
	if len(subsections) != 1 {
		t.Fatalf("xref has %d subsection headers, want exactly 1: %v", len(subsections), subsections)
	}
	if subsections[0] != "0 3" {
		t.Errorf("subsection header = %q, want \"0 3\" (objects 0, 1 and 2)", subsections[0])
	}
	if !strings.Contains(out, "xref\n0 3\n0000000000 65535 f \n") {
		t.Errorf("object 0's entry is not the free head pdfcpu itself writes: %q", out)
	}
}

// TestWriteDocumentFreeListChainsMultipleFreeEntries is the regression for
// the gap adversarial review found: TestWriteDocumentSingleXrefSubsectionWithFreeHead
// only ever exercises the free head pointing at object 0 (nothing free), so
// a mutation that destroys the chain construction entirely -- replacing
// sort.SearchInts's result with a constant, or dropping sort.Ints(freeNrs) --
// left the whole suite green. This builds a table with objects 3 and 5
// genuinely marked free (object 4 stays in use, so the free entries are not
// contiguous either) and pins the full chain: 0 -> 3 -> 5 -> 0.
func TestWriteDocumentFreeListChainsMultipleFreeEntries(t *testing.T) {
	xt := freshTable(t)
	for i := 0; i < 5; i++ {
		if _, err := xt.InsertObject(types.Dict{"N": types.Integer(i)}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Objects are 1 (the catalog) through 6. Free 3 and 5, in that order, so
	// the chain is not simply "the next object number" by coincidence.
	for _, n := range []int{3, 5} {
		e, ok := xt.FindTableEntry(n, 0)
		if !ok || e == nil {
			t.Fatalf("no table entry for object %d", n)
		}
		e.Free = true
		e.Object = nil
	}

	var buf bytes.Buffer
	if err := writeDocument(&buf, xt); err != nil {
		t.Fatalf("writeDocument: %v", err)
	}
	out := buf.String()

	lines := strings.Split(out, "\n")
	xrefStart := -1
	for i, l := range lines {
		if l == "0 7" {
			xrefStart = i
			break
		}
	}
	if xrefStart < 0 {
		t.Fatalf("no \"0 7\" xref subsection header in output: %q", out)
	}
	entries := lines[xrefStart+1 : xrefStart+8]
	if entries[0] != "0000000003 65535 f " {
		t.Errorf("object 0 (free head) = %q, want next=3", entries[0])
	}
	if entries[3] != "0000000005 00000 f " {
		t.Errorf("object 3 = %q, want free with next=5", entries[3])
	}
	if entries[5] != "0000000000 00000 f " {
		t.Errorf("object 5 = %q, want free with next=0 (end of chain)", entries[5])
	}
	for _, n := range []int{1, 2, 4, 6} {
		if !strings.HasSuffix(entries[n], " n ") {
			t.Errorf("object %d = %q, want an in-use (\"n\") entry", n, entries[n])
		}
	}
}

// TestWriteDocumentRefusesATableGapBetweenFilledAndFree is the regression for
// the sparse-freeNrs bug adversarial review demonstrated by hand: an object
// number in [1, max] that is neither filled nor genuinely marked free (e.g.
// simply absent from xt.Table, a gap) used to be silently treated as though
// it were part of the free chain, corrupting the chain's linkage for every
// free object numbered after it. auditTable now refuses such a table
// outright instead of writing a wrong chain.
func TestWriteDocumentRefusesATableGapBetweenFilledAndFree(t *testing.T) {
	xt := freshTable(t)
	for i := 0; i < 5; i++ {
		if _, err := xt.InsertObject(types.Dict{"N": types.Integer(i)}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Object 4 (the 4th InsertObject call above) is removed from the table
	// entirely -- neither filled nor Free -- simulating a gap.
	delete(xt.Table, 4)

	var buf bytes.Buffer
	err := writeDocument(&buf, xt)
	if err == nil {
		t.Fatal("writeDocument accepted a table with a gap between filled and free entries")
	}
	if !strings.Contains(err.Error(), "object 4") {
		t.Errorf("error = %q, want it to name object 4", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused write produced %d bytes; it must write none", buf.Len())
	}
}

func TestWriteDocumentHeaderNamesTheVersion(t *testing.T) {
	xt := freshTable(t)
	var buf bytes.Buffer
	if err := writeDocument(&buf, xt); err != nil {
		t.Fatalf("writeDocument: %v", err)
	}
	out := buf.Bytes()
	wantHeader := "%PDF-" + xt.VersionString() + "\n"
	if !bytes.HasPrefix(out, []byte(wantHeader)) {
		t.Errorf("header = %q, want prefix %q", out[:min(len(out), 20)], wantHeader)
	}
	if !bytes.Contains(out, []byte(binaryMarker)) {
		t.Error("the binary comment line (ISO 32000-1 7.5.2) is missing")
	}
}

// TestWriteDocumentStreamLengthAndBinaryPayload covers /Length matching the
// written payload and a binary payload -- including NUL and every high byte
// -- surviving byte for byte through a round trip via pdfcpu's own reader.
func TestWriteDocumentStreamLengthAndBinaryPayload(t *testing.T) {
	xt := freshTable(t)
	payload := make([]byte, 512)
	for i := range payload {
		payload[i] = byte(i)
	}
	sd := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject")}, Raw: payload}
	nr, err := xt.InsertObject(sd)
	if err != nil {
		t.Fatalf("insert stream: %v", err)
	}
	var buf bytes.Buffer
	if err := writeDocument(&buf, xt); err != nil {
		t.Fatalf("writeDocument: %v", err)
	}

	rxt := reopenXt(t, buf.Bytes())
	got, _, err := rxt.DereferenceStreamDict(types.IndirectRef{ObjectNumber: types.Integer(nr)})
	if err != nil || got == nil {
		t.Fatalf("re-reading the stream: %v", err)
	}
	if n, ok := got.Dict["Length"].(types.Integer); !ok || int(n) != len(payload) {
		t.Errorf("/Length = %v, want %d", got.Dict["Length"], len(payload))
	}
	if !bytes.Equal(got.Raw, payload) {
		t.Errorf("stream payload changed in the round trip: %d bytes in, %d bytes out",
			len(payload), len(got.Raw))
	}
}

// TestWriteDocumentPassesQpdfCheck is Correction 4: pdfcpu's own reader
// silently reconstructs a damaged classic xref table and reports success on
// it, so a round trip through this package's Open or Validate cannot catch a
// bug in the table this writer newly owns (the free list, /Size, a
// corrupted offset). qpdf does not reconstruct silently; it warns.
func TestWriteDocumentPassesQpdfCheck(t *testing.T) {
	qpdfPath, err := exec.LookPath("qpdf")
	if err != nil {
		t.Skip("qpdf not on PATH")
	}

	src := assembleFixture([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] /Resources << >> >>",
		streamObj("", "0 0 1 rg 10 10 50 50 re f"),
	})
	out := buildRaw(t, src)

	// qpdf does not read a PDF from stdin ("reading from stdin is not
	// supported"), so the bytes have to land on disk first.
	path := filepath.Join(t.TempDir(), "selfwrite.pdf")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("writing the fixture for qpdf: %v", err)
	}

	cmd := exec.Command(qpdfPath, "--check", path)
	report, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("qpdf --check: %v\n%s", err, report)
	}
	if strings.Contains(string(report), "WARNING") {
		t.Errorf("qpdf --check reported a warning on a self-written document:\n%s", report)
	}
}
