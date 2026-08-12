package byblos

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/jbig2"
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

// grayInfo is an image dictionary that says what the /Decode-applying path
// needs said: a matching size and a resolved /DeviceGray colour space. Tests
// that are not about the colour space start from this and change one field.
func grayInfo(w, h int) pdfdoc.ImageInfo {
	return pdfdoc.ImageInfo{Width: w, Height: h, BPC: 1, ColorSpace: "DeviceGray"}
}

// TestDecodeJBIG2PlacementAppliesTheDecodeArray is byb-e7n's acceptance in one
// function: an array byblos can read is APPLIED, not refused.
//
// It asserts against the table in grayLevels, which is poppler's answer and not
// this package's opinion -- so the cases are the rows of that table, and the
// two non-flip rows are the ones that matter. Reading /Decode as "invert when
// the array is [1 0]" passes the first three cases and fails the last two, and
// that is the wrong reading this exists to catch.
//
// The ink count guard is the same one TestExtractPageRasterDecodesASubstituted-
// JBIG2Page carries: a fixture with no ink cannot tell an inversion from a
// match, and every assertion below would hold vacuously on a blank page.
func TestDecodeJBIG2PlacementAppliesTheDecodeArray(t *testing.T) {
	src := jbig2TestBitmap()
	data, err := EncodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	var ink int
	for y := 0; y < src.Height; y++ {
		for x := 0; x < src.Width; x++ {
			if src.At(x, y) != 0 {
				ink++
			}
		}
	}
	if ink == 0 || ink == src.Width*src.Height {
		t.Fatalf("the fixture is %d ink pixels of %d; a uniform bitmap could not tell "+
			"any of these levels apart", ink, src.Width*src.Height)
	}

	for _, tc := range []struct {
		name             string
		decode           []float64
		wantInk, wantBkg uint8
	}{
		{"absent", nil, 0x00, 0xFF},
		{"default", []float64{0, 1}, 0x00, 0xFF},
		{"inverted", []float64{1, 0}, 0xFF, 0x00},
		{"grey-ink", []float64{0.5, 1}, 0x80, 0xFF},
		{"grey-background", []float64{0, 0.5}, 0x00, 0x80},
		{"flat-white", []float64{1, 1}, 0xFF, 0xFF},
		{"out-of-range-clamps", []float64{2, -1}, 0xFF, 0x00},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := grayInfo(src.Width, src.Height)
			if tc.decode != nil {
				info.Decode, info.DecodeArray = true, tc.decode
			}
			img, err := decodeJBIG2Placement(data, func(int) (pdfdoc.ImageInfo, bool) {
				return info, true
			}, 7)
			if err != nil {
				t.Fatalf("decodeJBIG2Placement with /Decode %v: %v", tc.decode, err)
			}
			g, ok := img.(*image.Gray)
			if !ok {
				t.Fatalf("raster is %T; want *image.Gray", img)
			}
			var wrong, firstX, firstY int
			var gotAt uint8
			for y := 0; y < src.Height; y++ {
				for x := 0; x < src.Width; x++ {
					want := tc.wantBkg
					if src.At(x, y) != 0 {
						want = tc.wantInk
					}
					got := g.Pix[y*g.Stride+x]
					if got != want {
						if wrong == 0 {
							firstX, firstY, gotAt = x, y, got
						}
						wrong++
					}
				}
			}
			if wrong != 0 {
				t.Errorf("/Decode %v: %d of %d pixels are wrong, first at (%d,%d) = %#02x; "+
					"want ink %#02x and background %#02x. poppler renders this array as those "+
					"two levels (see grayLevels).",
					tc.decode, wrong, src.Width*src.Height, firstX, firstY, gotAt,
					tc.wantInk, tc.wantBkg)
			}
		})
	}
}

// TestDecodeJBIG2PlacementRefusesADecodeArrayItCannotRead pins the other half
// of byb-e7n: the arrays that are NOT applied, and are not applied because the
// numbers do not mean grey levels there.
//
// Every one of these was a divert before byb-e7n and is a divert after it, so
// nothing here is a regression -- what is new is that the refusal is narrow.
// The failure mode it guards against is a widening: reading /Decode [1 0] in an
// /Indexed space as an inversion returns a raster that is not a negative of
// anything, it is a different image (grayLevels records poppler's numbers).
//
// The message substring is asserted rather than just the error, and the shared
// "/Decode array" prefix is deliberate: jbig2_symbol_probe_test.go's census
// classifies this gate by that phrase, so a refusal that drops it is a refusal
// no measurement can see.
func TestDecodeJBIG2PlacementRefusesADecodeArrayItCannotRead(t *testing.T) {
	src := jbig2TestBitmap()
	data, err := EncodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	for _, tc := range []struct {
		name string
		info func(pdfdoc.ImageInfo) pdfdoc.ImageInfo
		want string
	}{
		{"unresolved-colour-space", func(i pdfdoc.ImageInfo) pdfdoc.ImageInfo {
			i.ColorSpace = ""
			i.Decode, i.DecodeArray = true, []float64{1, 0}
			return i
		}, "against an array or an indirect reference"},
		{"device-rgb", func(i pdfdoc.ImageInfo) pdfdoc.ImageInfo {
			i.ColorSpace = "DeviceRGB"
			i.Decode, i.DecodeArray = true, []float64{1, 0}
			return i
		}, "against /DeviceRGB"},
		{"unreadable-entries", func(i pdfdoc.ImageInfo) pdfdoc.ImageInfo {
			i.Decode, i.DecodeArray = true, nil
			return i
		}, "entries are not all numbers"},
		{"six-entries", func(i pdfdoc.ImageInfo) pdfdoc.ImageInfo {
			i.Decode, i.DecodeArray = true, []float64{0, 1, 0, 1, 0, 1}
			return i
		}, "array of 6 entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.info(grayInfo(src.Width, src.Height))
			got, err := decodeJBIG2Placement(data, func(int) (pdfdoc.ImageInfo, bool) {
				return info, true
			}, 7)
			if err == nil {
				t.Fatalf("decodeJBIG2Placement returned a %v raster; want a refusal", got.Bounds())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v; want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "/Decode array") {
				t.Errorf("error = %v; want it to name the /Decode array, which is how the "+
					"byb-9v0 census attributes this gate", err)
			}
		})
	}
}

