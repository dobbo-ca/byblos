package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/dobbo-ca/byblos/internal/bench"
)

// measure runs one capability over one document and returns two samples: the
// one for Case.Run, and the one for Case.Prepare.
//
// WHY PREPARE IS MEASURED AT ALL. It used to be setup, and setup was not
// counted, so every metric described Run only. byb-om7.12 measured what that
// hid, on the four documents eligible for both capabilities: build-pdf's
// Prepare costs at least 43x its Run's latency and at least 280x its memory,
// and jbig2-generic's at least 14x and 352x. Prepare is where rasterOf --
// ExtractPageRaster, the most expensive thing byblos does per document -- and
// Sauvola and QuantizeIndexed live, for four of the nine capabilities. Scoring
// Run alone told a truth about the encode and a falsehood about byblos: a
// candidate could cut extraction by an order of magnitude and score +0.000.
//
// WHY IT IS A SEPARATE SAMPLE AND NOT FOLDED IN. The design's reason for
// excluding Prepare from Run's counters is sound and is kept: folding
// extraction into the jbig2 encode's numbers would make the encode's own size
// and memory unreadable. So Prepare gets its own sample, under the capability
// name "<capability>" + bench.PrepareSuffix. The scorer needs no change to read
// it, because a capability's weight is its measured share of each metric (spec
// section 5.2) -- the new pairs earn their weight from what they actually cost,
// and MetricWeights is untouched.
//
// A Prepare sample carries no size. Prepare returns a prepared input, not an
// encoded artifact, so OutBytes is 0 and the pair drops out of the size axis --
// Score skips any pair whose base value is 0. Prepare is therefore scoreable on
// latency, memory and the three disk axes, but never on size.
//
// ORDER MATTERS, AND RUN'S HALF IS DELIBERATELY UNCHANGED. Prepare is measured
// first in its own window; then the heap is settled and Run is measured exactly
// as it was before this existed; then Run's timing repetitions run, and only
// then Prepare's. Run's numbers are produced by the same procedure as before,
// so they stay comparable across this change.
// A capability with no preparation step returns ONE sample. Four of the nine
// take the document bytes directly, and a sample for their absent Prepare would
// measure only the 24 bytes of boxing an any -- see Case's doc comment for why
// that base is dangerous rather than merely useless.
func measure(capability, docName string, doc []byte, reps int) ([]bench.Sample, error) {
	c, ok := bench.CaseFor(capability)
	if !ok {
		return nil, fmt.Errorf("no case for capability %q", capability)
	}

	in := any(doc)
	var prepare bench.Sample
	if c.Prepare != nil {
		// in is filled by the counted call. Prepare yields a prepared input
		// rather than bytes, so the closure reports no artifact.
		var err error
		prepare, err = counted(capability+bench.PrepareSuffix, docName, func() ([]byte, error) {
			var err error
			in, err = c.Prepare(doc)
			return nil, err
		})
		if err != nil {
			return nil, err
		}
	}

	run, err := counted(capability, docName, func() ([]byte, error) { return c.Run(in) })
	if err != nil {
		return nil, err
	}

	for range reps {
		start := time.Now()
		if _, err := c.Run(in); err != nil {
			return nil, err
		}
		run.WallNS = append(run.WallNS, time.Since(start).Nanoseconds())
	}

	peak, unit := bench.PeakRSS()
	run.PeakRSS, run.PeakRSSUnit = peak, unit
	if c.Prepare == nil {
		return []bench.Sample{run}, nil
	}

	// Prepare is re-run from the document bytes rather than timed once, for the
	// same reason Run is: one wall time cannot show how much it moves.
	for range reps {
		start := time.Now()
		if _, err := c.Prepare(doc); err != nil {
			return nil, err
		}
		prepare.WallNS = append(prepare.WallNS, time.Since(start).Nanoseconds())
	}

	// Peak RSS is process-wide and is never scored, so both samples carry the
	// same reading rather than pretending it can be attributed.
	prepare.PeakRSS, prepare.PeakRSSUnit = peak, unit
	return []bench.Sample{run, prepare}, nil
}

// counted measures one call: the allocation delta, the /proc/self/io deltas and
// the bytes it produced.
//
// The deterministic metrics are taken from a single first call, before any
// repetition, because a second call would add its own allocations and syscalls
// to counters that are supposed to describe one unit of work. The heap is
// settled first so the TotalAlloc delta describes this call and not whatever
// the caller left pending.
func counted(capability, docName string, call func() ([]byte, error)) (bench.Sample, error) {
	s := bench.Sample{Capability: capability, Doc: docName}

	runtime.GC()

	ioBefore, haveIO := bench.ReadProcIO()
	var msBefore, msAfter runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	out, err := call()
	if err != nil {
		return bench.Sample{}, err
	}

	runtime.ReadMemStats(&msAfter)
	s.AllocBytes = int64(msAfter.TotalAlloc - msBefore.TotalAlloc)
	s.OutBytes = int64(len(out))

	if ioAfter, ok := bench.ReadProcIO(); ok && haveIO {
		s.DiskCounters = true
		s.WChar = ioAfter.WChar - ioBefore.WChar
		s.SysCW = ioAfter.SysCW - ioBefore.SysCW
		s.SysCR = ioAfter.SysCR - ioBefore.SysCR
	}
	return s, nil
}
