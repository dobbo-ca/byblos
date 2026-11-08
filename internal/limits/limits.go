// Package limits holds byblos's resource-limit contract (kleio in-process
// pipeline design, section 9A.1): the per-call caps, the typed errors, and the
// package-level memory budget. It is internal so the root package and pdfdoc
// share one definition; the root package re-exports the names callers use.
package limits

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// ErrLimit is wrapped by every *LimitError: an input breached a hard cap, or
// its own estimate is over the whole memory budget. Permanent: do not retry.
var ErrLimit = errors.New("byblos: resource limit exceeded")

// ErrMemoryLimit means the memory budget is held by other operations and this
// one would fit alone. Transient: retry. It is never wrapped by a *LimitError.
var ErrMemoryLimit = errors.New("byblos: memory budget unavailable")

// LimitError names the cap an input breached. Got is what the input needs
// (for a decode that stopped at its cap, Max+1: the real size was not read).
type LimitError struct {
	Name     string
	Got, Max int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("byblos: limit %s exceeded: %d > %d", e.Name, e.Got, e.Max)
}

func (e *LimitError) Unwrap() error { return ErrLimit }

// Limits bounds one call. The zero value, and any field <= 0, takes the safe
// default. Every cap is checked from a header before the allocation it guards.
type Limits struct {
	// MaxObjects caps the trailer /Size. Default 200 000.
	MaxObjects int64
	// MaxPages caps the page count. Default 10 000.
	MaxPages int64
	// MaxStreamBytes caps one decoded non-image stream. Default 64 MiB.
	MaxStreamBytes int64
	// MaxPageStreamBytes caps the decoded non-image streams of one page
	// (content and form streams) together. Default 128 MiB.
	MaxPageStreamBytes int64
	// MaxImagePixels caps width x height of one image. Default 500 000 000.
	MaxImagePixels int64
	// MaxFontProgramBytes caps one decoded embedded font program. Default 16 MiB.
	MaxFontProgramBytes int64
	// MaxImages, MaxInlineImages and MaxUnresolved cap the entries one page's
	// content walk records in Scan.Images, Scan.InlineImages and Scan.Unresolved
	// (a Do or BI is 8 or more bytes of content, a record 100 to 200 bytes).
	// Each defaults to MaxPageStreamBytes / 512 (262 144 at the default). Past
	// it the call fails: dropping images would change classification.
	MaxImages, MaxInlineImages, MaxUnresolved int64
	// MaxObjectStreamEntries caps the objects packed in object streams (their
	// /N) summed over one parse. Default: the object count the reservation
	// covers, since a packed object has a table entry like any other.
	MaxObjectStreamEntries int64
	// MaxTotalDecodeBytes caps the bytes pdfcpu decodes over one parse (object
	// streams, and whatever else it decodes through the table; decoding a
	// stream twice counts it twice). Default: MaxStreamBytes, so what the parse
	// retains stays inside the one decode slot the reservation holds.
	MaxTotalDecodeBytes int64
}

const DefaultFontProgramBytes = 16 << 20

func def(v, d int64) int64 {
	if v > 0 {
		return v
	}
	return d
}

func (l Limits) Objects() int64         { return def(l.MaxObjects, 200_000) }
func (l Limits) Pages() int64           { return def(l.MaxPages, 10_000) }
func (l Limits) StreamBytes() int64     { return def(l.MaxStreamBytes, 64<<20) }
func (l Limits) PageStreamBytes() int64 { return def(l.MaxPageStreamBytes, 128<<20) }
func (l Limits) ImagePixels() int64     { return def(l.MaxImagePixels, 500_000_000) }
func (l Limits) Images() int64          { return def(l.MaxImages, l.PageStreamBytes()/512) }
func (l Limits) InlineImages() int64    { return def(l.MaxInlineImages, l.PageStreamBytes()/512) }
func (l Limits) Unresolved() int64      { return def(l.MaxUnresolved, l.PageStreamBytes()/512) }
func (l Limits) ObjectStreamEntries() int64 {
	return min(def(l.MaxObjectStreamEntries, l.Objects()), l.Objects())
}
func (l Limits) TotalDecodeBytes() int64 { return def(l.MaxTotalDecodeBytes, l.StreamBytes()) }
func (l Limits) FontProgramBytes() int64 { return def(l.MaxFontProgramBytes, DefaultFontProgramBytes) }