// TestDecodeJBIG2PlacementReadsTheDecodeArrayBeforeOpeningTheStream keeps
// byb-e7n from quietly undoing the ordering argument decodeJBIG2Placement's own
// comment calls load-bearing.
//
// /Decode is a dictionary fact and stays the FIRST check, ahead of the pixel
// limit and the page-size comparison, so a refusal costs nothing. The
// observable form of that is the same one the size gate uses: hold the
// dictionary fixed, vary the stream across shapes that fail for unrelated
// reasons, and require the identical error. Only a check that never opens the
// stream can produce it.
func TestDecodeJBIG2PlacementReadsTheDecodeArrayBeforeOpeningTheStream(t *testing.T) {
	valid, err := EncodeJBIG2Generic(jbig2TestBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	// Refused on the colour space, and sized so that neither the pixel limit
	// nor the size comparison could be what refuses it instead.
	info := func(int) (pdfdoc.ImageInfo, bool) {
		return pdfdoc.ImageInfo{Width: 101, Height: 73, BPC: 1,
			Decode: true, DecodeArray: []float64{1, 0}}, true
	}
	var first, firstName string
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"too-short-for-a-segment-header", []byte("garbage")},
		{"empty", nil},
		{"valid-and-the-right-size", valid},
	} {
		got, err := decodeJBIG2Placement(c.data, info, 7)
		if err == nil {
			t.Fatalf("%s: an unreadable /Decode array must be refused", c.name)
		}
		if got != nil {
			t.Fatalf("%s: a refusal returned a %v raster", c.name, got.Bounds())
		}
		if first == "" {
			first, firstName = err.Error(), c.name
			continue
		}
		if err.Error() != first {
			t.Errorf("the refusal depends on the stream, so the /Decode check is no longer "+
				"reading the dictionary alone:\n  %s: %s\n  %s: %s",
				firstName, first, c.name, err.Error())
		}
	}
	if !strings.Contains(first, "/Decode array") {
		t.Errorf("the refusal does not name the /Decode array: %s", first)
	}
}

// TestDecodeJBIG2PlacementGuardsTheDictionary covers the dictionary facts that
// can make a perfectly decoded bitmap the wrong answer. They fail silently if
// unguarded, which is why they are checked rather than assumed:
//
//   - a /Width and /Height that disagree with the JBIG2 page mean the stream is
//     not the one this dictionary describes. The transposed case below is the
//     sharp one: 73x101 against a 101x73 page has the same pixel count, so a
//     check on area alone would pass it.
//   - a dictionary that is not there at all, which without its own branch is
//     read as a ZERO one and refused for saying 0x0.
//
// /Decode used to be a third entry here and is not one any more: byb-e7n
// applies the array, and TestDecodeJBIG2PlacementAppliesTheDecodeArray plus
// TestDecodeJBIG2PlacementRefusesADecodeArrayItCannotRead replace this case.
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

	// Each case names the substring its refusal must carry. "There was an
	// error" is not enough here: the last two cases are the same absent
	// dictionary reported two different ways, and without the "!ok" branch the
	// missing dictionary is silently read as a ZERO one and refused by the size
	// comparison instead -- "page is 101x73 but image 7's dictionary says 0x0",
	// which describes a dictionary that says nothing of the kind because there
	// is no dictionary. A caller reading that goes looking at the PDF for a
	// /Width of 0.
	for name, tc := range map[string]struct {
		info pdfdoc.ImageInfo
		ok   bool
		want string
	}{
		"transposed-size":      {pdfdoc.ImageInfo{Width: src.Height, Height: src.Width}, true, "dictionary says"},
		"wrong-width":          {pdfdoc.ImageInfo{Width: src.Width + 1, Height: src.Height}, true, "dictionary says"},
		"no-dictionary":        {pdfdoc.ImageInfo{}, false, "has no dictionary to check the decoded raster against"},
		"zero-size-dictionary": {pdfdoc.ImageInfo{}, true, "dictionary says 0x0"},
		// The size gate in FRONT of the size comparison, and the reason it needs
		// a case of its own: an oversize dictionary also disagrees with the page,
		// so the comparison below would refuse this stream anyway and the gate's
		// removal is invisible in the verdict. What the gate buys is the verdict
		// arriving without the stream being opened -- so this case asserts the
		// gate's OWN message. Loosen the gate (measured at 4x) and the refusal
		// becomes "page is 101x73 but ... dictionary says 8192x8193", which is a
		// true statement reached by parsing headers that never needed parsing,
		// and on a stream at the segment cap that is 10.6 MiB of parsing.
		//
		// 8192x8193 is one row past jbig2.MaxPagePixels, so the gate is what
		// refuses it and nothing else can be.
		"oversize-dictionary": {pdfdoc.ImageInfo{Width: 8192, Height: 8193}, true,
			"byblos renders a bilevel page at one byte per pixel"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeJBIG2Placement(data, info(tc.info, tc.ok), 7)
			if err == nil {
				t.Fatalf("decodeJBIG2Placement returned a %v raster; want an error", got.Bounds())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v; want it to contain %q, which names the dictionary fact "+
					"that is wrong. Every one of these is refused whatever the message says; "+
					"the message is what tells a caller which field to look at.", err, tc.want)
			}
		})
	}
}

