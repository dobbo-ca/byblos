package byblos

import (
	"image"
	"image/color"
	"testing"
)

func grayUniform(w, h int, v uint8) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return g
}

func TestSauvolaRejectsNilImage(t *testing.T) {
	if _, err := Sauvola(nil); err == nil {
		t.Fatal("Sauvola(nil): want error, got nil")
	}
}

func TestSauvolaRejectsEmptyBounds(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 0, 0))
	if _, err := Sauvola(img); err == nil {
		t.Fatal("Sauvola on empty bounds: want error, got nil")
	}
}

// A uniform page (blank paper, no text) has zero local variance everywhere,
// so no pixel should ever cross its own local threshold: Sauvola must not
// invent ink out of a blank scan.
func TestSauvolaUniformImageProducesNoInk(t *testing.T) {
	img := grayUniform(64, 64, 235) // near-white paper
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	if bm.Width != 64 || bm.Height != 64 {
		t.Fatalf("bitmap size = %dx%d, want 64x64", bm.Width, bm.Height)
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if bm.At(x, y) != 0 {
				t.Fatalf("At(%d,%d) = 1, want 0 (uniform image, no ink)", x, y)
			}
		}
	}
}

// A page that is mostly white with a small black glyph-sized mark should mark
// the mark's pixels as ink and leave the surrounding white background
// untouched. The mark (6x6) is deliberately small relative to the 31x31
// window, as real text strokes are: Sauvola's threshold at a pixel is set by
// its window's local mean and variance, and a window entirely INSIDE a large
// uniform dark fill (far larger than the window) has near-zero variance and a
// near-zero mean, driving the local threshold itself to near zero -- so large
// solid dark fills are a known Sauvola blind spot, not something this test
// exercises. A window straddling a small dark mark against a light
// background instead sees strong local contrast, which is exactly the case
// Sauvola is designed for.
func TestSauvolaMarksASmallDarkMarkAsInk(t *testing.T) {
	const w, h = 100, 100
	img := grayUniform(w, h, 255)
	for y := 47; y < 53; y++ {
		for x := 47; x < 53; x++ {
			img.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	if got := bm.At(50, 50); got != 1 {
		t.Errorf("At(50,50) (mark) = %d, want 1 (ink)", got)
	}
	if got := bm.At(5, 5); got != 0 {
		t.Errorf("At(5,5) (background corner) = %d, want 0 (no ink)", got)
	}
}

// Sauvola must be a pure function of its input: same image in, same bitmap
// out, every time.
func TestSauvolaIsDeterministic(t *testing.T) {
	const w, h = 50, 37
	img := grayUniform(w, h, 128)
	for y := 10; y < 20; y++ {
		for x := 5; x < 45; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x * 7) % 256)})
		}
	}
	first, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := Sauvola(img)
		if err != nil {
			t.Fatalf("Sauvola (run %d): %v", i, err)
		}
		if !first.Equal(again) {
			t.Fatalf("Sauvola produced different output on run %d", i)
		}
	}
}

// Sauvola accepts any image.Image, not only *image.Gray: a caller extracting
// a colour source image and thresholding it should not need to convert it by
// hand first.
func TestSauvolaAcceptsNonGrayImage(t *testing.T) {
	rgba := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			rgba.Set(x, y, color.White)
		}
	}
	if _, err := Sauvola(rgba); err != nil {
		t.Fatalf("Sauvola(*image.RGBA): %v", err)
	}
}

// A sub-image (or any image.Image whose Bounds().Min is not (0,0)) must be
// read at its own coordinates, not (0,0)-relative ones: a pixel read that
// silently offsets by Min turns real pixels into the zero value, corrupting
// every mean/variance in the window it falls in.
func TestSauvolaHandlesNonZeroOriginBounds(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			full.Set(x, y, color.White)
		}
	}
	for y := 10; y < 12; y++ {
		for x := 10; x < 12; x++ {
			full.Set(x, y, color.Black)
		}
	}
	sub := full.SubImage(image.Rect(5, 5, 15, 15)) // Bounds().Min = (5,5)

	bm, err := Sauvola(sub)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	// The black mark sits at absolute (10,10)-(12,12), i.e. relative (5,5) in
	// the 10x10 sub-image; the rest of the sub-image is white background.
	if got := bm.At(5, 5); got != 1 {
		t.Errorf("At(5,5) (mark) = %d, want 1 (ink)", got)
	}
	if got := bm.At(0, 0); got != 0 {
		t.Errorf("At(0,0) (background) = %d, want 0 (no ink)", got)
	}
}

// The bitmap Sauvola returns must be the exact type EncodeJBIG2Generic
// consumes -- that is the whole point of routing binarization through
// byblos's own Bitmap rather than a package-private type.
func TestSauvolaOutputFeedsEncodeJBIG2Generic(t *testing.T) {
	img := grayUniform(40, 40, 255)
	for y := 10; y < 30; y++ {
		for x := 10; x < 30; x++ {
			img.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	if _, err := EncodeJBIG2Generic(bm); err != nil {
		t.Fatalf("EncodeJBIG2Generic(Sauvola(img)): %v", err)
	}
}
