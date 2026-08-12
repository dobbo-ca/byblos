package byblos

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/jbig2"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// wastedWorkStreamHex is 67 bytes of legal, in-budget JBIG2 whose entire cost
// is thrown away.
//
// A page information segment declares a 1x1 page. One immediate generic region
// segment declares 8192x4095 -- 33,546,240 pixels -- at (0,0), template 0,
// TPGDON off, and carries not one byte of coded data. Every gate that existed
// when this fixture was written admitted it: 33,546,241 pixels and 4,193,281
// packed bytes were both inside the budgets, which charged the page and the
// region together and never asked whether the region could be SEEN.
//
// So the decoder ran the MQ decoder over 33.5 million pixels, and composite()
// then dropped 33,546,239 of them on the floor because they fall outside a 1x1
// page. The returned raster held ONE pixel. The other 33.5 million were work no
// caller can ever observe, which is the definition of an amplification: 67 bytes
// of input bought 33.5 million MQ decisions and a 1x1 answer. Measured on the
// tip that motivated these tests: 613ms through DecodeJBIG2Generic.
//
// The bytes are verbatim rather than generated so that this fixture cannot
// drift when a helper is refactored.
const wastedWorkStreamHex = "000000003000010000001300000001000000010000000000000000010000000000012600010000001a0000200000000fff0000000000000000000003fffdff02fefefe"

func wastedWorkStream(t *testing.T) []byte {
	t.Helper()
	data, err := hex.DecodeString(wastedWorkStreamHex)
	if err != nil {
		t.Fatalf("decoding the fixture hex: %v", err)
	}
	if len(data) != 67 {
		t.Fatalf("fixture is %d bytes; want 67", len(data))
	}
	return data
}

// assertDecodeCostIsBoundedByOutput is the shared assertion of the three tests
// below: whatever a JBIG2 entry point returns, the pixels it DECODED must be
// bounded by the pixels it can HAND BACK.
//
// The bound is 4x the returned raster plus 131,072, and both terms are there for
// a reason. The multiple leaves room for a legitimate region that overhangs its
// page on both axes, and for a stream that composes several regions over the
// same area; the constant leaves room for a small page under a region, where a
// multiple of the page is the wrong instrument entirely. 131,072 pixels is
// about 3 ms of decoding at the rate segment_decode.go measures, so the slack
// cannot hide a cost anyone would notice.
//
// Both terms are looser than the rule planStream actually enforces -- 4x the
// page plus overdrawFloorPixels, which is 65,536 -- and the slack here is
// deliberately TWICE that floor rather than equal to it. This asserts the
// PROPERTY, not the constant, and it has to keep holding if the constant is
// retuned; a slack that tracked the floor exactly would be a restatement of it.
//
// Nothing here is expressed in wall clock. A decode-cost bound asserted in
// milliseconds is a bound that fails on a busy CI runner and passes on a
// regression that happens to run on an idle one; the elapsed time is logged,
// and guarded only at a threshold no correct implementation could ever reach.
func assertDecodeCostIsBoundedByOutput(t *testing.T, what string, decoded, rasterPixels int64, elapsed time.Duration) {
	t.Helper()
	t.Logf("%s: decoded %d pixels to return a raster of %d pixels, in %v",
		what, decoded, rasterPixels, elapsed)

	const slack = 1 << 17
	if bound := 4*rasterPixels + slack; decoded > bound {
		t.Errorf("%s: the decoder ran the MQ decoder over %d pixels and returned a raster "+
			"holding %d; the bound is %d (4x the raster plus %d). %d pixels of decoding were "+
			"thrown away. The budgets charge a region's pixels but never ask whether the page "+
			"can show them, so 67 bytes buy unbounded decoding that produces no output.",
			what, decoded, rasterPixels, bound, slack, decoded-rasterPixels)
	}

	// Secondary only, and deliberately far above anything a loaded machine
	// explains. It exists to catch a cost that escapes the pixel counter
	// entirely -- not to measure the cost the counter already measures.
	if elapsed > 5*time.Second {
		t.Errorf("%s: took %v; nothing this path does on a 67-byte stream can take seconds",
			what, elapsed)
	}
}

