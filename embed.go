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
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"math/big"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfbuild"
)

// Resolution is a raster's pixels per inch along each axis, whole numbers as
// img2pdf reads them. Zero means the file declares none (BuildPage.DPI is then
// the caller's to choose; img2pdf uses 96). A PNG that declares only an aspect
// ratio (pHYs unit 0) gets img2pdf's 96 dpi default scaled by it, as img2pdf
// does.
type Resolution struct{ X, Y float64 }

// check refuses a resolution with one zero axis, which img2pdf divides by
// (both zero is "none declared").
func (r Resolution) check() error {
	if (r.X == 0) != (r.Y == 0) {
		return fmt.Errorf("resolution %gx%g has a zero axis", r.X, r.Y)
	}
	return nil
}

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// A PNG's declared Width*Height is bounded by Limits.MaxEncodedImagePixels,
// default 1<<26 = 67,108,864: this module's design point (also
// internal/jbig2's MaxPagePixels), 256 MB of RGBA, roughly twice a 600 DPI A4.

// EmbedPNG lifts a PNG file's pixel data into a ready EncodedImage under
// /FlateDecode, plus the resolution its pHYs chunk declares (zero if the file
// declares none -- see BuildPage's doc comment on why 0 is not silently
// defaulted), rounded per axis as img2pdf does: pixels per metre (pHYs unit 1),
// else an aspect ratio (unit 0) scaling img2pdf's 96 dpi default. A resolution
// with one zero axis is refused, as img2pdf divides by it.
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

	if err := info.dpi.check(); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedPNG: %w", err)
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
	var phys pdfbuild.PNGPhys
	sawIDAT := false
	for _, c := range chunks {
		// Pillow cannot open a file with a bad CRC on a chunk before the first
		// IDAT (TranscodePNG refuses it too); one after is never read.
		if c.typ == "IDAT" {
			sawIDAT = true
		} else if !sawIDAT && !c.crcOK {
			return info, fmt.Errorf("%s chunk: bad CRC", c.typ)
		}
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
			if err := phys.Add(c.data); err != nil {
				return info, err
			}
		}
	}
	if !sawIHDR {
		return info, fmt.Errorf("no IHDR chunk")
	}
	x, y := phys.Resolution()
	info.dpi = Resolution{x, y}
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
// reads it, per axis and rounded: the density of the last JFIF segment whose
// units are inches or centimetres, else the Exif XResolution and ResolutionUnit
// (72 if the file has an Exif block that does not give them), else zero. A
// resolution with one zero axis is refused, as img2pdf divides by it.
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
	// Pillow reads the version out of a JFIF segment and cannot open the file if it is short.
	if jpegSegment(data, func(m byte, p []byte) bool { return m == 0xE0 && bytes.HasPrefix(p, []byte("JFIF")) && len(p) < 7 }) != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJPEG: JFIF segment is too short")
	}
	dpi := jpegDPI(data)
	if err := dpi.check(); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJPEG: %w", err)
	}
	return EncodedImage{
		Width:      cfg.Width,
		Height:     cfg.Height,
		BPC:        8,
		ColorSpace: cs,
		Filter:     "DCTDecode",
		Decode:     decode,
		Data:       data,
	}, dpi, nil
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

// EXIFRotation returns the /Rotate (0, 90, 180 or 270) that a JPEG's or PNG's
// EXIF orientation asks for, for BuildPage.Rotate: orientation 1 or no Exif at
// all is 0, 3 is 180, 6 is 90, 8 is 270. Mirrored 2, 4, 5 and 7 are
// ErrMirroredOrientation; any other value, or one that is not a number, is an
// error. This is img2pdf's behaviour (checked on testdata/img2pdf), reading the
// Exif block as Pillow does: the Exif APP1 segments of a JPEG, joined, or a
// PNG's last eXIf chunk or tEXt chunk named exif, else its "Raw profile type
// exif" text (found anywhere in data: a caller streaming a PNG may pass the file
// up to its first IDAT); with no orientation in it, the tiff:Orientation of the
// XMP packet (a JPEG's last xap APP1 segment, a PNG's XML:com.adobe.xmp text); a
// block that is not a TIFF is an error where Pillow raises on it (always for a
// PNG, for a JPEG only with a JFIF density) and has no tags where it does not; a
// block that is malformed, or whose IFD0
// is cut short, is read as far as it goes, and a tag Pillow skips (a type it
// does not know, an empty value) is absent; the last of a repeated tag wins; the
// value may be any numeric type, its first element if there are several. It
// reads the headers only.
func EXIFRotation(data []byte) (int, error) {
	return EXIFRotationLimits(data, Limits{})
}

