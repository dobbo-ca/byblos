package byblos

import (
	"errors"
	"fmt"
	"image"

	"github.com/dobbo-ca/byblos/internal/jbig2"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// CapabilityJBIG2Generic is the provenance capability string recorded for a
// page compressed with lossless JBIG2 generic region coding. A document whose
// provenance carries it is exactly the upgrade set for a future jbig2-symbol
// capability (see FUTURE.md).
const CapabilityJBIG2Generic = "jbig2-generic"

// EncodeJBIG2Generic compresses a bitonal bitmap with lossless JBIG2 generic
// region coding and returns a JBIG2 bitstream in the embedded file organization
// required by the PDF JBIG2Decode filter.
//
// The coding is lossless: a decoder reconstructs b exactly, so no character can
// be substituted for another. Byblos does not implement lossy symbol matching
// and will not -- see FUTURE.md.
//
// To embed the result as a PDF image XObject, use these dictionary entries and
// nothing else:
//
//	/Type             /XObject
//	/Subtype          /Image
//	/Width            b.Width
//	/Height           b.Height
//	/ColorSpace       /DeviceGray
//	/BitsPerComponent 1
//	/Filter           /JBIG2Decode
//
// No /Decode array: the JBIG2Decode filter already presents a JBIG2 black pixel
// as the DeviceGray sample that renders black, so adding /Decode [1 0] inverts
// the page. No /DecodeParms and no /JBIG2Globals stream either: generic region
// coding produces no page-0 segments. The filter must not be used with inline
// images (ISO 32000-1:2008 7.4.7).
//
// EncodeJBIG2Generic does not copy b.Pix. It zeroes everything in each row past
// pixel b.Width-1: the padding bits inside that pixel's byte, and any whole
// bytes between there and b.Stride when the stride is larger than the minimal
// (b.Width+7)/8. On a well-formed bitmap that is a no-op, since all of it is
// required to be zero already. Pass a copy if that matters.
func EncodeJBIG2Generic(b *Bitmap) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: nil bitmap")
	}
	if b.Width <= 0 || b.Height <= 0 {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: bitmap is %dx%d; dimensions must be positive",
			b.Width, b.Height)
	}
	if b.Stride < (b.Width+7)/8 {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: stride %d is too small for width %d",
			b.Stride, b.Width)
	}
	stride64, height64 := int64(b.Stride), int64(b.Height)
	want := stride64 * height64
	if stride64 != 0 && want/stride64 != height64 {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: stride %d and height %d overflow",
			b.Stride, b.Height)
	}
	if int64(len(b.Pix)) < want {
		return nil, fmt.Errorf("byblos: EncodeJBIG2Generic: Pix is %d bytes; want at least %d",
			len(b.Pix), want)
	}

	// Same packing, same ink convention, so this shares the pixel buffer rather
	// than copying it. EncodeGenericRegion masks padding bits in place, which is
	// a no-op on a well-formed bitmap.
	return jbig2.EmbeddedStream(&jbig2.Bitmap{
		W:      b.Width,
		H:      b.Height,
		Stride: b.Stride,
		Pix:    b.Pix,
	})
}

// ErrUnsupportedJBIG2Feature reports a JBIG2 stream that parsed correctly and
// uses a coding feature byblos does not implement: a symbol dictionary or text
// region, refinement, halftones, MMR, or a generic region coded with anything
// other than GBTEMPLATE 0 and the nominal AT pixels.
//
// It is worth distinguishing from an ordinary decode failure. This error means
// the bytes are fine and byblos is not enough; a plain error means the bytes are
// not fine. An archive deciding what to re-process later acts on that difference
// -- the first is a page a future decoder recovers, the second is damage.
var ErrUnsupportedJBIG2Feature = errors.New("byblos: JBIG2 stream uses a feature byblos does not decode")

