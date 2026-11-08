package byblos

// Red-first limit tests (kleio in-process pipeline design, 9A.4 items 1-6).
// Every hostile fixture is generated here at test time and never committed.

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// pdfFile assembles a classic-xref PDF from object bodies (object i+1 is
// bodies[i]); trailerExtra is spliced into the trailer dictionary and size,
// when positive, overrides the honest /Size.
func pdfFile(bodies []string, trailerExtra string, size int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(bodies))
	for i, body := range bodies {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := b.Len()
	if size <= 0 {
		size = len(bodies) + 1
	}
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(bodies)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R %s>>\nstartxref\n%d\n%%%%EOF\n", size, trailerExtra, xref)
	return b.Bytes()
}

// zeros returns n zero bytes deflated as a zlib stream: a flate bomb of ratio
// about 1000.
func zeros(t testing.TB, n int) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	chunk := make([]byte, 1<<20)
	for left := n; left > 0; left -= len(chunk) {
		if left < len(chunk) {
			chunk = chunk[:left]
		}
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func flateStream(data []byte) string {
	return fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(data), data)
}

const (
	catalog = "<< /Type /Catalog /Pages 2 0 R >>"
	pages1  = "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
	page    = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %s >>"
)

// bombPDF is one page whose content is `contents` (an object body for object
// 4) and, when kids is more than one, the same stream listed that many times.
func bombPDF(contents string, ref string) []byte {
	return pdfFile([]string{catalog, pages1, fmt.Sprintf(page, ref), contents}, "", 0)
}

// paddedPDF is a one-page document with n objects in all (filler dictionaries
// from object 5), so its trailer /Size is n+1.
func paddedPDF(n int) []byte {
	bodies := []string{catalog, pages1, fmt.Sprintf(page, "4 0 R"), "<< /Length 5 >>\nstream\nq Q\n\nendstream"}
	for i := len(bodies); i < n; i++ {
		bodies = append(bodies, fmt.Sprintf("<< /K %d >>", i))
	}
	return pdfFile(bodies, "", 0)
}

func raceDetector() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

func totalAlloc() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

// hostile is the corpus 9A.4 items 2 and 4 run: each must be refused with a
// *LimitError of the named cap.
func hostile(t testing.TB) []struct {
	name, cap string
	pdf       []byte
	maxAlloc  uint64 // TotalAlloc delta ceiling
} {
	return []struct {
		name, cap string
		pdf       []byte
		maxAlloc  uint64
	}{
		// Refused from the header: nothing is parsed, so well under 1 MiB.
		{"forged-size", "objects", pdfFile([]string{catalog, pages1, fmt.Sprintf(page, "4 0 R"), "<< >>"}, "", 1_000_000_000), 1 << 20},
		{"forged-page-count", "pages", pdfFile([]string{catalog, "<< /Type /Pages /Kids [3 0 R] /Count 1000000 >>", fmt.Sprintf(page, "4 0 R"), "<< >>"}, "", 0), 1 << 20},
		// Refused during the decode, which stops at the cap: pdfcpu's output
		// buffer grows by doubling and is then copied, measured at about 4x the
		// 64 MiB cap. Without the cap it inflates all 160 MiB, about 4x that.
		{"stream-bomb", "stream-bytes", bombPDF(flateStream(zeros(t, 160<<20)), "4 0 R"), 5 * 64 << 20},
	}
}

func TestRefusesBeforeAllocating(t *testing.T) {
	for _, h := range hostile(t) {
		t.Run(h.name, func(t *testing.T) {
			before := totalAlloc()
			_, err := InspectDocument(context.Background(), bytes.NewReader(h.pdf), Limits{})
			delta := totalAlloc() - before
			var le *LimitError
			if !errors.As(err, &le) || le.Name != h.cap {
				t.Fatalf("err = %v; want *LimitError named %q", err, h.cap)
			}
			if !errors.Is(err, ErrLimit) || errors.Is(err, ErrMemoryLimit) {
				t.Errorf("err = %v; want it to wrap ErrLimit and not ErrMemoryLimit", err)
			}
			if delta > h.maxAlloc && !raceDetector() { // the race detector roughly doubles allocation
				t.Errorf("TotalAlloc delta = %d bytes; want <= %d", delta, h.maxAlloc)
			}
		})
	}
}

