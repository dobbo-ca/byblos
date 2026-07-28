package byblos

import "sync"

// ExtractCounters is a snapshot of ExtractPageRaster outcomes since process
// start, or since the last ResetExtractStats.
//
// The design of Byblos rests on the premise that a page which is not a single
// page-covering raster is rare (design spec section 2). These counters are how
// that premise is checked against reality rather than assumed. Export
// DivertRate from your application; if it is not small, the premise is wrong
// and the design needs revisiting.
type ExtractCounters struct {
	Attempted uint64
	Extracted uint64
	Diverted  uint64            // page understood, but not a single page-covering raster
	Failed    uint64            // could not be read at all: damaged file, missing page
	Reasons   map[string]uint64 // divert reason to count; see classify in extract.go
}

// DivertRate is the fraction of attempted pages that diverted. Failures are
// excluded from the numerator but not the denominator: a document Byblos could
// not read is a different problem from one it read and declined.
func (c ExtractCounters) DivertRate() float64 {
	if c.Attempted == 0 {
		return 0
	}
	return float64(c.Diverted) / float64(c.Attempted)
}

var (
	statsMu sync.Mutex
	stats   = ExtractCounters{Reasons: map[string]uint64{}}
)

// ExtractStats returns a snapshot. The returned Reasons map is a copy, so the
// caller may keep or mutate it freely.
func ExtractStats() ExtractCounters {
	statsMu.Lock()
	defer statsMu.Unlock()
	out := stats
	out.Reasons = make(map[string]uint64, len(stats.Reasons))
	for k, v := range stats.Reasons {
		out.Reasons[k] = v
	}
	return out
}

// ResetExtractStats zeroes every counter. Intended for tests and for a
// long-lived process that reports per-batch rather than cumulative rates.
func ResetExtractStats() {
	statsMu.Lock()
	defer statsMu.Unlock()
	stats = ExtractCounters{Reasons: map[string]uint64{}}
}

func countAttempt() {
	statsMu.Lock()
	stats.Attempted++
	statsMu.Unlock()
}

func countExtracted() {
	statsMu.Lock()
	stats.Extracted++
	statsMu.Unlock()
}

func countFailure() {
	statsMu.Lock()
	stats.Failed++
	statsMu.Unlock()
}

func countDivert(reason string) {
	statsMu.Lock()
	stats.Diverted++
	stats.Reasons[reason]++
	statsMu.Unlock()
}
