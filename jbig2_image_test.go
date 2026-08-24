package byblos

import (
	"testing"
)

// TestEncodeJBIG2ImageDictionary pins EncodeJBIG2Image's documented seven-
// entry dictionary. The pixel-exact round trip through ReplaceImages is
// covered by TestReplaceImagesCarriesASauvolaJBIG2PageIntoAnExistingPDF
// (sauvola_jbig2_join_test.go), which also drives EncodeJBIG2Image.
func TestEncodeJBIG2ImageDictionary(t *testing.T) {
	src := jbig2TestBitmap()
	img, err := EncodeJBIG2Image(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Image: %v", err)
	}
	if img.Width != src.Width || img.Height != src.Height || img.BPC != 1 ||
		img.ColorSpace.Name != "DeviceGray" || img.Filter != "JBIG2Decode" {
		t.Fatalf("EncodeJBIG2Image dictionary = %+v; want the documented seven entries", img)
	}
}
