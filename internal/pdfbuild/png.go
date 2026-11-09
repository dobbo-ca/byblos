package pdfbuild

// The PNG transcode (kleio in-process pipeline spec, row 9): a PNG that
// EmbedPNG cannot carry verbatim (alpha, tRNS, Adam7, 16-bit) becomes Flate
// colour plus a Flate /SMask, a row at a time. The file is read once, front to
// back; only a few rows are held, plus (for Adam7) the even rows the first six
// passes fill, and the two compressed outputs.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// zlibWriterBytes is the reservation for one level-1 zlib writer: its hash
// table and window are about 200 KB; the rest is margin.
const zlibWriterBytes = 1 << 20

// chunkReader reads PNG chunks and checks each one's CRC as its last byte is
// read.
type chunkReader struct {
	br  *bufio.Reader
	crc uint32
	n   int64 // body bytes left in the current chunk
	typ string
	bad error // a CRC mismatch seen while reading a body: the reader above may never read on to see it; only a chunk before the first IDAT is held to it
}

func (c *chunkReader) next() error {
	var h [8]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return fmt.Errorf("chunk header: %w", noEOF(err))
	}
	n := binary.BigEndian.Uint32(h[:4])
	if n > 1<<31-1 {
		return fmt.Errorf("chunk length %d", n)
	}
	c.n, c.typ, c.crc = int64(n), string(h[4:]), crc32.ChecksumIEEE(h[4:])
	if c.n == 0 {
		return c.checkCRC()
	}
	return nil
}

func (c *chunkReader) checkCRC() error {
	var b [4]byte
	if _, err := io.ReadFull(c.br, b[:]); err != nil {
		return fmt.Errorf("%s chunk: %w", c.typ, noEOF(err))
	}
	if binary.BigEndian.Uint32(b[:]) != c.crc {
		return fmt.Errorf("%s chunk: bad CRC", c.typ)
	}
	return nil
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.n == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	m, err := c.br.Read(p)
	c.crc = crc32.Update(c.crc, crc32.IEEETable, p[:m])
	c.n -= int64(m)
	if err != nil {
		return m, noEOF(err)
	}
	if c.n == 0 && c.bad == nil {
		c.bad = c.checkCRC()
	}
	return m, err
}

// body reads the rest of a chunk that must be at most max bytes.
func (c *chunkReader) body(max int64) ([]byte, error) {
	if c.n > max {
		return nil, fmt.Errorf("%s chunk of %d bytes", c.typ, c.n)
	}
	b := make([]byte, c.n)
	_, err := io.ReadFull(c, b)
	return b, err
}

func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// idat is the concatenated body of the consecutive IDAT chunks.
type idat struct{ c *chunkReader }

func (s idat) Read(p []byte) (int, error) {
	for s.c.n == 0 {
		if err := s.c.next(); err != nil {
			return 0, err
		}
		if s.c.typ != "IDAT" {
			return 0, io.ErrUnexpectedEOF
		}
	}
	return s.c.Read(p)
}

// sink collects a compressed stream, reserving its memory from the budget as it
// grows (twice what is held, since append doubles).
type sink struct {
	ctx  context.Context
	buf  []byte
	held int64
	rels []func()
}

func (s *sink) Write(p []byte) (int, error) {
	if need := 2 * int64(len(s.buf)+len(p)); need > s.held {
		extra := max(need-s.held, 1<<20)
		rel, err := limits.Reserve(s.ctx, extra)
		if err != nil {
			return 0, err
		}
		s.rels, s.held = append(s.rels, rel), s.held+extra
	}
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *sink) release() {
	for _, r := range s.rels {
		r()
	}
}

// sampleAt is the i-th sample of depth 1, 2, 4, 8 or 16 bits in a packed row.
func sampleAt(row []byte, i, depth int) uint16 {
	switch depth {
	case 16:
		return binary.BigEndian.Uint16(row[2*i:])
	case 8:
		return uint16(row[i])
	}
	bit := i * depth
	return uint16(row[bit/8]>>(8-depth-bit%8)) & (1<<depth - 1)
}

// putSample stores v as the i-th sample of depth 1, 2 or 4 bits.
func putSample(row []byte, i, depth int, v uint16) {
	bit := i * depth
	shift := 8 - depth - bit%8
	row[bit/8] = row[bit/8]&^(byte(1<<depth-1)<<shift) | byte(v)<<shift
}