// DecodeJBIG2Generic decodes a JBIG2 bitstream in the embedded file
// organization -- the form the PDF JBIG2Decode filter carries, and the form
// EncodeJBIG2Generic produces -- and returns the page bitmap. A set bit is ink,
// matching Bitmap's convention throughout.
//
// It inverts EncodeJBIG2Generic UP TO A SIZE, bit-identically, and nothing
// wider. Below that size every stream EncodeJBIG2Generic produces decodes back
// exactly, which is what makes a byblos archive re-openable by byblos. Above it,
// byblos can write a page it will not read back -- and that asymmetry is
// deliberate, not a gap. EncodeJBIG2Generic shipped in v0.1.0 with no size
// budget at all and callers pin that tag, so the write side is fixed; the read
// side is where an untrusted stream arrives, and the resource budgets that make
// it safe (internal/jbig2, MaxPagePixels and maxStreamBitmapBytes) necessarily
// bound what it will accept.
//
// The boundary, in terms a caller can act on:
//
//   - It decodes any page of at most 67,108,864 pixels whose bitmaps pack into
//     16 MiB. That covers every 600-dpi preservation master byblos is handed --
//     A4 (4961x7016), US Letter (5100x6600), US Legal (5100x8400) -- and 800-dpi
//     A4 (6614x9354, 61,867,356 pixels), which is the largest sheet size that
//     round-trips.
//   - It does not decode 600-dpi A3 (7016x9921, 69,605,736 pixels), anything at
//     1200 dpi, or a bitmap so narrow that row padding blows the 16 MiB -- a
//     1x8388609 column is an eighth of the pixel budget and two bytes past the
//     memory one. EncodeJBIG2Generic writes every one of them, and cheaply:
//     measured on a blank page with two corner pixels, 81 bytes for A3, for
//     1200-dpi Letter and for A2, and 109 for the 1x8388609 column, whose
//     8,388,609 single-pixel rows cost more coded bits than a page-shaped
//     bitmap of the same area.
//   - It does not decode a stream of more than 65,536 segments, whatever sizes
//     those segments declare. That bound is one region per row of the tallest
//     page the pixel budget admits, and what it limits is the cost of reading
//     the headers, which the size budgets cannot charge for because they are
//     evaluated from the headers (internal/jbig2, rule 5). Nothing
//     EncodeJBIG2Generic writes comes near it: it emits two segments per page.
//
// TestEncodeDecodeSizeBoundary pins that list. A caller holding a page larger
// than the envelope and needing to read it back must tile it; nothing here will
// silently produce a partial raster instead.
//
// It decodes IMMEDIATE GENERIC REGIONS ONLY, coded with GBTEMPLATE 0 and the
// nominal AT pixels of T.88 Table 5. Every other JBIG2 coding mode returns
// ErrUnsupportedJBIG2Feature and no bitmap. That covers a large share of the
// JBIG2 in the wild: archive scanners commonly emit symbol-mode (a symbol
// dictionary plus text regions), and none of it is decodable here.
//
// The refusal is the point, not a gap left to fill in later. The MQ arithmetic
// decoder returns a decision for any input whatsoever, so running it over a
// symbol-mode or MMR stream does not fail -- it yields a full-size bitmap of
// noise. An error a caller can route around is strictly better than a raster
// that is wrong without looking wrong.
//
// Page-0 (global) segments from a PDF /JBIG2Globals stream are not consulted.
// They carry symbol and pattern dictionaries, which nothing here can use.
func DecodeJBIG2Generic(data []byte) (*Bitmap, error) {
	b, err := jbig2.DecodeEmbeddedStream(data)
	if err != nil {
		if errors.Is(err, jbig2.ErrUnsupportedFeature) {
			return nil, fmt.Errorf("%w: %v", ErrUnsupportedJBIG2Feature, err)
		}
		return nil, fmt.Errorf("byblos: DecodeJBIG2Generic: %w", err)
	}
	// Same packing and the same ink convention in both directions, so the pixel
	// buffer transfers rather than being rewritten.
	return &Bitmap{Width: b.W, Height: b.H, Stride: b.Stride, Pix: b.Pix}, nil
}