// hostileJBIG2Stream builds a JBIG2 stream by hand -- a page information
// segment and one immediate generic region segment carrying NO coded data --
// so a test can name any page and region size it likes without encoding one.
//
// The MQ decoder returns a decision for any input and reads 1 bits past the end
// of its own, so a region with no coded data decodes at exactly the size its
// header claims. That is the whole shape of the attack these bounds exist for:
// the cost comes out of the HEADERS, not out of the data.
func hostileJBIG2Stream(pageW, pageH, regionW, regionH int) []byte {
	seg := func(num uint32, typ byte, data []byte) []byte {
		h := binary.BigEndian.AppendUint32(nil, num)
		h = append(h, typ, 0x00, 0x01)
		h = binary.BigEndian.AppendUint32(h, uint32(len(data)))
		return append(h, data...)
	}
	pi := binary.BigEndian.AppendUint32(nil, uint32(pageW))
	pi = binary.BigEndian.AppendUint32(pi, uint32(pageH))
	pi = binary.BigEndian.AppendUint32(pi, 0)
	pi = binary.BigEndian.AppendUint32(pi, 0)
	pi = append(pi, 0x01, 0x00, 0x00)

	r := binary.BigEndian.AppendUint32(nil, uint32(regionW))
	r = binary.BigEndian.AppendUint32(r, uint32(regionH))
	r = binary.BigEndian.AppendUint32(r, 0)
	r = binary.BigEndian.AppendUint32(r, 0)
	r = append(r, 0x00, 0x00) // combination operator OR; template 0, TPGDON off
	r = append(r, 0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE)

	return append(seg(0, 48, pi), seg(1, 38, r)...)
}

// TestExtractPageRasterBoundsTheCostOfAHostileJBIG2Page pins the DoS bound where
// it is actually reachable -- a hostile PDF handed to the public
// ExtractPageRaster -- and it pins it FROM ABOVE, which is the thing two rounds
// of this work were missing.
//
// The stream is the most ALLOCATING one every gate admits AT 67 BYTES: a page
// just under jbig2.MaxPagePixels with a 1x1 region on it, so nothing is refused
// and grayImage expands the whole page to one byte per pixel. Its size is
// derived from the constant, so raising the constant raises this test's cost;
// the ceiling it is measured against is a LITERAL, so raising the constant fails
// here. Measured across the three multipliers: x2, x3 and x10 all push the
// page's packed bitmap past the 16 MiB memory budget, so the page is refused
// outright and the "must still decode" fatal below fires.
//
// That qualifier was missing until round 7 and its absence was a real defect,
// not pedantry: this test said "the worst the gates admit" and a reader took the
// 88 MiB as a bound on the whole path. It is not. A 67-byte stream cannot carry
// segment HEADERS, and the header cost internal/jbig2's rule 5 concedes is what
// gets past 88 MiB -- see TestExtractPageRasterCeilingBoundsEveryStreamTheGatesAdmit
// below, which bounds the admitted set. What this test still holds, and holds
// tightly, is the PAGE-SIZE axis: 1.2x the measurement, so any regression that
// stops this path being O(page) fails here first and with a smaller stream.
//
// Round 1 of this work reported a 264,000x improvement and passed its whole
// suite while this same path turned 67 bytes into 8.36 seconds and 640 MiB. The
// reason no test caught it is that every test measured the stream shape its own
// author had in mind. This one measures ALLOCATION on the real entry point, so
// it does not need to have guessed the shape right.
func TestExtractPageRasterBoundsTheCostOfAHostileJBIG2Page(t *testing.T) {
	// The largest square page the pixel budget admits with a 1x1 region beside
	// it. 8191x8191 at MaxPagePixels = 1<<26; the arithmetic follows the
	// constant so that a mutation of the constant moves the cost.
	side := int(math.Sqrt(float64(jbig2.MaxPagePixels - 1)))
	data := hostileJBIG2Stream(side, side, 1, 1)

	in := corpusDoc(t, "scan")
	ref := inspect(t, "scan")[0].Images[0]
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: {
			Width: side, Height: side, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       data,
		},
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	doc := out.Bytes()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	pr, err := ExtractPageRaster(bytes.NewReader(doc), 1)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	grew := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d bytes of JBIG2 in a %d byte PDF, page %dx%d: err = %v, elapsed = %v, "+
		"TotalAlloc grew by %.1f MiB", len(data), len(doc), side, side, err, elapsed,
		float64(grew)/(1<<20))

	if err != nil {
		t.Fatalf("the worst stream the budgets ADMIT must still decode, or the bound is "+
			"pinned only from below and this test proves nothing: %v", err)
	}
	if b := pr.Image.Bounds(); b.Dx() != side || b.Dy() != side {
		t.Fatalf("raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), side, side)
	}

	// 640 MiB was measured on this path before any of this work. 72.3 MiB is what
	// it costs now: a 64 MiB *image.Gray, the packed page beneath it, and the PDF
	// machinery around both. The ceiling is a literal on purpose -- writing it in
	// terms of jbig2.MaxPagePixels would make it move with the mutation it exists
	// to catch -- and it is set at about 1.2x the measurement, the same headroom
	// it carried when the budget was half this size.
	const ceiling = 88 << 20
	if grew > ceiling {
		t.Errorf("the worst admitted JBIG2 page allocated %.1f MiB from %d bytes of stream; "+
			"the ceiling is %d MiB. Either jbig2.MaxPagePixels went up or something on this "+
			"path stopped being O(page).", float64(grew)/(1<<20), len(data), ceiling>>20)
	}
}