// EXIFRotationLimits is EXIFRotation under lim. A PNG whose Exif and XMP text
// chunks inflate to more than lim.MaxPNGTextBytes together (with its tEXt
// chunks) is refused as a *LimitError named png-text-bytes, as img2pdf refuses
// it ("Too much memory used in text chunks"). Other text chunks are not
// inflated. It takes no ctx: it reads one in-memory file's headers.
func EXIFRotationLimits(data []byte, lim Limits) (int, error) {
	b, err := exifBlock(data, lim)
	if err != nil {
		return 0, fmt.Errorf("byblos: EXIFRotation: %w", err)
	}
	if loadFails(b.tiff) {
		// Pillow raises when it loads the block, and a JPEG's read at open is
		// the one that is swallowed (and cached, XMP and all) unless the JFIF
		// density spared it.
		if bytes.HasPrefix(data, pngSignature[:]) || jfifDensity(data) != nil {
			return 0, fmt.Errorf("byblos: EXIFRotation: Exif block is not a TIFF file")
		}
		return 0, nil
	}
	var o float64
	if f := tiffField(b.tiff, 0x0112); f != nil {
		var ok bool
		if o, ok = f.num(); !ok {
			return 0, fmt.Errorf("byblos: EXIFRotation: orientation entry is not a number")
		}
	} else if x, ok := xmpOrientation(b.xmp); ok {
		o = float64(x)
	} else {
		return 0, nil
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
		return 0, fmt.Errorf("byblos: EXIFRotation: invalid orientation %v", o)
	}
}

// exifField is one IFD0 entry of an Exif block: typ is its TIFF type and val the
// bytes of its values.
type exifField struct {
	typ uint16
	val []byte
	bo  binary.ByteOrder
}

// num is f's first value as the number Pillow hands img2pdf, which compares it with the
// orientations (a rational with a zero denominator is NaN); false for a type
// that is not a number (BYTE, ASCII, UNDEFINED).
func (f *exifField) num() (float64, bool) {
	u, v := f.bo, f.val
	switch f.typ {
	case 3:
		return float64(u.Uint16(v)), true
	case 4, 13:
		return float64(u.Uint32(v)), true
	case 6:
		return float64(int8(v[0])), true
	case 8:
		return float64(int16(u.Uint16(v))), true
	case 9:
		return float64(int32(u.Uint32(v))), true
	case 16:
		return float64(u.Uint64(v)), true
	case 11:
		return float64(math.Float32frombits(u.Uint32(v))), true
	case 12:
		return math.Float64frombits(u.Uint64(v)), true
	case 5, 10:
		n, d := float64(u.Uint32(v)), float64(u.Uint32(v[4:]))
		if f.typ == 10 {
			n, d = float64(int32(u.Uint32(v))), float64(int32(u.Uint32(v[4:])))
		}
		if d == 0 {
			return math.NaN(), true
		}
		return n / d, true
	}
	return 0, false
}

// exifSize is the size of one value of each TIFF type Pillow reads; the other
// types are skipped.
var exifSize = [17]uint8{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 13: 4, 16: 8}

// exifData is what Pillow's getexif works from: the TIFF data of the Exif block
// (minus any "Exif\0\0" it starts with; exif reports whether there is a block
// at all) and the XMP packet.
type exifData struct {
	tiff, xmp []byte
	exif      bool
}

var (
	exifPrefix = []byte("Exif\x00\x00")
	xapPrefix  = []byte("http://ns.adobe.com/xap/1.0/\x00")
)

