package byblos

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"runtime"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/dct"
	"github.com/dobbo-ca/byblos/internal/limits"
)

// A 16x16 image dictionary wrapping a JPEG whose SOF claims NxN is refused from
// the SOF, before any decoder sizes a buffer from it.
func TestDCTSOFMustMatchDictionary(t *testing.T) {
	enc, _, err := EmbedJPEG(dcOnlyJPEG(16, 16, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	var pdf bytes.Buffer
	if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: 16, HeightPt: 16}}); err != nil {
		t.Fatal(err)
	}
	sof := []byte{0xFF, 0xC0, 0, 17, 8, 0, 16, 0, 16}
	at := bytes.Index(pdf.Bytes(), sof)
	if at < 0 {
		t.Fatal("SOF0 not found in the PDF")
	}
	for _, n := range []int{2000, 10000, 11800} {
		for _, junk := range []bool{false, true} {
			b := bytes.Clone(pdf.Bytes())
			copy(b[at+5:], []byte{byte(n >> 8), byte(n), byte(n >> 8), byte(n)})
			if junk { // decoders skip stray bytes before a marker; so must the check
				soi := bytes.Index(b, []byte{0xFF, 0xD8})
				b = append(b[:soi+2], append([]byte{0}, b[soi+2:]...)...)
			}
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			_, err := ExtractPageRaster(bytes.NewReader(b), 1)
			_, rerr := RenderPage(bytes.NewReader(b), 1, 64)
			runtime.ReadMemStats(&m1)
			if err == nil {
				t.Errorf("N=%d: ExtractPageRaster accepted a SOF the dictionary contradicts", n)
			}
			if d := m1.TotalAlloc - m0.TotalAlloc; d > 8<<20 {
				t.Errorf("N=%d: extract+render allocated %d bytes", n, d)
			}
			_ = rerr // RenderPage skips the image and carries on
		}
	}
}

func dctPDF(t *testing.T, jpg []byte, w, h int) []byte {
	t.Helper()
	enc, _, err := EmbedJPEG(jpg)
	if err != nil {
		t.Fatal(err)
	}
	var pdf bytes.Buffer
	if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: float64(w), HeightPt: float64(h)}}); err != nil {
		t.Fatal(err)
	}
	return pdf.Bytes()
}

// A DCT decode reserves from byblos's budget, the one pool every operation
// shares, and jpeg-go's own process-wide limiter is skipped: with 29 of 30 MiB
// held elsewhere a 22 MB decode waits (here until ctx ends) instead of running
// on a second, empty pool.
func TestDCTDecodeSharesByblosBudget(t *testing.T) {
	const side = 1450
	pdf := dctPDF(t, dcOnlyJPEG(side, side, 2, 2), side, side)
	f, _ := dct.Parse(pdf[bytes.Index(pdf, []byte{0xFF, 0xD8}):])
	if f.Need() < 20<<20 || f.Need() > 30<<20 {
		t.Fatalf("need %d bytes; the probe wants about 22 MB", f.Need())
	}
	prev := SetMemoryLimit(30 << 20)
	defer SetMemoryLimit(prev)
	release, err := limits.Reserve(context.Background(), 29<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := ExtractPageRasterContext(ctx, bytes.NewReader(pdf), 1); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("err = %v; want the decode to wait on the 29 MiB held and end with ErrMemoryLimit", err)
	}
	release()
	if _, err := ExtractPageRaster(bytes.NewReader(pdf), 1); err != nil {
		t.Fatalf("after the release: %v", err)
	}
}

// A four-component stream is decoded by pdfcpu itself, inside RawImage; its SOF
// is checked on the encoded bytes first, so a patched one is refused cheaply.
func TestDCTCMYKSOFRefusedBeforeDecode(t *testing.T) {
	pdf := dctPDF(t, img2pdfFile(t, "cmyk.jpg"), 16, 12)
	if _, err := ExtractPageRaster(bytes.NewReader(pdf), 1); err != nil {
		t.Fatalf("honest CMYK page: %v", err)
	}
	sof := []byte{0xFF, 0xC0, 0, 20, 8, 0, 12, 0, 16, 4}
	at := bytes.Index(pdf, sof)
	if at < 0 {
		t.Fatal("SOF0 not found in the PDF")
	}
	b := bytes.Clone(pdf)
	copy(b[at+5:], []byte{0x01, 0xF4, 0x01, 0xF4}) // 500 x 500, under the 1 MiB pdfcpu allows a DCT stream of any size
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	_, err := ExtractPageRaster(bytes.NewReader(b), 1)
	runtime.ReadMemStats(&m1)
	if err == nil {
		t.Error("ExtractPageRaster accepted a CMYK SOF the dictionary contradicts")
	}
	t.Logf("err = %v; allocated %d", err, m1.TotalAlloc-m0.TotalAlloc)
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 1<<20 {
		t.Errorf("allocated %d bytes before refusing", d)
	}
}

// A file whose SOF is smaller than its dictionary declares is not refused: it
// decodes at the SOF's size, as viewers do.
func TestDCTSmallerSOFDecodes(t *testing.T) {
	pdf := bytes.Replace(dctPDF(t, dcOnlyJPEG(16, 16, 1, 1), 16, 16), []byte("/Width 16"), []byte("/Width 32"), 1)
	if !bytes.Contains(pdf, []byte("/Width 32")) {
		t.Fatal("no /Width 16 to widen")
	}
	pr, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	if b := pr.Image.Bounds(); b.Dx() != 16 || b.Dy() != 16 {
		t.Errorf("decoded %v; want the SOF's 16x16", b)
	}
}

// Need covers what jpeg-go holds: a decode bounded by it, as extract bounds
// its own, is never refused for lack of bytes.
func TestDCTNeedCoversDecode(t *testing.T) {
	for _, s := range [][2]int{{1, 1}, {2, 2}, {1, 2}, {4, 1}, {4, 2}} {
		for _, wh := range [][2]int{{1, 1}, {75, 50}, {1000, 701}} {
			data := dcOnlyJPEG(wh[0], wh[1], s[0], s[1])
			f, ok := dct.Parse(data)
			if !ok {
				t.Fatalf("%v %v: no SOF", s, wh)
			}
			if _, err := decodeDCT(context.Background(), data, f, 0); err != nil {
				t.Errorf("sampling %v, %v: %v", s, wh, err)
			}
		}
	}
	for _, wh := range [][2]int{{1, 1}, {21, 15}, {1000, 701}} { // grey, which jpeg-go returns as *image.Gray
		var b bytes.Buffer
		if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, wh[0], wh[1])), nil); err != nil {
			t.Fatal(err)
		}
		f, _ := dct.Parse(b.Bytes())
		if _, err := decodeDCT(context.Background(), b.Bytes(), f, 0); err != nil {
			t.Errorf("grey %v: %v", wh, err)
		}
	}
}
