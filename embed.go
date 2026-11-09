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

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// A PNG's declared Width*Height is bounded by Limits.MaxEncodedImagePixels,
// default 1<<26 = 67,108,864: this module's design point (also
// internal/jbig2's MaxPagePixels), 256 MB of RGBA, roughly twice a 600 DPI A4.

// EmbedPNG lifts a PNG file's pixel data into a ready EncodedImage under
// /FlateDecode, plus the DPI its pHYs chunk declares (0 if the file declares
// none -- see BuildPage's doc comment on why 0 is not silently defaulted).
//
// Refused, because none of these can be carried by this route: colour types 4
// and 6 (grayscale+alpha, truecolour+alpha -- alpha needs an /SMask split
// this function does not do), a tRNS chunk on any other colour type (that's
// colour-key or per-palette-entry transparency -- the same SMask gap),
// interlace method other than 0 (Adam7 rows are not PDF scanlines) and 16-bit
// depth (internal/pdfbuild's writer rejects it; see validatePage's doc
// comment).
func EmbedPNG(data []byte) (EncodedImage, float64, error) {
	return EmbedPNGLimits(data, Limits{})
}

// EmbedPNGLimits is EmbedPNG under lim. An IHDR over lim.MaxEncodedImagePixels
// is refused as a *LimitError named encoded-image-pixels, from the header and
// before the image is built. It takes no ctx: it reads one in-memory file's
// headers and copies its IDAT, less than a band (context.go).
func EmbedPNGLimits(data []byte, lim Limits) (EncodedImage, float64, error) {
	info, err := parsePNG(data)
	if err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: %w", err)
	}
	if info.bitDepth == 16 {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: 16-bit-per-component PNG is not supported")
	}
	if info.interlace != 0 {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: interlaced PNG is not supported")
	}
	if info.hasTRNS {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type %d has a tRNS chunk, which needs an /SMask split this function does not do", info.colorType)
	}
	// byb-37h: refuse an oversized IHDR before it becomes an EncodedImage
	// that a caller other than BuildPDF might use directly. MulSat because
	// width and height are each a raw uint32 out of IHDR, and their product
	// can overflow before a comparison would catch it.
	if err := limits.Check("encoded-image-pixels", limits.MulSat(int64(info.width), int64(info.height)), lim.EncodedImagePixels()); err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: %w", err)
	}

	var cs ColorSpace
	var colors int
	switch info.colorType {
	case 0: // grayscale
		if info.bitDepth != 1 && info.bitDepth != 2 && info.bitDepth != 4 && info.bitDepth != 8 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type 0 does not allow bit depth %d", info.bitDepth)
		}
		cs, colors = ColorSpace{Name: "DeviceGray"}, 1
	case 2: // truecolour
		if info.bitDepth != 8 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type 2 does not allow bit depth %d", info.bitDepth)
		}
		cs, colors = ColorSpace{Name: "DeviceRGB"}, 3
	case 3: // indexed
		if info.bitDepth != 1 && info.bitDepth != 2 && info.bitDepth != 4 && info.bitDepth != 8 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type 3 does not allow bit depth %d", info.bitDepth)
		}
		if len(info.plte) == 0 || len(info.plte)%3 != 0 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: indexed PNG has no usable PLTE chunk")
		}
		cs = ColorSpace{Name: "Indexed", Base: "DeviceRGB", HiVal: len(info.plte)/3 - 1, Lookup: info.plte}
		colors = 1
	case 4, 6:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type %d carries alpha, which needs an /SMask split this function does not do", info.colorType)
	default:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: unsupported PNG colour type %d", info.colorType)
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
	dpi                 float64
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
				if ppux := binary.BigEndian.Uint32(c.data[0:4]); ppux > 0 {
					info.dpi = float64(ppux) * 0.0254
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
// generation of quality -- plus the DPI its JFIF APP0 segment declares (0 if
// the file declares none or carries only an aspect ratio).
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
func EmbedJPEG(data []byte) (EncodedImage, float64, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJPEG: %w", err)
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
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJPEG: unsupported JPEG colour model")
	}
	return EncodedImage{
		Width:      cfg.Width,
		Height:     cfg.Height,
		BPC:        8,
		ColorSpace: cs,
		Filter:     "DCTDecode",
		Decode:     decode,
		Data:       data,
	}, jfifDPI(data), nil
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
// entry is malformed, is an error. All of this is img2pdf's behaviour (checked
// on testdata/img2pdf). It reads the headers only.
func EXIFRotation(data []byte) (int, error) {
	exif := jpegSegment(data, func(m byte, p []byte) bool { return m == 0xE1 && bytes.HasPrefix(p, []byte("Exif\x00\x00")) })
	if exif == nil {
		return 0, nil
	}
	t := exif[6:]
	var bo binary.ByteOrder
	switch {
	case bytes.HasPrefix(t, []byte("II\x2a\x00")):
		bo = binary.LittleEndian
	case bytes.HasPrefix(t, []byte("MM\x00\x2a")):
		bo = binary.BigEndian
	default:
		return 0, fmt.Errorf("byblos: EXIFRotation: bad TIFF header")
	}
	if len(t) < 8 {
		return 0, fmt.Errorf("byblos: EXIFRotation: bad TIFF header")
	}
	ifd := int64(bo.Uint32(t[4:]))
	if ifd+2 > int64(len(t)) {
		return 0, fmt.Errorf("byblos: EXIFRotation: IFD0 is outside the Exif block")
	}
	n := int64(bo.Uint16(t[ifd:]))
	for e := ifd + 2; n > 0 && e+12 <= int64(len(t)); e, n = e+12, n-1 {
		if bo.Uint16(t[e:]) != 0x0112 {
			continue
		}
		if bo.Uint16(t[e+2:]) != 3 || bo.Uint32(t[e+4:]) != 1 {
			return 0, fmt.Errorf("byblos: EXIFRotation: orientation entry is not one SHORT")
		}
		switch o := bo.Uint16(t[e+8:]); o {
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
	return 0, nil
}

// EmbedJP2 lifts a JP2 file under /JPXDecode, verbatim, after reading its
// boxes as img2pdf does: size and bit depth from ihdr, colour from the first
// colr box, which must be enumerated sRGB (DeviceRGB) or greyscale
// (DeviceGray); CMYK, sYCC and ICC-profile files are refused, as img2pdf
// refuses them. The DPI is the res box's capture resolution (0 if none). The
// pixel cap in lim is checked against the ihdr before the image is built, so a
// forged header is refused as a *LimitError. byblos does not decode JPX; the
// returned image is for BuildPDF only. It reads the boxes only and takes no ctx.
func EmbedJP2(data []byte, lim Limits) (EncodedImage, float64, error) {
	var w, h uint32
	var depth, enumCS int
	var dpi float64
	haveIHDR, haveColr, haveCodestream := false, false, false
	if !bytes.HasPrefix(data, pdfbuild.JP2Signature) {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: no JP2 signature box")
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
			case "resc":
				if len(body) == 10 && dpi == 0 { // HRcN HRcD at 4..8, HRcE at 9
					if d := float64(binary.BigEndian.Uint16(body[6:])); d > 0 {
						dpi = float64(binary.BigEndian.Uint16(body[4:])) / d * math.Pow(10, float64(int8(body[9]))) * 0.0254
					}
				}
			case "jp2c":
				haveCodestream = true
			}
			b = b[size:]
		}
		return nil
	}
	if err := walk(data, 0); err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	if !haveIHDR || !haveColr || !haveCodestream {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: no ihdr, colr or codestream box")
	}
	if w == 0 || h == 0 {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: ihdr declares %dx%d", w, h)
	}
	if err := limits.Check("encoded-image-pixels", limits.MulSat(int64(w), int64(h)), lim.EncodedImagePixels()); err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	if err := limits.Check("encoded-image-pixels", int64(max(w, h)), lim.EncodedImagePixels()); err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	var cs ColorSpace
	switch enumCS {
	case 16:
		cs = ColorSpace{Name: "DeviceRGB"}
	case 17:
		cs = ColorSpace{Name: "DeviceGray"}
	default:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJP2: only sRGB and greyscale are supported, got enumerated colour space %d", enumCS)
	}
	return EncodedImage{Width: int(w), Height: int(h), BPC: depth, ColorSpace: cs, Filter: "JPXDecode", Data: data}, dpi, nil
}

// jfifDPI returns the DPI data's JFIF APP0 segment declares, or 0 if data
// declares none or carries only an aspect ratio. Per the JFIF spec (ITU-T
// T.871 sec 4), an APP0 JFIF marker segment -- if present -- must be the
// first segment after SOI, so this looks only there rather than walking the
// whole marker chain; jpeg.DecodeConfig has already validated data's overall
// structure by the time this runs.
func jfifDPI(data []byte) float64 {
	if len(data) < 20 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF || data[3] != 0xE0 {
		return 0
	}
	length := int(binary.BigEndian.Uint16(data[4:6]))
	if length < 14 || 4+length > len(data) {
		return 0
	}
	seg := data[6 : 4+length]
	if len(seg) < 12 || string(seg[0:5]) != "JFIF\x00" {
		return 0
	}
	units := seg[7]
	density := int(binary.BigEndian.Uint16(seg[8:10]))
	switch {
	case units == 1 && density > 0: // dots per inch
		return float64(density)
	case units == 2 && density > 0: // dots per cm
		return float64(density) * 2.54
	}
	return 0
}
