package byblos

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	jbig2 "github.com/dobbo-ca/jbig2-go"
)

// CapabilityJBIG2Generic is the provenance capability string recorded for a
// page compressed with lossless JBIG2 generic region coding. A document whose
// provenance carries it is exactly the upgrade set for a future jbig2-symbol
// capability (see FUTURE.md).
const CapabilityJBIG2Generic = "jbig2-generic"

// maxJBIG2Pixels bounds a JBIG2 image's declared width x height. grayImage
// expands the page to one byte per pixel, 8x the packed bitmap, so this is the
// largest single allocation on the JBIG2 extraction path.
const maxJBIG2Pixels = 1 << 26

// Caps handed to jbig2-go, the same as the decoder this replaced used: they bound
// the page, its regions and every symbol, so a cap scaled to the dictionary's page
// would starve a document whose global dictionary is large. maxJBIG2Pixels is also
// the dictionary size gate, and a quarter of it is the 16 MiB packed-bitmap cap
// jbig2-go derives from it.
const (
	maxJBIG2Symbols = 1 << 16
	maxJBIG2Regions = 1 << 16
)

// jbig2DecodeBound is the most memory jbig2-go can estimate for a decode under
// the caps above (its docs/guides/memory-limits.md): the packed bitmaps at their
// cap, 96 bytes a declared symbol, 256 a segment, the joined stream, and 256 KiB
// of context arrays. It is a ceiling, not the estimate itself, because the
// estimate is only known once jbig2-go has read the headers.
func jbig2DecodeBound(streamLen int) int64 {
	return maxJBIG2Pixels/4 + 96*maxJBIG2Symbols + 256*maxJBIG2Regions + int64(streamLen) + 4<<16
}

// decodeJBIG2Placement is extractPage's JBIG2 branch: decode the stream through
// jbig2-go and hand back a raster in the polarity a renderer would show.
//
// The page size is checked BEFORE anything is reserved or decoded: jbig2.PageSize
// reads the page-information segment from the headers, and a page whose size
// disagrees with the dictionary is refused there, because decoding a page this
// big only to divert it costs seconds of arithmetic decoding on every pass. A
// striped page (height 0xFFFFFFFF, sized from its regions), a stream with no
// page-information segment and malformed headers have no size from PageSize
// (jbig2.ErrNoPageSize); they are not refused for that. Decode reads the headers
// again and refuses what is malformed or unsupported, and the decoded page's
// size is compared with the dictionary after it, so a striped page decodes and
// is checked as soon as it exists. That is a known deviation from the decoder this
// replaced, which sized a striped page from its regions and refused a mismatch
// before decoding: jbig2-go exposes no plan-based size.
//
// There is ONE memory pool. byblos reserves the raster (one byte a pixel) and
// jbig2-go's whole decode (jbig2DecodeBound) from its own budget, as the DCT
// path does, and hands jbig2-go Reserved and MaxBytes so it skips its own
// process-wide pool: byblos.SetMemoryLimit bounds the decode. A reservation over
// the whole budget is byblos's *LimitError (permanent), a budget held by others
// waits until ctx and is ErrMemoryLimit (transient); a *jbig2.LimitError named
// MaxBytes or MemoryLimit, which jbig2-go raises when it can never fit, is a
// *LimitError too. /Decode is applied through grayLevels, because on a 1-bit
// image [1 0] inverts the page.
//
// Errors opening with "jbig2:" are the document's or the codec's; byblos's
// *LimitError, ErrMemoryLimit and a cancelled ctx's error are not, and extractPage
// passes those through. A coding mode jbig2-go does not decode carries
// jbig2.ErrUnsupportedFeature.
func decodeJBIG2Placement(ctx context.Context, data, globals []byte, imageInfo func(int) (pdfdoc.ImageInfo, bool), id int) (image.Image, error) {
	img, _, err := decodeJBIG2(ctx, data, globals, imageInfo, id, false)
	return img, err
}

