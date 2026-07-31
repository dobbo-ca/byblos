package byblos

import (
	"fmt"

	"github.com/dobbo-ca/byblos/internal/jbig2"
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
