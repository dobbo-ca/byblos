package byblos

// Stage 2.2: the PNG transcode (alpha and tRNS to /SMask, Adam7, 16-bit) and
// the streaming page writer. The oracle for pixels is the standard library's
// image/png decode of the same bytes, as the spec asks (row 9).

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image/color"
	"image/png"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// pngSpec describes a PNG to generate. sample(x, y, c) is the c-th channel of
// pixel (x, y), already within the depth.
type pngSpec struct {
	w, h          int
	ctype, depth  byte
	interlaced    bool
	plte, trns    []byte
	sample        func(x, y, c int) uint16
	splitIDATAt   int // bytes of the zlib stream in the first IDAT chunk; 0 = one chunk
	corruptCRC    bool
	truncateAfter int    // keep this many bytes of the zlib stream; 0 = all
	extraRaw      int    // bytes of pixel data past the last row
	pre           []byte // chunks between pHYs and the first IDAT
	trailer       []byte // chunks between the last IDAT and IEND
	noIEND        bool
	dropTail      int // bytes cut from the end of the zlib stream
	badAdler      bool
}

func (s pngSpec) channels() int {
	return map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[s.ctype]
}

func (s pngSpec) bitsPP() int { return s.channels() * int(s.depth) }

// packRow packs the pixels at xs of row y.
func (s pngSpec) packRow(y int, xs []int) []byte {
	var out []byte
	var acc, nacc int
	for _, x := range xs {
		for c := 0; c < s.channels(); c++ {
			v := s.sample(x, y, c)
			switch {
			case s.depth == 16:
				out = binary.BigEndian.AppendUint16(out, v)
			case s.depth == 8:
				out = append(out, byte(v))
			default:
				acc = acc<<s.depth | int(v)
				if nacc += int(s.depth); nacc == 8 {
					out, acc, nacc = append(out, byte(acc)), 0, 0
				}
			}
		}
	}
	if nacc > 0 {
		out = append(out, byte(acc<<(8-nacc)))
	}
	return out
}

func paeth(a, b, c int) int {
	p := a + b - c
	pa, pb, pc := abs(p-a), abs(p-b), abs(p-c)
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

// filterRow applies PNG filter ft to raw given the previous raw row.
func filterRow(ft int, raw, prev []byte, bpp int) []byte {
	out := []byte{byte(ft)}
	for i, v := range raw {
		var a, b, c int
		if i >= bpp {
			a = int(raw[i-bpp])
			c = int(prev[i-bpp])
		}
		b = int(prev[i])
		var p int
		switch ft {
		case 1:
			p = a
		case 2:
			p = b
		case 3:
			p = (a + b) / 2
		case 4:
			p = paeth(a, b, c)
		}
		out = append(out, byte(int(v)-p))
	}
	return out
}

var adam7 = [7][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} // x0, y0, dx, dy

func chunk(typ string, body []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	out = append(out, typ...)
	out = append(out, body...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), body...)))
}

