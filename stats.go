package byblos

import "sync"

// ExtractCounters is a snapshot of ExtractPageRaster outcomes since process
// start, or since the last ResetExtractStats.
//
// The design of Byblos rests on the premise that a page which is not a single
// raster is rare (design spec section 2). These counters are how
// that premise is checked against reality rather than assumed. Export
// UnhandledRate from your application; if it is not small, the premise is wrong
// and the design needs revisiting.
//
// Every attempt increments exactly one of Extracted, Diverted and Failed, so
// those three always sum to Attempted.
type ExtractCounters struct {
	Attempted uint64
	Extracted uint64
	// Partial counts the extracted pages whose raster does not fill the page
	// box. It is not a fourth outcome — it is a subset of Extracted, and does
	// not disturb the sum above.
	//
	// It exists because byb-b1.3 retired the "not-page-covering" divert reason.
	// Those 132 measured pages are ordinary scans placed at their natural
	// resolution and they now extract, which is correct, but without this they
	// would vanish from the instrumentation the whole design is checked by.
	Partial  uint64
	Diverted uint64            // page understood, but not a single raster
	Failed   uint64            // could not be read at all: damaged file, missing page
	Reasons  map[string]uint64 // divert reason to count; see classify in extract.go
}

// DivertRate is the fraction of attempted pages that diverted. Failures are
// excluded from the numerator but not the denominator: a document Byblos could
// not read is a different problem from one it read and declined.
//
// That makes DivertRate the wrong number to watch on its own, and byb-5kk is
// the demonstration: a page-tree bug made 1,266 pages of a real archive sample
// unreadable, and because every one of them landed in Failed, the defect pushed
// the measured divert rate *down*. A whole document class became unprocessable
// while the metric meant to detect exactly that moved in the reassuring
// direction. Watch UnhandledRate; reach for this one to split the total.
func (c ExtractCounters) DivertRate() float64 {
	if c.Attempted == 0 {
		return 0
	}
	return float64(c.Diverted) / float64(c.Attempted)
}

// UnhandledRate is the fraction of attempted pages that produced no raster,
// whether Byblos declined them or could not read them at all.
//
// This is the premise check. It has no blind spot by construction — it is
// 1 - Extracted/Attempted — so no outcome can grow without moving it, and no
// change to the divert vocabulary can quietly stop counting a class of page.
// Diverted and Failed remain readable side by side when the number is bad and
// the question becomes which problem it is.
func (c ExtractCounters) UnhandledRate() float64 {
	if c.Attempted == 0 {
		return 0
	}
	return float64(c.Diverted+c.Failed) / float64(c.Attempted)
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

func countExtracted(coversPage bool) {
	statsMu.Lock()
	stats.Extracted++
	if !coversPage {
		stats.Partial++
	}
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