// TestDefaultsAreSafe is 9A.4 item 4: no SetMemoryLimit call and a zero Limits
// refuse the hostile corpus, whatever the budget.
func TestDefaultsAreSafe(t *testing.T) {
	prev := SetMemoryLimit(0)
	defer SetMemoryLimit(prev)
	for _, h := range hostile(t) {
		t.Run(h.name, func(t *testing.T) {
			var zero Limits
			if _, err := InspectDocument(context.Background(), bytes.NewReader(h.pdf), zero); !errors.Is(err, ErrLimit) {
				t.Errorf("err = %v; want ErrLimit", err)
			}
		})
	}
}

// The per-page running total: three streams under the 64 MiB per-stream cap
// that together pass the 128 MiB page cap.
func TestPageStreamTotalIsRefused(t *testing.T) {
	z := zeros(t, 50<<20)
	pdf := bombPDF(flateStream(z), "[4 0 R 4 0 R 4 0 R]")
	_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{})
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "page-stream-bytes" {
		t.Fatalf("err = %v; want *LimitError page-stream-bytes", err)
	}
	// Two of them fit.
	pdf = bombPDF(flateStream(z), "[4 0 R 4 0 R]")
	if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{}); err != nil {
		t.Fatalf("two 50 MiB streams (100 MiB, under the page cap): %v", err)
	}
}

// A form XObject over the cap is refused too, though XObject can only answer
// yes or no; the first breach is kept and returned by the page loop.
func TestFormStreamBombIsRefused(t *testing.T) {
	form := fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(zeros(t, 160<<20)), zeros(t, 160<<20))
	pdf := pdfFile([]string{
		catalog, pages1,
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /F 5 0 R >> >> >>",
		"<< /Length 8 >>\nstream\n/F Do\n\n\nendstream",
		form,
	}, "", 0)
	_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{})
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "stream-bytes" {
		t.Fatalf("err = %v; want *LimitError stream-bytes", err)
	}
}

func TestLimitsAreConfigurable(t *testing.T) {
	pdf := paddedPDF(100)
	if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{MaxObjects: 50}); !errors.Is(err, ErrLimit) {
		t.Errorf("MaxObjects 50 on a 101-object file: err = %v; want ErrLimit", err)
	}
	if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{MaxObjects: 101}); err != nil {
		t.Errorf("MaxObjects 101: %v", err)
	}
}

// liveHeap is the bytes the program holds after a GC. The budget bounds what an
// operation HOLDS; TotalAlloc also counts the parse's garbage (about 9 KB per
// object here), which the GC reclaims and GOMEMLIMIT bounds.
func liveHeap() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// peakHeap runs f and returns the most live heap it held over the heap before
// it, sampled every 200us.
func peakHeap(f func()) uint64 {
	read := func() uint64 {
		s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		metrics.Read(s)
		return s[0].Value.Uint64()
	}
	runtime.GC()
	base := read()
	var peak atomic.Uint64
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			if v := read(); v > peak.Load() {
				peak.Store(v)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	f()
	close(done)
	<-stopped
	return max(peak.Load(), base) - base
}

// repeated is n bytes of content-stream filler, deflated.
func repeated(t testing.TB, n int) []byte { return repeatedOf(t, n, "q Q\n") }

// repeatedOf is repeated with the operators of unit.
func repeatedOf(t testing.TB, n int, unit string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	chunk := bytes.Repeat([]byte(unit), 1<<18)
	for left := n; left > 0; left -= len(chunk) {
		if _, err := zw.Write(chunk[:min(left, len(chunk))]); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	return out.Bytes()
}

// A decode at the stream cap holds at most what DecodeBytes reserves for it.
func TestDecodeReservationCoversPeak(t *testing.T) {
	pdf := bombPDF(flateStream(repeated(t, 160<<20)), "4 0 R") // refused at the 64 MiB cap
	peak := peakHeap(func() {
		_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{})
		var le *LimitError
		if !errors.As(err, &le) || le.Name != "stream-bytes" {
			t.Errorf("err = %v; want *LimitError stream-bytes", err)
		}
	})
	if est := uint64(limits.DecodeBytes(64 << 20)); peak > est {
		t.Errorf("peak %d > reservation %d", peak, est)
	}
	t.Logf("peak %d MiB, reservation %d MiB", peak>>20, limits.DecodeBytes(64<<20)>>20)
}

// A document whose object table alone is over the budget is refused outright:
// waiting for its own reservation would end only with ctx.
func TestOwnReservationDoesNotDeadlock(t *testing.T) {
	prev := SetMemoryLimit(300 << 20)
	defer SetMemoryLimit(prev)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := InspectDocument(ctx, bytes.NewReader(paddedPDF(210_000)), Limits{MaxObjects: 300_000})
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "memory-budget" || errors.Is(err, ErrMemoryLimit) {
		t.Errorf("err = %v; want a permanent memory-budget *LimitError", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v; the refusal needs no wait", d)
	}
}

// The decode slot shrinks to what the budget leaves after the object table, so
// an ordinary document is not refused for room it will not use: kleio's 300 MiB
// takes 40 000 objects, and a small file fits a budget under the default
// slot (GOMEMLIMIT=400MiB halves to 200 MiB).
func TestDecodeSlotFitsTheBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int64
		pdf    []byte
	}{
		{"40k objects in 300 MiB", 300 << 20, paddedPDF(40_000)},
		{"150k objects in 300 MiB", 300 << 20, paddedPDF(150_000)},
		{"small file in 200 MiB", 200 << 20, paddedPDF(10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := SetMemoryLimit(tc.budget)
			defer SetMemoryLimit(prev)
			if _, err := InspectDocument(context.Background(), bytes.NewReader(tc.pdf), Limits{}); err != nil {
				t.Errorf("err = %v; want nil", err)
			}
		})
	}
}