// decodeJBIG2GenericNoPanic calls DecodeJBIG2Generic and turns a panic into a
// failure of the CALLING SUBTEST rather than a crashed test binary.
//
// It is not softening anything: the panic is still a failure and the message
// still carries the stack that names the line, but one missing length guard then
// fails one subtest instead of taking the whole package's tests down with it and
// hiding the other three.
func decodeJBIG2GenericNoPanic(t *testing.T, s []byte) (b *Bitmap, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DecodeJBIG2Generic PANICKED on a %d-byte stream (% 02X): %v\n\n%s",
				len(s), s, r, debug.Stack())
		}
	}()
	return DecodeJBIG2Generic(s)
}

// TestDecodeJBIG2GenericRefusesShortHeadersInsteadOfPanicking pins the six
// LENGTH GUARDS that stand between a truncated JBIG2 header and an index or
// slice-bounds panic inside the decoder.
//
// All six guards are present and correct at this tip. What was missing is any
// test that notices their removal: each of the six deletions -- the 11-byte
// minimum on a segment header, the check that the header's variable-width fields
// do not run past the stream, the 19-byte minimum on a page information body,
// the 17-byte minimum on a region segment information field, and the 18- and
// 26-byte minima on a generic region body -- builds clean and passes the entire
// suite, while turning the corresponding stream below into a PANIC out of the
// exported DecodeJBIG2Generic.
//
// A panic is the one failure mode this API cannot have. DecodeJBIG2Generic runs
// on bytes taken straight out of an untrusted PDF -- that is extract.go's JBIG2
// branch, wired up by byb-riy -- so an attacker picks these bytes, and a caller
// that can route around an error cannot route around a crashed process. Every
// one of these streams is under 70 bytes.
//
// The streams are LITERAL HEX rather than assembled by this package's encoder,
// and that is deliberate twice over. EncodeJBIG2Generic cannot produce any of
// them, so there is no builder to borrow; and a stream derived from a helper
// would move with the helper and quietly stop being the shape that panics. These
// bytes ARE the shape that panics, so they are checked in as bytes.
//
// EVERY ONE OF THE SIX IS PINNED AT ITS BOUNDARY as well as at its presence,
// and the two are different properties: a stream that is short by four bytes is
// refused by the correct guard and by three wrong ones, so it pins that a check
// exists and not where it sits. Each guard therefore has a case one byte inside
// it, and each of those cases was run RED against the guard loosened by one
// before it was trusted. The last of the six to get one is the header-length
// comparison below, whose fixture overshot by four bytes.
//
// Each case asserts the guard's OUTCOME, not merely the absence of a panic: the
// error has to name the field and the byte count that was actually short, which
// pins the diagnosis as well as the guard. And none of them may carry
// ErrUnsupportedJBIG2Feature -- a truncated header is DAMAGE, and filing damage
// under the sentinel that tells an archive "a future decoder recovers this page"
// is a misfiling the archive then acts on.
func TestDecodeJBIG2GenericRefusesShortHeadersInsteadOfPanicking(t *testing.T) {
	for _, c := range []struct {
		name string
		// The stream, as hex. Byte counts in the comments are of the DECODED
		// stream.
		stream string
		// A substring the refusal must contain. Each names both the structure
		// that was short and the length it actually had, so a guard replaced by a
		// vaguer one further down the parse also fails here.
		want string
		// What deleting the guard does instead, quoted from the run that found it.
		panics string
	}{
		// internal/jbig2/segment_decode.go, parseSegments: len(rest) < 11.
		//
		// A T.88 7.2 segment header is eleven bytes at its narrowest -- four of
		// segment number, one of flags, one referred-to count, one page
		// association and four of data length. This stream is FIVE bytes: a
		// segment number and a flags byte declaring type 48, and nothing else.
		// Without the guard the parse reads the referred-to count at rest[5] of a
		// 5-byte slice.
		{
			name:   "segment-header-under-11-bytes",
			stream: "0000000030",
			want:   "segment header at offset 0 is 5 bytes",
			panics: "index out of range [5] with length 5",
		},

		// The same guard AT ITS BOUNDARY, which the five-byte case above cannot
		// reach: five bytes is refused by a minimum of 11, of 10, of 9 and of 6,
		// so it pins that a minimum exists and not what it is.
		//
		// Ten bytes -- the eleven-byte header with the last byte of its data
		// length field cut off. A minimum of 10 admits it, and it is then refused
		// two fields later as a header that runs past the end of the stream. The
		// verdict is still a refusal; the DIAGNOSIS is wrong, and it points a
		// caller at the variable-width referred-to fields of T.88 7.2.4-7.2.6
		// when what is actually missing is the fixed tail of 7.2.7.
		{
			name:   "segment-header-exactly-10-bytes",
			stream: "00000000300001000000",
			want:   "segment header at offset 0 is 10 bytes",
			panics: "accepted by a minimum of 10 and misreported as a header running past the end",
		},

		// internal/jbig2/segment_decode.go, parseSegments:
		// i < 0 || i+4 > len(rest).
		//
		// The other end of the same header, and the one the 11-byte minimum
		// above does NOT cover: a segment header is eleven bytes at its
		// NARROWEST, and every one of the variable-width fields of T.88 7.2.4,
		// 7.2.5 and 7.2.6 makes it longer.
		//
		// AT THE BOUNDARY, and it has to be, because this guard is an
		// arithmetic comparison and not a minimum length: the case that pins it
		// must be over by EXACTLY ONE BYTE. This stream is eleven bytes and
		// spends them like this -- segment number 0, a flags byte of 0x27 (type
		// 39, bit 6 clear so the page association is one byte), a referred-to
		// count byte of 0x20 declaring ONE reference, one one-byte referred-to
		// segment number (one byte because this segment's own number is at most
		// 256, T.88 7.2.5) and a one-byte page association. That puts the
		// four-byte data length field of 7.2.7 at rest[8:12] of an eleven-byte
		// slice: one byte past the end and no more.
		//
		// The fixture this replaced declared FOUR references, which put the
		// field at rest[11:15] -- four bytes past the end. It killed the
		// guard's DELETION and survived every off-by-one: loosened to
		// "i+4 > len(rest)+1" the parse still refused it, and that mutant
		// passed the whole suite while turning this stream into a panic out of
		// the exported API. Overshooting a boundary pins that a check exists
		// and not where it sits.
		//
		// Deleting the guard is not a bounds error in Go's eyes unless the
		// backing array ends there too, and that is exactly why it is worth a
		// literal stream: hex.DecodeString allocates a slice whose capacity IS
		// its length, which is what a stream lifted out of a PDF looks like, and
		// the read then goes past the ALLOCATION. Handed a buffer with slack
		// instead, the mutant does something quieter and worse -- it reads a data
		// length out of whatever follows the stream in memory and reports a
		// segment that parsed cleanly.
		{
			name:   "segment-header-one-byte-longer-than-the-stream",
			stream: "0000000027200000000000",
			want:   "header runs past the end of the stream",
			panics: "slice bounds out of range [:12] with capacity 11",
		},

		// internal/jbig2/segment_decode.go, planStream: len(sg.data) < 19.
		//
		// 52 bytes: a page information segment declaring FOUR bytes of body,
		// followed by a complete 26-byte generic region segment for an 8x8 page.
		// The body is long enough for the width field and nothing else. Without
		// the guard the page flags are read from sg.data[16] of a 4-byte slice --
		// and the guard is what makes the width, the height and the default pixel
		// value all readable from one bounds check.
		{
			name: "page-information-body-under-19-bytes",
			stream: "000000003000010000000400000008000000012600010000001a00000008" +
				"000000080000000000000000000803fffdff02fefefe",
			want:   "page information segment is 4 bytes",
			panics: "index out of range [16] with length 4",
		},

		// The same guard AT ITS BOUNDARY, and unlike every other case here the
		// loosened guard does not panic and does not misdiagnose -- it ACCEPTS.
		//
		// The body is EIGHTEEN bytes: width, height, the two resolution fields
		// and the flags byte are all present, and only the second byte of the
		// two-byte striping information of T.88 7.4.8.5 is missing. A minimum of
		// 18 reads every field it wants inside the slice, so the stream decodes
		// and hands back an 8x8 page built from a page information segment that
		// is not one. The rest of the stream is well formed on purpose, so the
		// guard is the only thing standing between this and a raster.
		{
			name: "page-information-body-18-bytes",
			stream: "0000000030000100000012" + "000000080000000800000000000000000100" +
				"000000012600010000001a" + "00000008000000080000000000000000000803fffdff02fefefe",
			want:   "page information segment is 18 bytes",
			panics: "ACCEPTED, and an 8x8 raster is handed back for a truncated page segment",
		},

		// internal/jbig2/segment_decode.go, parseRegionInfo: len(d) < 17.
		//
		// The FIRST length check any region body meets, and the one guarding the
		// widest read: parseRegionInfo takes four uint32 fields and a flags byte
		// out of the T.88 7.4.1 region segment information field, and it is
		// called from TWO places -- planStream's budget loop, over every region
		// in the stream, and decodeGenericRegionSegment. The budget loop is the
		// one that matters, because it runs from PageSize, which the PDF layer
		// calls FIRST on bytes out of an untrusted file.
		//
		// 45 bytes: a well-formed 8x8 page, then a region segment declaring a
		// FOUR-byte body -- the width field and nothing else. Without the guard
		// the height is read from d[4:8] of a 4-byte slice.
		{
			name: "region-info-body-under-17-bytes",
			stream: "00000000300001000000130000000800000008000000000000000001000000" +
				"00000127000100000004" + "00000008",
			want:   "region segment info is 4 bytes",
			panics: "slice bounds out of range [:8] with capacity 4",
		},

		// The same guard one byte short of satisfying it, which is a different
		// read: 57 bytes, a SIXTEEN-byte region body. Every uint32 field is
		// present and only the external combination operator byte at d[16] is
		// missing, so this case fails on an index rather than on a slice bound
		// and would survive a guard rewritten as "len(d) < 16".
		{
			name: "region-info-body-16-bytes",
			stream: "00000000300001000000130000000800000008000000000000000001000000" +
				"00000127000100000010" + "00000008000000080000000000000000",
			want:   "region segment info is 16 bytes",
			panics: "index out of range [16] with length 16",
		},

		// internal/jbig2/segment_decode.go, decodeGenericRegionSegment: len(d) < 18.
		//
		// 58 bytes: a well-formed 8x8 page, then a generic region segment whose
		// body is SEVENTEEN bytes -- exactly the T.88 7.4.1 region segment
		// information field and not one byte more. parseRegionInfo is satisfied,
		// because seventeen bytes is all it needs; the generic region flags byte
		// of 7.4.6.2 is the eighteenth and is not there. Without the guard d[17]
		// indexes one past the end.
		{
			name: "generic-region-body-under-18-bytes",
			stream: "00000000300001000000130000000800000008000000000000000001000000" +
				"000001260001000000110000000800000008000000000000000000",
			want:   "generic region segment is 17 bytes",
			panics: "index out of range [17] with length 17",
		},

		// internal/jbig2/segment_decode.go, decodeGenericRegionSegment: len(d) < 26.
		//
		// 59 bytes, and one byte longer than the case above: the body is
		// EIGHTEEN bytes, so the flags byte is present and MMR, GBTEMPLATE and
		// EXTTEMPLATE all check out, and the parse walks on to the eight-byte AT
		// field of T.88 7.4.6.3 that is not there. Without the guard d[18:26] is
		// taken from a slice whose backing array ends at 23.
		{
			name: "generic-region-body-under-26-bytes",
			stream: "0000000030000100000013000000080000000800000000000000000100000000" +
				"000126000100000012000000080000000800000000000000000000",
			want:   "generic region segment is 18 bytes",
			panics: "slice bounds out of range [:26] with capacity 23",
		},

		// The same guard, at the length where deleting it does NOT panic -- which
		// is why the case above is not enough on its own.
		//
		// 66 bytes, and the segment order is reversed on purpose: a generic
		// region segment whose body is TWENTY-FIVE bytes comes FIRST, and the
		// page information segment follows it. The region body carries the first
		// SEVEN of the eight nominal AT bytes (03 FF FD FF 02 FE FE) and stops.
		//
		// segment.data is a sub-slice of the caller's buffer, not a copy, so with
		// the guard deleted d[18:26] is in range of the BACKING ARRAY and reads
		// its eighth byte from the segment that follows -- the high byte of the
		// next segment's number, 0x00, which is not the 0xFE the nominal AT field
		// ends with. So the decoder does not panic and does not report a short
		// segment: it reports "AT pixels are not the nominal ones" and hands back
		// ErrUnsupportedJBIG2Feature, having reached its verdict from a byte
		// OUTSIDE the segment it was decoding. That is the misfiling the sentinel
		// exists to prevent -- a damaged stream filed as a page some future
		// decoder recovers -- and it is why this case asserts the message and the
		// sentinel and not just "no panic".
		{
			name: "generic-region-body-25-bytes-must-not-read-past-the-segment",
			stream: "000000012600010000001900000008000000080000000000000000000003fffd" +
				"ff02fefe000000003000010000001300000008000000080000000000000000010000",
			want:   "generic region segment is 25 bytes",
			panics: "ErrUnsupportedJBIG2Feature, decided from a byte outside the segment",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := hex.DecodeString(c.stream)
			if err != nil {
				t.Fatalf("the case's own hex is malformed: %v", err)
			}
			t.Logf("%d bytes: % 02X", len(s), s)

			got, derr := decodeJBIG2GenericNoPanic(t, s)
			if derr == nil {
				t.Fatalf("DecodeJBIG2Generic returned a %dx%d bitmap for a truncated header; "+
					"want a refusal naming %q", got.Width, got.Height, c.want)
			}
			if got != nil {
				t.Error("a bitmap was returned alongside the error")
			}
			if !strings.Contains(derr.Error(), c.want) {
				t.Errorf("error = %v;\nwant it to contain %q. The length guard is what produces "+
					"that diagnosis from the header alone; without it this stream is %s.",
					derr, c.want, c.panics)
			}
			if errors.Is(derr, ErrUnsupportedJBIG2Feature) {
				t.Errorf("error = %v; a truncated header is DAMAGE, not a coding feature byblos "+
					"has not implemented. Reporting it under ErrUnsupportedJBIG2Feature tells an "+
					"archive the page is recoverable by a future decoder, and it is not -- the "+
					"bytes are not there.", derr)
			}
		})
	}
}

