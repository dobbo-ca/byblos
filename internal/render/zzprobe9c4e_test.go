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

func popplerPNG(t *testing.T, pdf []byte, args ...string) image.Image {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "page.pdf")
	if err := os.WriteFile(p, pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	a := append([]string{"-r", "72", "-png"}, args...)
	a = append(a, p, filepath.Join(dir, "o"))
	out, err := exec.Command("pdftoppm", a...).CombinedOutput()
	if err != nil {
		t.Fatalf("pdftoppm %v: %v: %s", a, err, out)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "o*.png"))
	if len(m) != 1 {
		t.Fatalf("got %v", m)
	}
	f, err := os.Open(m[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func cropRotPDF(cs string, mw, mh, lx, ly, ux, uy float64, rot int) []byte {
	return wrapPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /CropBox [%g %g %g %g] /Rotate %d /Contents 4 0 R >>",
			mw, mh, lx, ly, ux, uy, rot),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cs), cs),
	})
}

// inkBox returns the bounding box of non-white pixels.
func inkBox(img image.Image) image.Rectangle {
	b := img.Bounds()
	minx, miny, maxx, maxy := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, _ := img.At(x, y).RGBA()
			if r>>8 < 200 || g>>8 < 200 || bb>>8 < 200 {
				if x < minx {
					minx = x
				}
				if y < miny {
					miny = y
				}
				if x > maxx {
					maxx = x
				}
				if y > maxy {
					maxy = y
				}
			}
		}
	}
	return image.Rect(minx, miny, maxx, maxy)
}

func TestProbeTranslatedCrop(t *testing.T) {
	lx, ly, ux, uy := 50.0, 30.0, 290.0, 190.0 // 240x160
	cs := "1 0 0 1 50 30 cm " + oracleContent
	box := content.Box{LLX: lx, LLY: ly, URX: ux, URY: uy}
	for _, rot := range []int{0, 90, 180, 270} {
		oracle := popplerPNG(t, cropRotPDF(cs, 400, 300, lx, ly, ux, uy, rot), "-cropbox")
		got, err := Page(context.Background(), []byte(cs), box, rot, 1, nil, nil)
		if err != nil {
			t.Fatalf("rot %d: %v", rot, err)
		}
		if got.Bounds() != oracle.Bounds() {
			t.Errorf("rot %d: bounds %v vs poppler %v", rot, got.Bounds(), oracle.Bounds())
			continue
		}
		f := mismatchFraction(t, got, oracle)
		t.Logf("crop rot %d: mismatch %.2f%%  ink got=%v poppler=%v", rot, f*100, inkBox(got), inkBox(oracle))
		if f > 0.05 {
			t.Errorf("rot %d: %.2f%% mismatch", rot, f*100)
		}
	}
}

func TestProbeCeilSlack(t *testing.T) {
	lx, ly, ux, uy := 0.0, 0.0, 240.4, 160.6
	cs := oracleContent
	box := content.Box{LLX: lx, LLY: ly, URX: ux, URY: uy}
	for _, rot := range []int{0, 90, 180, 270} {
		oracle := popplerPNG(t, cropRotPDF(cs, ux, uy, lx, ly, ux, uy, rot))
		got, err := Page(context.Background(), []byte(cs), box, rot, 1, nil, nil)
		if err != nil {
			t.Fatalf("rot %d: %v", rot, err)
		}
		if got.Bounds() != oracle.Bounds() {
			t.Errorf("rot %d: bounds %v vs poppler %v", rot, got.Bounds(), oracle.Bounds())
			continue
		}
		f := mismatchFraction(t, got, oracle)
		t.Logf("ceil rot %d: mismatch %.2f%%  ink got=%v poppler=%v", rot, f*100, inkBox(got), inkBox(oracle))
	}
}