// A trailer that understates /Size cannot park an object table the reservation
// never covered: the reservation comes from the objects in the file, so 150k
// objects under /Size 10 are refused by a budget that cannot hold their table.
func TestUnderReportedSizeIsReservedForUnderTheBudget(t *testing.T) {
	prev := SetMemoryLimit(200 << 20)
	defer SetMemoryLimit(prev)
	pdf := paddedPDF(150_000)
	pdf = bytes.Replace(pdf, []byte("/Size 150001"), []byte("/Size 10"), 1)
	_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{})
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "memory-budget" {
		t.Errorf("err = %v; want *LimitError memory-budget", err)
	}
}

// TestEstimateCoversAllocation is 9A.4 item 1: the object-table reservation
// covers what pdfcpu's parse holds and is not more than 1.25x it.
func TestEstimateCoversAllocation(t *testing.T) {
	for _, n := range []int{20_000, 100_000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			pdf := paddedPDF(n)
			before := liveHeap()
			d, err := pdfdoc.Open(bytes.NewReader(pdf))
			if err != nil {
				t.Fatal(err)
			}
			measured := liveHeap() - before
			runtime.KeepAlive(d)
			est := uint64(limits.ObjectTableBytes(int64(n + 1)))
			if measured > est {
				t.Errorf("measured %d > estimate %d: the reservation does not cover the parse", measured, est)
			}
			if float64(est) > 1.25*float64(measured) {
				t.Errorf("estimate %d > 1.25 x measured %d: the reservation is too loose", est, measured)
			}
			t.Logf("n=%d measured=%d estimate=%d (%.2fx), %.0f B/object", n, measured, est, float64(est)/float64(measured), float64(measured)/float64(n))
		})
	}
}

// TestBudgetHolds is 9A.4 item 3 at the byblos level: the object table is
// reserved, a budget it cannot fit in is a permanent error, and a budget held
// by someone else is the transient one.
func TestBudgetHolds(t *testing.T) {
	pdf := paddedPDF(5_000)
	need := limits.ObjectTableBytes(5_001)

	prev := SetMemoryLimit(need - 1)
	defer SetMemoryLimit(prev)
	_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{})
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "memory-budget" || errors.Is(err, ErrMemoryLimit) {
		t.Errorf("estimate over the whole budget: err = %v; want a permanent memory-budget *LimitError", err)
	}

	// Room for the table and one decode (the stream cap's reservation is taken
	// whole before each decode), all of it held by someone else.
	total := need + limits.DecodeBytes(64<<20)
	SetMemoryLimit(total)
	hold, err := limits.Reserve(context.Background(), total)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = InspectDocument(ctx, bytes.NewReader(pdf), Limits{})
	if !errors.Is(err, ErrMemoryLimit) || errors.Is(err, ErrLimit) {
		t.Errorf("budget held by another: err = %v; want transient ErrMemoryLimit", err)
	}
	// Item 5: the wait ends with ctx.
	if d := time.Since(start); d > time.Second {
		t.Errorf("returned %v after a 50ms deadline", d)
	}
	hold()
	if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{}); err != nil {
		t.Errorf("after release: %v", err)
	}
	if got := SetMemoryLimit(0); got != total {
		t.Errorf("SetMemoryLimit returned %d; want the previous %d", got, total)
	}
}

