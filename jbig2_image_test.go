package byblos

import (
	"bytes"
	"image/color"
	"testing"
)

// TestEncodeJBIG2ImageRoundTripsThroughReplaceImages is byb-js5.3's acceptance
// test: EncodeJBIG2Image must return an EncodedImage a caller can hand to
// ReplaceImages verbatim -- no hand-transcribed dictionary -- and get back the
// same pixels, not the photographic negative EncodeJBIG2Generic's doc comment
// warns a wrong transcription produces.
func TestEncodeJBIG2ImageRoundTripsThroughReplaceImages(t *testing.T) {
	in := corpusDoc(t, "scan")
	ref := inspect(t, "scan")[0].Images[0]

	src := jbig2TestBitmap()
	img, err := EncodeJBIG2Image(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Image: %v", err)
	}
	if img.Width != src.Width || img.Height != src.Height || img.BPC != 1 ||
		img.ColorSpace.Name != "DeviceGray" || img.Filter != "JBIG2Decode" {
		t.Fatalf("EncodeJBIG2Image dictionary = %+v; want the documented seven entries", img)
	}

	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: img,
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	pr, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster after substituting EncodeJBIG2Image's output: %v", err)
	}
	b := pr.Image.Bounds()
	if b.Dx() != src.Width || b.Dy() != src.Height {
		t.Fatalf("extracted raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), src.Width, src.Height)
	}
	var wrong, ink int
	for y := 0; y < src.Height; y++ {
		for x := 0; x < src.Width; x++ {
			want := uint8(0xFF)
			if src.At(x, y) != 0 {
				want = 0x00
				ink++
			}
			got := color.GrayModel.Convert(pr.Image.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			if got != want {
				wrong++
			}
		}
	}
	if ink == 0 {
		t.Fatal("the fixture has no ink; this test could not tell an inversion from a match")
	}
	if wrong != 0 {
		t.Errorf("%d of %d pixels differ; EncodeJBIG2Image's round trip through the PDF is not lossless",
			wrong, src.Width*src.Height)
	}
}