// Check returns a *LimitError when got > max.
func Check(name string, got, max int64) error {
	if got > max {
		return &LimitError{Name: name, Got: got, Max: max}
	}
	return nil
}

// PerObjectBytes is the object-table cost reserved per trailer /Size entry.
// TestEstimateCoversAllocation holds it to the measured cost of pdfcpu's
// ReadContext on the pinned version.
const PerObjectBytes = 1536

// ObjectTableBytes is the reservation for a document declaring size objects.
func ObjectTableBytes(size int64) int64 { return size * PerObjectBytes }

// DecodeBytes is the reservation for one stream decode capped at max. pdfcpu
// grows its output buffer by doubling and then copies it, measured at a peak of
// about 3.7x the cap (TestDecodeReservationCoversPeak), so 4x.
func DecodeBytes(max int64) int64 { return max * 4 }

type waiter struct {
	n       int64
	ch      chan struct{}
	granted bool
}

type budget struct {
	mu     sync.Mutex
	inited bool
	limit  int64 // <= 0: none
	used   int64
	q      []*waiter
}

var global budget

func (b *budget) cur() int64 {
	if !b.inited {
		b.inited = true
		b.limit = defaultLimit(debug.SetMemoryLimit(-1), os.ReadFile)
	}
	return b.limit
}

// grant wakes queued reservations in FIFO order while the head fits.
func (b *budget) grant() {
	for len(b.q) > 0 {
		w := b.q[0]
		if b.limit > 0 && b.used+w.n > b.limit {
			return
		}
		b.used += w.n
		w.granted = true
		close(w.ch)
		b.q = b.q[1:]
	}
}

// SetMemoryLimit sets the bytes all byblos operations in flight may hold
// together; n <= 0 means none. It returns the previous value.
func SetMemoryLimit(n int64) int64 {
	global.mu.Lock()
	defer global.mu.Unlock()
	prev := global.cur()
	if n < 0 {
		n = 0
	}
	global.limit = n
	global.grant()
	return prev
}

// MemoryLimit is the budget now in force; <= 0 means none.
func MemoryLimit() int64 {
	global.mu.Lock()
	defer global.mu.Unlock()
	return global.cur()
}

// Reserve takes n bytes from the budget before the caller allocates them,
// waiting in FIFO order under ctx. Its own need over the whole limit is a
// permanent *LimitError; a wait that ctx ends is an error wrapping
// ErrMemoryLimit and ctx.Err(). The returned func releases the bytes.
func Reserve(ctx context.Context, n int64) (func(), error) {
	b := &global
	b.mu.Lock()
	lim := b.cur()
	if lim <= 0 || n <= 0 {
		b.mu.Unlock()
		return func() {}, nil
	}
	if n > lim {
		b.mu.Unlock()
		return nil, &LimitError{Name: "memory-budget", Got: n, Max: lim}
	}
	release := func() {
		b.mu.Lock()
		b.used = max(b.used-n, 0) // SetMemoryLimit(0) in between stops counting
		b.grant()
		b.mu.Unlock()
	}
	if len(b.q) == 0 && b.used+n <= lim {
		b.used += n
		b.mu.Unlock()
		return release, nil
	}
	w := &waiter{n: n, ch: make(chan struct{})}
	b.q = append(b.q, w)
	b.mu.Unlock()
	select {
	case <-w.ch:
		return release, nil
	case <-ctx.Done():
		b.mu.Lock()
		defer b.mu.Unlock()
		if w.granted {
			b.used -= n
		} else {
			for i, x := range b.q {
				if x == w {
					b.q = append(b.q[:i], b.q[i+1:]...)
					break
				}
			}
		}
		b.grant()
		return nil, fmt.Errorf("%w: %w", ErrMemoryLimit, ctx.Err())
	}
}

// defaultLimit is half of the Go memory limit, else half of the cgroup limit,
// else none (0).
func defaultLimit(goLimit int64, readFile func(string) ([]byte, error)) int64 {
	if goLimit > 0 && goLimit != math.MaxInt64 {
		return goLimit / 2
	}
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := readFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err == nil && v > 0 && v < 1<<60 { // "max" and cgroup v1's unlimited value are not limits
			return v / 2
		}
	}
	return 0
}
