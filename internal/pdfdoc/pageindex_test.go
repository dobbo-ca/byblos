package pdfdoc

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
)

// rawPDF writes objects 1..len(bodies) with a classic xref; object 1 is the
// catalog. Synthetic, generated here.
func rawPDF(bodies []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(bodies))
	for i, body := range bodies {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(bodies)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(bodies)+1, x)
	return b.Bytes()
}

// flatPDF is the tree scanners write: one /Pages node, n leaf pages.
func flatPDF(n int) []byte {
	kids := ""
	bodies := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", i+3)
		bodies = append(bodies, "<< /Type /Page /Parent 2 0 R >>")
	}
	bodies[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 100 200] >>", kids, n)
	return rawPDF(bodies)
}

func TestPageLookupCostIsFlatPerPage(t *testing.T) {
	var per [2]float64
	for i, n := range []int{24, 48} {
		d, err := Open(bytes.NewReader(flatPDF(n)))
		if err != nil {
			t.Fatal(err)
		}
		before := PageTreeSteps.Load()
		for p := 1; p <= n; p++ {
			if _, err := d.Page(p); err != nil {
				t.Fatal(err)
			}
		}
		per[i] = float64(PageTreeSteps.Load()-before) / float64(n)
	}
	// One walk to index plus the path to each page: about 3 steps a page at
	// any size. A scan from the root grows with the page count instead.
	if per[0] == 0 || per[0] > 4 || per[1] > 4 {
		t.Fatalf("page-tree steps per page = %.1f at 24 pages, %.1f at 48; want a small constant", per[0], per[1])
	}
}

// The indexed lookup must return what pdfcpu's PageDict returns, inheritance
// included.
func TestIndexedPageDictMatchesPdfcpu(t *testing.T) {
	data := rawPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 8 0 R] /Count 5 /MediaBox [0 0 100 200] /Rotate 90 >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [5 0 R 6 0 R 7 0 R] /Count 3 /Rotate 180 /Resources << /ProcSet [/PDF] >> >>",
		"<< /Type /Page /Parent 4 0 R >>",
		"<< /Type /Page /Parent 4 0 R /CropBox [10 10 50 50] /MediaBox [0 0 300 400] >>",
		"<< /Type /Page /Parent 4 0 R /Rotate 0 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << >> >>",
	})
	od, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	d := od.(*doc)
	if d.buildPageIndex() == nil {
		t.Fatal("tree was not indexed")
	}
	for n := 1; n <= 5; n++ {
		got, ginh, err := d.pageDict(n)
		if err != nil {
			t.Fatal(err)
		}
		want, _, winh, err := d.ctx.XRefTable.PageDict(context.Background(), n, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(ginh, winh) {
			t.Errorf("page %d: indexed %v %+v, pdfcpu %v %+v", n, got, ginh, want, winh)
		}
	}
}

// A /Pages node without /Count is a tree the index does not understand:
// pdfcpu enters it and its attributes leak into later pages, so lookups must
// stay on pdfcpu's path and return the same values.
func TestPageIndexFallsBackWithoutCount(t *testing.T) {
	data := rawPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 100 200] >>",
		"<< /Type /Pages /Parent 2 0 R /Kids [4 0 R] /Rotate 90 /MediaBox [0 0 1 1] >>",
		"<< /Type /Page /Parent 3 0 R >>",
		"<< /Type /Page /Parent 2 0 R >>",
	})
	od, err := Open(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	d := od.(*doc)
	if d.buildPageIndex() != nil {
		t.Fatal("tree with a /Pages node lacking /Count was indexed")
	}
	got, ginh, err := d.pageDict(2)
	if err != nil {
		t.Fatal(err)
	}
	want, _, winh, err := d.ctx.XRefTable.PageDict(context.Background(), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(ginh, winh) {
		t.Errorf("page 2: got %v %+v, pdfcpu %v %+v", got, ginh, want, winh)
	}
}

// pdfcpu reads a leaf's /Count too (a malformed one errors, a non-positive one
// skips the leaf), so a leaf carrying /Count stays on pdfcpu's path.
func TestPageIndexFallsBackWithLeafCount(t *testing.T) {
	for _, leaf := range []string{"/Count /X", "/Count -1 /MediaBox [0 0 100 200]"} {
		data := rawPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 400] >>",
			"<< /Type /Page /Parent 2 0 R " + leaf + " >>",
			"<< /Type /Page /Parent 2 0 R /Rotate 90 >>",
		})
		od, err := Open(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		d := od.(*doc)
		if d.buildPageIndex() != nil {
			t.Fatalf("%q: tree with a leaf /Count was indexed", leaf)
		}
		got, ginh, gerr := d.pageDict(1)
		want, _, winh, werr := d.ctx.XRefTable.PageDict(context.Background(), 1, false)
		if (gerr == nil) != (werr == nil) || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(ginh, winh) {
			t.Errorf("%q: got %v %v, pdfcpu %v %v", leaf, got, gerr, want, werr)
		}
	}
}