func (s pngSpec) file(t testing.TB) []byte {
	t.Helper()
	bpp := max(1, s.bitsPP()/8)
	var raw []byte
	pass := func(xs []int, ys []int) {
		var prev []byte
		for i, y := range ys {
			r := s.packRow(y, xs)
			if prev == nil {
				prev = make([]byte, len(r))
			}
			raw = append(raw, filterRow(i%5, r, prev, bpp)...)
			prev = r
		}
	}
	span := func(start, step, n int) []int {
		var v []int
		for i := start; i < n; i += step {
			v = append(v, i)
		}
		return v
	}
	if s.interlaced {
		for _, p := range adam7 {
			if xs, ys := span(p[0], p[2], s.w), span(p[1], p[3], s.h); len(xs) > 0 && len(ys) > 0 {
				pass(xs, ys)
			}
		}
	} else {
		pass(span(0, 1, s.w), span(0, 1, s.h))
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(append(raw, make([]byte, s.extraRaw)...))
	_ = zw.Close()
	zs := z.Bytes()
	if s.truncateAfter > 0 {
		zs = zs[:s.truncateAfter]
	}
	zs = zs[:len(zs)-s.dropTail]
	if s.badAdler {
		zs[len(zs)-1] ^= 1
	}
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, uint32(s.w)), uint32(s.h))
	il := byte(0)
	if s.interlaced {
		il = 1
	}
	ihdr = append(ihdr, s.depth, s.ctype, 0, 0, il)
	out := append([]byte(nil), pngSignature[:]...)
	out = append(out, chunk("IHDR", ihdr)...)
	if s.plte != nil {
		out = append(out, chunk("PLTE", s.plte)...)
	}
	if s.trns != nil {
		out = append(out, chunk("tRNS", s.trns)...)
	}
	out = append(out, chunk("pHYs", []byte{0, 0, 0x0B, 0x13, 0, 0, 0x0B, 0x13, 1})...)
	out = append(out, s.pre...)
	if n := s.splitIDATAt; n > 0 && n < len(zs) {
		out = append(out, chunk("IDAT", zs[:n])...)
		zs = zs[n:]
	}
	idat := chunk("IDAT", zs)
	if s.corruptCRC {
		idat[len(idat)-1] ^= 0xFF
	}
	out = append(out, idat...)
	out = append(out, s.trailer...)
	if s.noIEND {
		return out
	}
	return append(out, chunk("IEND", nil)...)
}

func basicSample(depth byte, salt int) func(x, y, c int) uint16 {
	return func(x, y, c int) uint16 { return uint16((x*31 + y*17 + c*7 + salt) % (1 << depth)) }
}

func palette(n int) []byte {
	p := make([]byte, 3*n)
	for i := range p {
		p[i] = byte(i*37 + 11)
	}
	return p
}

// reconstruct reads an EncodedImage (and its SMask) back into NRGBA64 pixels.
func reconstruct(t *testing.T, img EncodedImage) func(x, y int) color.NRGBA64 {
	t.Helper()
	rows := func(e EncodedImage, comps int) ([]byte, int) {
		zr, err := zlib.NewReader(bytes.NewReader(e.Data))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		rl := (e.Width*comps*e.BPC + 7) / 8
		if len(raw) != rl*e.Height {
			t.Fatalf("stream is %d bytes; %dx%d at %d bpc x %d is %d", len(raw), e.Width, e.Height, e.BPC, comps, rl*e.Height)
		}
		return raw, rl
	}
	sample := func(raw []byte, rl, bpc, comps, x, y, c int) (v, max uint32) {
		i := x*comps + c
		switch bpc {
		case 16:
			return uint32(binary.BigEndian.Uint16(raw[y*rl+2*i:])), 65535
		case 8:
			return uint32(raw[y*rl+i]), 255
		}
		bit := i * bpc
		return uint32(raw[y*rl+bit/8]>>(8-bpc-bit%8)) & (1<<bpc - 1), 1<<bpc - 1
	}
	scale := func(v, max uint32) uint16 { return uint16(v * 65535 / max) }
	comps := map[string]int{"DeviceGray": 1, "DeviceRGB": 3, "Indexed": 1}[img.ColorSpace.Name]
	raw, rl := rows(img, comps)
	var araw []byte
	var arl int
	if img.SMask != nil {
		if img.SMask.ColorSpace.Name != "DeviceGray" || img.SMask.Width != img.Width || img.SMask.Height != img.Height {
			t.Fatalf("bad SMask %+v", *img.SMask)
		}
		araw, arl = rows(*img.SMask, 1)
	}
	return func(x, y int) color.NRGBA64 {
		var c [3]uint16
		for k := 0; k < comps; k++ {
			v, m := sample(raw, rl, img.BPC, comps, x, y, k)
			c[k] = scale(v, m)
			if img.ColorSpace.Name == "DeviceGray" {
				c = [3]uint16{c[0], c[0], c[0]}
			}
		}
		if img.ColorSpace.Name == "Indexed" {
			v, _ := sample(raw, rl, img.BPC, 1, x, y, 0)
			for k := 0; k < 3; k++ {
				c[k] = uint16(img.ColorSpace.Lookup[3*int(v)+k]) * 257
			}
		}
		a := uint16(65535)
		if araw != nil {
			v, m := sample(araw, arl, img.SMask.BPC, 1, x, y, 0)
			a = scale(v, m)
		}
		return color.NRGBA64{c[0], c[1], c[2], a}
	}
}

