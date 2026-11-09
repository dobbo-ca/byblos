package byblos

// EmbedPNG and EmbedJPEG are the missing route byb-js5.5 names: BuildPDF's
// only image-bytes constructor before this file was QuantizeIndexed, which is
// lossy by construction (median-cut/Lloyd over <=256 colours) and refuses any
// non-opaque pixel. A caller with an already-encoded PNG or JPEG that must
// reach a PDF LOSSLESSLY -- Kleio's img2pdf call site is exactly this -- had
// no exported way to do that.
//
// Both functions lift the source bytes rather than re-encoding them: EmbedPNG
// pulls the IHDR fields and the concatenated IDAT payload straight out of the
// PNG's own chunk framing (that payload already IS a /FlateDecode
// /Predictor 15 scanline stream -- see extractPLTEAndIDAT's doc comment in
// quantize_indexed.go for why), and EmbedJPEG carries the JPEG bytes
// unmodified under /DCTDecode, using image/jpeg.DecodeConfig for the
// dimensions and colour space and a JFIF APP0 scan for the declared density.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"math"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfbuild"
)

// Resolution is a raster's pixels per inch along each axis, whole numbers as
// img2pdf reads them. Zero means the file declares none (BuildPage.DPI is then
// the caller's to choose; img2pdf uses 96).
type Resolution struct{ X, Y float64 }

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// A PNG's declared Width*Height is bounded by Limits.MaxEncodedImagePixels,
// default 1<<26 = 67,108,864: this module's design point (also
// internal/jbig2's MaxPagePixels), 256 MB of RGBA, roughly twice a 600 DPI A4.

// EmbedPNG lifts a PNG file's pixel data into a ready EncodedImage under
// /FlateDecode, plus the resolution its pHYs chunk declares (zero if the file
// declares none -- see BuildPage's doc comment on why 0 is not silently
// defaulted), rounded per axis as img2pdf does.
//
// Refused, because none of these can be carried by this route: colour types 4
// and 6 (grayscale+alpha, truecolour+alpha -- alpha needs an /SMask split
// this function does not do), a tRNS chunk on any other colour type (that's
// colour-key or per-palette-entry transparency -- the same SMask gap),
// interlace method other than 0 (Adam7 rows are not PDF scanlines) and 16-bit
// depth (internal/pdfbuild's writer rejects it; see validatePage's doc
// comment).
func EmbedPNG(data []byte) (EncodedImage, Resolution, error) {
	return EmbedPNGLimits(data, Limits{})
}

// EmbedPNGLimits is EmbedPNG under lim. An IHDR over lim.MaxEncodedImagePixels
// is refused as a *LimitError named encoded-image-pixels, from the header and
// before the image is built. It takes no ctx: it reads one in-memory file's
// headers and copies its IDAT, less than a band (context.go).
func EmbedPNGLimits(data []byte, lim Limits) (EncodedImage, Resolution, error) {
	info, err := parsePNG(data)
	if err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: %w", err)
	}
	if info.bitDepth == 16 {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: 16-bit-per-component PNG is not supported")
	}
	if info.interlace != 0 {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: interlaced PNG is not supported")
	}
	if info.hasTRNS {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: colour type %d has a tRNS chunk, which needs an /SMask split this function does not do", info.colorType)
	}
	// byb-37h: refuse an oversized IHDR before it becomes an EncodedImage
	// that a caller other than BuildPDF might use directly. MulSat because
	// width and height are each a raw uint32 out of IHDR, and their product
	// can overflow before a comparison would catch it.
	if err := limits.Check("encoded-image-pixels", limits.MulSat(int64(info.width), int64(info.height)), lim.EncodedImagePixels()); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: %w", err)
	}

	var cs ColorSpace
	var colors int
	switch info.colorType {
	case 0: // grayscale
		if info.bitDepth != 1 && info.bitDepth != 2 && info.bitDepth != 4 && info.bitDepth != 8 {
			return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: colour type 0 does not allow bit depth %d", info.bitDepth)
		}
		cs, colors = ColorSpace{Name: "DeviceGray"}, 1
	case 2: // truecolour
		if info.bitDepth != 8 {
			return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: colour type 2 does not allow bit depth %d", info.bitDepth)
		}
		cs, colors = ColorSpace{Name: "DeviceRGB"}, 3
	case 3: // indexed
		if info.bitDepth != 1 && info.bitDepth != 2 && info.bitDepth != 4 && info.bitDepth != 8 {
			return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: colour type 3 does not allow bit depth %d", info.bitDepth)
		}
		if len(info.plte) == 0 || len(info.plte)%3 != 0 {
			return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: indexed PNG has no usable PLTE chunk")
		}
		cs = ColorSpace{Name: "Indexed", Base: "DeviceRGB", HiVal: len(info.plte)/3 - 1, Lookup: info.plte}
		colors = 1
	case 4, 6:
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: colour type %d carries alpha, which needs an /SMask split this function does not do", info.colorType)
	default:
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: unsupported PNG colour type %d", info.colorType)
	}

	depth := int(info.bitDepth)
	return EncodedImage{
		Width:      info.width,
		Height:     info.height,
		BPC:        depth,
		ColorSpace: cs,
		Filter:     "FlateDecode",
		DecodeParms: &DecodeParms{
			Predictor:        15,
			Colors:           colors,
			BitsPerComponent: depth,
			Columns:          info.width,
		},
		Data: info.idat,
	}, info.dpi, nil
}

