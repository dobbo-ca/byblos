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
// It is the exact inverse of EncodeJBIG2Generic and nothing wider: every stream
// EncodeJBIG2Generic produces decodes back bit-identically, which is what makes
// a byblos archive re-openable by byblos.
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
// the concrete type for a fast path, including Downsample in this package.
// decodeJBIG2Placement is extractPage's JBIG2 branch: decode the stream, check
// it against the image dictionary, and hand back a raster in the polarity a
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
	b, err := jbig2.DecodeEmbeddedStream(data)
	if err != nil {
		return nil, err
	}
	if b.W != info.Width || b.H != info.Height {
		return nil, fmt.Errorf("jbig2: page is %dx%d but image %d's dictionary says %dx%d",
			b.W, b.H, id, info.Width, info.Height)
	}
	return grayImage(b), nil
}

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
