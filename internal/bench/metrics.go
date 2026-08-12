package bench

import (
	"bufio"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// procIO holds the three /proc/self/io counters design spec section 2 scores:
// wchar for bytes written, syscw and syscr for write and read syscall counts.
//
// All three are deterministic for the same code over the same input, which is
// what lets them live in the committed baseline (spec section 5.3).
type procIO struct {
	WChar int64
	SysCW int64
	SysCR int64
}

// readProcIO reads /proc/self/io.
//
// ok is false on any platform without that file, which is every platform
// except Linux. A caller MUST record that as absence rather than substituting
// zero: zero is the reading for "wrote nothing", and a macOS run that reported
// it would claim a disk improvement CI could not reproduce.
func readProcIO() (procIO, bool) {
	f, err := os.Open("/proc/self/io")
	if err != nil {
		return procIO{}, false
	}
	defer f.Close()
	return parseProcIO(f)
}

// parseProcIO is readProcIO's body, split out so it can be tested against a
// fixture on a machine that has no /proc.
func parseProcIO(r io.Reader) (procIO, bool) {
	var out procIO
	var sawWChar, sawCW, sawCR bool
	s := bufio.NewScanner(r)
	for s.Scan() {
		key, value, found := strings.Cut(s.Text(), ":")
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "wchar":
			out.WChar, sawWChar = n, true
		case "syscw":
			out.SysCW, sawCW = n, true
		case "syscr":
			out.SysCR, sawCR = n, true
		}
	}
	if s.Err() != nil {
		return procIO{}, false
	}
	return out, sawWChar && sawCW && sawCR
}

// peakRSS returns ru_maxrss and the unit it is expressed in.
//
// Linux reports kilobytes and Darwin reports bytes. The value is informational
// only -- design spec section 2 scores memory on runtime.MemStats.TotalAlloc,
// because peak RSS depends on when the garbage collector happened to run -- so
// it is recorded with its unit rather than normalised into a number that would
// invite comparison across platforms.
func peakRSS() (value int64, unit string) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, "unknown"
	}
	if runtime.GOOS == "linux" {
		return int64(ru.Maxrss), "KiB"
	}
	return int64(ru.Maxrss), "bytes"
}