// TestDecodeJBIG2GenericBoundsDecodingByWhatItCanReturn is the defect at its
// narrowest: the exported decoder, one call, no PDF around it.
//
// It must either REFUSE the stream -- which is a legitimate answer, the region
// is 33.5 million pixels for a one-pixel page and no honest producer writes
// that -- or decode only what it can hand back. What it must not do is what it
// did before this test existed: succeed, return a 1x1 bitmap, err = nil, and
// charge 33.5 million pixels of arithmetic decoding for it. It now takes the
// first branch, refusing from the headers with 0 pixels decoded.
func TestDecodeJBIG2GenericBoundsDecodingByWhatItCanReturn(t *testing.T) {
	data := wastedWorkStream(t)

	before := jbig2.DecodedPixels()
	start := time.Now()
	got, err := DecodeJBIG2Generic(data)
	elapsed := time.Since(start)
	decoded := jbig2.DecodedPixels() - before

	if err != nil {
		t.Logf("refused, which is an acceptable answer: %v", err)
		if decoded > 0 {
			t.Errorf("the stream was refused only AFTER decoding %d pixels; a refusal has to "+
				"come from the headers, or it costs the same as an acceptance", decoded)
		}
		return
	}
	assertDecodeCostIsBoundedByOutput(t, "DecodeJBIG2Generic",
		decoded, int64(got.Width)*int64(got.Height), elapsed)
}

// TestExtractPageRasterBoundsDecodingByWhatItCanReturn drives the same stream
// through the public entry point a caller actually reaches with an untrusted
// file: a PDF whose image dictionary agrees with the stream's 1x1 page, so
// every check in decodeJBIG2Placement passes and the decoder runs in full.
//
// The dictionary agreeing is the point. decodeJBIG2Placement's size comparison
// is what stops a MISMATCHED stream from being paid for, and it is doing its
// job here -- 1x1 against 1x1 matches. The cost is not a mismatch, it is a
// region inside the budget that the page cannot show.
func TestExtractPageRasterBoundsDecodingByWhatItCanReturn(t *testing.T) {
	data := wastedWorkStream(t)

	in := corpusDoc(t, "scan")
	ref := inspect(t, "scan")[0].Images[0]
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), map[int]EncodedImage{
		ref.ObjNr: {
			Width: 1, Height: 1, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       data,
		},
	}); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	doc := out.Bytes()

	before := jbig2.DecodedPixels()
	start := time.Now()
	pr, err := ExtractPageRaster(bytes.NewReader(doc), 1)
	elapsed := time.Since(start)
	decoded := jbig2.DecodedPixels() - before

	if err != nil {
		t.Logf("diverted, which is an acceptable answer: %v", err)
		if decoded > 0 {
			t.Errorf("the page was diverted only AFTER decoding %d pixels", decoded)
		}
		return
	}
	b := pr.Image.Bounds()
	assertDecodeCostIsBoundedByOutput(t,
		fmt.Sprintf("ExtractPageRaster on a %d-byte PDF carrying 67 bytes of JBIG2", len(doc)),
		decoded, int64(b.Dx())*int64(b.Dy()), elapsed)
}

