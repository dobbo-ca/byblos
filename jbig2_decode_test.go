package byblos

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// jbig2TestBitmap is a deterministic bitmap chosen to be hostile to the bugs a
// friendly fixture hides: the dimensions are unequal (so a transposed width and
// height shows), the width is not a multiple of 8 (so row padding is live), and
// the content is asymmetric in both axes (so an off-by-one in placement or a
// mirrored row order shows).
func jbig2TestBitmap() *Bitmap {
	b := NewBitmap(101, 73)
	for y := 0; y < b.Height; y++ {
		for x := 0; x < b.Width; x++ {
			switch {
			case y == 0, x == 0: // a solid top row and left column
				b.Set(x, y, 1)
			case (x*7+y*13)%11 < 4:
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// TestDecodeJBIG2GenericRoundTripsTheEncoder is the public-API half of the
// byb-riy acceptance: whatever EncodeJBIG2Generic writes, DecodeJBIG2Generic
// must read back bit-identically.
//
// The internal round trip in internal/jbig2 proves the codec. This proves the
// two exported functions are actually wired to it in the same polarity and the
// same packing -- the seam where an inversion would be introduced.
func TestDecodeJBIG2GenericRoundTripsTheEncoder(t *testing.T) {
	for _, want := range []*Bitmap{
		jbig2TestBitmap(),
		NewBitmap(1, 1),
		NewBitmap(64, 8),
		func() *Bitmap { // all ink, non-byte-aligned
			b := NewBitmap(13, 11)
			for y := 0; y < b.Height; y++ {
				for x := 0; x < b.Width; x++ {
					b.Set(x, y, 1)
				}
			}
			return b
		}(),
	} {
		data, err := EncodeJBIG2Generic(want.Clone())
		if err != nil {
			t.Fatalf("%dx%d: EncodeJBIG2Generic: %v", want.Width, want.Height, err)
		}
		got, err := DecodeJBIG2Generic(data)
		if err != nil {
			t.Fatalf("%dx%d: DecodeJBIG2Generic: %v", want.Width, want.Height, err)
		}
		if !got.Equal(want) {
			t.Errorf("%dx%d: round trip is not bit-identical", want.Width, want.Height)
		}
	}
}

// TestDecodeJBIG2GenericRejectsNonGeneric pins the documented residue as
// behaviour rather than as a doc comment. Symbol-mode JBIG2 -- what most archive
// scanners emit, and most of the 6,818 unsupported-codec-jbig2 pages on byb-riy
// -- must come back as ErrUnsupportedJBIG2Feature and NO bitmap.
//
// A returned bitmap here would be the worst outcome this package can produce:
// the MQ decoder yields a decision for any input, so decoding a symbol
// dictionary as a generic region succeeds and gives back noise.
func TestDecodeJBIG2GenericRejectsNonGeneric(t *testing.T) {
	base, err := EncodeJBIG2Generic(jbig2TestBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	// Segment type lives in the flags byte of the segment header. The generic
	// region segment is the second one, after an 11-byte header and a 19-byte
	// page information body.
	const regionTypeAt = 11 + 19 + 4
	if base[regionTypeAt] != 39 {
		t.Fatalf("offset assumption broken: segment type byte = %d, want 39", base[regionTypeAt])
	}
	for name, typ := range map[string]byte{
		"symbol-dictionary": 0,
		"text-region":       6,
		"halftone-region":   22,
		"refinement-region": 42,
	} {
		t.Run(name, func(t *testing.T) {
			s := bytes.Clone(base)
			s[regionTypeAt] = typ
			got, err := DecodeJBIG2Generic(s)
			if err == nil {
				t.Fatalf("DecodeJBIG2Generic returned a %dx%d bitmap; want ErrUnsupportedJBIG2Feature",
					got.Width, got.Height)
			}
			if !errors.Is(err, ErrUnsupportedJBIG2Feature) {
				t.Fatalf("error = %v; want ErrUnsupportedJBIG2Feature", err)
			}
			if got != nil {
				t.Error("a bitmap was returned alongside the error")
			}
		})
	}

	// A corrupt stream is a plain error, NOT ErrUnsupportedJBIG2Feature: the
	// difference is what tells an archive "a future decoder recovers this page"
	// from "these bytes are damaged".
	if _, err := DecodeJBIG2Generic([]byte("not a jbig2 stream")); err == nil {
		t.Error("DecodeJBIG2Generic on garbage: want an error")
	} else if errors.Is(err, ErrUnsupportedJBIG2Feature) {
		t.Errorf("garbage reported as an unsupported FEATURE: %v", err)
	}
}

// TestExtractPageRasterDecodesASubstitutedJBIG2Page is byb-riy's headline claim,
// end to end and through nothing but the public API.
//
// byb-fp6 measured the bug in exactly this shape: ExtractPageRaster succeeded on
// a document, and returned ErrUnsupportedImageCodec after a JBIG2 substitution
// through ReplaceImages -- byblos producing a document it could not read back.
// This drives that same route and requires the pixels to survive it.
//
// The comparison is on PIXELS, not on "no error". Ink must come back as black
// (/DeviceGray 0) and background as white, which is the inversion across the
// Bitmap-to-PDF boundary that a decoder gets wrong in the one way no error
// reports: a perfect photographic negative of the page.
func TestExtractPageRasterDecodesASubstitutedJBIG2Page(t *testing.T) {
	in := corpusDoc(t, "scan")
	before := inspect(t, "scan")
	if len(before) != 1 || len(before[0].Images) != 1 {
		t.Fatalf("fixture is %d pages with %d images on page 1; want 1 and 1", len(before), len(before[0].Images))
	}
	ref := before[0].Images[0]

	src := jbig2TestBitmap()
	data, err := EncodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}

	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: {
			Width:      src.Width,
			Height:     src.Height,
			BPC:        1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       data,
		},
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	pr, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster after a JBIG2 substitution: %v "+
			"(this is byb-riy: byblos wrote a document it cannot read back)", err)
	}
	b := pr.Image.Bounds()
	if b.Dx() != src.Width || b.Dy() != src.Height {
		t.Fatalf("extracted raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), src.Width, src.Height)
	}
	var wrong, ink int
	var firstX, firstY int
	for y := 0; y < src.Height; y++ {
		for x := 0; x < src.Width; x++ {
			want := uint8(0xFF) // background is white in /DeviceGray
			if src.At(x, y) != 0 {
				want = 0x00 // ink is black
				ink++
			}
			got := color.GrayModel.Convert(pr.Image.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			if got != want {
				if wrong == 0 {
					firstX, firstY = x, y
				}
				wrong++
			}
		}
	}
	if ink == 0 {
		t.Fatal("the fixture has no ink; this test could not tell an inversion from a match")
	}
	if wrong != 0 {
		t.Errorf("%d of %d pixels differ, first at (%d,%d); the JBIG2 round trip through the PDF is not lossless",
			wrong, src.Width*src.Height, firstX, firstY)
	}
}

// TestExtractPageRasterRejectsJBIG2ItCannotDecode is the other half: wiring the
// decoder in must not turn an undecodable stream into a raster. The stream here
// is symbol-mode, which byblos does not implement and which is the bulk of the
// inbound JBIG2 population, so the page must still divert -- with the same
// reason string and the same sentinel as before this decoder existed.
func TestExtractPageRasterRejectsJBIG2ItCannotDecode(t *testing.T) {
	in := corpusDoc(t, "scan")
	ref := inspect(t, "scan")[0].Images[0]

	src := jbig2TestBitmap()
	data, err := EncodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	// Retype the region segment as a text region: a legal segment type this
	// package does not decode.
	data[11+19+4] = 6

	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: {
			Width: src.Width, Height: src.Height, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       data,
		},
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}

	pr, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err == nil {
		t.Fatalf("ExtractPageRaster returned a %v raster for a text-region stream; want a divert",
			pr.Image.Bounds())
	}
	if !errors.Is(err, ErrUnsupportedImageCodec) {
		t.Fatalf("error = %v; want ErrUnsupportedImageCodec", err)
	}
	if errors.Is(err, ErrNotSingleRaster) {
		t.Error("a JBIG2 page-covering scan IS a single raster; it must not also report ErrNotSingleRaster")
	}
}

// TestDecodeJBIG2PlacementGuardsTheDictionary covers the two dictionary facts
// that can make a perfectly decoded bitmap the wrong answer. Both fail
// silently if unguarded, which is why they are checked rather than assumed:
//
//   - /Decode [1 0] on a 1-bit image inverts it, so the page would come back as
//     its own photographic negative with no error anywhere. ImageInfo records
//     only that the array is PRESENT, so there is nothing to apply.
//   - a /Width and /Height that disagree with the JBIG2 page mean the stream is
//     not the one this dictionary describes. The transposed case below is the
//     sharp one: 73x101 against a 101x73 page has the same pixel count, so a
//     check on area alone would pass it.
//
// This drives decodeJBIG2Placement directly. Going through a real PDF would
// mean splicing /Decode into a written file, and splicing bytes into a PDF
// invalidates its cross-reference table -- the document then fails to open,
// which is a different error reaching this assertion for the wrong reason.
func TestDecodeJBIG2PlacementGuardsTheDictionary(t *testing.T) {
	src := jbig2TestBitmap()
	data, err := EncodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	info := func(i pdfdoc.ImageInfo, ok bool) func(int) (pdfdoc.ImageInfo, bool) {
		return func(int) (pdfdoc.ImageInfo, bool) { return i, ok }
	}
	good := pdfdoc.ImageInfo{Width: src.Width, Height: src.Height}

	// The control: with a dictionary that agrees, the raster comes back.
	img, err := decodeJBIG2Placement(data, info(good, true), 7)
	if err != nil {
		t.Fatalf("decodeJBIG2Placement on a matching dictionary: %v", err)
	}
	if _, ok := img.(*image.Gray); !ok {
		t.Errorf("decoded raster is %T; want *image.Gray, which Downsample and optimize type-switch on", img)
	}

	for name, tc := range map[string]struct {
		info pdfdoc.ImageInfo
		ok   bool
	}{
		"decode-array":         {pdfdoc.ImageInfo{Width: src.Width, Height: src.Height, Decode: true}, true},
		"transposed-size":      {pdfdoc.ImageInfo{Width: src.Height, Height: src.Width}, true},
		"wrong-width":          {pdfdoc.ImageInfo{Width: src.Width + 1, Height: src.Height}, true},
		"no-dictionary":        {pdfdoc.ImageInfo{}, false},
		"zero-size-dictionary": {pdfdoc.ImageInfo{}, true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeJBIG2Placement(data, info(tc.info, tc.ok), 7)
			if err == nil {
				t.Fatalf("decodeJBIG2Placement returned a %v raster; want an error", got.Bounds())
			}
		})
	}
}
