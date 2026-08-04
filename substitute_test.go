package byblos

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// halveGray nearest-neighbour subsamples src to half its width and height as
// 8bpc grey samples, row-major. It gives ReplaceImages a raster that is
// genuinely SMALLER and genuinely DIFFERENT, rather than the same pixels
// re-encoded -- the only kind of substitution that can catch a /Width,
// /Height or payload rewrite that did not happen.
func halveGray(src image.Image) (samples []byte, w, h int) {
	b := src.Bounds()
	w, h = b.Dx()/2, b.Dy()/2
	samples = make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g := color.GrayModel.Convert(src.At(b.Min.X+x*2, b.Min.Y+y*2)).(color.Gray)
			samples[y*w+x] = g.Y
		}
	}
	return samples, w, h
}

// graySamples reads img back as 8bpc grey samples, row-major, so a raster that
// went in through ReplaceImages can be compared with what comes out.
func graySamples(img image.Image) []byte {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out = append(out, color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y)
		}
	}
	return out
}

// byb-fp6's acceptance: bytes one of Byblos' encoders produced can be put back
// into an existing PDF from OUTSIDE the module. This drives the whole route --
// Inspect to find the image, ExtractPageRaster to get its pixels, a smaller
// raster back in through ReplaceImages -- and checks the substitution reached
// the file rather than only the process's copy of the dictionary.
func TestReplaceImagesSubstitutesAnImageStream(t *testing.T) {
	in := corpusDoc(t, "scan")

	before := inspect(t, "scan")
	if len(before) != 1 || len(before[0].Images) != 1 {
		t.Fatalf("fixture is %d pages with %d images on page 1; want 1 and 1", len(before), len(before[0].Images))
	}
	ref := before[0].Images[0]
	if ref.ObjNr <= 0 {
		t.Fatalf("ImageRef.ObjNr = %d; want the image XObject's PDF object number", ref.ObjNr)
	}

	pr, err := ExtractPageRaster(bytes.NewReader(in), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	small, nw, nh := halveGray(pr.Image)
	if nw >= ref.Width || nh >= ref.Height {
		t.Fatalf("the substituted raster is not smaller: %dx%d -> %dx%d", ref.Width, ref.Height, nw, nh)
	}
	if nw == nh {
		t.Fatalf("the substituted raster is square (%dx%d); this test cannot catch a transposed width/height", nw, nh)
	}

	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: {
			Width:      nw,
			Height:     nh,
			BPC:        8,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "FlateDecode",
			Data:       flateEncode(t, small),
		},
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	after, err := Inspect(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Inspect after substitution: %v", err)
	}
	if len(after) != 1 || len(after[0].Images) != 1 {
		t.Fatalf("after substitution: %d pages with %d images on page 1; want 1 and 1", len(after), len(after[0].Images))
	}
	got := after[0].Images[0]
	if got.Width != nw || got.Height != nh {
		t.Errorf("stored raster is %dx%d; want %dx%d", got.Width, got.Height, nw, nh)
	}
	// Placement is a CTM on the unit square, so a raster of different pixel
	// dimensions must land in exactly the same place.
	if got.Bounds != ref.Bounds {
		t.Errorf("Bounds moved: %v -> %v", ref.Bounds, got.Bounds)
	}
	if got.Placement != ref.Placement {
		t.Errorf("Placement changed: %v -> %v", ref.Placement, got.Placement)
	}

	// The pixels, not just the dictionary: decode the substituted stream back
	// out of the written file.
	pr2, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster after substitution: %v", err)
	}
	if b := pr2.Image.Bounds(); b.Dx() != nw || b.Dy() != nh {
		t.Fatalf("extracted raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), nw, nh)
	}
	if gotPix := graySamples(pr2.Image); !bytes.Equal(gotPix, small) {
		t.Errorf("extracted pixels are not the substituted raster (%d bytes vs %d)", len(gotPix), len(small))
	}
}

// One image XObject can serve several pages, and ImageRef.ObjNr is the only
// signal a caller has that it does. Substituting it once must change every
// page that paints it -- the caller memoizes per object number, which it
// cannot do if the seam is keyed by page.
func TestReplaceImagesSubstitutesAnXObjectSharedByTwoPages(t *testing.T) {
	in := corpus.ScanJPEG()

	before, err := Inspect(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("fixture has %d pages; want 2", len(before))
	}
	objNr := before[0].Images[0].ObjNr
	if before[1].Images[0].ObjNr != objNr {
		t.Fatalf("the fixture's two pages name different image objects (%d, %d); this test proves nothing",
			objNr, before[1].Images[0].ObjNr)
	}

	const nw, nh = 40, 30
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		objNr: flateGrayImage(t, nw, nh, 1),
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	after, err := Inspect(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Inspect after substitution: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("after substitution: %d pages; want 2", len(after))
	}
	for i, p := range after {
		if len(p.Images) != 1 {
			t.Fatalf("page %d has %d images; want 1", i+1, len(p.Images))
		}
		if p.Images[0].Width != nw || p.Images[0].Height != nh {
			t.Errorf("page %d stored raster is %dx%d; want %dx%d",
				i+1, p.Images[0].Width, p.Images[0].Height, nw, nh)
		}
	}
}

// An image is only substitutable once the document has resolved it, and that
// happens by walking a page's content stream. Nothing says which page paints a
// given object, so every page has to be walked: a seam that resolved only page
// 1 would work on the single-page fixtures above and refuse the second page of
// every real scan.
func TestReplaceImagesSubstitutesAnImageOnALaterPage(t *testing.T) {
	in := corpusDoc(t, "mixed")

	before, err := Inspect(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(before) != 2 || len(before[0].Images) != 0 || len(before[1].Images) != 1 {
		t.Fatalf("fixture is not born-digital-then-scan: %d pages, %d and %d images",
			len(before), len(before[0].Images), len(before[1].Images))
	}

	const nw, nh = 40, 30
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		before[1].Images[0].ObjNr: flateGrayImage(t, nw, nh, 2),
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	after, err := Inspect(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Inspect after substitution: %v", err)
	}
	if len(after) != 2 || len(after[1].Images) != 1 {
		t.Fatalf("after substitution: %d pages, %d images on page 2", len(after), len(after[1].Images))
	}
	if got := after[1].Images[0]; got.Width != nw || got.Height != nh {
		t.Errorf("page 2 stored raster is %dx%d; want %dx%d", got.Width, got.Height, nw, nh)
	}
}

// What the P1 on byb-fp6 actually buys: an image one of Byblos' own encoders
// produced reaching a real document. QuantizeIndexed (byb-96p) emits /Indexed
// samples under /FlateDecode with a PNG predictor, and until this seam was
// exported there was no route for those bytes into an existing PDF at all.
//
// This is not a test of QuantizeIndexed; it is a test that the seam carries
// the whole shape -- palette, 4 bits per component, /DecodeParms -- and that
// the page still extracts afterwards, which a JBIG2 substitution would not
// (extract.go diverts codecs Byblos cannot decode).
func TestReplaceImagesCarriesAQuantizedIndexedImageIntoAnExistingPDF(t *testing.T) {
	in := corpusDoc(t, "scan")

	before := inspect(t, "scan")
	ref := before[0].Images[0]
	pr, err := ExtractPageRaster(bytes.NewReader(in), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	enc, err := QuantizeIndexed(pr.Image, 16)
	if err != nil {
		t.Fatalf("QuantizeIndexed: %v", err)
	}
	if enc.ColorSpace.Name != "Indexed" || enc.DecodeParms == nil {
		t.Fatalf("QuantizeIndexed produced %+v with parms %+v; this test is aimed at the Indexed shape",
			enc.ColorSpace, enc.DecodeParms)
	}

	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{ref.ObjNr: enc}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	after, err := Inspect(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Inspect after substitution: %v", err)
	}
	if got := after[0].Images[0]; got.Width != enc.Width || got.Height != enc.Height {
		t.Errorf("stored raster is %dx%d; want %dx%d", got.Width, got.Height, enc.Width, enc.Height)
	}
	pr2, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster after substitution: %v", err)
	}
	if b := pr2.Image.Bounds(); b.Dx() != enc.Width || b.Dy() != enc.Height {
		t.Errorf("extracted raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), enc.Width, enc.Height)
	}

	// Width/Height and a decodable raster can be satisfied by leaving the
	// ORIGINAL stream untouched, since QuantizeIndexed preserves pixel
	// dimensions. Only these checks prove the substitution actually reached
	// the file: the quantized stream's own bytes, its bit depth, and its
	// palette/predictor shape.
	if !bytes.Contains(out.Bytes(), enc.Data) {
		t.Error("written PDF does not contain the quantized stream's bytes; substitution did not happen")
	}
	if want := []byte(fmt.Sprintf("/BitsPerComponent %d", enc.BPC)); !bytes.Contains(out.Bytes(), want) {
		t.Errorf("written PDF does not declare %s", want)
	}
	if !bytes.Contains(out.Bytes(), []byte("/Indexed")) {
		t.Error("written PDF does not declare an /Indexed colour space")
	}
	if !bytes.Contains(out.Bytes(), []byte("/Predictor")) {
		t.Error("written PDF does not carry /DecodeParms /Predictor")
	}
}

// A substitution that cannot be made correctly must fail loudly and write
// nothing: a half-substituted document that opens cleanly is the failure this
// seam must not produce.
func TestReplaceImagesRefusesWhatItCannotDoCorrectly(t *testing.T) {
	good := flateGrayImage(t, 8, 8, 3)

	t.Run("no substitutions", func(t *testing.T) {
		var out bytes.Buffer
		if err := ReplaceImages(&out, bytes.NewReader(corpusDoc(t, "scan")), nil); err == nil {
			t.Error("an empty substitution map was accepted")
		}
		if out.Len() != 0 {
			t.Errorf("wrote %d bytes for a call that failed", out.Len())
		}
	})

	t.Run("object number is not an image on this document", func(t *testing.T) {
		var out bytes.Buffer
		err := ReplaceImages(&out, bytes.NewReader(corpusDoc(t, "scan")), map[int]EncodedImage{9999: good})
		if err == nil {
			t.Error("substituting an object number the document has no image for succeeded")
		}
		if out.Len() != 0 {
			t.Errorf("wrote %d bytes for a call that failed", out.Len())
		}
	})

	// Inherited from the seam, not re-derived here: an image carrying /SMask
	// describes transparency keyed to the samples being replaced (write.go).
	t.Run("smask", func(t *testing.T) {
		in := corpus.ScanSMaskJPEG()
		pages, err := Inspect(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if len(pages) != 1 || len(pages[0].Images) != 1 {
			t.Fatalf("fixture is %d pages with %d images on page 1; want 1 and 1", len(pages), len(pages[0].Images))
		}
		var out bytes.Buffer
		err = ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{pages[0].Images[0].ObjNr: good})
		if err == nil {
			t.Error("substituting an image that carries an /SMask succeeded")
		}
		if out.Len() != 0 {
			t.Errorf("wrote %d bytes for a call that failed", out.Len())
		}
	})
}