// TestRecordExtractionBoundsDecodingByWhatItCanReturn is the entry point round
// 3's measurement table left out entirely, and it is the one that multiplies.
//
// RecordExtraction runs extractPage over EVERY page of a document, so a
// document carrying the stream on n pages costs n times the amplification --
// from the same 67 bytes, repeated. The "dup-raster" fixture is two pages
// holding the raster as two distinct image objects, so replacing both makes the
// document pay twice, and the count asserted is against the pixels BOTH pages
// can return.
func TestRecordExtractionBoundsDecodingByWhatItCanReturn(t *testing.T) {
	data := wastedWorkStream(t)

	in := corpusDoc(t, "dup-raster")
	pages := inspect(t, "dup-raster")
	if len(pages) != 2 {
		t.Fatalf("the dup-raster fixture is %d pages; want 2", len(pages))
	}
	repl := map[int]EncodedImage{}
	for _, p := range pages {
		if len(p.Images) != 1 {
			t.Fatalf("page %d has %d images; want 1", p.Index, len(p.Images))
		}
		repl[p.Images[0].ObjNr] = EncodedImage{
			Width: 1, Height: 1, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       data,
		}
	}
	if len(repl) != 2 {
		t.Fatalf("the two pages share %d image object(s); want 2 distinct ones", len(repl))
	}
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), repl); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	doc := out.Bytes()

	before := jbig2.DecodedPixels()
	start := time.Now()
	prov, err := RecordExtraction(bytes.NewReader(doc))
	elapsed := time.Since(start)
	decoded := jbig2.DecodedPixels() - before
	if err != nil {
		t.Fatalf("RecordExtraction: %v", err)
	}

	// Every page that extracted can return its raster; every page that diverted
	// can return nothing at all. Both are counted the same way: a diverted page
	// contributes zero, because the caller gets zero pixels from it.
	var returnable int64
	var extracted, diverted int
	for _, pg := range prov.Pages {
		if pg.Diverted != "" {
			diverted++
			continue
		}
		extracted++
		returnable += 1 // the 1x1 raster each admitted page hands back
	}
	t.Logf("%d-byte, %d-page PDF: %d pages extracted, %d diverted", len(doc), len(prov.Pages), extracted, diverted)
	if extracted == 0 && diverted == len(prov.Pages) {
		t.Logf("every page diverted, which is an acceptable answer")
		if decoded > 0 {
			t.Errorf("every page diverted, yet %d pixels were decoded on the way", decoded)
		}
		return
	}
	assertDecodeCostIsBoundedByOutput(t, "RecordExtraction over 2 pages", decoded, returnable, elapsed)
}

// TestDecodeJBIG2PlacementAdmitsAFullResolutionPage is the from-below pin on
// the PDF side of the same constant.
//
// decodeJBIG2Placement refuses an image on its DICTIONARY's declared dimensions
// alone, against jbig2.MaxPagePixels, because grayImage expands the page to one
// byte per pixel. At MaxPagePixels = 1<<25 that gate refused a 600-dpi A4 page
// and a 600-dpi US Letter page -- the standard preservation-master resolutions
// for bitonal text, and the exact design point segment_decode.go's own comment
// claimed. Nothing failed when that happened, which is why this exists.
//
// The stream passed is deliberately unparseable, so this asserts one thing and
// nothing else: whatever refuses these pages, it must not be the size gate.
// Getting past that gate to a complaint about the bytes is a pass.
func TestDecodeJBIG2PlacementAdmitsAFullResolutionPage(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"600dpi-A4", 4961, 7016},     // 34,806,376 pixels
		{"600dpi-Letter", 5100, 6600}, // 33,660,000 pixels
	} {
		t.Run(c.name, func(t *testing.T) {
			info := func(int) (pdfdoc.ImageInfo, bool) {
				return pdfdoc.ImageInfo{Width: c.w, Height: c.h}, true
			}
			_, err := decodeJBIG2Placement([]byte("garbage"), nil, info, 7)
			if err == nil {
				t.Fatal("an unparseable stream must still be an error")
			}
			if strings.Contains(err.Error(), "dictionary declares") {
				t.Fatalf("a %dx%d page is a 600-dpi preservation master and must reach the "+
					"decoder, not be refused on its declared size: %v", c.w, c.h, err)
			}
		})
	}
}