// exifBlock finds data's Exif block as Pillow takes it. A JPEG's is the first
// Exif APP1 segment, its XMP the last xap one. A PNG's is the last of its eXIf
// chunks and tEXt chunks named exif, else its "Raw profile type exif" text
// (ImageMagick's), a hex dump, which is an error if it does not parse.
func exifBlock(data []byte, lim Limits) (d exifData, err error) {
	if bytes.HasPrefix(data, pngSignature[:]) {
		chunks, _ := walkPNGChunks(data)
		var raw []byte
		text := textBudget{max: lim.PNGTextBytes()}
		for _, c := range chunks {
			if c.typ == "eXIf" {
				d.tiff, d.exif = c.data, true
				continue
			}
			k, v, ok, err := pngText(c, &text)
			if err != nil {
				return d, err
			}
			switch {
			case !ok:
			case k == "exif" && c.typ == "tEXt":
				d.tiff, d.exif = v, true
			case k == "Raw profile type exif":
				raw = v
			case k == "XML:com.adobe.xmp":
				d.xmp = v
			}
		}
		if !d.exif && raw != nil {
			d.exif = true
			if lines := bytes.Split(raw, []byte("\n")); len(lines) > 3 {
				if d.tiff, err = fromHex(bytes.Join(lines[3:], nil)); err != nil {
					return d, err
				}
			}
		}
	} else {
		// Pillow joins every Exif segment into one block, a later one after the
		// first's end. ponytail: capped at 1 MiB so a file of nothing but APP1
		// segments cannot make a copy of itself; a real block is under 64 KiB.
		jpegSegment(data, func(m byte, p []byte) bool {
			if m == 0xE1 && bytes.HasPrefix(p, exifPrefix) {
				if !d.exif {
					d.tiff, d.exif = p[6:], true
				} else if len(d.tiff) < 1<<20 {
					d.tiff = append(d.tiff[:len(d.tiff):len(d.tiff)], p[6:]...)
				}
			} else if m == 0xE1 && bytes.HasPrefix(p, xapPrefix) {
				d.xmp = p[len(xapPrefix):]
			}
			return false
		})
	}
	for bytes.HasPrefix(d.tiff, exifPrefix) {
		d.tiff = d.tiff[6:]
	}
	return d, nil
}

// pngText is the key and value Pillow's PngStream gives a tEXt, zTXt or iTXt
// chunk (ok is false for a chunk it ignores), or its error.
func pngText(c pngChunk, text *textBudget) (k string, v []byte, ok bool, err error) {
	key, v, found := bytes.Cut(c.data, []byte{0})
	// Only these two compressed texts are read (exifBlock), so only they are
	// inflated.
	skip := string(key) != "Raw profile type exif" && string(key) != "XML:com.adobe.xmp"
	switch c.typ {
	case "zTXt":
		if skip {
			return "", nil, false, nil
		}
		if len(v) > 0 && v[0] != 0 {
			return "", nil, false, fmt.Errorf("unknown compression method %d in zTXt chunk", v[0])
		}
		if len(v) > 0 {
			v = v[1:]
		}
		v, err = inflateText(v, text)
	case "iTXt":
		// compression flag and method, language, translated key, text
		var parts [][]byte
		if len(v) >= 2 {
			parts = bytes.SplitN(v[2:], []byte{0}, 3)
		}
		if !found || len(parts) < 3 {
			return "", nil, false, nil
		}
		if v[0] != 0 {
			if skip || v[1] != 0 {
				return "", nil, false, nil
			}
			parts[2], err = inflateText(parts[2], text)
		} else {
			err = text.add(len(parts[2]))
		}
		v = parts[2]
	case "tEXt":
		err = text.add(len(v))
	default:
		return "", nil, false, nil
	}
	return string(key), v, len(key) > 0, err
}

// textBudget is Pillow's text_memory: what a file's text chunks may hold.
type textBudget struct{ used, max int64 }

func (t *textBudget) add(n int) error {
	if err := limits.Check("png-text-bytes", t.used+int64(n), t.max); err != nil {
		return err
	}
	t.used += int64(n)
	return nil
}

// inflateText is Pillow's _safe_zlib_decompress for text: nothing if the data
// is corrupt, what there is if it is cut short, an error past a MiB or past
// what is left of the file's text budget. It reads no more than that, so a
// bomb is never inflated.
func inflateText(b []byte, text *textBudget) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, nil
	}
	out, err := io.ReadAll(io.LimitReader(zr, min(1<<20, text.max-text.used)+1))
	if len(out) > 1<<20 {
		return nil, fmt.Errorf("decompressed text is too large")
	}
	if err := text.add(len(out)); err != nil {
		return nil, err
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, nil
	}
	return out, nil
}