// TestDecodeJBIG2PlacementGuardsTheDictionaryHeight pins the HEIGHT half of
// decodeJBIG2Placement's size comparison, which no test in the tree could
// detect the deletion of.
//
// The existing table around TestDecodeJBIG2PlacementGuardsTheDictionary pins the
// width half and only the width half. Its three size cases -- transposed 73x101,
// wrong-width 102x73, and the zero dictionary -- all disagree with the page on
// WIDTH as well, so each is still refused by "w != info.Width" alone. Delete
// "|| h != info.Height" and the whole file still passes.
//
// The case that separates them is a dictionary that agrees on width and lies
// about height, and it is worth having as its own test because it is precisely
// the /Width-/Height confusion the function's doc comment says the comparison
// exists to catch. Reproduced against the mutant: a 100x50 JBIG2 page under a
// dictionary declaring /Width 100 /Height 100 is ACCEPTED, and the caller is
// handed back a 100x50 raster for an image the PDF says is 100 rows tall --
// half a page of content placed as though it were a whole one, with no error
// anywhere. That is the silent-accept failure mode this package treats as worse
// than a refusal: the geometry the caller gets does not describe the pixels.
//
// The stream is built by hand rather than encoded, so the page really is 100x50
// and the assertion does not depend on what the encoder happens to emit.
func TestDecodeJBIG2PlacementGuardsTheDictionaryHeight(t *testing.T) {
	const pageW, pageH = 100, 50
	data := hostileJBIG2Stream(pageW, pageH, pageW, pageH)

	// Agrees on width, and is square where the page is not. 10,000 pixels is
	// far inside jbig2.MaxPagePixels, so the size gate in front cannot be what
	// refuses this and the comparison under test is the only thing left.
	info := func(int) (pdfdoc.ImageInfo, bool) {
		return pdfdoc.ImageInfo{Width: pageW, Height: 2 * pageH}, true
	}

	got, err := decodeJBIG2Placement(data, info, 7)
	if err == nil {
		t.Fatalf("a %dx%d JBIG2 page under a dictionary declaring %dx%d was accepted and "+
			"returned a %v raster. The dictionary is what the PDF says the image is; a page "+
			"of a different height means this stream is not the one the dictionary describes, "+
			"and the placement geometry the caller gets back does not describe these pixels.",
			pageW, pageH, pageW, 2*pageH, got.Bounds())
	}
	t.Logf("refused: %v", err)
	for _, want := range []string{
		fmt.Sprintf("%dx%d", pageW, pageH),   // what the stream actually is
		fmt.Sprintf("%dx%d", pageW, 2*pageH), // what the dictionary claimed
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v; want it to name %q. A caller reading this has to be able to "+
				"tell which of the two sizes came from the PDF and which came from the "+
				"stream, or they cannot tell a mis-parsed stream from a wrong dictionary.",
				err, want)
		}
	}
}