// 9A.4 item 3: several documents decoding at once under one budget never hold
// more live heap than it.
func TestBudgetHoldsPeak(t *testing.T) {
	pdf := bombPDF(flateStream(repeated(t, 20<<20)), "4 0 R")
	budget := limits.ObjectTableBytes(5) + limits.DecodeBytes(64<<20)
	prev := SetMemoryLimit(budget)
	defer SetMemoryLimit(prev)
	peak := peakHeap(func() {
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	})
	if peak > uint64(budget) {
		t.Errorf("peak %d > budget %d", peak, budget)
	}
	t.Logf("peak %d MiB, budget %d MiB", peak>>20, budget>>20)
}

// The content walk's records are part of the peak: a 60 MiB stream of
// path-painting operators (60 KB deflated) once retained 2.5 GB of Paints
// under a 300 MiB budget.
func TestBudgetHoldsPeakOnPaintHeavyContent(t *testing.T) {
	pdf := bombPDF(flateStream(repeatedOf(t, 60<<20, "0 0 m 1 1 l S\n")), "4 0 R")
	prev := SetMemoryLimit(300 << 20)
	defer SetMemoryLimit(prev)
	peak := peakHeap(func() {
		if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{}); err != nil {
			t.Error(err)
		}
	})
	if peak > 300<<20 {
		t.Errorf("peak %d MiB > budget 300 MiB", peak>>20)
	}
	t.Logf("peak %d MiB", peak>>20)
}

// TestNoGoroutinesStarted is 9A.4 item 6: byblos starts no goroutines of its
// own, so MaxGoroutines has nothing to bound.
func TestNoGoroutinesStarted(t *testing.T) {
	before := runtime.NumGoroutine()
	if _, err := InspectDocument(context.Background(), bytes.NewReader(paddedPDF(1000)), Limits{}); err != nil {
		t.Fatal(err)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d -> %d", before, after)
	}
}

func TestInspectDocumentReportsCatalogFlags(t *testing.T) {
	cases := []struct {
		name, catalog        string
		signature, rendering bool
	}{
		{"plain", catalog, false, false},
		{"signed", "<< /Type /Catalog /Pages 2 0 R /AcroForm << /SigFlags 3 >> >>", true, false},
		{"signflags-append-only", "<< /Type /Catalog /Pages 2 0 R /AcroForm << /SigFlags 2 >> >>", false, false},
		{"dynamic-xfa", "<< /Type /Catalog /Pages 2 0 R /NeedsRendering true >>", false, true},
		{"needsrendering-false", "<< /Type /Catalog /Pages 2 0 R /NeedsRendering false >>", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pdf := pdfFile([]string{c.catalog, pages1, fmt.Sprintf(page, "4 0 R"), "<< /Length 5 >>\nstream\nq Q\n\nendstream"}, "", 0)
			di, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if di.HasSignature != c.signature || di.NeedsRendering != c.rendering || di.Encrypted || len(di.Pages) != 1 {
				t.Errorf("got %+v; want signature=%v rendering=%v", di, c.signature, c.rendering)
			}
		})
	}
}

// Pages that share one content stream each stay under the per-page image cap
// and together hold far more than the document may.
func TestImagesAcrossPagesAreCapped(t *testing.T) {
	const pages = 3
	kids := "[3 0 R 3 0 R 3 0 R]"
	pdf := pdfFile([]string{
		catalog,
		fmt.Sprintf("<< /Type /Pages /Kids %s /Count %d >>", kids, pages),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length 560 >>\nstream\n" + strings.Repeat("/Im0 Do\n", 70) + "\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n0\nendstream",
	}, "", 0)
	lim := Limits{MaxImages: 100, MaxInlineImages: 100}
	_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), lim)
	var le *LimitError
	if !errors.As(err, &le) || le.Name != "images" {
		t.Fatalf("err = %v; want *LimitError images", err)
	}
}