// fromHex is Python's bytes.fromhex: pairs of hex digits, ASCII white space
// allowed between pairs.
func fromHex(s []byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); {
		if bytes.IndexByte([]byte(" \t\n\r\v\f"), s[i]) >= 0 {
			i++
			continue
		}
		var b [1]byte
		if i+2 > len(s) {
			return nil, fmt.Errorf("Raw profile type exif is not hex")
		}
		if _, err := hex.Decode(b[:], s[i:i+2]); err != nil {
			return nil, fmt.Errorf("Raw profile type exif is not hex")
		}
		out, i = append(out, b[0]), i+2
	}
	return out, nil
}

// xmpOrientation is the digit Pillow takes from an XMP packet: the first
// tiff:Orientation=" or tiff:Orientation> that a digit follows.
func xmpOrientation(x []byte) (int, bool) {
	for p := []byte("tiff:Orientation"); ; {
		i := bytes.Index(x, p)
		if i < 0 {
			return 0, false
		}
		x = x[i+len(p):]
		r := bytes.TrimPrefix(x, []byte(`="`))
		if len(r) == len(x) {
			r = bytes.TrimPrefix(x, []byte(">"))
		}
		if len(r) < len(x) && len(r) > 0 && r[0] >= '0' && r[0] <= '9' {
			return int(r[0] - '0'), true
		}
	}
}

// loadFails reports whether Pillow raises when it loads t as an Exif block: it
// is not empty and has no TIFF header (or BigTIFF's) with room for its offset.
func loadFails(t []byte) bool {
	if len(t) == 0 {
		return false
	}
	if len(t) < 8 || t[0] != t[1] || (t[0] != 'I' && t[0] != 'M') || t[2]&t[3] != 0 || t[2]|t[3] != 0x2a && t[2]|t[3] != 0x2b {
		return true
	}
	return t[2]|t[3] == 0x2b && len(t) < 16
}

// exifTag finds tag in IFD0 of data's Exif block (see tiffField); exif reports
// whether there is a block at all.
func exifTag(data []byte, tag uint16) (f *exifField, exif bool) {
	d, _ := exifBlock(data, Limits{})
	return tiffField(d.tiff, tag), d.exif
}

// tiffField finds tag in IFD0 of the Exif block t, loaded as Pillow loads it
// (img2pdf and the JPEG resolution both go through it): a block without a
// TIFF header or with IFD0 out of range has no tags; entries are read in order
// and the last of a repeated tag wins; an entry of a type Pillow does not know
// or with no values is skipped; an entry whose values lie outside the block
// ends the read, keeping the tags before it, as does an IFD0 cut short. f is
// nil when the tag is absent.
func tiffField(t []byte, tag uint16) (f *exifField) {
	// The second pair is 0x002a or 0x2a00 (Pillow accepts either order); BigTIFF is not read.
	if len(t) < 8 || t[0] != t[1] || (t[0] != 'I' && t[0] != 'M') || t[2]|t[3] != 0x2a || t[2]&t[3] != 0 {
		return nil
	}
	var bo binary.ByteOrder = binary.BigEndian
	if t[0] == 'I' {
		bo = binary.LittleEndian
	}
	ifd := uint64(bo.Uint32(t[4:]))
	if ifd+2 > uint64(len(t)) {
		return nil
	}
	n := bo.Uint16(t[ifd:])
	for e := ifd + 2; n > 0 && e+12 <= uint64(len(t)); e, n = e+12, n-1 {
		typ, count := bo.Uint16(t[e+2:]), bo.Uint32(t[e+4:])
		if int(typ) >= len(exifSize) || exifSize[typ] == 0 || count == 0 {
			continue
		}
		val := t[e+8 : e+12] // inline if every value fits, else an offset to them
		if size := uint64(exifSize[typ]) * uint64(count); size > 4 {
			off := uint64(bo.Uint32(val))
			if off+size > uint64(len(t)) {
				break
			}
			val = t[off:]
		}
		if bo.Uint16(t[e:]) == tag {
			f = &exifField{typ, val[:uint64(exifSize[typ])*uint64(count)], bo}
		}
	}
	return f
}

