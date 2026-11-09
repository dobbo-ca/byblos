package byblos

import (
	"bytes"
	"runtime"
	"testing"
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
		b := bytes.Clone(pdf.Bytes())
		copy(b[at+5:], []byte{byte(n >> 8), byte(n), byte(n >> 8), byte(n)})
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