// decodeJBIG2Placement is extractPage's JBIG2 branch: check the stream against
// the image dictionary, decode it, and hand back a raster in the polarity a
// renderer would show.
//
// Two dictionary facts can make a correctly decoded bitmap the wrong answer,
// and both are checked rather than assumed away, because each one's failure
// mode is a silently inverted or mis-shaped page rather than an error:
//
//   - /Decode remaps samples. On a 1-bit image /Decode [1 0] inverts it, so a
//     page that decoded perfectly comes out as its own negative. ImageInfo
//     records only that the array is PRESENT, not its contents, so there is
//     nothing here to apply and the page diverts.
//   - /Width and /Height are what the PDF says the raster is. A JBIG2 page of a
//     different size means the stream was not the one this dictionary describes,
//     or that it was mis-parsed; either way the placement geometry the caller
//     gets back would not describe the pixels.
//
// The ORDER of those checks is load-bearing, not tidiness. This runs on bytes
// taken straight out of an untrusted PDF, and a JBIG2 generic region is the one
// codec byblos handles whose output is not bounded by its input: 67 bytes can
// legitimately ask for half a billion pixels (see internal/jbig2's budgets).
// Decoding first and comparing afterwards means a stream that was NEVER going to
// be accepted -- the dictionary says 1x1 and the page is 8191x8191 -- is paid
// for in full before it is refused. Measured on this code with the two steps
// swapped: 67 bytes of JBIG2, 1.531s, 16.0 MiB, and 67,092,481 MQ decisions to
// produce an error the 26-byte region headers already implied. jbig2.PageSize
// resolves the page from those headers, so in the order written here the same
// mismatch is refused in about a microsecond, having decoded nothing.
//
// The size cap in front of it is the same argument one step earlier. grayImage
// expands the page to ONE BYTE PER PIXEL, 8x the packed bitmap, so the raster
// byblos hands out is the largest thing on this path; jbig2.MaxPagePixels bounds
// it at 64 MiB, and testing the dictionary's own declared dimensions against
// that refuses an absurd image without opening the stream at all.
//
// MEASURED through ExtractPageRaster on the worst streams all three gates
// admit AT 67 BYTES, both of them 67 bytes of JBIG2 in a 1008-byte PDF. The most
// ALLOCATION is an 8191x8191 page under a 1x1 region: 72.3 MiB in 53ms, one
// pixel decoded and 67,092,481 handed back. The most TIME is the same page under
// a page-covering region: 1.77s and 80.3 MiB, 67,092,481 decoded for 67,092,481
// handed back. Both are one maximal page and nothing more, which is the bar
// internal/jbig2's budget comment states -- cost proportional to useful output.
// A legitimate 600-dpi A4 scan on this path costs 909ms and 41.8 MiB.
//
// THAT QUALIFIER IS LOAD-BEARING and an earlier version of this comment did not
// carry it, which is how jbig2_decode_test.go's 88 MiB ceiling came to be
// applied to a wider set of streams than it was measured on. The most
// allocation full stop is not a 67-byte stream, because a 67-byte stream cannot
// carry segment HEADERS: a 1024x65536 page under 65,535 one-pixel regions --
// 2,424,825 bytes, exactly internal/jbig2's segment cap, and admitted by every
// rule -- costs 113.7 MiB and 130ms for the same one maximal page. The extra
// over the 67-byte case is the header cost rule 5 concedes plus the 2.4 MB PDF
// that has to be read to reach it, and both are proportional to the input: the
// same document with ONE more segment, refused from the headers with nothing
// decoded, already costs 18.0 MiB on its own.
// TestExtractPageRasterCeilingBoundsEveryStreamTheGatesAdmit is what bounds the
// whole admitted set; the 88 MiB figure remains the tight bound on the 67-byte
// shape, where it is 1.2x the measurement.
//
// The stream that used to escape all of it -- a 1x1 page under an 8192x4095
// region, 67 bytes, every gate satisfied, 33,546,240 pixels decoded and then
// dropped for a one-pixel raster -- now costs 67us here and decodes nothing:
// internal/jbig2 refuses it from the headers, because the page cannot show what
// the regions ask to decode. With no budget at all a 326-byte stream cost 38.1s
// and 512 MiB.
//
// /ImageMask, /SMask and /Mask need no check here: opaqueCover already refuses
// to treat such an image as the page's raster, so classify has diverted the page
// as vector-paint long before this runs.
//
// Every error it returns opens with "jbig2:", matching the errors
// internal/jbig2 returns, so extractPage can wrap the lot without naming the
// codec a second time.
func decodeJBIG2Placement(data []byte, imageInfo func(int) (pdfdoc.ImageInfo, bool), id int) (image.Image, error) {
	info, ok := imageInfo(id)
	if !ok {
		return nil, fmt.Errorf("jbig2: image %d has no dictionary to check the decoded raster against", id)
	}
	if info.Decode {
		return nil, fmt.Errorf("jbig2: image %d carries a /Decode array, which byblos cannot apply to a bilevel raster", id)
	}
	if px := int64(info.Width) * int64(info.Height); px > jbig2.MaxPagePixels {
		return nil, fmt.Errorf("jbig2: image %d's dictionary declares %dx%d, %d pixels; byblos renders a "+
			"bilevel page at one byte per pixel and the limit is %d",
			id, info.Width, info.Height, px, int64(jbig2.MaxPagePixels))
	}
	w, h, err := jbig2.PageSize(data)
	if err != nil {
		return nil, err
	}
	if w != info.Width || h != info.Height {
		return nil, fmt.Errorf("jbig2: page is %dx%d but image %d's dictionary says %dx%d",
			w, h, id, info.Width, info.Height)
	}
	b, err := jbig2.DecodeEmbeddedStream(data)
	if err != nil {
		return nil, err
	}
	return grayImage(b), nil
}

// grayImage renders b as an 8-bit greyscale image under the PDF /DeviceGray
// convention: ink (bit 1) becomes 0x00 and background becomes 0xFF.
//
// That inversion is the Bitmap-to-PDF boundary Bitmap's own doc comment names.
// It is applied here rather than left to the caller because the result is handed
// out as an image.Image to code -- OCR, thumbnailing, human review -- that has
// no way to know a photographic negative from a page.
//
// *image.Gray rather than a one-bit image.Image over the same buffer: it costs
// 8x the memory and buys interoperability with everything that type-switches on
// the concrete type for a fast path, including Downsample in this package. That
// 8x is why jbig2.MaxPagePixels is the bound that matters on this path: the
// result of this function is MaxPagePixels bytes, the largest single allocation
// anywhere in a JBIG2 extraction.
func grayImage(b *jbig2.Bitmap) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, b.W, b.H))
	for y := 0; y < b.H; y++ {
		row := g.Pix[y*g.Stride : y*g.Stride+b.W]
		for x := range row {
			if b.Get(x, y) != 0 {
				row[x] = 0x00
			} else {
				row[x] = 0xFF
			}
		}
	}
	return g
}