// hostileJBIG2StreamManyRegions builds the same hand-made stream as
// hostileJBIG2Stream but with n one-pixel region segments instead of one, so a
// test can name a SEGMENT COUNT as well as a page size.
//
// It is the other axis of the same attack. hostileJBIG2Stream makes the cost
// come out of one region's declared size; this makes it come out of the number
// of segment headers, which is what internal/jbig2's rule 5 bounds and what
// nothing on the PDF path has ever been measured against.
func hostileJBIG2StreamManyRegions(pageW, pageH, n int) []byte {
	seg := func(num uint32, typ byte, data []byte) []byte {
		h := binary.BigEndian.AppendUint32(nil, num)
		h = append(h, typ, 0x00, 0x01)
		h = binary.BigEndian.AppendUint32(h, uint32(len(data)))
		return append(h, data...)
	}
	pi := binary.BigEndian.AppendUint32(nil, uint32(pageW))
	pi = binary.BigEndian.AppendUint32(pi, uint32(pageH))
	pi = binary.BigEndian.AppendUint32(pi, 0)
	pi = binary.BigEndian.AppendUint32(pi, 0)
	pi = append(pi, 0x01, 0x00, 0x00)

	out := seg(0, 48, pi)
	for i := 0; i < n; i++ {
		r := binary.BigEndian.AppendUint32(nil, 1) // a 1x1 region, so the pixel
		r = binary.BigEndian.AppendUint32(r, 1)    // budget is nowhere near reached
		r = binary.BigEndian.AppendUint32(r, 0)
		r = binary.BigEndian.AppendUint32(r, 0)
		r = append(r, 0x00, 0x08) // OR; template 0, TPGDON on
		r = append(r, 0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE)
		out = append(out, seg(uint32(i+1), 38, r)...)
	}
	return out
}