// nrgba64 is c without the premultiplied round trip, which loses low bits.
func nrgba64(c color.Color) color.NRGBA64 {
	if n, ok := c.(color.NRGBA); ok {
		return color.NRGBA64{uint16(n.R) * 257, uint16(n.G) * 257, uint16(n.B) * 257, uint16(n.A) * 257}
	}
	return color.NRGBA64Model.Convert(c).(color.NRGBA64)
}

func transcode(t testing.TB, file []byte, lim Limits, scratch func(int64) ([]byte, error)) (EncodedImage, Resolution, error) {
	t.Helper()
	return TranscodePNG(context.Background(), bytes.NewReader(file), lim, scratch)
}

func TestTranscodePNGMatchesStdlib(t *testing.T) {
	type tc struct {
		name         string
		ctype, depth byte
		trns         func(s pngSpec) []byte
		plte         int
	}
	keyGray := func(s pngSpec) []byte { return binary.BigEndian.AppendUint16(nil, s.sample(0, 0, 0)) }
	keyRGB := func(s pngSpec) []byte {
		var k []byte
		for c := 0; c < 3; c++ {
			k = binary.BigEndian.AppendUint16(k, s.sample(0, 0, c))
		}
		return k
	}
	cases := []tc{
		{"gray1", 0, 1, nil, 0}, {"gray2", 0, 2, nil, 0}, {"gray4", 0, 4, nil, 0}, {"gray8", 0, 8, nil, 0}, {"gray16", 0, 16, nil, 0},
		{"rgb8", 2, 8, nil, 0}, {"rgb16", 2, 16, nil, 0},
		{"pal1", 3, 1, nil, 2}, {"pal2", 3, 2, nil, 4}, {"pal4", 3, 4, nil, 16}, {"pal8", 3, 8, nil, 256},
		{"gray-alpha8", 4, 8, nil, 0}, {"gray-alpha16", 4, 16, nil, 0},
		{"rgba8", 6, 8, nil, 0}, {"rgba16", 6, 16, nil, 0},
		{"gray1-trns", 0, 1, keyGray, 0}, {"gray4-trns", 0, 4, keyGray, 0}, {"gray8-trns", 0, 8, keyGray, 0}, {"gray16-trns", 0, 16, keyGray, 0},
		{"rgb8-trns", 2, 8, keyRGB, 0}, {"rgb16-trns", 2, 16, keyRGB, 0},
		{"pal4-trns", 3, 4, func(pngSpec) []byte { return []byte{0, 128, 255} }, 16},
		{"pal8-trns", 3, 8, func(pngSpec) []byte { return []byte{10, 0, 200, 255, 7} }, 200},
	}
	sizes := [][2]int{{1, 1}, {2, 1}, {1, 3}, {5, 4}, {13, 11}, {16, 16}, {17, 9}}
	for _, c := range cases {
		for _, il := range []bool{false, true} {
			for _, sz := range sizes {
				name := fmt.Sprintf("%s/il=%v/%dx%d", c.name, il, sz[0], sz[1])
				t.Run(name, func(t *testing.T) {
					s := pngSpec{w: sz[0], h: sz[1], ctype: c.ctype, depth: c.depth, interlaced: il, sample: basicSample(c.depth, len(name)), splitIDATAt: 9}
					if c.plte > 0 {
						s.plte = palette(c.plte)
						s.sample = func(x, y, _ int) uint16 { return uint16((x*5 + y*3) % c.plte) }
					}
					if c.trns != nil {
						s.trns = c.trns(s)
					}
					file := s.file(t)
					want, err := png.Decode(bytes.NewReader(file))
					if err != nil {
						t.Fatalf("stdlib: %v", err)
					}
					img, dpi, err := transcode(t, file, Limits{}, nil)
					if err != nil {
						t.Fatalf("TranscodePNG: %v", err)
					}
					if dpi != (Resolution{72, 72}) { // pHYs 2835 px/m is 72.009, which img2pdf rounds
						t.Errorf("dpi = %v", dpi)
					}
					if img.Width != s.w || img.Height != s.h || img.Filter != "FlateDecode" || img.DecodeParms != nil {
						t.Fatalf("image = %+v", img)
					}
					if wantBPC := int(c.depth); img.BPC != wantBPC {
						t.Errorf("BPC = %d; want %d", img.BPC, wantBPC)
					}
					hasAlpha := c.ctype == 4 || c.ctype == 6 || c.trns != nil
					if (img.SMask != nil) != hasAlpha {
						t.Errorf("SMask present = %v; want %v", img.SMask != nil, hasAlpha)
					}
					px := reconstruct(t, img)
					for y := 0; y < s.h; y++ {
						for x := 0; x < s.w; x++ {
							w := nrgba64(want.At(x, y))
							if got := px(x, y); got != w {
								t.Fatalf("pixel (%d,%d) = %v; stdlib %v", x, y, got, w)
							}
						}
					}
					// And it is a PDF the package reads.
					out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 72}})
					if err := pdfdoc.Validate(bytes.NewReader(out)); err != nil {
						t.Fatalf("Validate: %v", err)
					}
					if _, err := Inspect(bytes.NewReader(out)); err != nil {
						t.Fatalf("Inspect: %v", err)
					}
				})
			}
		}
	}
}

