package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/dobbo-ca/byblos/internal/bench"
)

// measure runs one capability over one document and returns the sample.
//
// Order matters. The deterministic metrics are taken from a single first call,
// before any repetition, because a second call would add its own allocations
// and syscalls to counters that are supposed to describe one unit of work. The
// timing repetitions run afterwards and contribute nothing but wall times.
func measure(capability, docName string, doc []byte, reps int) (bench.Sample, error) {
	c, ok := bench.CaseFor(capability)
	if !ok {
		return bench.Sample{}, fmt.Errorf("no case for capability %q", capability)
	}

	in, err := c.Prepare(doc)
	if err != nil {
		return bench.Sample{}, err
	}

	s := bench.Sample{Capability: capability, Doc: docName}

	// Settle the heap so the TotalAlloc delta describes the call and not
	// whatever Prepare left pending.
	runtime.GC()

	ioBefore, haveIO := bench.ReadProcIO()
	var msBefore, msAfter runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	out, err := c.Run(in)
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

	for range reps {
		start := time.Now()
		if _, err := c.Run(in); err != nil {
			return bench.Sample{}, err
		}
		s.WallNS = append(s.WallNS, time.Since(start).Nanoseconds())
	}

	s.PeakRSS, s.PeakRSSUnit = bench.PeakRSS()
	return s, nil
}
