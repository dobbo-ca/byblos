package corpus

import (
	"bytes"
	"fmt"
	"testing"
)

func wantNames() []string {
	return []string{
		"born-digital", "scan", "scan-rotated", "scan-in-form",
		"scan-clipped-corner", "scan-cropped-by-form-bbox",
		"scan-clip-narrower-than-raster-box", "scan-clipped-away",
		"scan-deskewed", "scan-natural-dpi", "scan-stamped",
		"scan-mirrored", "scan-quarter-turn",
		"tiled", "overlay-text", "overlay-vector", "background-wash",
		"invisible-text", "invisible-text-in-form",
		"invisible-text-form-inherits", "invisible-text-bracketed",
		"mixed",
		"dup-raster", "jbig2", "stacked", "stacked-in-form",
		"stacked-smask", "stacked-alpha", "mrc", "mrc-inset-base",
		"indirect-kids", "malformed", "scan-reversed-cropbox",
		"blank-page", "booklet", "scan-bilevel",
	}
}

func TestAllReturnsTheExpectedCorpus(t *testing.T) {
	got := All()
	if len(got) != len(wantNames()) {
		t.Fatalf("All() returned %d documents; want %d", len(got), len(wantNames()))
	}
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d.Name] {
			t.Errorf("duplicate document name %q", d.Name)
		}
		seen[d.Name] = true
		if d.Desc == "" {
			t.Errorf("document %q has no description", d.Name)
		}
		if len(d.Data) == 0 {
			t.Errorf("document %q is empty", d.Name)
		}
		if !bytes.HasPrefix(d.Data, []byte("%PDF-")) {
			t.Errorf("document %q does not start with %%PDF-", d.Name)
		}
	}
	for _, n := range wantNames() {
		if !seen[n] {
			t.Errorf("All() is missing %q", n)
		}
	}
}

// TestAllMatchesTheDeclaredCount is the near half of byb-a20's corpus pin.
//
// wantNames above already fails when a document is added without being named,
// so this is not about the corpus changing unnoticed. It is about Count, which
// is the value the design spec's section 8 row and five doc comments are
// checked against by TestCorpusCountClaimsMatchTheCorpus in the root package.
// If Count could drift from All(), the prose would be pinned to a stale number
// instead of to the corpus, which is the failure byb-a20 exists to stop.
func TestAllMatchesTheDeclaredCount(t *testing.T) {
	if got := len(All()); got != Count {
		t.Errorf("All() returns %d documents; Count says %d. Count is what the design "+
			"spec's corpus figures are pinned to: update it, then run the root package's "+
			"TestCorpusCountClaimsMatchTheCorpus and fix every figure it lists.", got, Count)
	}
	if ReadableCount >= Count {
		t.Errorf("ReadableCount = %d and Count = %d; the corpus carries a deliberately "+
			"unreadable document, so ReadableCount must be the smaller", ReadableCount, Count)
	}
}

// The corpus is a fixture. If it is not byte-stable, the committed poppler
// goldens in Task 12 stop meaning anything.
func TestGenerationIsDeterministic(t *testing.T) {
	a, b := All(), All()
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("document %q differs between two calls to All()", a[i].Name)
		}
	}
}

func TestByName(t *testing.T) {
	if _, ok := ByName("scan"); !ok {
		t.Error("ByName(\"scan\") not found")
	}
	if _, ok := ByName("nope"); ok {
		t.Error("ByName(\"nope\") reported found")
	}
}

func TestMalformedIsATruncatedScan(t *testing.T) {
	scan, _ := ByName("scan")
	bad, _ := ByName("malformed")
	if len(bad) >= len(scan) {
		t.Fatalf("malformed is %d bytes, scan is %d; want strictly shorter", len(bad), len(scan))
	}
	if !bytes.HasPrefix(scan, bad) {
		t.Error("malformed is not a prefix of scan; it should be a plain truncation")
	}
	if bytes.Contains(bad, []byte("startxref")) {
		t.Error("malformed still contains startxref; truncate harder")
	}
}

// RotateInheritance's whole point is that page 1 declares no /Rotate of its
// own and page 2 does. Byb-yul.2's inspect_test.go asserts only the reported
// values, which stay green even if a later edit gives page 1 its own /Rotate
// -- at which point the fixture stops testing inheritance at all. Pin the
// bytes directly, the way TestMalformedIsATruncatedScan pins "malformed".
func TestRotateInheritanceFixtureShape(t *testing.T) {
	doc := RotateInheritance()
	if !bytes.Contains(doc, []byte("/Type /Pages /Kids [")) || !bytes.Contains(doc, []byte("/Rotate 90")) {
		t.Error("want the /Pages node to declare /Rotate 90")
	}
	var pageObjs [][]byte
	for _, obj := range bytes.Split(doc, []byte("endobj\n")) {
		if bytes.Contains(obj, []byte("/Type /Page /Parent")) {
			pageObjs = append(pageObjs, obj)
		}
	}
	if len(pageObjs) != 2 {
		t.Fatalf("got %d /Type /Page objects; want 2", len(pageObjs))
	}
	if bytes.Contains(pageObjs[0], []byte("/Rotate")) {
		t.Errorf("page 1's dict declares its own /Rotate, defeating the fixture's purpose: %s", pageObjs[0])
	}
	if !bytes.Contains(pageObjs[1], []byte("/Rotate 180")) {
		t.Errorf("page 2's dict does not declare /Rotate 180: %s", pageObjs[1])
	}
}

// A self-check on the hand-rolled PDF writer: every xref offset must land on
// its own "N 0 obj" header. A writer bug here would surface much later as an
// unexplained pdfcpu parse failure.
func TestXrefOffsetsPointAtTheirObjects(t *testing.T) {
	for _, d := range All() {
		if d.Name == "malformed" {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			data := d.Data
			i := bytes.LastIndex(data, []byte("startxref"))
			if i < 0 {
				t.Fatal("no startxref")
			}
			var start int
			// The EOL after "startxref" must be trimmed before Sscanf:
			// fmt.Sscanf requires newlines in the input to be matched by
			// newlines in the format, so "%d" against "\n123\n" fails with
			// "unexpected newline" on every document.
			tail := bytes.TrimLeft(data[i+len("startxref"):], " \r\n")
			if _, err := fmt.Sscanf(string(tail), "%d", &start); err != nil {
				t.Fatalf("parsing startxref: %v", err)
			}
			if start <= 0 || start >= len(data) || !bytes.HasPrefix(data[start:], []byte("xref\n")) {
				t.Fatalf("startxref %d does not point at an xref table", start)
			}
			hdr := data[start+len("xref\n"):]
			var count int
			if _, err := fmt.Sscanf(string(hdr), "0 %d", &count); err != nil {
				t.Fatalf("parsing xref subsection header: %v", err)
			}
			entries := hdr[bytes.IndexByte(hdr, '\n')+1:]
			if len(entries) < count*20 {
				t.Fatalf("xref table has %d bytes; want at least %d for %d entries", len(entries), count*20, count)
			}
			for n := 1; n < count; n++ {
				var off int
				if _, err := fmt.Sscanf(string(entries[n*20:n*20+10]), "%d", &off); err != nil {
					t.Fatalf("object %d: parsing xref entry: %v", n, err)
				}
				want := fmt.Sprintf("%d 0 obj", n)
				if off <= 0 || off >= len(data) || !bytes.HasPrefix(data[off:], []byte(want)) {
					end := min(off+16, len(data))
					t.Errorf("object %d: xref offset %d points at %q; want %q", n, off, data[off:end], want)
				}
			}
		})
	}
}
