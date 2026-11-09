package byblos

// Stage 2.2 (kleio in-process pipeline spec, row 9): the image kinds BuildPDF
// and the Embed functions refused until now, held to what img2pdf made of the
// same files. testdata/img2pdf/oracle.json is img2pdf 0.6.3's output for the
// fixtures beside it (generated once by testdata/img2pdf/gen.py in the kleio
// toolchain image); nothing here needs img2pdf at test time.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"regexp"
	"runtime"
	"strconv"
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
	// A resc box: 1969 / 1 pixels per metre vertically (50 dpi) and 3937 / 1
	// horizontally (100 dpi), 10^0.
	resc := binary.BigEndian.AppendUint16(nil, 1969)
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
	if dpi != (Resolution{100, 50}) {
		t.Errorf("dpi = %v; want 100 x 50 (3937 and 1969 px/m, as img2pdf rounds them)", dpi)
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

// --- the page box, against img2pdf -----------------------------------------------

// exifVariants are the oracle-only Exif files, exif-6.jpg with its orientation
// entry replaced as testdata/img2pdf/gen.py does.
var exifVariants = map[string][]byte{
	"exif-long.jpg":   {1, 0x12, 0, 4, 0, 0, 0, 1, 0, 0, 0, 6},
	"exif-short0.jpg": {1, 0x12, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0},
	"exif-short2.jpg": {1, 0x12, 0, 3, 0, 0, 0, 2, 0, 6, 0, 1},
}

// oracleInput is the input an oracle.json entry was made from: the file in
// testdata/img2pdf, or for the exif variants, exif-6.jpg with its orientation
// replaced.
func oracleInput(t *testing.T, name string) []byte {
	t.Helper()
	if b, err := os.ReadFile("testdata/img2pdf/" + name); err == nil {
		return b
	}
	repl, ok := exifVariants[name]
	if !ok {
		var o byte
		if _, err := fmt.Sscanf(name, "exif-%d.jpg", &o); err != nil {
			t.Fatalf("no input for oracle entry %s", name)
		}
		repl = []byte{1, 0x12, 0, 3, 0, 0, 0, 1, 0, o, 0, 0}
	}
	return bytes.Replace(img2pdfFile(t, "exif-6.jpg"), []byte{1, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0}, repl, 1)
}

// TestEmbedPageBoxMatchesImg2pdf builds a page from every accepted oracle
// entry the way a caller does (the Resolution Embed returns, 96 when it
// declares none, EXIFRotation) and holds its MediaBox and /Rotate to what
// img2pdf wrote; an entry img2pdf refuses must be refused.
func TestEmbedPageBoxMatchesImg2pdf(t *testing.T) {
	for name, want := range img2pdfOracle(t) {
		t.Run(name, func(t *testing.T) {
			data := oracleInput(t, name)
			var img EncodedImage
			var res Resolution
			var err error
			rotate := 0
			switch {
			case strings.HasSuffix(name, ".jpg"):
				img, res, err = EmbedJPEG(data)
			case strings.HasSuffix(name, ".jp2"):
				img, res, err = EmbedJP2(data, Limits{})
			case strings.HasPrefix(name, "rgba-"):
				img, res, err = TranscodePNG(t.Context(), bytes.NewReader(data), Limits{}, nil)
			case strings.HasSuffix(name, ".png"):
				img, res, err = EmbedPNG(data)
			default:
				t.Fatalf("no Embed function for oracle entry %s", name)
			}
			if err == nil {
				rotate, err = EXIFRotation(data)
			}
			if want.Error != "" {
				if err == nil {
					t.Errorf("accepted; img2pdf refuses it (%s)", want.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res == (Resolution{}) {
				res = Resolution{96, 96}
			}
			out := buildOrFatal(t, []BuildPage{{Image: img, DPI: res.X, DPIY: res.Y, Rotate: rotate}})
			d := pageDict(t, out)
			m := regexp.MustCompile(`/MediaBox \[([^\]]*)\]`).FindStringSubmatch(d)
			if m == nil {
				t.Fatalf("no MediaBox in %s", d)
			}
			box := strings.Fields(m[1])
			if len(box) != len(want.Mediabox) {
				t.Fatalf("MediaBox %v; img2pdf wrote %v", box, want.Mediabox)
			}
			for i, f := range box {
				if v, err := strconv.ParseFloat(f, 64); err != nil || math.Abs(v-want.Mediabox[i]) > 1e-4 {
					t.Errorf("MediaBox %v; img2pdf wrote %v (from %v dpi)", box, want.Mediabox, res)
					break
				}
			}
			if name == "exif-dup-1then6.jpg" || name == "exif-two-segments.jpg" {
				if strings.Contains(d, "/Rotate") { // ambiguous: rotation 0 by rule (orientation_test.go)
					t.Errorf("ambiguous file got a rotated page %s", d)
				}
				return
			}
			if got := strings.Contains(d, fmt.Sprintf("/Rotate %d", want.Rotate)); got != (want.Rotate != 0) || (want.Rotate == 0 && strings.Contains(d, "/Rotate")) {
				t.Errorf("page %s; img2pdf wrote /Rotate %d", d, want.Rotate)
			}
		})
	}
}

// TestEXIFRotationOracleFiles holds EXIFRotation to img2pdf on every exif-*
// oracle entry, the entries Pillow reads leniently (LONG, empty, two values)
// among them.
func TestEXIFRotationOracleFiles(t *testing.T) {
	for name, want := range img2pdfOracle(t) {
		if !strings.HasPrefix(name, "exif-") {
			continue
		}
		got, err := EXIFRotation(oracleInput(t, name))
		if name == "exif-dup-1then6.jpg" || name == "exif-two-segments.jpg" { // repeated metadata is ambiguous, rotation 0 (orientation_test.go)
			if o, err := EXIFOrientation(oracleInput(t, name), Limits{}); err != nil || o != (Orientation{Ambiguous: true}) {
				t.Errorf("%s: got %+v, %v; want ambiguous", name, o, err)
			}
			continue
		}
		if want.Error != "" {
			if err == nil {
				t.Errorf("%s: got %d; img2pdf refuses (%s)", name, got, want.Error)
			}
		} else if err != nil || got != want.Rotate {
			t.Errorf("%s: got %d, %v; img2pdf wrote /Rotate %d", name, got, err, want.Rotate)
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

// pngBeforeIDAT is smallPNG with the chunks written before its IDAT.
func pngBeforeIDAT(t *testing.T, chunks ...[]byte) []byte {
	t.Helper()
	d := smallPNG(t)
	i := bytes.Index(d, []byte("IDAT")) - 4
	out := append([]byte(nil), d[:i]...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, d[i:]...)
}

func pngTextChunk(typ, key string, text []byte) []byte {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(text)
	w.Close()
	var b bytes.Buffer
	writeHostileChunk(&b, typ, append([]byte(key+"\x00\x00"), z.Bytes()...))
	return b.Bytes()
}

// A file of zTXt chunks that each inflate to a megabyte must not be inflated
// in full: every key is held to Limits.MaxPNGTextBytes (img2pdf: "Too much memory used in text chunks").
func TestEXIFRotationPNGTextBomb(t *testing.T) {
	const n = 200
	zeros := make([]byte, 1<<20)
	var other, wanted [][]byte
	for i := 0; i < n; i++ {
		other = append(other, pngTextChunk("zTXt", "Comment", zeros))
		wanted = append(wanted, pngTextChunk("zTXt", "XML:com.adobe.xmp", zeros))
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	if _, err := EXIFRotation(pngBeforeIDAT(t, other...)); limitNamed(err) != "png-text-bytes" {
		t.Errorf("unread keys: err = %v; want *LimitError png-text-bytes", err)
	}
	_, err := EXIFRotationLimits(pngBeforeIDAT(t, wanted...), Limits{MaxPNGTextBytes: 8 << 20})
	runtime.ReadMemStats(&m1)
	if limitNamed(err) != "png-text-bytes" {
		t.Errorf("err = %v; want *LimitError png-text-bytes", err)
	}
	if got := m1.TotalAlloc - m0.TotalAlloc; got > 64<<20 {
		t.Errorf("allocated %d MiB for %d MiB of bomb", got>>20, n)
	}
}

// Pillow cannot open a PNG whose pre-IDAT eXIf has a bad CRC.
func TestEmbedPNGRefusesBadCRCBeforeIDAT(t *testing.T) {
	var b bytes.Buffer
	writeHostileChunk(&b, "eXIf", []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x00"))
	good := pngBeforeIDAT(t, b.Bytes())
	if _, _, err := EmbedPNG(good); err != nil {
		t.Fatalf("good CRC: %v", err)
	}
	bad := pngBeforeIDAT(t, b.Bytes())
	bad[bytes.Index(bad, []byte("eXIf"))+4+10+1]++ // a CRC byte of the eXIf chunk
	if _, _, err := EmbedPNG(bad); err == nil || !strings.Contains(err.Error(), "bad CRC") {
		t.Errorf("err = %v; want bad CRC", err)
	}
}

func FuzzEXIFRotation(f *testing.F) {
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 8, 'E', 'x', 'i', 'f', 0, 0})
	f.Add([]byte{})
	f.Add(append(pngSignature[:], pngTextChunk("zTXt", "XML:com.adobe.xmp", []byte("tiff:Orientation=\"6\""))...))
	f.Add(append(pngSignature[:], pngTextChunk("iTXt", "Raw profile type exif", []byte("\n\n\n4d4d002a"))...))
	f.Fuzz(func(t *testing.T, data []byte) {
		jpegDPI(data)
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		if r, err := EXIFRotationLimits(data, Limits{MaxPNGTextBytes: 1 << 20}); err == nil && r != 0 && r != 90 && r != 180 && r != 270 {
			t.Fatalf("rotation %d", r)
		}
		runtime.ReadMemStats(&m1)
		if got := m1.TotalAlloc - m0.TotalAlloc; got > 64<<20+16*uint64(len(data)) {
			t.Fatalf("allocated %d bytes for %d", got, len(data))
		}
	})
}