// decodeJBIG2 is decodeJBIG2Placement, or with packed the same page as a Bitmap
// whose set bits are the pixels that render dark (below 128), the raster's 8
// bytes a pixel never built: the reservation is the decode's alone. The Bitmap
// is the decode's own buffer, so nothing is copied.
func decodeJBIG2(ctx context.Context, data, globals []byte, imageInfo func(int) (pdfdoc.ImageInfo, bool), id int, packed bool) (image.Image, *Bitmap, error) {
	info, ok := imageInfo(id)
	if !ok {
		return nil, nil, fmt.Errorf("jbig2: image %d has no dictionary to check the decoded raster against", id)
	}
	ink, background, err := grayLevels(info, id)
	if err != nil {
		return nil, nil, err
	}
	px := int64(info.Width) * int64(info.Height)
	if px > maxJBIG2Pixels {
		return nil, nil, fmt.Errorf("jbig2: image %d's dictionary declares %dx%d, %d pixels; byblos renders a "+
			"bilevel page at one byte per pixel and the limit is %d",
			id, info.Width, info.Height, px, int64(maxJBIG2Pixels))
	}
	if w, h, err := jbig2.PageSize(data, globals); err == nil && (w != info.Width || h != info.Height) {
		return nil, nil, fmt.Errorf("jbig2: page is %dx%d but image %d's dictionary says %dx%d",
			w, h, id, info.Width, info.Height)
	}
	bound := jbig2DecodeBound(len(data) + len(globals))
	need := bound
	if !packed {
		need += px
	}
	release, err := limits.Reserve(ctx, need)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	b, err := jbig2.Decode(ctx, data, globals, jbig2.Limits{
		PagePixels: maxJBIG2Pixels, Symbols: maxJBIG2Symbols, Regions: maxJBIG2Regions,
		MaxBytes: bound, Reserved: true,
	})
	if err != nil {
		var le *jbig2.LimitError
		if errors.As(err, &le) && (le.Name == "MaxBytes" || le.Name == "MemoryLimit") {
			return nil, nil, &limits.LimitError{Name: "jbig2-memory", Got: le.Got, Max: le.Max}
		}
		return nil, nil, err
	}
	if b.W != info.Width || b.H != info.Height {
		return nil, nil, fmt.Errorf("jbig2: page is %dx%d but image %d's dictionary says %dx%d",
			b.W, b.H, id, info.Width, info.Height)
	}
	if packed {
		// An ink pixel renders as ink, a clear one as background.
		return nil, packBits(b.Pix, b.W, b.H, b.Stride, ink < 128, background < 128), nil
	}
	return grayImage(b, ink, background), nil, nil
}

// defaultInk and defaultBackground are the /DeviceGray samples a 1-bit image
// carries when it declares no /Decode array: the default array is [0 1] (ISO
// 32000-1 table 39), and the JBIG2Decode filter presents a JBIG2 ink pixel as
// sample 0.
const (
	defaultInk        uint8 = 0x00
	defaultBackground uint8 = 0xFF
)

