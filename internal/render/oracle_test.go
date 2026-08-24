package render

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
)

// vectorPDF wraps a content stream in a minimal one-page PDF, the hand-rolled
// idiom internal/corpus uses for fixtures.
func vectorPDF(contentStream string, w, h float64) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Contents 4 0 R >>", w, h),
	}
	var buf []byte
	buf = append(buf, "%PDF-1.4\n"...)
	var offsets []int
	for _, o := range objs {
		offsets = append(offsets, len(buf))
		buf = append(buf, fmt.Sprintf("%d 0 obj\n%s\nendobj\n", len(offsets), o)...)
	}
	offsets = append(offsets, len(buf))
	buf = append(buf, fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(contentStream), contentStream)...)
	xref := len(buf)
	buf = append(buf, fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)...)
	for _, off := range offsets {
		buf = append(buf, fmt.Sprintf("%010d 00000 n \n", off)...)
	}
	buf = append(buf, fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xref)...)
	return buf
}

// oracleContent exercises both winding rules, a stroke, a curve, and both
// device colour spaces, placed asymmetrically so a flipped or mirrored
// renderer cannot pass by luck.
const oracleContent = "1 0 0 rg 20 110 80 60 re f " +
	"0 0 1 rg 110 110 70 70 re 130 130 30 30 re f* " +
	"0 g 6 w 20 20 m 90 80 l S " +
	"0.5 g 110 60 m 180 60 l 180 20 110 20 110 60 c h f"

// mismatchFraction counts pixels whose channels differ by more than 40/255
// between the two images -- room for poppler's antialiased edges, none for a
// shape in the wrong place.
func mismatchFraction(t *testing.T, a *image.RGBA, b image.Image) float64 {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("size mismatch: %v vs %v", a.Bounds(), b.Bounds())
	}
	diff := func(x, y uint32) int32 {
		d := int32(x>>8) - int32(y>>8)
		if d < 0 {
			d = -d
		}
		return d
	}
	bad, total := 0, 0
	bnd := a.Bounds()
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			total++
			if diff(ar, br) > 40 || diff(ag, bg) > 40 || diff(ab, bb) > 40 {
				bad++
			}
		}
	}
	return float64(bad) / float64(total)
}

// TestRenderAgreesWithPdftoppm compares this renderer against poppler at 72
// DPI on a page of filled and stroked paths: at most 5% of pixels may
// disagree (antialiased edges), and the do-nothing null -- a blank raster --
// must FAIL the same metric, which pins that the metric can fail at all
// (byb-8b9.1's acceptance).
func TestRenderAgreesWithPdftoppm(t *testing.T) {
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skipf("pdftoppm not on PATH: %v", err)
	}
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "vector.pdf")
	if err := os.WriteFile(pdfPath, vectorPDF(oracleContent, 200, 200), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pdftoppm, "-r", "72", "-png", pdfPath, filepath.Join(dir, "oracle")).CombinedOutput()
	if err != nil {
		t.Fatalf("pdftoppm: %v: %s", err, out)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "oracle*.png"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one pdftoppm page, got %v (err %v)", matches, err)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oracle, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode pdftoppm output: %v", err)
	}

	box := content.Box{LLX: 0, LLY: 0, URX: 200, URY: 200}
	got, err := Page(context.Background(), []byte(oracleContent), box, 1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	const tolerance = 0.05
	frac := mismatchFraction(t, got, oracle)
	t.Logf("mismatch vs pdftoppm: %.2f%% of pixels", frac*100)
	if frac > tolerance {
		t.Errorf("rendered page disagrees with pdftoppm on %.1f%% of pixels; tolerance %.0f%%",
			frac*100, tolerance*100)
	}

	// The null check: a blank canvas must not pass the metric, or the metric
	// measures nothing.
	blank, err := Page(context.Background(), nil, box, 1)
	if err != nil {
		t.Fatalf("Page(blank): %v", err)
	}
	if frac := mismatchFraction(t, blank, oracle); frac <= tolerance {
		t.Errorf("a BLANK raster is within tolerance of pdftoppm (%.1f%% mismatch); the oracle metric is broken", frac*100)
	}
}