// pngInfo is everything EmbedPNG needs out of one walk over a PNG file's
// chunk framing (ISO/IEC 15948).
type pngInfo struct {
	width, height       int
	bitDepth, colorType byte
	interlace           byte
	plte, idat          []byte
	hasTRNS             bool
	dpi                 Resolution
}

func parsePNG(data []byte) (pngInfo, error) {
	var info pngInfo
	if len(data) < len(pngSignature) || [8]byte(data[:8]) != pngSignature {
		return info, fmt.Errorf("not a PNG file")
	}
	chunks, err := walkPNGChunks(data)
	if err != nil {
		return info, err
	}
	sawIHDR := false
	for _, c := range chunks {
		switch c.typ {
		case "IHDR":
			if len(c.data) != 13 {
				return info, fmt.Errorf("IHDR is %d bytes; want 13", len(c.data))
			}
			info.width = int(binary.BigEndian.Uint32(c.data[0:4]))
			info.height = int(binary.BigEndian.Uint32(c.data[4:8]))
			info.bitDepth = c.data[8]
			info.colorType = c.data[9]
			info.interlace = c.data[12]
			sawIHDR = true
		case "PLTE":
			info.plte = append([]byte(nil), c.data...)
		case "IDAT":
			info.idat = append(info.idat, c.data...)
		case "tRNS":
			info.hasTRNS = true
		case "pHYs":
			if len(c.data) == 9 && c.data[8] == 1 { // unit specifier 1 = metre
				info.dpi = Resolution{
					math.RoundToEven(float64(binary.BigEndian.Uint32(c.data[0:4])) * 0.0254),
					math.RoundToEven(float64(binary.BigEndian.Uint32(c.data[4:8])) * 0.0254),
				}
			}
		}
	}
	if !sawIHDR {
		return info, fmt.Errorf("no IHDR chunk")
	}
	if info.width <= 0 || info.height <= 0 {
		return info, fmt.Errorf("IHDR declares %dx%d; dimensions must be positive", info.width, info.height)
	}
	if info.idat == nil {
		return info, fmt.Errorf("no IDAT chunk")
	}
	return info, nil
}

