package byblos

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMeasureOpenOnce times Inspect plus every page's raster, one-shot against
// open-once, on generated documents. It runs only in the hosted-runner
// measure workflow (BYBLOS_MEASURE=1), never locally.
func TestMeasureOpenOnce(t *testing.T) {
	if os.Getenv("BYBLOS_MEASURE") == "" {
		t.Skip("set BYBLOS_MEASURE=1 (CI measure workflow only)")
	}
	ctx := context.Background()
	for _, n := range []int{300, 2000} {
		pages := make([]BuildPage, n)
		for i := range pages {
			pages[i] = BuildPage{Image: flateGrayImage(t, 100, 100, i), WidthPt: 612, HeightPt: 792, DPI: 100}
		}
		var buf bytes.Buffer
		if err := BuildPDF(&buf, pages); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()

		start := time.Now()
		if _, err := InspectContext(ctx, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		for p := 1; p <= n; p++ {
			if _, err := ExtractPageRasterLimits(ctx, bytes.NewReader(data), p, Limits{}); err != nil {
				t.Fatal(err)
			}
		}
		oneShot := time.Since(start)

		start = time.Now()
		doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.Info(ctx); err != nil {
			t.Fatal(err)
		}
		for p := 1; p <= n; p++ {
			if _, err := doc.PageRaster(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
		doc.Close()
		t.Logf("MEASURE pages=%d bytes=%d one-shot=%v open-once=%v", n, len(data), oneShot, time.Since(start))
	}
}

// TestMeasurePageData times OpenDocument, Info and every PageRaster apart, on
// generated documents of empty pages in one flat page tree, to show whether the
// cost per page grows with the page count. Hosted runner only.
func TestMeasurePageData(t *testing.T) {
	if os.Getenv("BYBLOS_MEASURE") == "" {
		t.Skip("set BYBLOS_MEASURE=1 (CI measure workflow only)")
	}
	ctx := context.Background()
	for _, n := range []int{1000, 2000, 10000} {
		var b bytes.Buffer
		b.WriteString("%PDF-1.4\n")
		offs := make([]int, 0, n+2)
		obj := func(body string) {
			offs = append(offs, b.Len())
			fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), body)
		}
		kids := make([]byte, 0, 8*n)
		for i := 0; i < n; i++ {
			kids = fmt.Appendf(kids, "%d 0 R ", i+3)
		}
		obj("<< /Type /Catalog /Pages 2 0 R >>")
		obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 612 792] >>", kids, n))
		for i := 0; i < n; i++ {
			obj("<< /Type /Page /Parent 2 0 R >>")
		}
		x := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
		for _, o := range offs {
			fmt.Fprintf(&b, "%010d 00000 n \n", o)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, x)

		start := time.Now()
		doc, err := OpenDocument(ctx, bytes.NewReader(b.Bytes()), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		tOpen := time.Since(start)
		start = time.Now()
		if _, err := doc.Info(ctx); err != nil {
			t.Fatal(err)
		}
		tInfo := time.Since(start)
		start = time.Now()
		for p := 1; p <= n; p++ {
			_, _ = doc.PageRaster(ctx, p) // an empty page has no raster; the time is the point
		}
		tRaster := time.Since(start)
		doc.Close()
		t.Logf("MEASURE pages=%d open=%v info=%v rasters=%v", n, tOpen, tInfo, tRaster)
	}
}

// TestMeasureOpenDocumentHeap reports live heap (after GC, above the file's own
// bytes) once OpenDocument returns and after pages have run, on a generated 200
// page document of 1 MiB incompressible images. With image bytes held from the
// open it is the file's size from the first line on; per page it should be flat.
// Hosted runner only.
func TestMeasureOpenDocumentHeap(t *testing.T) {
	if os.Getenv("BYBLOS_MEASURE") == "" {
		t.Skip("set BYBLOS_MEASURE=1 (CI measure workflow only)")
	}
	const pages = 200
	rng := rand.New(rand.NewSource(1))
	px := make([]byte, 1000*1000)
	bp := make([]BuildPage, pages)
	for i := range bp {
		rng.Read(px)
		bp[i] = BuildPage{Image: EncodedImage{Width: 1000, Height: 1000, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: flateEncode(t, px)}, WidthPt: 612, HeightPt: 792, DPI: 100}
	}
	var buf bytes.Buffer
	if err := BuildPDF(&buf, bp); err != nil {
		t.Fatal(err)
	}
	bp = nil
	data := buf.Bytes()
	live := func() float64 {
		var m runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc) / 1e6
	}
	ctx := context.Background()
	base := live()
	doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURE heap pdf_mb=%.0f pages=%d base_mb=%.0f after_open_mb=%.0f", float64(len(data))/1e6, pages, base, live())
	for p := 1; p <= pages; p++ {
		if _, err := doc.PageRaster(ctx, p); err != nil {
			t.Fatal(err)
		}
		if p%50 == 0 {
			t.Logf("MEASURE heap after_page=%d live_mb=%.0f", p, live())
		}
	}
	doc.Close()
}