// grayLevels resolves the /DeviceGray samples an ink bit and a background bit
// must become, applying image id's /Decode array.
//
// THE ARRAY IS APPLIED AT THIS BOUNDARY, and that is the whole of byb-e7n's
// design question. Two other places could have taken it -- invert the Bitmap
// after decoding, or carry a polarity flag beside the raster -- and both are
// worse for the same reason. Bitmap's convention is that a set bit is ink
// (bitmap.go), the JBIG2 codec produces exactly that, and neither fact has
// anything to do with /Decode. What /Decode remaps is the SAMPLE a renderer
// reads, which is what grayImage already builds, and already builds by
// inverting. So no new concept enters: grayImage's two constants become two
// arguments, and no caller learns a new field.
//
// POPPLER IS THE SPECIFICATION HERE (byb-3jq, byb-62t) and it was asked rather
// than remembered. Measured with pdftoppm 26.06.0 on a 4x4 JBIG2 raster whose
// top-left quadrant is ink, /DeviceGray, 1 bit per component:
//
//	/Decode absent    ink 0    background 255
//	/Decode [0 1]     ink 0    background 255   the default; a no-op
//	/Decode [1 0]     ink 255  background 0     exactly inverted
//	/Decode [0.5 1]   ink 128  background 255   not every array is a flip
//	/Decode [0 0]     ink 0    background 0     and not every one is bilevel
//	/Decode [2 -1]    ink 255  background 0     out of range, clamped
//
// The scaling is round(v * 255) and was checked rather than assumed: the same
// probe on [0.25 0.75] gives 64 and 191, and on [0.3333 0.6667] gives 85 and
// 170, which is what graySample computes to the level.
//
// So the rule is a two-entry lookup table rather than a flip: sample 0 takes
// the array's first number and sample 1 its second. Reading it as "invert when
// the array is [1 0]" gets the same answer on the two common arrays and a
// silently wrong one on the other four rows.
//
// WHAT IS STILL REFUSED. Each is a case where the numbers do not mean grey
// levels, so applying them would not be a narrower fix but a wrong one:
//
//   - Any colour space other than /DeviceGray, INCLUDING an unresolved one.
//     ImageInfo.ColorSpace is "" for an array or an indirect reference, which
//     is exactly where /Indexed lives. Measured with the same probe on
//     [/Indexed /DeviceGray 1 <8020>]: /Decode [1 0] swaps the two PALETTE
//     entries and poppler paints 32 and 128 where this would paint 255 and 0.
//   - An array pdfdoc could not read as numbers (ImageInfo.DecodeArray nil
//     while ImageInfo.Decode is true), or one that is not two numbers long. A
//     one-component image's array has two entries (ISO 32000-1 table 39), and
//     an array of any other length describes an image this is not.
//
// Both refusals still say "/Decode array", because the byb-9v0 census
// classifies this gate by that phrase and a refusal it cannot see is a refusal
// nobody counts.
//
// WHAT THE CORPUS ACTUALLY HOLDS, and it is not what the shape of this function
// suggests. Measured 2026-08-11 by a since-removed probe test over all four
// corpora, 5,672 sample files and 169,376 sample pages: of the 6,818 pages that are a single
// page-covering JBIG2 raster, 4,371 were held by this gate, and every one of
// them is /DeviceGray at 1 bit per component with an array of exactly two
// numbers. Two arrays appear in the whole sample and no others:
//
//	[0 1]   4,304 pages   the DEFAULT, which remaps nothing
//	[1 0]      67 pages   the inversion
//
// So the 2,773 pages this releases are all [0 1]: byblos was refusing them over
// an array that changes no pixel. And all 67 pages that DO invert are also
// coded in a mode byblos cannot decode, so not one of them is released here --
// the inverting path is live in the code and dead in the corpus, and it stops
// being dead when byb-9v0 lands a symbol-mode decoder.
//
// THAT IS WHY THE RENDER TEST EXISTS. No corpus page reaches this function with
// [1 0], so a corpus sweep cannot tell a correct inversion from a missing one.
// TestExtractedJBIG2RasterCarriesThePopplerGreysForADecodeArray builds the page
// the corpus does not have and asks poppler; it is the only assertion in the
// tree that fails if this table is wrong.
func grayLevels(info pdfdoc.ImageInfo, id int) (ink, background uint8, err error) {
	if !info.Decode {
		return defaultInk, defaultBackground, nil
	}
	if info.ColorSpace != "DeviceGray" {
		cs := info.ColorSpace
		if cs == "" {
			cs = "an array or an indirect reference"
		} else {
			cs = "/" + cs
		}
		return 0, 0, fmt.Errorf("jbig2: image %d carries a /Decode array against %s; byblos applies "+
			"one only in /DeviceGray, where its two numbers ARE the grey levels", id, cs)
	}
	if info.DecodeArray == nil {
		return 0, 0, fmt.Errorf("jbig2: image %d carries a /Decode array whose entries are not all "+
			"numbers, so there is nothing to apply", id)
	}
	if len(info.DecodeArray) != 2 {
		return 0, 0, fmt.Errorf("jbig2: image %d carries a /Decode array of %d entries; a "+
			"one-component image's array has two", id, len(info.DecodeArray))
	}
	return graySample(info.DecodeArray[0]), graySample(info.DecodeArray[1]), nil
}

// graySample maps a /DeviceGray component to an 8-bit sample, clamping outside
// [0, 1] as poppler does (see grayLevels for the measurement).
//
// The first case is written as a negated test rather than v <= 0 so that a NaN
// takes it. A float-to-uint8 conversion of a NaN is not defined by the Go
// specification, and these numbers come out of an untrusted file.
func graySample(v float64) uint8 {
	switch {
	case !(v > 0):
		return 0x00
	case v >= 1:
		return 0xFF
	}
	return uint8(math.Round(v * 255))
}

// grayImage renders b as an 8-bit greyscale image, writing ink for a set bit
// and background for a clear one. Under the PDF /DeviceGray convention and an
// absent /Decode array those are 0x00 and 0xFF; grayLevels resolves them.
//
// That inversion is the Bitmap-to-PDF boundary Bitmap's own doc comment names.
// It is applied here rather than left to the caller because the result is handed
// out as an image.Image to code -- OCR, thumbnailing, human review -- that has
// no way to know a photographic negative from a page. Taking the two levels as
// arguments is what lets /Decode be applied at the same seam and not a second
// one; grayLevels says why that seam is the right one.
//
// *image.Gray rather than a one-bit image.Image over the same buffer: it costs
// 8x the memory and buys interoperability with everything that type-switches on
// the concrete type for a fast path, including Downsample in this package. That
// 8x is why maxJBIG2Pixels is the bound that matters on this path: the
// result of this function is up to maxJBIG2Pixels bytes, the largest single
// allocation byblos makes in a JBIG2 extraction.
func grayImage(b *jbig2.Bitmap, ink, background uint8) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, b.W, b.H))
	for y := 0; y < b.H; y++ {
		row := g.Pix[y*g.Stride : y*g.Stride+b.W]
		for x := range row {
			if b.Get(x, y) != 0 {
				row[x] = ink
			} else {
				row[x] = background
			}
		}
	}
	return g
}