// EmbedJPEG lifts a JPEG file's compressed data into a ready EncodedImage
// under /DCTDecode -- carried verbatim, not re-encoded, so the embed costs no
// generation of quality -- plus its resolution as img2pdf (through Pillow)
// reads it, per axis and rounded: the JFIF density if the units are inches or
// centimetres, else the Exif XResolution and ResolutionUnit (72 if the file
// has an Exif block that does not give them), else zero.
//
// Dimensions, precision and colour components come from image/jpeg's own
// DecodeConfig, which reads exactly the same SOF marker byblos's own JPEG
// reader (extract.go) does. DecodeConfig accepts only single-scan baseline/
// extended-sequential/progressive, 8-bit-precision frames.
//
// A 4-component file (CMYK, or Adobe YCCK) is DeviceCMYK. Adobe writes such
// samples inverted and marks the file with an APP14 "Adobe" segment, so with
// that segment present the image carries /Decode [1 0 1 0 1 0 1 0], as img2pdf
// writes it; without it the samples are taken as plain CMYK. byblos's own read
// side does not decode CMYK JPEG, so ExtractPageRaster refuses such a page.
func EmbedJPEG(data []byte) (EncodedImage, Resolution, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJPEG: %w", err)
	}
	var cs ColorSpace
	var decode []float64
	switch cfg.ColorModel {
	case color.GrayModel:
		cs = ColorSpace{Name: "DeviceGray"}
	case color.YCbCrModel, color.RGBAModel:
		cs = ColorSpace{Name: "DeviceRGB"}
	case color.CMYKModel:
		cs = ColorSpace{Name: "DeviceCMYK"}
		if hasAdobeSegment(data) {
			decode = []float64{1, 0, 1, 0, 1, 0, 1, 0}
		}
	default:
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJPEG: unsupported JPEG colour model")
	}
	return EncodedImage{
		Width:      cfg.Width,
		Height:     cfg.Height,
		BPC:        8,
		ColorSpace: cs,
		Filter:     "DCTDecode",
		Decode:     decode,
		Data:       data,
	}, jpegDPI(data), nil
}

// jpegSegment returns the payload (after the two length bytes) of the first
// marker segment before the scan for which pick returns true, or nil. It walks
// only the headers, so it is bounded by the file and allocates nothing.
func jpegSegment(data []byte, pick func(marker byte, payload []byte) bool) []byte {
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return nil
		}
		m := data[i+1]
		switch {
		case m == 0xFF: // fill byte
			i++
			continue
		case m == 0x01 || m >= 0xD0 && m <= 0xD8: // no length
			i += 2
			continue
		case m == 0xD9 || m == 0xDA: // end of image, start of scan
			return nil
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return nil
		}
		if p := data[i+4 : i+2+n]; pick(m, p) {
			return p
		}
		i += 2 + n
	}
	return nil
}

func hasAdobeSegment(data []byte) bool {
	return jpegSegment(data, func(m byte, p []byte) bool { return m == 0xEE && bytes.HasPrefix(p, []byte("Adobe")) }) != nil
}

// ErrMirroredOrientation is EXIFRotation's refusal of orientations 2, 4, 5 and
// 7, which flip the image and which /Rotate cannot express. img2pdf refuses
// them too, so the in-process path parks them as it does today.
var ErrMirroredOrientation = errors.New("byblos: EXIF orientation is mirrored")

// EXIFRotation returns the /Rotate (0, 90, 180 or 270) that a JPEG's EXIF
// orientation asks for, for BuildPage.Rotate: orientation 1 or no Exif at all
// is 0, 3 is 180, 6 is 90, 8 is 270. Mirrored 2, 4, 5 and 7 are
// ErrMirroredOrientation; any other value, or an Exif block whose orientation
// entry is malformed, is an error. This is img2pdf's behaviour (checked on
// testdata/img2pdf), including Pillow's leniency: the value may be a SHORT or a
// LONG, only the first of several counts, and an empty entry is no orientation.
// It reads the headers only.
func EXIFRotation(data []byte) (int, error) {
	f, _, err := exifTag(data, 0x0112)
	if err != nil {
		return 0, fmt.Errorf("byblos: EXIFRotation: %w", err)
	}
	if f == nil {
		return 0, nil
	}
	o, ok := f.uint()
	if !ok {
		return 0, fmt.Errorf("byblos: EXIFRotation: orientation entry is not a SHORT or LONG")
	}
	switch o {
	case 1:
		return 0, nil
	case 3:
		return 180, nil
	case 6:
		return 90, nil
	case 8:
		return 270, nil
	case 2, 4, 5, 7:
		return 0, ErrMirroredOrientation
	default:
		return 0, fmt.Errorf("byblos: EXIFRotation: invalid orientation %d", o)
	}
}

// exifField is the first value of one IFD0 entry of a JPEG's Exif block: typ is
// its TIFF type and val the bytes of that value, nil for a type exifTag does not
// size (anything but SHORT, LONG and RATIONAL).
type exifField struct {
	typ uint16
	val []byte
	bo  binary.ByteOrder
}

