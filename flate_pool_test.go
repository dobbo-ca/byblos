package byblos

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/ascii85"
	"errors"
	"fmt"
	"math/rand"
	"runtime/debug"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/limits"
)

// flateImagePDF is one Letter page drawing a w x h Flate image of n components
// (DeviceGray/RGB/CMYK), with an optional 8-bit gray /SMask of the same size.
func flateImagePDF(t testing.TB, w, h, n int, smask bool) []byte {
	t.Helper()
	cs := map[int]string{1: "DeviceGray", 3: "DeviceRGB", 4: "DeviceCMYK"}[n]
	raw := func(n int) []byte {
		var out bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&out, zlib.BestSpeed)
		row := make([]byte, w*n)
		for y := 0; y < h; y++ {
			for i := range row {
				row[i] = byte((i*7 + y*13) >> 3)
			}
			zw.Write(row)
		}
		zw.Close()
		return out.Bytes()
	}
	img := raw(n)
	extra := ""
	bodies := []string{catalog, pages1,
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream"}
	if smask {
		extra = " /SMask 6 0 R"
	}
	bodies = append(bodies, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent 8 /Filter /FlateDecode /Length %d%s >>\nstream\n%s\nendstream", w, h, cs, len(img), extra, img))
	if smask {
		sm := raw(1)
		bodies = append(bodies, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", w, h, len(sm), sm))
	}
	return pdfFile(bodies, "", 0)
}

// Flate pages are decoded by pdfcpu, which holds no budget of its own: byblos
// reserves for it, so a page that needs more than the pool is refused (a
// permanent *LimitError) and the heap stays inside the pool.
func TestFlatePagesStayInPool(t *testing.T) {
	if raceDetector() {
		t.Skip("the race detector inflates the heap")
	}
	const pool = 64 << 20
	defer debug.SetGCPercent(debug.SetGCPercent(5)) // peakHeap counts garbage the GC has not swept
	for _, c := range []struct {
		name    string
		w, h, n int
		sm      bool
		fits    bool
	}{
		{"rgb", 2600, 3365, 3, false, false},
		{"cmyk", 2400, 3105, 4, false, false},
		{"rgb-smask", 2600, 3365, 3, true, false},
		{"rgb-150dpi", 1275, 1650, 3, false, true},
		{"rgb-150dpi-smask", 1275, 1650, 3, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			pdf := flateImagePDF(t, c.w, c.h, c.n, c.sm)
			defer SetMemoryLimit(SetMemoryLimit(pool))
			var err error
			peak := peakHeap(func() { _, err = ExtractPageRaster(bytes.NewReader(pdf), 1) })
			var le *LimitError
			if c.fits && err != nil || !c.fits && (!errors.As(err, &le) || le.Name != "memory-budget") {
				t.Errorf("err = %v; fits = %v", err, c.fits)
			}
			if peak > pool {
				t.Errorf("peak %d MiB > the %d MiB pool", peak>>20, pool>>20)
			}
		})
	}
}

// A decode waits for the pool like a DCT one does: with 70 of 100 MiB held, a
// 1275x1650 RGB page (about 34 MiB) waits until ctx ends, then runs once freed.
func TestFlateDecodeWaitsForPool(t *testing.T) {
	pdf := flateImagePDF(t, 1275, 1650, 3, false)
	defer SetMemoryLimit(SetMemoryLimit(100 << 20))
	release, err := limits.Reserve(context.Background(), 70<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := ExtractPageRasterContext(ctx, bytes.NewReader(pdf), 1); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("err = %v; want ErrMemoryLimit while 70 of 100 MiB are held", err)
	}
	release()
	if _, err := ExtractPageRaster(bytes.NewReader(pdf), 1); err != nil {
		t.Fatalf("after the release: %v", err)
	}
}

// An image stream that inflates past what its dictionary allows is refused
// while it inflates: a 100x100 RGB image is 30 100 bytes with its predictor
// tags, so 35 000 is over, and a bomb stops at the same cap.
func TestFlateInflatingPastItsDictionaryIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		n    int
	}{{"slightly over", 35_000}, {"bomb", 160 << 20}} {
		t.Run(c.name, func(t *testing.T) {
			stream := zeros(t, c.n)
			pdf := pdfFile([]string{catalog, pages1,
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
				"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream",
				fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 100 /Height 100 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}, "", 0)
			before := totalAlloc()
			_, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
			after := totalAlloc()
			var le *LimitError
			if !errors.As(err, &le) || le.Name != "image-bytes" || le.Max != 30100 {
				t.Errorf("err = %v; want *LimitError image-bytes at 30100", err)
			}
			if d := after - before; d > 4<<20 {
				t.Errorf("allocated %d bytes", d)
			}
		})
	}
}

// A soft mask is decoded under the size its own dictionary allows too: this one
// declares 10x10 and inflates to 20 000 bytes.
func TestFlateSoftMaskPastItsDictionaryIsRefused(t *testing.T) {
	stream := zeros(t, 20_000)
	pdf := pdfFile([]string{catalog, pages1,
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream",
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 100 /Height 100 /ColorSpace /DeviceGray /BitsPerComponent 8 /SMask 6 0 R /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(zeros(t, 10100)), zeros(t, 10100)),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 10 /Height 10 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}, "", 0)
	_, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "image-bytes" || le.Max != 110 {
		t.Errorf("err = %v; want *LimitError image-bytes at 110", err)
	}
}