// TestExtractPageRasterCeilingBoundsEveryStreamTheGatesAdmit bounds ALLOCATION
// on this path over the whole set of streams the gates admit, not over one
// stream shape, which is the difference between a ceiling and an anecdote.
//
// TestExtractPageRasterBoundsTheCostOfAHostileJBIG2Page above measures a single
// shape -- an 8191x8191 page under a 1x1 region, 67 bytes -- and until round 7
// called it "the most ALLOCATING one every gate admits". It is not. A 67-byte
// stream cannot carry segment HEADERS, and rule 5 admits 65,536 of them. The
// segment count is a second axis and that test does not vary it at all:
//
//	at-cap segments   1024x65536 page, 65,535 one-pixel regions, 2,424,825 bytes
//	                  -> 113.7 MiB, 130 ms, a 1024x65536 raster
//	square page       8191x8191 page, one 1x1 region, 67 bytes
//	                  -> 72.3 MiB, 52 ms
//	square page       8191x8191 page, a page-covering region, 67 bytes
//	                  -> 80.3 MiB, 1.77 s
//
// THE RESOLUTION TAKEN IS TO RAISE THE LITERAL AND NAME THE NEW WORST SHAPE,
// not to tighten a budget, and the reason is that nothing here is out of
// proportion. Every one of these returns ONE maximal page. Above the 72.3 MiB
// that page costs, the at-cap stream's extra 41.4 MiB is bought with 2.4 MB of
// input and is bounded by rule 5 in both of its parts: the same 2,425,816-byte
// document with ONE more segment, refused from the headers with nothing decoded,
// already costs 18.0 MiB just to read the PDF and parse the descriptors, and the
// rest is the region bitmaps and per-segment overhead the 16 MiB bitmap budget
// concedes. Tightening maxStreamSegments is the only lever that would move it,
// and that constant is derived from one region per row of the tallest page rule
// 1 admits (see internal/jbig2's rule 5); lowering it to fit a test literal
// would start refusing legitimately striped pages, which is the byb-riy bug.
//
// So 140 MiB, measured at 1.23x the worst of the three, the same headroom the
// 88 MiB figure carries over its own shape. The two literals are separate on
// purpose. 88 stays where it is and stays TIGHT on the page-size axis, so a
// regression that stops this path being O(page) still fails there first, from a
// 67-byte stream; this one is the outer bound and it is the one that has to be
// true of every stream, whatever its size.
func TestExtractPageRasterCeilingBoundsEveryStreamTheGatesAdmit(t *testing.T) {
	// A literal, not an expression over jbig2.MaxPagePixels or
	// maxStreamSegments: a ceiling written in terms of the constants it exists
	// to bound moves with the mutation it exists to catch.
	const ceiling = 140 << 20

	side := int(math.Sqrt(float64(jbig2.MaxPagePixels - 1)))
	for _, c := range []struct {
		name string
		w, h int
		data []byte
	}{
		// 1024 is the narrowest width the cap's own derivation contemplates, and
		// MaxPagePixels over it is 65,536 rows -- so this page is exactly the
		// budget, and one region per row is exactly the cap.
		{"at-cap-segment-count", 1024, 65536, hostileJBIG2StreamManyRegions(1024, 65536, 65535)},
		{"widest-page-tiny-region", side, side, hostileJBIG2Stream(side, side, 1, 1)},
		{"widest-page-full-region", side, side, hostileJBIG2Stream(side, side, side, side)},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := corpusDoc(t, "scan")
			ref := inspect(t, "scan")[0].Images[0]
			var out bytes.Buffer
			if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
				ref.ObjNr: {
					Width: c.w, Height: c.h, BPC: 1,
					ColorSpace: ColorSpace{Name: "DeviceGray"},
					Filter:     "JBIG2Decode",
					Data:       c.data,
				},
			}); err != nil {
				t.Fatalf("ReplaceImages: %v", err)
			}
			doc := out.Bytes()

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			start := time.Now()
			pr, err := ExtractPageRaster(bytes.NewReader(doc), 1)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			grew := after.TotalAlloc - before.TotalAlloc
			t.Logf("%d bytes of JBIG2 in a %d-byte PDF, page %dx%d: err = %v, elapsed = %v, "+
				"TotalAlloc grew by %.1f MiB", len(c.data), len(doc), c.w, c.h, err, elapsed,
				float64(grew)/(1<<20))

			if err != nil {
				t.Fatalf("this stream is ADMITTED by every gate on the path, so it has to "+
					"decode or the shape being bounded is not the shape being measured: %v", err)
			}
			if b := pr.Image.Bounds(); b.Dx() != c.w || b.Dy() != c.h {
				t.Fatalf("raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), c.w, c.h)
			}
			if grew > ceiling {
				t.Errorf("an admitted JBIG2 page allocated %.1f MiB from %d bytes of stream; "+
					"the ceiling this path commits to over EVERY admitted stream is %d MiB. "+
					"Check which axis moved before raising it again: this bound was already "+
					"raised once, from 88 MiB, because it had only ever been measured by "+
					"varying the page size and the SEGMENT COUNT is a second axis. A third "+
					"one is a rule that stopped charging what it charges.",
					float64(grew)/(1<<20), len(c.data), ceiling>>20)
			}
		})
	}
}