// adam7 is each pass's x0, y0, dx, dy. Passes 1 to 6 hold exactly the even rows
// of the image and pass 7 the odd ones, whole rows.
var adam7 = [7][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}

// TranscodePNG reads the PNG on r once and returns Flate colour with a Flate
// /SMask for alpha or tRNS (an 8-bit mask, or the PNG's 16 bits for a 16-bit
// alpha channel), at the PNG's own depth; palette files stay /Indexed. The
// resolution is the pHYs chunks', per axis (x then y), each rounded to a whole
// number as img2pdf rounds it (0 if none; see PNGPhys).
//
// Limits are applied from the header before anything is allocated: the pixel
// cap, then the budget for the row buffers and compressors. An Adam7 file needs
// half the image at native sample size for the even rows; scratch, when not nil,
// is called once with that size after the header checks and supplies the bytes
// (the caller reserves them from its own budget), otherwise they are reserved
// from the byblos budget and allocated here. The compressed output grows under
// the budget while it is built, and is released when the call returns: the
// returned bytes are the caller's to account for.
//
// The file is read as far as Pillow reads it for img2pdf, and no further: the
// CRC of every chunk before the first IDAT is checked, but not an IDAT's, and
// once the last row is out nothing after it is looked at. Inflated data past the
// last row is ignored (as is a zlib trailer cut short, so a missing IEND, a bad
// CRC after the image and extra pixel data are all accepted, as img2pdf accepts
// them); a bad zlib checksum on a stream that ends with the last row is an
// error. Cancellation is checked between rows.
func TranscodePNG(ctx context.Context, r io.Reader, lim limits.Limits, scratch func(int64) ([]byte, error)) (pdfdoc.EncodedImage, float64, float64, error) {
	var zero pdfdoc.EncodedImage
	c := &chunkReader{br: bufio.NewReaderSize(r, 32<<10)}
	var sig [8]byte
	if _, err := io.ReadFull(c.br, sig[:]); err != nil || !bytes.Equal(sig[:], pngSignature) {
		return zero, 0, 0, fmt.Errorf("not a PNG file")
	}
	var w, h, depth, ctype, interlace int
	var plte, trns []byte
	var phys PNGPhys
	haveIHDR := false
	for c.typ != "IDAT" || c.n == 0 {
		if err := c.next(); err != nil {
			return zero, 0, 0, err
		}
		if !haveIHDR && c.typ != "IHDR" {
			return zero, 0, 0, fmt.Errorf("first chunk is %s, not IHDR", c.typ)
		}
		var err error
		switch c.typ {
		case "IHDR":
			var b []byte
			if haveIHDR || c.n != 13 {
				return zero, 0, 0, fmt.Errorf("bad IHDR chunk")
			}
			if b, err = c.body(13); err == nil {
				uw, uh := binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])
				if uw == 0 || uh == 0 || uw > 1<<31-1 || uh > 1<<31-1 {
					return zero, 0, 0, fmt.Errorf("IHDR declares %dx%d", uw, uh)
				}
				w, h, depth, ctype, interlace, haveIHDR = int(uw), int(uh), int(b[8]), int(b[9]), int(b[12]), true
			}
		case "PLTE":
			if plte, err = c.body(768); err == nil && (len(plte) == 0 || len(plte)%3 != 0) {
				err = fmt.Errorf("PLTE of %d bytes", len(plte))
			}
		case "tRNS":
			trns, err = c.body(256)
		case "pHYs":
			b := make([]byte, min(c.n, 9))
			if _, err = io.ReadFull(c, b); err == nil {
				if err = phys.Add(b); err == nil {
					_, err = io.Copy(io.Discard, c)
				}
			}
		case "IEND":
			return zero, 0, 0, fmt.Errorf("no IDAT chunk")
		case "IDAT":
		default:
			_, err = io.Copy(io.Discard, c)
		}
		if err == nil {
			err = c.bad
		}
		if err != nil {
			return zero, 0, 0, err
		}
	}

	dpiX, dpiY := phys.Resolution()

	// Header checks, before any allocation.
	channels, okDepth := map[int]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[ctype], false
	switch ctype {
	case 0, 3:
		okDepth = depth == 1 || depth == 2 || depth == 4 || depth == 8 || (ctype == 0 && depth == 16)
	default:
		okDepth = depth == 8 || depth == 16
	}
	if channels == 0 || !okDepth || interlace > 1 {
		return zero, 0, 0, fmt.Errorf("colour type %d at %d bits, interlace %d, is not a PNG", ctype, depth, interlace)
	}
	if err := limits.Check("encoded-image-pixels", int64(max(w, h)), lim.EncodedImagePixels()); err != nil {
		return zero, 0, 0, err
	}
	if err := limits.Check("encoded-image-pixels", limits.MulSat(int64(w), int64(h)), lim.EncodedImagePixels()); err != nil {
		return zero, 0, 0, err
	}
	hasAlphaChannel := ctype == 4 || ctype == 6
	switch {
	case hasAlphaChannel && trns != nil:
		return zero, 0, 0, fmt.Errorf("tRNS on a colour type with alpha")
	case ctype == 3 && (plte == nil || len(plte)/3 > 1<<depth || len(trns) > len(plte)/3):
		return zero, 0, 0, fmt.Errorf("palette missing or too long for %d bits", depth)
	case ctype == 0 && trns != nil && len(trns) != 2, ctype == 2 && trns != nil && len(trns) != 6:
		return zero, 0, 0, fmt.Errorf("tRNS of %d bytes for colour type %d", len(trns), ctype)
	}

	bitsPP := channels * depth
	rowBytes := (w*bitsPP + 7) / 8 // an unpacked row of the image
	bpp := max(1, bitsPP/8)
	hasMask := hasAlphaChannel || trns != nil
	colorBits, maskBits := bitsPP, 0 // per pixel
	if hasAlphaChannel {
		colorBits, maskBits = bitsPP-depth, depth
	} else if hasMask {
		maskBits = 8
	}
	colorRow := (w*colorBits + 7) / 8
	maskRow := (w*maskBits + 7) / 8
	var scratchBytes int64
	if interlace == 1 {
		scratchBytes = limits.MulSat(int64((h+1)/2), int64(rowBytes))
	}
	held := 2*int64(rowBytes+1) + int64(colorRow+maskRow) + zlibWriterBytes
	if hasMask {
		held += zlibWriterBytes
	}
	if scratch == nil {
		held += scratchBytes
	}
	release, err := limits.Reserve(ctx, held)
	if err != nil {
		return zero, 0, 0, err
	}
	defer release()
	var even []byte
	if scratchBytes > 0 {
		if scratch == nil {
			even = make([]byte, scratchBytes)
		} else if even, err = scratch(scratchBytes); err != nil {
			return zero, 0, 0, err
		} else if int64(len(even)) < scratchBytes {
			return zero, 0, 0, fmt.Errorf("scratch is %d bytes, %d needed", len(even), scratchBytes)
		}
		clear(even[:scratchBytes])
	}

	zr, err := zlib.NewReader(idat{c})
	if err != nil {
		return zero, 0, 0, fmt.Errorf("IDAT: %w", noEOF(err))
	}
	colorOut, maskOut := &sink{ctx: ctx}, &sink{ctx: ctx}
	defer colorOut.release()
	defer maskOut.release()
	cz, _ := zlib.NewWriterLevel(colorOut, zlib.BestSpeed)
	mz, _ := zlib.NewWriterLevel(maskOut, zlib.BestSpeed)

	cur, prev := make([]byte, rowBytes+1), make([]byte, rowBytes+1)
	crow, mrow := make([]byte, colorRow), make([]byte, maskRow)
	// readRow reads and unfilters one row of a pass whose rows are n bytes into
	// cur[1:n+1] and returns them; prev is the row above in the same pass.
	readRow := func(n int) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(zr, cur[:n+1]); err != nil {
			return nil, fmt.Errorf("IDAT: %w", noEOF(err))
		}
		if err := unfilterPNGRow(cur[0], cur[1:n+1], prev[1:n+1], bpp); err != nil {
			return nil, err
		}
		cur, prev = prev, cur
		return prev[1 : n+1], nil // the row just reconstructed
	}
	var key [3]uint16
	if trns != nil && ctype != 3 {
		for i := 0; i < len(trns)/2; i++ {
			key[i] = binary.BigEndian.Uint16(trns[2*i:])
			if depth < 16 {
				key[i] &= 0xFF
			}
		}
	}
	// emit splits one finished row into colour and mask and compresses both.
	emit := func(row []byte) error {
		out := row
		switch ctype {
		case 4, 6:
			sz, cs := depth/8, channels-1
			for x := 0; x < w; x++ {
				copy(crow[x*cs*sz:], row[x*channels*sz:x*channels*sz+cs*sz])
				copy(mrow[x*sz:], row[(x*channels+cs)*sz:(x*channels+channels)*sz])
			}
			out = crow
		case 3:
			if trns != nil {
				for x := 0; x < w; x++ {
					a := byte(255)
					if i := int(sampleAt(row, x, depth)); i < len(trns) {
						a = trns[i]
					}
					mrow[x] = a
				}
			}
		default:
			if trns != nil {
				for x := 0; x < w; x++ {
					same := true
					for k := 0; k < channels; k++ {
						same = same && sampleAt(row, x*channels+k, depth) == key[k]
					}
					mrow[x] = 255
					if same {
						mrow[x] = 0
					}
				}
			}
		}
		if _, err := cz.Write(out); err != nil {
			return err
		}
		if hasMask {
			_, err := mz.Write(mrow)
			return err
		}
		return nil
	}

	if interlace == 0 {
		for y := 0; y < h; y++ {
			row, err := readRow(rowBytes)
			if err == nil {
				err = emit(row)
			}
			if err != nil {
				return zero, 0, 0, err
			}
		}
	} else {
		for _, p := range adam7[:6] {
			pw, ph := (w-p[0]+p[2]-1)/p[2], (h-p[1]+p[3]-1)/p[3]
			if pw <= 0 || ph <= 0 {
				continue
			}
			n := (pw*bitsPP + 7) / 8
			clear(prev[:n+1])
			for j := 0; j < ph; j++ {
				row, err := readRow(n)
				if err != nil {
					return zero, 0, 0, err
				}
				dst := even[(p[1]+j*p[3])/2*rowBytes:][:rowBytes]
				for i := 0; i < pw; i++ {
					x := p[0] + i*p[2]
					if bitsPP >= 8 {
						copy(dst[x*bpp:], row[i*bpp:(i+1)*bpp])
					} else {
						putSample(dst, x, depth, sampleAt(row, i, depth))
					}
				}
			}
		}
		clear(prev[:rowBytes+1])
		for y := 0; y < h; y++ {
			var row []byte
			if y%2 == 0 {
				row = even[y/2*rowBytes:][:rowBytes]
			} else if row, err = readRow(rowBytes); err != nil {
				return zero, 0, 0, err
			}
			if err := emit(row); err != nil {
				return zero, 0, 0, err
			}
		}
	}
	// Past the last row: more inflated data, or a zlib trailer cut short, is
	// ignored, as Pillow ignores it; a stream that ends here must check out.
	var one [1]byte
	if n, err := zr.Read(one[:]); n == 0 && err != nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
		return zero, 0, 0, fmt.Errorf("IDAT: %w", err)
	}
	if err := cz.Close(); err != nil {
		return zero, 0, 0, err
	}
	img := pdfdoc.EncodedImage{Width: w, Height: h, BPC: depth, Filter: "FlateDecode", Data: colorOut.buf}
	switch ctype {
	case 0, 4:
		img.ColorSpace = pdfdoc.ColorSpace{Name: "DeviceGray"}
	case 2, 6:
		img.ColorSpace = pdfdoc.ColorSpace{Name: "DeviceRGB"}
	case 3:
		img.ColorSpace = pdfdoc.ColorSpace{Name: "Indexed", Base: "DeviceRGB", HiVal: len(plte)/3 - 1, Lookup: plte}
	}
	if hasMask {
		if err := mz.Close(); err != nil {
			return zero, 0, 0, err
		}
		bpc := 8
		if hasAlphaChannel {
			bpc = depth
		}
		img.SMask = &pdfdoc.EncodedImage{Width: w, Height: h, BPC: bpc, ColorSpace: pdfdoc.ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: maskOut.buf}
	}
	return img, dpiX, dpiY, nil
}