func TestTranscodePNGInterlacedScratchIsHalfTheImage(t *testing.T) {
	s := pngSpec{w: 9, h: 7, ctype: 6, depth: 16, interlaced: true, sample: basicSample(16, 3)}
	var asked []int64
	_, _, err := transcode(t, s.file(t), Limits{}, func(n int64) ([]byte, error) {
		asked = append(asked, n)
		return make([]byte, n), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(4 * 9 * 8); len(asked) != 1 || asked[0] != want { // ceil(7/2) rows of 9 px x 8 bytes
		t.Errorf("scratch asked %v; want [%d]", asked, want)
	}
	// Non-interlaced files ask for none.
	s.interlaced = false
	asked = nil
	if _, _, err := transcode(t, s.file(t), Limits{}, func(n int64) ([]byte, error) { asked = append(asked, n); return nil, nil }); err != nil || len(asked) != 0 {
		t.Errorf("non-interlaced: asked %v, err %v", asked, err)
	}
	// A scratch buffer that is too small is an error, not a panic; dirty bytes are fine.
	s.interlaced = true
	if _, _, err := transcode(t, s.file(t), Limits{}, func(n int64) ([]byte, error) { return make([]byte, n-1), nil }); err == nil {
		t.Error("short scratch accepted")
	}
	img, _, err := transcode(t, s.file(t), Limits{}, func(n int64) ([]byte, error) { return bytes.Repeat([]byte{0xA5}, int(n)), nil })
	if err != nil {
		t.Fatal(err)
	}
	want, _ := png.Decode(bytes.NewReader(s.file(t)))
	px := reconstruct(t, img)
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			if got, w := px(x, y), nrgba64(want.At(x, y)); got != w {
				t.Fatalf("dirty scratch: pixel (%d,%d) = %v; want %v", x, y, got, w)
			}
		}
	}
}

func TestTranscodePNGLimitsBeforeAllocation(t *testing.T) {
	// 100000 x 100000 RGBA16 declared in a file of a few hundred bytes.
	hdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 100000), 100000)
	forged := append(append([]byte(nil), pngSignature[:]...), chunk("IHDR", append(hdr, 16, 6, 0, 0, 1))...)
	forged = append(forged, chunk("IDAT", []byte{0x78, 0x9c, 0x03, 0, 0, 0, 0, 1})...)
	called := false
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err := transcode(t, forged, Limits{}, func(int64) ([]byte, error) { called = true; return nil, nil })
	runtime.ReadMemStats(&after)
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "encoded-image-pixels" {
		t.Fatalf("err = %v; want encoded-image-pixels LimitError", err)
	}
	if called {
		t.Error("scratch was requested for a refused image")
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Errorf("allocated %d bytes refusing a forged header", d)
	}
	// The configured cap, not a fixed one.
	s := pngSpec{w: 20, h: 20, ctype: 2, depth: 8, sample: basicSample(8, 0)}
	if _, _, err := transcode(t, s.file(t), Limits{MaxEncodedImagePixels: 399}, nil); !errors.As(err, &le) {
		t.Errorf("cap 399 on 400 px: %v", err)
	}
	if _, _, err := transcode(t, s.file(t), Limits{MaxEncodedImagePixels: 400}, nil); err != nil {
		t.Errorf("cap 400 on 400 px: %v", err)
	}
}