// uint is f as a SHORT or LONG.
func (f *exifField) uint() (uint32, bool) {
	switch f.typ {
	case 3:
		return uint32(f.bo.Uint16(f.val)), true
	case 4:
		return f.bo.Uint32(f.val), true
	}
	return 0, false
}

// exifTag finds tag in IFD0 of data's Exif block. exif reports whether there is
// a block at all; f is nil when the tag is absent or its count is zero (Pillow
// drops an empty entry). It reads the headers only.
func exifTag(data []byte, tag uint16) (f *exifField, exif bool, err error) {
	seg := jpegSegment(data, func(m byte, p []byte) bool { return m == 0xE1 && bytes.HasPrefix(p, []byte("Exif\x00\x00")) })
	if seg == nil {
		return nil, false, nil
	}
	t := seg[6:]
	var bo binary.ByteOrder
	switch {
	case bytes.HasPrefix(t, []byte("II\x2a\x00")):
		bo = binary.LittleEndian
	case bytes.HasPrefix(t, []byte("MM\x00\x2a")):
		bo = binary.BigEndian
	default:
		return nil, true, fmt.Errorf("bad TIFF header")
	}
	if len(t) < 8 {
		return nil, true, fmt.Errorf("bad TIFF header")
	}
	ifd := int64(bo.Uint32(t[4:]))
	if ifd+2 > int64(len(t)) {
		return nil, true, fmt.Errorf("IFD0 is outside the Exif block")
	}
	n := int64(bo.Uint16(t[ifd:]))
	for e := ifd + 2; n > 0 && e+12 <= int64(len(t)); e, n = e+12, n-1 {
		if bo.Uint16(t[e:]) != tag {
			continue
		}
		typ, count := bo.Uint16(t[e+2:]), bo.Uint32(t[e+4:])
		size := map[uint16]uint64{3: 2, 4: 4, 5: 8}[typ]
		if count == 0 {
			return nil, true, nil
		}
		f = &exifField{typ: typ, bo: bo}
		if size == 0 {
			return f, true, nil
		}
		val := t[e+8 : e+12] // inline if the whole value fits, else an offset to it
		if size*uint64(count) > 4 {
			off := uint64(bo.Uint32(val))
			if off+size > uint64(len(t)) {
				return nil, true, fmt.Errorf("value of tag %#x is outside the Exif block", tag)
			}
			val = t[off:]
		}
		f.val = val[:size]
		return f, true, nil
	}
	return nil, true, nil
}

