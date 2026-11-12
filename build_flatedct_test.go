package byblos

// B3: BuildPDF writes Flate-over-DCT ([/FlateDecode /DCTDecode]) and a JBIG2
// image with its /JBIG2Globals stream.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/dobbo-ca/byblos/internal/jbig2"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

func TestBuildPDFFlateOverDCTExtractsLikePlainDCT(t *testing.T) {
	plain, _ := jpegImage(t, 20, 20)
	wrapped := plain
	wrapped.FlateLevel = 6
	a := buildOrFatal(t, []BuildPage{{Image: plain, DPI: 300}})
	b := buildOrFatal(t, []BuildPage{{Image: wrapped, DPI: 300}})
	if !bytes.Contains(b, []byte("/Filter [/FlateDecode /DCTDecode]")) {
		t.Error("want /Filter [/FlateDecode /DCTDecode]")
	}
	ra, err := ExtractPageRaster(bytes.NewReader(a), 1)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := ExtractPageRaster(bytes.NewReader(b), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster(flate+dct): %v", err)
	}
	if pixelHash(ra.Image) != pixelHash(rb.Image) {
		t.Error("Flate-wrapped DCT page decodes to different pixels than the plain one")
	}
}

// firstSegment splits an embedded JBIG2 stream after its first segment (the
// page information), so the first part can stand as the globals.
func firstSegment(t *testing.T, s []byte) (first, rest []byte) {
	t.Helper()
	n := 11 + int(binary.BigEndian.Uint32(s[7:11]))
	return s[:n], s[n:]
}

func jbig2GlobalsImage(t *testing.T) (EncodedImage, []byte) {
	bm := jbig2.NewBitmap(24, 24)
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			if (x/4+y/4)%2 == 0 {
				bm.Set(x, y, 1)
			}
		}
	}
	stream, err := jbig2.EmbeddedStream(bm)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jbig2Image(t, 24, 24)
	globals, rest := firstSegment(t, stream)
	img.Data = rest
	img.JBIG2Globals = globals
	return img, globals
}

func TestBuildPDFJBIG2GlobalsRoundTripBitExact(t *testing.T) {
	img, globals := jbig2GlobalsImage(t)
	out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 300}})
	d, err := pdfdoc.Open(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	pg, err := d.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	xo, _ := d.XObject(pg.Scope, "Im0")
	id := xo.ID
	g, err := d.RawImageGlobals(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, globals) {
		t.Error("globals did not round-trip")
	}
	raw, _, err := d.RawImage(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, img.Data) {
		t.Error("JBIG2 data did not round-trip")
	}
}

func TestBuildPDFRefusesMalformedChainsAndGlobals(t *testing.T) {
	dct, _ := jpegImage(t, 8, 8)
	jb, _ := jbig2GlobalsImage(t)
	flate := flateGrayImage(t, 8, 8, 1)
	cases := map[string]func(*EncodedImage){
		"level too high":       func(i *EncodedImage) { *i = dct; i.FlateLevel = 10 },
		"negative level":       func(i *EncodedImage) { *i = dct; i.FlateLevel = -1 },
		"flate over flate":     func(i *EncodedImage) { *i = flate; i.FlateLevel = 6 },
		"flate over jbig2":     func(i *EncodedImage) { *i = jb; i.FlateLevel = 6 },
		"dct without SOI":      func(i *EncodedImage) { *i = dct; i.FlateLevel = 6; i.Data = []byte("not a jpeg") },
		"dct without SOF":      func(i *EncodedImage) { *i = dct; i.FlateLevel = 6; i.Data = dct.Data[:2] },
		"globals on dct":       func(i *EncodedImage) { *i = dct; i.JBIG2Globals = []byte{1} },
		"globals do not parse": func(i *EncodedImage) { *i = jb; i.JBIG2Globals = []byte("garbage") },
		"globals truncated":    func(i *EncodedImage) { *i = jb; i.JBIG2Globals = jb.JBIG2Globals[:len(jb.JBIG2Globals)-1] },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			var img EncodedImage
			mut(&img)
			var buf bytes.Buffer
			if err := BuildPDF(&buf, []BuildPage{{Image: img, DPI: 300}}); err == nil {
				t.Error("BuildPDF accepted it")
			}
		})
	}
}

// Golden: the bytes are fixed on every arch. Update the hashes only for a
// deliberate change to the writer.
func TestBuildPDFFlateDCTAndGlobalsGolden(t *testing.T) {
	dct, _ := jpegImage(t, 20, 20)
	dct.FlateLevel = 6
	jb, _ := jbig2GlobalsImage(t)
	for name, c := range map[string]struct {
		img  EncodedImage
		want string
	}{"flate-dct": {dct, "0a36b2ddcfe1998b5161c7aaa06ca0e6fec80e8b35e5c801e1968c5fd2ba0f64"}, "jbig2-globals": {jb, "45a6242c467651f69447bdbaebd4cf23785c11120e2f28248fc92ff39446332e"}} {
		sum := sha256.Sum256(buildOrFatal(t, []BuildPage{{Image: c.img, DPI: 300}}))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s: sha256 = %s; want %s", name, got, c.want)
		}
	}
}