func TestTranscodePNGBudget(t *testing.T) {
	prev := SetMemoryLimit(1 << 16)
	defer SetMemoryLimit(prev)
	// 4096 px x 8 bytes a row: its four row buffers do not fit in 64 KiB.
	s := pngSpec{w: 4096, h: 2, ctype: 6, depth: 16, sample: basicSample(16, 0)}
	_, _, err := transcode(t, s.file(t), Limits{}, nil)
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "memory-budget" {
		t.Fatalf("err = %v; want memory-budget LimitError", err)
	}
	SetMemoryLimit(prev)
	if _, _, err := transcode(t, s.file(t), Limits{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTranscodePNGStreamsRows(t *testing.T) {
	// 6000 x 6000 8-bit RGBA, one colour: 144 MB raw, a few hundred KB as PNG.
	s := pngSpec{w: 6000, h: 6000, ctype: 6, depth: 8, sample: func(x, y, c int) uint16 { return uint16(c * 20) }}
	file := s.fileConstant(t)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	img, _, err := transcode(t, file, Limits{MaxEncodedImagePixels: 1 << 26}, nil)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 32<<20 {
		t.Errorf("allocated %d MB transcoding a 144 MB image: it is not streaming", d>>20)
	}
	if img.SMask == nil || img.Width != 6000 {
		t.Errorf("image = %+v", img)
	}
}

// fileConstant is file() for a spec whose rows are all equal, without holding
// the raw image: each row is filtered with type 0 and streamed into the zlib
// writer.
func (s pngSpec) fileConstant(t testing.TB) []byte {
	row := append([]byte{0}, s.packRow(0, func() []int {
		xs := make([]int, s.w)
		for i := range xs {
			xs[i] = i
		}
		return xs
	}())...)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	for y := 0; y < s.h; y++ {
		_, _ = zw.Write(row)
	}
	_ = zw.Close()
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, uint32(s.w)), uint32(s.h))
	out := append([]byte(nil), pngSignature[:]...)
	out = append(out, chunk("IHDR", append(ihdr, s.depth, s.ctype, 0, 0, 0))...)
	out = append(out, chunk("IDAT", z.Bytes())...)
	return append(out, chunk("IEND", nil)...)
}

func TestTranscodePNGRefusesDamage(t *testing.T) {
	good := pngSpec{w: 12, h: 12, ctype: 6, depth: 8, sample: basicSample(8, 1)}
	full := good.file(t)
	cases := map[string][]byte{
		"empty":         nil,
		"not-png":       []byte("GIF89a not a png at all"),
		"truncated-sig": full[:5],
		"truncated-idat": func() []byte {
			s := good
			s.truncateAfter = 20
			return s.file(t)
		}(),
		"bad-crc-before-image":         func() []byte { s := good; s.pre = badCRC("tEXt"); return s.file(t) }(),
		"bad-crc-unknown-before-image": func() []byte { s := good; s.pre = badCRC("zzZz"); return s.file(t) }(),
		"bad-adler":                    func() []byte { s := good; s.badAdler = true; return s.file(t) }(),
		"no-idat":                      append(append([]byte(nil), full[:33]...), chunk("IEND", nil)...),
		"bad-depth": func() []byte {
			s := good
			s.depth = 4 // RGBA does not allow 4 bits
			return s.file(t)
		}(),
		"pal-no-plte": pngSpec{w: 4, h: 4, ctype: 3, depth: 8, sample: basicSample(8, 0)}.file(t),
	}
	for name, data := range cases {
		if _, _, err := transcode(t, data, Limits{}, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := TranscodePNG(ctx, bytes.NewReader(full), Limits{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: %v", err)
	}
}

// TestTranscodePNGAcceptsWhatImg2pdfAccepts holds the contract set by Pillow's
// reading, which img2pdf inherits: once the last row is out nothing after it is
// looked at, and an IDAT's CRC is not checked, so a file img2pdf converts is
// transcoded to the same bytes as its clean twin even though image/png refuses it.
func TestTranscodePNGAcceptsWhatImg2pdfAccepts(t *testing.T) {
	good := pngSpec{w: 2, h: 2, ctype: 6, depth: 8, sample: basicSample(8, 1)}
	want, _, err := transcode(t, good.file(t), Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*pngSpec){
		"no-iend":              func(s *pngSpec) { s.noIEND = true },
		"truncated-iend":       func(s *pngSpec) { s.noIEND, s.trailer = true, chunk("IEND", nil)[:8] },
		"extra-pixel-data":     func(s *pngSpec) { s.extraRaw = 9 },
		"bad-crc-after-image":  func(s *pngSpec) { s.trailer = badCRC("tEXt") },
		"bad-crc-iend":         func(s *pngSpec) { s.noIEND, s.trailer = true, badCRC("IEND") },
		"bad-crc-idat":         func(s *pngSpec) { s.corruptCRC = true },
		"three-bytes-after":    func(s *pngSpec) { s.noIEND, s.trailer = true, []byte{0, 0, 0} },
		"zlib-trailer-cut":     func(s *pngSpec) { s.dropTail = 2 },
		"zlib-trailer-missing": func(s *pngSpec) { s.dropTail = 4 },
	} {
		s := good
		mod(&s)
		file := s.file(t)
		if name != "zlib-trailer-cut" && name != "zlib-trailer-missing" && name != "three-bytes-after" && name != "bad-crc-idat" {
			if _, err := png.Decode(bytes.NewReader(file)); err == nil {
				t.Fatalf("%s: stdlib accepts the file", name)
			}
		}
		got, _, err := transcode(t, file, Limits{}, nil)
		if err != nil {
			t.Errorf("%s: %v; img2pdf converts it", name, err)
		} else if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("%s: transcoded to other bytes than the clean file", name)
		}
	}
}

func badCRC(typ string) []byte {
	c := chunk(typ, []byte("k\x00v"))
	c[len(c)-1] ^= 0xFF
	return c
}

// --- streaming page writer -----------------------------------------------------

func TestBuildWriterMatchesBuildPDF(t *testing.T) {
	jimg, _ := jpegImage(t, 20, 30)
	pages := []BuildPage{
		{Image: flateGrayImage(t, 10, 12, 1), DPI: 72},
		{Image: jimg, DPI: 150, Rotate: 90},
		{Image: flateGrayImage(t, 8, 8, 2), WidthPt: 100, HeightPt: 50},
	}
	want := buildOrFatal(t, pages)
	var got bytes.Buffer
	bw := NewBuildWriter(&got, Limits{})
	for i, p := range pages {
		if err := bw.AddPage(context.Background(), p); err != nil {
			t.Fatalf("AddPage %d: %v", i, err)
		}
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("writer output differs from BuildPDF's (%d vs %d bytes)", got.Len(), len(want))
	}
}

func TestBuildWriterWritesEachPageAsItIsAdded(t *testing.T) {
	var buf bytes.Buffer
	bw := NewBuildWriter(&buf, Limits{})
	if err := bw.AddPage(context.Background(), BuildPage{Image: flateGrayImage(t, 40, 40, 1), DPI: 72}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("/Subtype /Image")) {
		t.Error("the page's image is not in the output before Close")
	}
	if bytes.Contains(buf.Bytes(), []byte("/Type /Pages")) {
		t.Error("the page tree was written before Close")
	}
	n := buf.Len()
	// A page it refuses writes nothing and leaves the writer usable.
	if err := bw.AddPage(context.Background(), BuildPage{Image: flateGrayImage(t, 4, 4, 1)}); err == nil {
		t.Error("page with no box and no DPI accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bw.AddPage(ctx, BuildPage{Image: flateGrayImage(t, 4, 4, 1), DPI: 72}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if buf.Len() != n {
		t.Errorf("refused pages wrote %d bytes", buf.Len()-n)
	}
	if err := bw.AddPage(context.Background(), BuildPage{Image: flateGrayImage(t, 4, 4, 2), DPI: 72}); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	pages, err := Inspect(bytes.NewReader(buf.Bytes()))
	if err != nil || len(pages) != 2 {
		t.Fatalf("Inspect = %d pages, %v", len(pages), err)
	}
	if err := bw.AddPage(context.Background(), BuildPage{Image: flateGrayImage(t, 4, 4, 2), DPI: 72}); err == nil {
		t.Error("AddPage after Close accepted")
	}
	if err := NewBuildWriter(&bytes.Buffer{}, Limits{}).Close(); err == nil {
		t.Error("Close with no pages accepted")
	}
}

func TestBuildWriterPageCap(t *testing.T) {
	bw := NewBuildWriter(io.Discard, Limits{MaxPages: 2})
	img := flateGrayImage(t, 2, 2, 1)
	for i := 0; i < 2; i++ {
		if err := bw.AddPage(context.Background(), BuildPage{Image: img, DPI: 72}); err != nil {
			t.Fatal(err)
		}
	}
	var le *LimitError
	if err := bw.AddPage(context.Background(), BuildPage{Image: img, DPI: 72}); !errors.As(err, &le) || le.Name != "pages" {
		t.Errorf("third page: %v", err)
	}
}

func TestBuildPDFWritesSMask(t *testing.T) {
	s := pngSpec{w: 6, h: 5, ctype: 6, depth: 8, sample: basicSample(8, 5)}
	img, _, err := transcode(t, s.file(t), Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 72}})
	if !strings.Contains(string(out), "/SMask ") {
		t.Fatal("no /SMask in the image dictionary")
	}
	// A mask the image does not fit is refused.
	bad := img
	sm := *img.SMask
	sm.Width++
	bad.SMask = &sm
	if err := BuildPDF(io.Discard, []BuildPage{{Image: bad, DPI: 72}}); err == nil {
		t.Error("SMask of another size accepted")
	}
	sm = *img.SMask
	sm.ColorSpace = ColorSpace{Name: "DeviceRGB"}
	bad.SMask = &sm
	if err := BuildPDF(io.Discard, []BuildPage{{Image: bad, DPI: 72}}); err == nil {
		t.Error("RGB SMask accepted")
	}
}

// --- fuzz -----------------------------------------------------------------------

func FuzzPNGTranscode(f *testing.F) {
	t := &testing.T{}
	for _, s := range []pngSpec{
		{w: 5, h: 4, ctype: 6, depth: 8, sample: basicSample(8, 1)},
		{w: 9, h: 7, ctype: 6, depth: 16, interlaced: true, sample: basicSample(16, 2)},
		{w: 7, h: 3, ctype: 0, depth: 1, interlaced: true, trns: []byte{0, 1}, sample: basicSample(1, 3)},
		{w: 6, h: 6, ctype: 3, depth: 4, plte: palette(16), trns: []byte{3}, sample: func(x, y, _ int) uint16 { return uint16((x + y) % 16) }},
	} {
		f.Add(s.file(t))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxEncodedImagePixels: 1 << 12}
		img, _, err := TranscodePNG(context.Background(), bytes.NewReader(data), lim, func(n int64) ([]byte, error) {
			if n > 1<<16 {
				t.Fatalf("scratch of %d bytes asked for a %d px cap", n, lim.MaxEncodedImagePixels)
			}
			return make([]byte, n), nil
		})
		if err != nil {
			return
		}
		if int64(img.Width)*int64(img.Height) > lim.MaxEncodedImagePixels {
			t.Fatalf("accepted %dx%d", img.Width, img.Height)
		}
		if err := BuildPDF(io.Discard, []BuildPage{{Image: img, DPI: 72}}); err != nil {
			t.Fatalf("accepted but BuildPDF refuses: %v", err)
		}
	})
}
