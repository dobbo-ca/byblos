package byblos

// Stage 2.2 (kleio in-process pipeline spec, row 9): the image kinds BuildPDF
// and the Embed functions refused until now, held to what img2pdf made of the
// same files. testdata/img2pdf/oracle.json is img2pdf 0.6.3's output for the
// fixtures beside it (generated once by testdata/img2pdf/gen.py in the kleio
// toolchain image); nothing here needs img2pdf at test time.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"strings"
	"testing"
)

type img2pdfEntry struct {
	Mediabox []float64 `json:"mediabox"`
	Rotate   int       `json:"rotate"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	BPC      int       `json:"bpc"`
	Filter   string    `json:"filter"`
	CS       string    `json:"colorspace"`
	Decode   []float64 `json:"decode"`
	Error    string    `json:"error"`
}

func img2pdfOracle(t *testing.T) map[string]img2pdfEntry {
	t.Helper()
	raw, err := os.ReadFile("testdata/img2pdf/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]img2pdfEntry
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func img2pdfFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/img2pdf/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pageDict returns the text of the first /Type /Page dictionary.
func pageDict(t *testing.T, pdf []byte) string {
	t.Helper()
	i := bytes.Index(pdf, []byte("/Type /Page "))
	if i < 0 {
		t.Fatal("no page dictionary")
	}
	return string(pdf[i : i+bytes.Index(pdf[i:], []byte(">> >>"))])
}

// --- 16 bits per component -------------------------------------------------

func TestBuildPDFCarries16BitFlate(t *testing.T) {
	for _, tc := range []struct {
		cs string
		n  int
	}{{"DeviceGray", 1}, {"DeviceRGB", 3}} {
		t.Run(tc.cs, func(t *testing.T) {
			const w, h = 7, 5
			px := make([]byte, w*h*tc.n*2)
			for i := range px {
				px[i] = byte(i*7 + 3)
			}
			img := EncodedImage{Width: w, Height: h, BPC: 16, ColorSpace: ColorSpace{Name: tc.cs},
				Filter: "FlateDecode", Data: flateEncode(t, px)}
			out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 72}})
			if d := imageDictString(t, out, "FlateDecode"); !strings.Contains(d, "/BitsPerComponent 16") {
				t.Errorf("image dictionary lacks /BitsPerComponent 16: %s", d)
			}
			// The reader the stage was blocked on (spec: it panicked on a
			// 16-bpc DeviceGray image) reads it back.
			pr, err := ExtractPageRaster(bytes.NewReader(out), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster: %v", err)
			}
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					r, g, b, _ := pr.Image.At(x, y).RGBA()
					o := (y*w + x) * tc.n * 2
					// image.RGBA keeps 8 bits: the 16-bit sample scaled down, truncated.
					c := func(k int) uint32 { return uint32(binary.BigEndian.Uint16(px[o+2*k:])) / 257 * 257 }
					want := [3]uint32{c(0), c(0), c(0)}
					if tc.n == 3 {
						want = [3]uint32{c(0), c(1), c(2)}
					}
					if got := [3]uint32{r, g, b}; got != want {
						t.Fatalf("pixel (%d,%d) = %v; want %v", x, y, got, want)
					}
				}
			}
		})
	}
}

func TestBuildPDFRefuses16BitIndexed(t *testing.T) {
	img := EncodedImage{Width: 2, Height: 2, BPC: 16,
		ColorSpace: ColorSpace{Name: "Indexed", Base: "DeviceRGB", HiVal: 0, Lookup: []byte{0, 0, 0}},
		Filter:     "FlateDecode", Data: flateEncode(t, make([]byte, 8))}
	if err := BuildPDF(&bytes.Buffer{}, []BuildPage{{Image: img, DPI: 72}}); err == nil {
		t.Fatal("16-bpc /Indexed was written")
	}
}

// --- CMYK / YCCK JPEG -------------------------------------------------------

func TestEmbedJPEGCMYKMatchesImg2pdf(t *testing.T) {
	oracle := img2pdfOracle(t)
	adobeYCCK := append([]byte(nil), img2pdfFile(t, "cmyk.jpg")...)
	// The Adobe APP14 transform byte: 0 CMYK, 2 YCCK.
	i := bytes.Index(adobeYCCK, []byte("Adobe"))
	adobeYCCK[i+11] = 2
	for _, tc := range []struct {
		name string
		data []byte
		want img2pdfEntry
	}{
		{"cmyk.jpg", img2pdfFile(t, "cmyk.jpg"), oracle["cmyk.jpg"]},
		{"cmyk-noadobe.jpg", img2pdfFile(t, "cmyk-noadobe.jpg"), oracle["cmyk-noadobe.jpg"]},
		{"ycck", adobeYCCK, oracle["cmyk.jpg"]}, // img2pdf treats both Adobe transforms alike
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, _, err := EmbedJPEG(tc.data)
			if err != nil {
				t.Fatalf("EmbedJPEG: %v", err)
			}
			if img.ColorSpace.Name != "DeviceCMYK" || img.Filter != "DCTDecode" || img.BPC != tc.want.BPC ||
				img.Width != tc.want.Width || img.Height != tc.want.Height || !bytes.Equal(img.Data, tc.data) {
				t.Errorf("EncodedImage = %+v; img2pdf wrote %+v", img, tc.want)
			}
			if fmt.Sprint(img.Decode) != fmt.Sprint(append([]float64(nil), tc.want.Decode...)) && len(img.Decode)+len(tc.want.Decode) > 0 {
				t.Errorf("Decode = %v; img2pdf wrote %v", img.Decode, tc.want.Decode)
			}
			out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 72}})
			d := imageDictString(t, out, "DCTDecode")
			if !strings.Contains(d, "/ColorSpace /DeviceCMYK") {
				t.Errorf("no DeviceCMYK in %s", d)
			}
			if has := strings.Contains(d, "/Decode [1 0 1 0 1 0 1 0]"); has != (len(tc.want.Decode) > 0) {
				t.Errorf("/Decode present = %v; img2pdf's has %v: %s", has, tc.want.Decode, d)
			}
			if !bytes.Contains(out, tc.data) {
				t.Error("the JPEG was not carried verbatim")
			}
		})
	}
}

func TestBuildPDFRefusesBadDecodeArray(t *testing.T) {
	img, _, err := EmbedJPEG(img2pdfFile(t, "cmyk.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range [][]float64{{1, 0}, {1, 0, 1, 0, 1, 0, 1, 0, 1}, {1, 0, 1, 0, 1, 0, 1, nanF()}} {
		img.Decode = d
		if err := BuildPDF(&bytes.Buffer{}, []BuildPage{{Image: img, DPI: 72}}); err == nil {
			t.Errorf("Decode %v was written", d)
		}
	}
}

func nanF() float64 { var z float64; return z / z }

// --- JP2 passthrough ---------------------------------------------------------

func TestEmbedJP2MatchesImg2pdf(t *testing.T) {
	oracle := img2pdfOracle(t)
	for _, name := range []string{"rgb.jp2", "gray.jp2"} {
		t.Run(name, func(t *testing.T) {
			data, want := img2pdfFile(t, name), oracle[name]
			img, _, err := EmbedJP2(data, Limits{})
			if err != nil {
				t.Fatalf("EmbedJP2: %v", err)
			}
			if img.Filter != "JPXDecode" || "/"+img.ColorSpace.Name != want.CS || img.BPC != want.BPC ||
				img.Width != want.Width || img.Height != want.Height || !bytes.Equal(img.Data, data) {
				t.Errorf("EncodedImage = %+v; img2pdf wrote %+v", img, want)
			}
			out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 96}})
			if !bytes.Contains(out, data) {
				t.Error("the JP2 file was not carried verbatim")
			}
			if d := imageDictString(t, out, "JPXDecode"); !strings.Contains(d, "/ColorSpace "+want.CS) {
				t.Errorf("colour space not %s: %s", want.CS, d)
			}
		})
	}
}

func TestEmbedJP2RefusesWhatImg2pdfRefuses(t *testing.T) {
	if want := img2pdfOracle(t)["cmyk.jp2"]; want.Error == "" {
		t.Fatal("oracle says img2pdf accepts cmyk.jp2")
	}
	if _, _, err := EmbedJP2(img2pdfFile(t, "cmyk.jp2"), Limits{}); err == nil {
		t.Error("CMYK JP2 accepted; img2pdf refuses it")
	}
}

// jp2 assembles a JP2 file from boxes, so the parser's edges are testable
// without codestream bytes.
func jp2(w, h uint32, bpc byte, enumCS uint32, extra ...[]byte) []byte {
	box := func(typ string, body ...[]byte) []byte {
		b := bytes.Join(body, nil)
		return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(b))), append([]byte(typ), b...)...)
	}
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, h), w)
	ihdr = append(ihdr, 0, 3, bpc, 7, 0, 0)
	colr := append([]byte{1, 0, 0}, binary.BigEndian.AppendUint32(nil, enumCS)...)
	jp2h := box("jp2h", box("ihdr", ihdr), box("colr", colr), bytes.Join(extra, nil))
	return bytes.Join([][]byte{
		box("jP  ", []byte{0x0D, 0x0A, 0x87, 0x0A}),
		box("ftyp", []byte("jp2 "), make([]byte, 4), []byte("jp2 ")),
		jp2h,
		box("jp2c", []byte{0xFF, 0x4F, 0xFF, 0xD9}),
	}, nil)
}

func TestEmbedJP2ParsesBoxes(t *testing.T) {
	// A resc box: 3937 / 1 pixels per metre vertically and horizontally, 10^0.
	resc := binary.BigEndian.AppendUint16(nil, 3937)
	resc = append(resc, 0, 1, 0x0F, 0x61, 0, 1, 0, 0)
	res := append(binary.BigEndian.AppendUint32(nil, 8+8+10), []byte("res ")...)
	res = append(res, binary.BigEndian.AppendUint32(nil, 8+10)...)
	res = append(append(res, []byte("resc")...), resc...)

	img, dpi, err := EmbedJP2(jp2(30, 20, 7, 17, res), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 30 || img.Height != 20 || img.BPC != 8 || img.ColorSpace.Name != "DeviceGray" {
		t.Errorf("got %+v", img)
	}
	if dpi < 99.9 || dpi > 100.1 {
		t.Errorf("dpi = %v; want 100 (3937 px/m)", dpi)
	}
	for name, data := range map[string][]byte{
		"empty":        nil,
		"no-signature": jp2(1, 1, 7, 16)[12:],
		"truncated":    jp2(1, 1, 7, 16)[:40],
		"icc":          bytes.Replace(jp2(1, 1, 7, 16), []byte{1, 0, 0, 0, 0, 0, 16}, []byte{2, 0, 0, 0, 0, 0, 16}, 1),
		"cmyk":         jp2(1, 1, 7, 12),
		"sycc":         jp2(1, 1, 7, 18),
		"zero-width":   jp2(0, 1, 7, 16),
		"per-comp-bpc": jp2(1, 1, 0xFF, 16),
	} {
		if _, _, err := EmbedJP2(data, Limits{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEmbedJP2LimitsBeforeAnything(t *testing.T) {
	// A forged ihdr: 4 billion square pixels in a 100-byte file.
	_, _, err := EmbedJP2(jp2(1<<31, 1<<31, 7, 16), Limits{})
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "encoded-image-pixels" {
		t.Fatalf("err = %v; want encoded-image-pixels LimitError", err)
	}
	if _, _, err := EmbedJP2(jp2(100, 100, 7, 16), Limits{MaxEncodedImagePixels: 99}); !errors.As(err, &le) {
		t.Errorf("configured cap not applied: %v", err)
	}
}

func TestBuildPDFRefusesJPXThatIsNotJP2(t *testing.T) {
	img := EncodedImage{Width: 1, Height: 1, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceRGB"}, Filter: "JPXDecode", Data: []byte{0}}
	if err := BuildPDF(&bytes.Buffer{}, []BuildPage{{Image: img, DPI: 72}}); err == nil {
		t.Fatal("JPXDecode with no JP2 signature was written")
	}
}

// --- EXIF orientation -> /Rotate ---------------------------------------------

// withOrientation splices an Exif APP1 holding only the orientation tag after
// the SOI of a small JPEG.
func withOrientation(t *testing.T, o uint16, little bool) []byte {
	t.Helper()
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	var bo binary.AppendByteOrder = binary.BigEndian
	hdr := "MM\x00\x2a"
	if little {
		bo, hdr = binary.LittleEndian, "II\x2a\x00"
	}
	tiff := []byte(hdr)
	tiff = bo.AppendUint32(tiff, 8)
	tiff = bo.AppendUint16(tiff, 1)
	tiff = bo.AppendUint16(tiff, 0x0112)
	tiff = bo.AppendUint16(tiff, 3)
	tiff = bo.AppendUint32(tiff, 1)
	tiff = bo.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := append([]byte{0xFF, 0xE1}, binary.BigEndian.AppendUint16(nil, uint16(2+len(seg)))...)
	return append(append(append([]byte(nil), jb.Bytes()[:2]...), append(app1, seg...)...), jb.Bytes()[2:]...)
}

func TestEXIFRotationMatchesImg2pdf(t *testing.T) {
	oracle := img2pdfOracle(t)
	for o := 0; o <= 9; o++ {
		want := oracle[fmt.Sprintf("exif-%d.jpg", o)]
		for _, little := range []bool{false, true} {
			got, err := EXIFRotation(withOrientation(t, uint16(o), little))
			if want.Error != "" {
				if err == nil {
					t.Errorf("orientation %d (little=%v): got %d; img2pdf refuses (%s)", o, little, got, want.Error)
				}
				continue
			}
			if err != nil || got != want.Rotate {
				t.Errorf("orientation %d (little=%v): got %d, %v; img2pdf wrote /Rotate %d", o, little, got, err, want.Rotate)
			}
		}
	}
	// The pillow-made file itself, and a file with no Exif at all.
	if got, err := EXIFRotation(img2pdfFile(t, "exif-6.jpg")); err != nil || got != 90 {
		t.Errorf("exif-6.jpg: %d, %v", got, err)
	}
	var plain bytes.Buffer
	_ = jpeg.Encode(&plain, image.NewGray(image.Rect(0, 0, 4, 4)), nil)
	if got, err := EXIFRotation(plain.Bytes()); err != nil || got != 0 {
		t.Errorf("no Exif: %d, %v; want 0, nil", got, err)
	}
}

func TestEXIFRotationMirroredIsATypedRefusal(t *testing.T) {
	for _, o := range []uint16{2, 4, 5, 7} {
		if _, err := EXIFRotation(withOrientation(t, o, false)); !errors.Is(err, ErrMirroredOrientation) {
			t.Errorf("orientation %d: err = %v; want ErrMirroredOrientation", o, err)
		}
	}
}

func TestBuildPageRotate(t *testing.T) {
	img := flateGrayImage(t, 10, 20, 1)
	for _, r := range []int{90, 180, 270} {
		out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 72, Rotate: r}})
		if d := pageDict(t, out); !strings.Contains(d, fmt.Sprintf("/Rotate %d", r)) || !strings.Contains(d, "/MediaBox [0 0 10 20]") {
			t.Errorf("Rotate %d: %s", r, d)
		}
		pages, err := Inspect(bytes.NewReader(out))
		if err != nil || pages[0].Rotate != r {
			t.Errorf("Rotate %d: Inspect = %v, %v", r, pages, err)
		}
	}
	if d := pageDict(t, buildOrFatal(t, []BuildPage{{Image: img, DPI: 72}})); strings.Contains(d, "/Rotate") {
		t.Errorf("Rotate 0 wrote /Rotate: %s", d)
	}
	for _, r := range []int{45, -90, 360, 91} {
		if err := BuildPDF(&bytes.Buffer{}, []BuildPage{{Image: img, DPI: 72, Rotate: r}}); err == nil {
			t.Errorf("Rotate %d accepted", r)
		}
	}
}

// --- fuzz --------------------------------------------------------------------

// FuzzBuildPDF throws declared images at BuildPDF under tight limits. Whatever
// it accepts must be a PDF the package reads back; nothing may panic.
func FuzzBuildPDF(f *testing.F) {
	jpg, _ := os.ReadFile("testdata/img2pdf/cmyk.jpg")
	jp2f, _ := os.ReadFile("testdata/img2pdf/rgb.jp2")
	f.Add("DCTDecode", "DeviceCMYK", 8, 16, 12, 0, jpg)
	f.Add("JPXDecode", "DeviceRGB", 8, 16, 12, 90, jp2f)
	f.Add("FlateDecode", "DeviceGray", 16, 2, 2, 180, []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})
	f.Add("FlateDecode", "Indexed", 16, 1, 1, 270, []byte{})
	f.Fuzz(func(t *testing.T, filter, cs string, bpc, w, h, rot int, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		img := EncodedImage{Width: w, Height: h, BPC: bpc, ColorSpace: ColorSpace{Name: cs}, Filter: filter, Data: data}
		var out bytes.Buffer
		err := BuildPDFLimits(t.Context(), &out, []BuildPage{{Image: img, DPI: 72, Rotate: rot}}, Limits{MaxEncodedImagePixels: 1 << 16})
		if err != nil {
			return
		}
		if _, err := Inspect(bytes.NewReader(out.Bytes())); err != nil {
			t.Fatalf("accepted %s/%s/%d but Inspect fails: %v", filter, cs, bpc, err)
		}
	})
}

func FuzzEmbedJP2(f *testing.F) {
	f.Add(jp2(3, 3, 7, 16))
	f.Add(jp2(1<<31, 1, 7, 17))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		img, _, err := EmbedJP2(data, Limits{MaxEncodedImagePixels: 1 << 20})
		if err == nil && (img.Width <= 0 || img.Height <= 0 || img.Width*img.Height > 1<<20) {
			t.Fatalf("accepted %dx%d", img.Width, img.Height)
		}
	})
}

func FuzzEXIFRotation(f *testing.F) {
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 8, 'E', 'x', 'i', 'f', 0, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if r, err := EXIFRotation(data); err == nil && r != 0 && r != 90 && r != 180 && r != 270 {
			t.Fatalf("rotation %d", r)
		}
	})
}
