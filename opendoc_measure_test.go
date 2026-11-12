package byblos

import (
	"bytes"
	"context"
	"os"
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