// EmbedJP2 lifts a JP2 file under /JPXDecode, verbatim, after reading its
// boxes as img2pdf does: size and bit depth from ihdr, colour from the first
// colr box, which must be enumerated sRGB (DeviceRGB) or greyscale
// (DeviceGray); CMYK, sYCC and ICC-profile files are refused, as img2pdf
// refuses them. The resolution is the res box's capture resolution, rounded per axis as img2pdf rounds it (zero if none). The
// pixel cap in lim is checked against the ihdr before the image is built, so a
// forged header is refused as a *LimitError. byblos does not decode JPX; the
// returned image is for BuildPDF only. It reads the boxes only and takes no ctx.
func EmbedJP2(data []byte, lim Limits) (EncodedImage, Resolution, error) {
	var w, h uint32
	var depth, enumCS int
	var dpi Resolution
	haveIHDR, haveColr, haveCodestream := false, false, false
	if !bytes.HasPrefix(data, pdfbuild.JP2Signature) {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: no JP2 signature box")
	}
	var walk func(b []byte, nest int) error
	walk = func(b []byte, nest int) error {
		for len(b) > 0 {
			if len(b) < 8 {
				return fmt.Errorf("truncated box header")
			}
			size, typ, hdr := uint64(binary.BigEndian.Uint32(b)), string(b[4:8]), uint64(8)
			switch {
			case size == 1:
				if len(b) < 16 {
					return fmt.Errorf("truncated box header")
				}
				size, hdr = binary.BigEndian.Uint64(b[8:]), 16
			case size == 0:
				size = uint64(len(b))
			}
			if size < hdr || size > uint64(len(b)) {
				return fmt.Errorf("%q box overruns the file", typ)
			}
			body := b[hdr:size]
			switch typ {
			case "jp2h", "res ":
				if nest > 2 {
					return fmt.Errorf("boxes nest too deep")
				}
				if err := walk(body, nest+1); err != nil {
					return err
				}
			case "ihdr":
				if len(body) != 14 || haveIHDR {
					return fmt.Errorf("bad ihdr box")
				}
				h, w = binary.BigEndian.Uint32(body), binary.BigEndian.Uint32(body[4:])
				if body[10] == 0xFF || body[10]&0x80 != 0 {
					return fmt.Errorf("components differ in depth or are signed")
				}
				depth, haveIHDR = int(body[10])+1, true
			case "colr":
				if haveColr {
					break // only the first applies
				}
				if len(body) < 3 || body[0] != 1 || len(body) < 7 {
					return fmt.Errorf("colour is not an enumerated space")
				}
				enumCS, haveColr = int(binary.BigEndian.Uint32(body[3:])), true
			case "resc": // VRcN VRcD HRcN HRcD VRcE HRcE
				if len(body) == 10 && dpi == (Resolution{}) {
					perInch := func(num, den uint16, exp byte) float64 {
						if den == 0 {
							return 0
						}
						return math.RoundToEven(float64(num) / float64(den) * math.Pow(10, float64(exp)) * 0.0254)
					}
					u := binary.BigEndian.Uint16
					dpi = Resolution{perInch(u(body[4:]), u(body[6:]), body[9]), perInch(u(body[0:]), u(body[2:]), body[8])}
				}
			case "jp2c":
				haveCodestream = true
			}
			b = b[size:]
		}
		return nil
	}
	if err := walk(data, 0); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	if !haveIHDR || !haveColr || !haveCodestream {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: no ihdr, colr or codestream box")
	}
	if w == 0 || h == 0 {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: ihdr declares %dx%d", w, h)
	}
	if err := limits.Check("encoded-image-pixels", limits.MulSat(int64(w), int64(h)), lim.EncodedImagePixels()); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	if err := limits.Check("encoded-image-pixels", int64(max(w, h)), lim.EncodedImagePixels()); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	var cs ColorSpace
	switch enumCS {
	case 16:
		cs = ColorSpace{Name: "DeviceRGB"}
	case 17:
		cs = ColorSpace{Name: "DeviceGray"}
	default:
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: only sRGB and greyscale are supported, got enumerated colour space %d", enumCS)
	}
	return EncodedImage{Width: int(w), Height: int(h), BPC: depth, ColorSpace: cs, Filter: "JPXDecode", Data: data}, dpi, nil
}

// jpegDPI is the resolution Pillow reads from a JPEG, rounded as img2pdf
// rounds it: a JFIF density in inches or centimetres, else (with an Exif block
// present) the Exif XResolution in ResolutionUnit, 72 when the block lacks
// them or they do not parse, else zero. Pillow takes the vertical resolution
// from XResolution too.
func jpegDPI(data []byte) Resolution {
	if p := jpegSegment(data, func(m byte, p []byte) bool { return m == 0xE0 && bytes.HasPrefix(p, []byte("JFIF")) }); len(p) >= 12 {
		x, y := float64(binary.BigEndian.Uint16(p[8:])), float64(binary.BigEndian.Uint16(p[10:]))
		switch p[7] {
		case 1:
			return Resolution{x, y}
		case 2:
			return Resolution{math.RoundToEven(x * 2.54), math.RoundToEven(y * 2.54)}
		}
	}
	res, exif, err := exifTag(data, 0x011A)
	if !exif {
		return Resolution{}
	}
	unit, _, _ := exifTag(data, 0x0128)
	if err != nil || res == nil || unit == nil {
		return Resolution{72, 72}
	}
	var dpi float64
	if res.typ == 5 {
		num, den := res.bo.Uint32(res.val), res.bo.Uint32(res.val[4:])
		if den == 0 {
			return Resolution{72, 72}
		}
		dpi = float64(num) / float64(den)
	} else if v, ok := res.uint(); ok {
		dpi = float64(v)
	} else {
		return Resolution{72, 72}
	}
	if u, _ := unit.uint(); u == 3 {
		dpi *= 2.54
	}
	dpi = math.RoundToEven(dpi)
	return Resolution{dpi, dpi}
}
