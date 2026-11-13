package byblos

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	jbig2 "github.com/dobbo-ca/jbig2-go"
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
	data, err := encodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("encodeJBIG2Generic: %v", err)
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
	data, err := encodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("encodeJBIG2Generic: %v", err)
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
	data, err := encodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("encodeJBIG2Generic: %v", err)
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
			img, err := decodeJBIG2Placement(context.Background(), data, nil, func(int) (pdfdoc.ImageInfo, bool) {
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
// "/Decode array" prefix is deliberate: the byb-9v0 census
// classified this gate by that phrase, so a refusal that drops it is a refusal
// no measurement can see.
func TestDecodeJBIG2PlacementRefusesADecodeArrayItCannotRead(t *testing.T) {
	src := jbig2TestBitmap()
	data, err := encodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("encodeJBIG2Generic: %v", err)
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
			got, err := decodeJBIG2Placement(context.Background(), data, nil, func(int) (pdfdoc.ImageInfo, bool) {
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
	valid, err := encodeJBIG2Generic(jbig2TestBitmap())
	if err != nil {
		t.Fatalf("encodeJBIG2Generic: %v", err)
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
		got, err := decodeJBIG2Placement(context.Background(), c.data, nil, info, 7)
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
	data, err := encodeJBIG2Generic(src.Clone())
	if err != nil {
		t.Fatalf("encodeJBIG2Generic: %v", err)
	}
	info := func(i pdfdoc.ImageInfo, ok bool) func(int) (pdfdoc.ImageInfo, bool) {
		return func(int) (pdfdoc.ImageInfo, bool) { return i, ok }
	}
	good := pdfdoc.ImageInfo{Width: src.Width, Height: src.Height}

	// The control: with a dictionary that agrees, the raster comes back.
	img, err := decodeJBIG2Placement(context.Background(), data, nil, info(good, true), 7)
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
		// 8192x8193 is one row past maxJBIG2Pixels, so the gate is what
		// refuses it and nothing else can be.
		"oversize-dictionary": {pdfdoc.ImageInfo{Width: 8192, Height: 8193}, true,
			"byblos renders a bilevel page at one byte per pixel"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeJBIG2Placement(context.Background(), data, nil, info(tc.info, tc.ok), 7)
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
	// far inside maxJBIG2Pixels, so the size gate in front cannot be what
	// refuses this and the comparison under test is the only thing left.
	info := func(int) (pdfdoc.ImageInfo, bool) {
		return pdfdoc.ImageInfo{Width: pageW, Height: 2 * pageH}, true
	}

	got, err := decodeJBIG2Placement(context.Background(), data, nil, info, 7)
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

// hostileJBIG2StreamManyRegions builds the same hand-made stream as
// hostileJBIG2Stream but with n one-pixel region segments instead of one, so a
// test can name a SEGMENT COUNT as well as a page size.
//
// It is the other axis of the same attack. hostileJBIG2Stream makes the cost
// come out of one region's declared size; this makes it come out of the number
// of segment headers, which is what jbig2-go's header budgets bounds and what
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

// jbig2ReplacedDoc is the "scan" fixture with its image replaced by a JBIG2
// stream of jbig2TestBitmap, so ExtractPageRaster reaches the JBIG2 arm.
func jbig2ReplacedDoc(t *testing.T) []byte {
	t.Helper()
	ref := inspect(t, "scan")[0].Images[0]
	src := jbig2TestBitmap()
	img, err := encodeJBIG2Image(src)
	if err != nil {
		t.Fatalf("encodeJBIG2Image: %v", err)
	}
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(corpusDoc(t, "scan")), map[int]EncodedImage{ref.ObjNr: img}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	return out.Bytes()
}

// jbig2Info is a dictionary for an image of the given size.
func jbig2Info(w, h int) func(int) (pdfdoc.ImageInfo, bool) {
	return func(int) (pdfdoc.ImageInfo, bool) { return grayInfo(w, h), true }
}

// TestJBIG2RequestOverTheWholeBudgetIsPermanent: a decode whose reservation is
// larger than byblos's whole budget can never fit, so retrying it forever is
// wrong. It must be a *LimitError (permanent), not ErrMemoryLimit and not a
// refusal or divert. byblos.SetMemoryLimit is the only pool: jbig2-go's own is
// never consulted (jbig2.SetMemoryLimit(1) must change nothing).
func TestJBIG2RequestOverTheWholeBudgetIsPermanent(t *testing.T) {
	doc := jbig2ReplacedDoc(t)
	src := jbig2TestBitmap()
	jbig2.SetMemoryLimit(1)
	t.Cleanup(func() { jbig2.SetMemoryLimit(0) })
	defer SetMemoryLimit(SetMemoryLimit(32 << 20)) // above a small PDF's parse, below jbig2DecodeBound

	ResetExtractStats()
	_, err := ExtractPageRaster(bytes.NewReader(doc), 1)
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v; want a *LimitError", err)
	}
	if le.Got < jbig2DecodeBound(0) {
		t.Errorf("LimitError %+v is not the JBIG2 reservation (want Got >= %d); the budget tripped earlier", le, jbig2DecodeBound(0))
	}
	if errors.Is(err, ErrMemoryLimit) {
		t.Errorf("err = %v; a request that can never fit must not be transient", err)
	}
	var rr *RasterRefusal
	if errors.As(err, &rr) {
		t.Errorf("a budget overflow became a refusal %q", rr.Reason)
	}
	if got := ExtractStats(); got.Diverted != 0 {
		t.Errorf("a budget overflow was counted as a divert: %+v", got)
	}

	// The same through the placement, with a budget below even the raster.
	SetMemoryLimit(1 << 10)
	data, _ := encodeJBIG2Generic(src)
	_, err = decodeJBIG2Placement(context.Background(), data, nil, jbig2Info(src.Width, src.Height), 7)
	if !errors.As(err, &le) || errors.Is(err, ErrMemoryLimit) {
		t.Errorf("tiny budget: err = %v; want a permanent *LimitError", err)
	}
}

// TestJBIG2DecodeUsesOnlyByblosBudget: byblos reserves the decode from its own
// pool and passes Reserved to jbig2-go, so jbig2-go's process-wide pool is never
// consulted. With that pool at 1 byte, a page that fits byblos's budget still
// decodes; without Reserved it would fail on jbig2-go's pool.
func TestJBIG2DecodeUsesOnlyByblosBudget(t *testing.T) {
	doc := jbig2ReplacedDoc(t)
	jbig2.SetMemoryLimit(1)
	t.Cleanup(func() { jbig2.SetMemoryLimit(0) })
	if _, err := ExtractPageRaster(bytes.NewReader(doc), 1); err != nil {
		t.Fatalf("err = %v; jbig2-go's own pool was consulted", err)
	}
}

// TestJBIG2BudgetHeldElsewhereIsTransient: a decode that fits the budget alone
// but not while another operation holds it waits for it, as a DCT decode does
// (TestDCTDecodeSharesByblosBudget), and through the public API ends with
// ErrMemoryLimit, counted as Failed, when ctx does.
func TestJBIG2BudgetHeldElsewhereIsTransient(t *testing.T) {
	doc := jbig2ReplacedDoc(t)
	src := jbig2TestBitmap()
	px := int64(src.Width * src.Height)
	defer SetMemoryLimit(SetMemoryLimit(px + jbig2DecodeBound(0) + 1<<20))
	release, err := limits.Reserve(context.Background(), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	ResetExtractStats()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = ExtractPageRasterContext(ctx, bytes.NewReader(doc), 1)
	var le *LimitError
	if !errors.Is(err, ErrMemoryLimit) || errors.As(err, &le) {
		t.Fatalf("err = %v; want transient ErrMemoryLimit, not a *LimitError", err)
	}
	if got := ExtractStats(); got.Failed != 1 || got.Diverted != 0 {
		t.Errorf("stats = %+v; want one Failed and no divert", got)
	}
	release()
	if _, err := ExtractPageRaster(bytes.NewReader(doc), 1); err != nil {
		t.Fatalf("after the release: %v", err)
	}
}

// jbig2StripedStream is a legal striped page by hand: page height 0xFFFFFFFF
// with the striping bit set, one generic region of w x h with no coded data (the
// MQ decoder reads 1 bits past its end, so the region decodes at its declared
// size), and an end-of-stripe segment at row h-1, which is what fixes the page
// height (T.88 7.4.8.2).
func jbig2StripedStream(w, h int) []byte {
	seg := func(num uint32, typ byte, data []byte) []byte {
		hd := binary.BigEndian.AppendUint32(nil, num)
		hd = append(hd, typ, 0x00, 0x01)
		hd = binary.BigEndian.AppendUint32(hd, uint32(len(data)))
		return append(hd, data...)
	}
	pi := binary.BigEndian.AppendUint32(nil, uint32(w))
	pi = binary.BigEndian.AppendUint32(pi, 0xFFFFFFFF)
	pi = binary.BigEndian.AppendUint32(pi, 0)
	pi = binary.BigEndian.AppendUint32(pi, 0)
	pi = append(pi, 0x01, 0x80, 0x10) // striped, maximum stripe size 16
	r := binary.BigEndian.AppendUint32(nil, uint32(w))
	r = binary.BigEndian.AppendUint32(r, uint32(h))
	r = binary.BigEndian.AppendUint32(r, 0)
	r = binary.BigEndian.AppendUint32(r, 0)
	r = append(r, 0x00, 0x00)
	r = append(r, 0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE)
	eos := binary.BigEndian.AppendUint32(nil, uint32(h-1))
	return append(append(seg(0, 48, pi), seg(1, 38, r)...), seg(2, 50, eos)...)
}

// TestDecodeJBIG2PlacementDecodesAStripedPage: a legal striped stream has no
// size in its headers (jbig2.PageSize returns ErrNoPageSize), and byblos must
// neither refuse nor divert it for that. It decodes and the decoded size is
// checked against the dictionary afterwards, as main did, through the public
// ExtractPageRaster as well as the placement.
func TestDecodeJBIG2PlacementDecodesAStripedPage(t *testing.T) {
	const w, h = 24, 10
	stream := jbig2StripedStream(w, h)
	if _, _, err := jbig2.PageSize(stream, nil); !errors.Is(err, jbig2.ErrNoPageSize) {
		t.Fatalf("PageSize err = %v; the fixture must be a stream PageSize cannot size", err)
	}
	img, err := decodeJBIG2Placement(context.Background(), stream, nil, jbig2Info(w, h), 7)
	if err != nil {
		t.Fatalf("a legal striped page was refused: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("raster is %v; want %dx%d", b, w, h)
	}
	// The decoded size is still checked against the dictionary.
	_, err = decodeJBIG2Placement(context.Background(), stream, nil, jbig2Info(w, h+1), 7)
	if err == nil || !strings.Contains(err.Error(), "page is 24x10 but image 7's dictionary says 24x11") {
		t.Fatalf("err = %v; want the post-decode size mismatch", err)
	}

	in := corpusDoc(t, "scan")
	ref := inspect(t, "scan")[0].Images[0]
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: {Width: w, Height: h, BPC: 1, ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter: "JBIG2Decode", Data: stream},
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	ResetExtractStats()
	pr, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster on a striped page: %v", err)
	}
	if b := pr.Image.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Errorf("extracted raster is %v; want %dx%d", b, w, h)
	}
	if got := ExtractStats(); got.Diverted != 0 || got.Failed != 0 {
		t.Errorf("a striped page moved the failure counters: %+v", got)
	}
}

// cancelInJBIG2 is cancelled by the first ctx.Err() call made from inside
// jbig2-go, and stays cancelled, so the cancel lands during jbig2.Decode and
// nowhere earlier.
type cancelInJBIG2 struct {
	context.Context
	hit bool
}

func (c *cancelInJBIG2) Err() error {
	if c.hit {
		return context.Canceled
	}
	pcs := make([]uintptr, 16)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "dobbo-ca/jbig2-go") {
			c.hit = true
			return context.Canceled
		}
		if !more {
			return nil
		}
	}
}

// TestJBIG2CancelMidDecodeIsNotADivert: a cancellation that lands inside
// jbig2.Decode is the caller's, so it comes back as the context error and does
// not move the counters (TestCancelledExtractionDoesNotMoveTheCounters).
func TestJBIG2CancelMidDecodeIsNotADivert(t *testing.T) {
	doc := jbig2ReplacedDoc(t)
	ResetExtractStats()
	cc := &cancelInJBIG2{Context: context.Background()}
	_, err := ExtractPageRasterContext(cc, bytes.NewReader(doc), 1)
	if !cc.hit {
		t.Fatal("jbig2-go never checked the context; the test proved nothing")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if got := ExtractStats(); got.Diverted != 0 || got.Failed != 0 || len(got.Reasons) != 0 {
		t.Fatalf("a cancel inside the decode moved the counters: %+v", got)
	}
}

// TestDecodeJBIG2PlacementAdmitsAFullResolutionPage pins maxJBIG2Pixels from
// below: a 600-dpi A4 or Letter master must get past byblos's size gate. The
// stream is unparseable on purpose, so the only claim is that the refusal, if
// any, is not the gate.
func TestDecodeJBIG2PlacementAdmitsAFullResolutionPage(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"600dpi-A4", 4961, 7016},
		{"600dpi-Letter", 5100, 6600},
	} {
		t.Run(c.name, func(t *testing.T) {
			info := func(int) (pdfdoc.ImageInfo, bool) {
				return pdfdoc.ImageInfo{Width: c.w, Height: c.h}, true
			}
			_, err := decodeJBIG2Placement(context.Background(), []byte("garbage"), nil, info, 7)
			if err == nil {
				t.Fatal("an unparseable stream must still be an error")
			}
			if strings.Contains(err.Error(), "dictionary declares") {
				t.Fatalf("a %dx%d page is a 600-dpi master and must reach the decoder: %v", c.w, c.h, err)
			}
		})
	}
}

// TestDecodeJBIG2PlacementComparesSizesBeforeDecoding pins that a page whose
// size disagrees with the dictionary is refused from its page-information
// segment. The stream is cut right after that segment, so a decode would fail
// with a different error; the size-mismatch error proves nothing was decoded.
func TestDecodeJBIG2PlacementComparesSizesBeforeDecoding(t *testing.T) {
	stream, err := jbig2.Encode(context.Background(), jbig2.NewBitmap(8000, 8000), jbig2.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	w, h, err := jbig2.PageSize(stream, nil)
	if err != nil || w != 8000 || h != 8000 {
		t.Fatalf("jbig2.PageSize = %d, %d, %v; want 8000, 8000, nil", w, h, err)
	}
	// Page-information segment: 11-byte header (no referred-to segments, 1-byte
	// page association) plus its 19-byte body.
	head := stream[:30]
	info := func(int) (pdfdoc.ImageInfo, bool) {
		return pdfdoc.ImageInfo{Width: 10, Height: 10, BPC: 1, ColorSpace: "DeviceGray"}, true
	}
	_, err = decodeJBIG2Placement(context.Background(), head, nil, info, 7)
	if err == nil || !strings.Contains(err.Error(), "page is 8000x8000 but image 7's dictionary says 10x10") {
		t.Fatalf("err = %v; want the size mismatch, raised before decoding", err)
	}
}

// wastedWorkStreamHex is 67 bytes of legal JBIG2 whose cost would be thrown
// away: a 1x1 page and one generic region of 8192x4095 (33,546,240 pixels, no
// coded data) at (0,0), 4,193,281 packed bytes together, inside the byte cap. A
// decoder that charged only the region's pixels would run the MQ decoder over
// 33.5 million of them to hand back one. jbig2-go refuses it from the headers.
const wastedWorkStreamHex = "000000003000010000001300000001000000010000000000000000010000000000012600010000001a0000200000000fff0000000000000000000003fffdff02fefefe"

// wastedWorkDoc is a document whose pages (a fixture's own image objects) are
// replaced by the 67-byte stream under a 1x1 dictionary, so the dictionary
// agrees with the page and only the region's cost is in question.
func wastedWorkDoc(t *testing.T, fixture string) []byte {
	t.Helper()
	data, err := hex.DecodeString(wastedWorkStreamHex)
	if err != nil || len(data) != 67 {
		t.Fatalf("fixture hex: %d bytes, %v", len(data), err)
	}
	repl := map[int]EncodedImage{}
	for _, p := range inspect(t, fixture) {
		repl[p.Images[0].ObjNr] = EncodedImage{Width: 1, Height: 1, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "JBIG2Decode", Data: data}
	}
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(corpusDoc(t, fixture)), repl); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	return out.Bytes()
}

// TestExtractPageRasterBoundsDecodingByWhatItCanReturn drives the stream
// through ExtractPageRaster: jbig2-go refuses it from its headers, so the page
// is diverted at once (the wall-clock guard is far above any header parse).
func TestExtractPageRasterBoundsDecodingByWhatItCanReturn(t *testing.T) {
	doc := wastedWorkDoc(t, "scan")
	start := time.Now()
	pr, err := ExtractPageRaster(bytes.NewReader(doc), 1)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v on a 67-byte stream", elapsed)
	}
	if err == nil {
		if b := pr.Image.Bounds(); b.Dx() != 1 || b.Dy() != 1 {
			t.Fatalf("returned a %v raster for a 1x1 page", b)
		}
		return
	}
	var rr *RasterRefusal
	if !errors.As(err, &rr) || rr.Reason != "unsupported-codec-jbig2" {
		t.Fatalf("err = %v; want the stream refused as unsupported-codec-jbig2", err)
	}
}

// TestRecordExtractionBoundsDecodingByWhatItCanReturn is the same over every
// page of a document (RecordExtraction pays once per page).
func TestRecordExtractionBoundsDecodingByWhatItCanReturn(t *testing.T) {
	doc := wastedWorkDoc(t, "dup-raster")
	start := time.Now()
	prov, err := RecordExtraction(bytes.NewReader(doc))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v over 2 pages of a 67-byte stream", elapsed)
	}
	if err != nil {
		t.Fatalf("RecordExtraction: %v", err)
	}
	for _, pg := range prov.Pages {
		if pg.Diverted != "" && pg.Diverted != "unsupported-codec-jbig2" {
			t.Errorf("a page diverted as %q; want it refused as unsupported-codec-jbig2 or extracted", pg.Diverted)
		}
	}
}