// EmbedJP2 lifts a JP2 file under /JPXDecode, verbatim, after reading its
// boxes as img2pdf does: size and bit depth from ihdr, colour from the first
// colr box, which must be enumerated sRGB (DeviceRGB) or greyscale
// (DeviceGray); CMYK, sYCC and ICC-profile files are refused, as img2pdf
// refuses them. The resolution is the res box's first capture resolution box,
// rounded per axis as img2pdf rounds it (zero if none); a resc box that is not
// 10 bytes, has a zero denominator or gives one zero axis is refused, as img2pdf
// refuses it. The pixel cap in lim is checked against the ihdr before the
// image is built, so a forged header is refused as a *LimitError. byblos does
// not decode JPX; the returned image is for BuildPDF only. It reads the boxes
// only and takes no ctx.
func EmbedJP2(data []byte, lim Limits) (EncodedImage, Resolution, error) {
	var w, h uint32
	var depth, enumCS int
	var dpi Resolution
	haveIHDR, haveColr, haveCodestream, haveRes := false, false, false, false
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
			case "resc": // VRcN VRcD HRcN HRcD VRcE HRcE; img2pdf reads the first, and refuses any but 10 bytes or a zero denominator
				if haveRes {
					break
				}
				haveRes = true
				u := binary.BigEndian.Uint16
				if len(body) != 10 || u(body[2:]) == 0 || u(body[6:]) == 0 {
					return fmt.Errorf("resc box of %d bytes, or with a zero denominator", len(body))
				}
				dpi = Resolution{jp2PerInch(u(body[4:]), u(body[6:]), body[9]), jp2PerInch(u(body[0:]), u(body[2:]), body[8])}
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
	if err := dpi.check(); err != nil {
		return EncodedImage{}, Resolution{}, fmt.Errorf("byblos: EmbedJP2: %w", err)
	}
	return EncodedImage{Width: int(w), Height: int(h), BPC: depth, ColorSpace: cs, Filter: "JPXDecode", Data: data}, dpi, nil
}

// jp2PerInch is a resc capture resolution, num/den x 10^exp per metre, in
// pixels per inch: Pillow's exact (254 x num x 10^exp) / (10000 x den), rounded
// to even as img2pdf rounds it. Floating-point arithmetic would round 3.5 to 3.
func jp2PerInch(num, den uint16, exp byte) float64 {
	r := new(big.Rat).SetFrac64(254*int64(num), 10000*int64(den))
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)))
	f, _ := r.Float64()
	return math.RoundToEven(f)
}

// jfifDensity is the last JFIF segment whose units are inches or centimetres,
// the one Pillow takes a density from; nil if none.
func jfifDensity(data []byte) (jfif []byte) {
	jpegSegment(data, func(m byte, p []byte) bool {
		if m == 0xE0 && bytes.HasPrefix(p, []byte("JFIF")) && len(p) >= 12 && (p[7] == 1 || p[7] == 2) {
			jfif = p
		}
		return false
	})
	return jfif
}

// jpegDPI is the resolution Pillow reads from a JPEG, rounded as img2pdf
// rounds it: the density of the last JFIF segment in inches or centimetres,
// else (with an Exif block present) the Exif XResolution in ResolutionUnit, 72
// when the block lacks them or they do not parse, else zero. Pillow takes the
// vertical resolution from XResolution too.
func jpegDPI(data []byte) Resolution {
	jfif := jfifDensity(data)
	if jfif != nil {
		x, y := float64(binary.BigEndian.Uint16(jfif[8:])), float64(binary.BigEndian.Uint16(jfif[10:]))
		if jfif[7] == 2 {
			x, y = math.RoundToEven(x*2.54), math.RoundToEven(y*2.54)
		}
		return Resolution{x, y}
	}
	res, exif := exifTag(data, 0x011A)
	if !exif {
		return Resolution{}
	}
	unit, _ := exifTag(data, 0x0128)
	if res == nil || unit == nil {
		return Resolution{72, 72}
	}
	dpi, ok := res.num()
	if res.typ == 1 || res.typ == 7 { // Pillow hands over bytes, and divides the first by the second
		ok = len(res.val) > 1 && res.val[1] != 0
		if ok {
			dpi = float64(res.val[0]) / float64(res.val[1])
		}
	}
	if !ok || math.IsNaN(dpi) {
		return Resolution{72, 72}
	}
	if u, _ := unit.num(); u == 3 {
		dpi *= 2.54
	}
	dpi = math.RoundToEven(dpi)
	return Resolution{dpi, dpi}
}
