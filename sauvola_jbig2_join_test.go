package byblos

import (
	"bytes"
	"image/color"
	"testing"
)

// TestReplaceImagesCarriesASauvolaJBIG2PageIntoAnExistingPDF is byb-js5.4:
// nothing else in the suite drives corpusDoc -> ExtractPageRaster -> Sauvola
// -> EncodeJBIG2Generic -> ReplaceImages -> re-extract on a REAL raster.
// sauvola_test.go covers Sauvola -> EncodeJBIG2Generic on synthetic images;
// jbig2_decode_test.go covers EncodeJBIG2Generic -> ReplaceImages -> re-extract
// on a synthetic bitmap. This is the seam neither half crosses, modelled on
// TestReplaceImagesCarriesAQuantizedIndexedImageIntoAnExistingPDF
// (substitute_test.go) for the pngquant chain.
//
// The "scan" fixture is deliberately the right shape for this test and not an
// accident: it is an 8-bit /DeviceGray raster (internal/corpus/corpus.go's
// imageDict), not already bilevel, so Sauvola runs on the kind of source it is
// documented for. Feeding an already-1-bpc source through Sauvola would hit its
// known hollowing failure once ExtractPageRaster widens it back to RGBA
// (sauvola.go's doc comment) -- this test asserts the fixture does NOT take
// that path, so a future substitution of "scan" for a bilevel fixture is
// caught here instead of silently changing what the test proves.
func TestReplaceImagesCarriesASauvolaJBIG2PageIntoAnExistingPDF(t *testing.T) {
	in := corpusDoc(t, "scan")
	before := inspect(t, "scan")
	if len(before) != 1 || len(before[0].Images) != 1 {
		t.Fatalf("fixture is %d pages with %d images on page 1; want 1 and 1", len(before), len(before[0].Images))
	}
	ref := before[0].Images[0]
	if ref.Bitonal {
		t.Fatal("fixture image is already bilevel; this test requires a non-bilevel " +
			"source so Sauvola exercises real adaptive thresholding, not its " +
			"documented hollowing failure on a widened bilevel source")
	}

	pr, err := ExtractPageRaster(bytes.NewReader(in), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}

	bm, err := Sauvola(pr.Image)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}

	enc, err := EncodeJBIG2Image(bm.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Image: %v", err)
	}

	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{pr.ObjNr: enc}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	pr2, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster after a Sauvola/JBIG2 substitution: %v", err)
	}
	b := pr2.Image.Bounds()
	if b.Dx() != bm.Width || b.Dy() != bm.Height {
		t.Fatalf("extracted raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), bm.Width, bm.Height)
	}

	var wrong, ink int
	for y := 0; y < bm.Height; y++ {
		for x := 0; x < bm.Width; x++ {
			want := uint8(0xFF) // background is white in /DeviceGray
			if bm.At(x, y) != 0 {
				want = 0x00 // ink is black
				ink++
			}
			got := color.GrayModel.Convert(pr2.Image.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			if got != want {
				wrong++
			}
		}
	}
	if ink == 0 {
		t.Fatal("Sauvola produced no ink on the scan fixture; this test could not tell an inversion from a match")
	}
	if wrong != 0 {
		t.Errorf("%d of %d pixels differ; the Sauvola->JBIG2->ReplaceImages join is not pixel-exact",
			wrong, bm.Width*bm.Height)
	}
}