// pdfcpu's PNG output and RawImage's copy of it are as big as the decoded
// pixels when they do not compress: noise that fits stays in the pool, and
// noise that does not is refused, where a smooth page of the same size ran on.
func TestFlateNoiseStaysInPool(t *testing.T) {
	if raceDetector() {
		t.Skip("the race detector inflates the heap")
	}
	const pool = 64 << 20
	defer debug.SetGCPercent(debug.SetGCPercent(5))
	for _, c := range []struct {
		side int
		fits bool
	}{{1950, false}, {1600, true}} {
		t.Run(fmt.Sprint(c.side), func(t *testing.T) {
			noise := make([]byte, c.side*c.side*3)
			rand.New(rand.NewSource(1)).Read(noise)
			var z bytes.Buffer
			zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
			zw.Write(noise)
			zw.Close()
			pdf := pdfFile([]string{catalog, pages1,
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
				"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream",
				fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", c.side, c.side, z.Len(), z.Bytes())}, "", 0)
			defer SetMemoryLimit(SetMemoryLimit(pool))
			var err error
			peak := peakHeap(func() { _, err = ExtractPageRaster(bytes.NewReader(pdf), 1) })
			var le *LimitError
			if c.fits && err != nil || !c.fits && (!errors.As(err, &le) || le.Name != "memory-budget") {
				t.Errorf("err = %v; fits = %v", err, c.fits)
			}
			if peak > pool {
				t.Errorf("peak %d MiB > the %d MiB pool", peak>>20, pool>>20)
			}
		})
	}
}

// A soft mask is held to its own dictionary whatever its last filter is: this
// one declares 10x10 and is a Flate stage around 20 000 bytes of ASCII85 text.
func TestFlateSoftMaskA85PastItsDictionaryIsRefused(t *testing.T) {
	var a85 bytes.Buffer
	w := ascii85.NewEncoder(&a85)
	w.Write(make([]byte, 16_000))
	w.Close()
	a85.WriteString("~>")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(a85.Bytes())
	zw.Close()
	img := zeros(t, 1_001_000)
	pdf := pdfFile([]string{catalog, pages1,
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream",
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1000 /Height 1000 /ColorSpace /DeviceGray /BitsPerComponent 8 /SMask 6 0 R /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(img), img),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 10 /Height 10 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter [/FlateDecode /ASCII85Decode] /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes())}, "", 0)
	_, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "image-bytes" || le.Max != 284 {
		t.Errorf("err = %v; want *LimitError image-bytes at 284", err)
	}
}

// An Indexed sample is one index, not the base space's components: a 1500x1500
// 8-bit image fits a 64 MiB pool with its ordinary palette, and a palette that
// inflates past 256 entries is refused.
func TestFlateIndexed(t *testing.T) {
	const s = 1500
	pdf := func(lut []byte) []byte {
		img := zeros(t, s*(s+1))
		return pdfFile([]string{catalog, pages1,
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
			"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream",
			fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace [/Indexed /DeviceRGB 255 6 0 R] /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", s, s, len(img), img),
			fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(lut), lut)}, "", 0)
	}
	defer SetMemoryLimit(SetMemoryLimit(64 << 20))
	if _, err := ExtractPageRaster(bytes.NewReader(pdf(zeros(t, 768))), 1); err != nil {
		t.Errorf("ordinary palette: %v", err)
	}
	_, err := ExtractPageRaster(bytes.NewReader(pdf(zeros(t, 1<<20))), 1)
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "image-bytes" || le.Max != 768 {
		t.Errorf("err = %v; want *LimitError image-bytes at 768", err)
	}
}

// pdfcpu renders DeviceGray and Indexed as RGB, so the PNG it encodes is 3
// bytes a pixel though the dictionary says 1: noise must still stay in the pool.
func TestFlateGrayNoiseStaysInPool(t *testing.T) {
	if raceDetector() {
		t.Skip("the race detector inflates the heap")
	}
	const pool = 64 << 20
	defer debug.SetGCPercent(debug.SetGCPercent(5))
	for _, c := range []struct {
		side int
		fits bool
	}{{2300, false}, {1500, true}} {
		t.Run(fmt.Sprint(c.side), func(t *testing.T) {
			noise := make([]byte, c.side*c.side)
			rand.New(rand.NewSource(1)).Read(noise)
			var z bytes.Buffer
			zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
			zw.Write(noise)
			zw.Close()
			pdf := pdfFile([]string{catalog, pages1,
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
				"<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream",
				fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", c.side, c.side, z.Len(), z.Bytes())}, "", 0)
			defer SetMemoryLimit(SetMemoryLimit(pool))
			var err error
			peak := peakHeap(func() { _, err = ExtractPageRaster(bytes.NewReader(pdf), 1) })
			var le *LimitError
			if c.fits && err != nil || !c.fits && (!errors.As(err, &le) || le.Name != "memory-budget") {
				t.Errorf("err = %v; fits = %v", err, c.fits)
			}
			if peak > pool {
				t.Errorf("peak %d MiB > the %d MiB pool", peak>>20, pool>>20)
			}
		})
	}
}