// TestDecodeJBIG2PlacementRefusesAnAbsurdDictionaryWithoutOpeningTheStream is
// the kill-power test for the size gate in decodeJBIG2Placement, which today no
// test in the tree can detect the deletion of.
//
// The gate's contract is not "an absurd image errors" -- an absurd image errors
// either way, further down, because the stream will not match it. The contract
// is that the refusal comes from the DICTIONARY ALONE, before the stream is
// opened at all. That is observable without timing anything: hold the
// dictionary fixed, vary the stream across three shapes that fail for three
// completely different reasons, and require the SAME error every time. Only a
// gate that never looks at the stream can produce that.
//
// Delete the gate and each stream produces its own error -- a short-header
// parse failure, an empty-stream failure, a size mismatch -- and this fails.
func TestDecodeJBIG2PlacementRefusesAnAbsurdDictionaryWithoutOpeningTheStream(t *testing.T) {
	valid, err := EncodeJBIG2Generic(jbig2TestBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	// 65536 x 65536 is 4,294,967,296 pixels, which grayImage would expand to
	// 4 GiB of *image.Gray.
	info := func(int) (pdfdoc.ImageInfo, bool) {
		return pdfdoc.ImageInfo{Width: 65536, Height: 65536}, true
	}

	streams := map[string][]byte{
		"too-short-for-a-segment-header": []byte("garbage"),
		"empty":                          nil,
		"valid-but-a-different-size":     valid,
	}
	var first, firstName string
	for name, s := range streams {
		_, err := decodeJBIG2Placement(s, nil, info, 7)
		if err == nil {
			t.Fatalf("%s: a 65536x65536 dictionary must be refused", name)
		}
		t.Logf("%s: %v", name, err)
		if first == "" {
			first, firstName = err.Error(), name
			continue
		}
		if err.Error() != first {
			t.Errorf("the refusal depends on the stream, so it is not coming from the "+
				"dictionary:\n  %s: %s\n  %s: %s\nThe size gate exists to refuse an absurd "+
				"image WITHOUT opening the stream; an error that varies with the stream's "+
				"bytes proves the stream was opened.", firstName, first, name, err.Error())
		}
	}
	if !strings.Contains(first, "65536x65536") {
		t.Errorf("the refusal does not name the dictionary's own declared size: %s", first)
	}
}

// TestDecodeJBIG2PlacementComparesSizesBeforeDecoding is the kill-power test
// for the ORDER of the checks in decodeJBIG2Placement, which the function's own
// comment calls "load-bearing, not tidiness" and which no test in the tree can
// currently detect the reversal of.
//
// The reason it is invisible is that reversing the order changes no RESULT. A
// stream whose page disagrees with the dictionary is refused either way, with
// the same error. What changes is the COST: comparing first refuses it for the
// price of parsing 26-byte region headers, and decoding first pays for the
// whole page before discovering nobody wanted it.
//
// So the assertion is on the pixel counter, and it is exact rather than
// bounded: a stream that is going to be refused on its size must be decoded
// ZERO pixels' worth. The stream here is a 4096x4096 page under a region
// covering it -- a quarter of what the budgets admit, and an entirely ordinary
// shape -- so getting the order wrong costs 16,777,216 MQ decisions to produce
// an error that was available from the headers. Measured under that reversal:
// 431ms in place of about a microsecond. The worst the budgets admit is four
// times as much again, 67,092,481 decisions and 1.53s.
func TestDecodeJBIG2PlacementComparesSizesBeforeDecoding(t *testing.T) {
	data := hostileJBIG2Stream(4096, 4096, 4096, 4096)
	// The dictionary disagrees, and is small enough that the size gate in front
	// cannot be what refuses it -- otherwise this would pass without the order
	// under test running at all.
	info := func(int) (pdfdoc.ImageInfo, bool) {
		return pdfdoc.ImageInfo{Width: 100, Height: 100}, true
	}

	before := jbig2.DecodedPixels()
	start := time.Now()
	got, err := decodeJBIG2Placement(data, nil, info, 7)
	elapsed := time.Since(start)
	decoded := jbig2.DecodedPixels() - before

	if err == nil {
		t.Fatalf("a 4096x4096 page against a 100x100 dictionary returned a %v raster; want an error",
			got.Bounds())
	}
	t.Logf("refused in %v after decoding %d pixels: %v", elapsed, decoded, err)
	if decoded != 0 {
		t.Errorf("a stream that was never going to be accepted was decoded first: %d pixels "+
			"of arithmetic decoding for an answer the headers already gave. jbig2.PageSize "+
			"resolves the page from 26-byte region headers; comparing it against the "+
			"dictionary BEFORE decoding is what makes the refusal free.", decoded)
	}
}
