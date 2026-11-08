package pdfdoc

import (
	"bytes"
	"errors"
	"io"
	"strconv"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// trailerSize reads the /Size of the last trailer (or cross-reference stream
// dictionary) without parsing the file, so the object cap and the object-table
// reservation come before pdfcpu allocates anything. Like a reader, it finds
// the last startxref anywhere in the file (junk after %%EOF included) and
// follows it. ok is false when there is no startxref or no /Size there.
func trailerSize(rs io.ReadSeeker) (size int64, ok bool) {
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, false
	}
	sx := lastIndex(rs, []byte("startxref"), end, end)
	if sx < 0 {
		return 0, false
	}
	off, ok := intAfter(readAt(rs, sx+int64(len("startxref")), 64))
	if !ok {
		return 0, false
	}
	// Offsets count from the %PDF header, as a reader takes them.
	if i := indexFrom(rs, []byte("%PDF-"), 0, end); i > 0 {
		off += i
	}
	// Cross-reference stream: /Size is in the dictionary at the startxref offset.
	if h := readAt(rs, off, 4096); len(h) > 0 && h[0] >= '0' && h[0] <= '9' {
		return sizeIn(h)
	}
	// Classic table: the trailer that follows the xref at the startxref offset.
	// In a linearized file that is the first-page trailer, which holds the
	// real total, not the last trailer in the file.
	t := indexFrom(rs, []byte("trailer"), off, sx)
	if t < 0 {
		return 0, false
	}
	return sizeIn(readAt(rs, t, 4096))
}

// objectsIn is the object count the cap and the reservation rest on: the larger
// of the trailer /Size and objectBound. The trailer is only a claim; pdfcpu
// rebuilds the table by scanning when it cannot use it (a corrupt xref stream,
// a startxref that lands on an object), and holds every object it finds.
func objectsIn(rs io.ReadSeeker, limit int64) int64 {
	size, _ := trailerSize(rs)
	return max(size, objectBound(rs, limit))
}

// objectBound is an upper bound on the objects pdfcpu can create from the
// file, from one pass through a fixed buffer: the distinct object numbers in
// "N G obj" headers (a bitset; a number above limit counts as over it, and a
// rewritten object counts once) plus the /N of every /ObjStm dictionary, whose
// packed objects have no header.
func objectBound(rs io.ReadSeeker, limit int64) (n int64) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	const ov = 256 // longest look-behind or look-ahead of a match
	seen := make([]uint64, limit/64+1)
	buf := make([]byte, 2*ov+64<<10)
	kept := 0
	for first := true; ; first = false {
		m, err := io.ReadFull(rs, buf[kept:])
		b := buf[:kept+m]
		lo, hi := ov, len(b)-ov // matches starting here; the overlap is the next chunk's
		if first {
			lo = 0
		}
		if err != nil {
			hi = len(b)
		}
		n += headersIn(b, lo, hi, seen, limit) + packedIn(b, lo, hi, limit)
		if err != nil {
			return n
		}
		kept = copy(buf, b[len(b)-2*ov:])
	}
}

// headersIn counts the object numbers in "N G obj" headers starting in [lo, hi)
// of b that seen does not hold yet, and records them.
func headersIn(b []byte, lo, hi int, seen []uint64, limit int64) (n int64) {
	for i := lo; ; {
		j := bytes.Index(b[i:], []byte("obj"))
		if j < 0 || i+j >= hi {
			return n
		}
		p := i + j
		i = p + 3
		if p >= 3 && string(b[p-3:p]) == "end" {
			continue
		}
		_, s, ok := numBefore(b, p)
		if !ok {
			continue
		}
		num, _, ok := numBefore(b, s)
		switch {
		case !ok:
		case num > limit:
			n += limit + 1
		case seen[num/64]&(1<<(num%64)) == 0:
			seen[num/64] |= 1 << (num % 64)
			n++
		}
	}
}

// packedIn sums the /N of the /ObjStm dictionaries whose /Type starts in [lo, hi).
func packedIn(b []byte, lo, hi int, limit int64) (n int64) {
	for i := lo; ; {
		j := bytes.Index(b[i:], []byte("/ObjStm"))
		if j < 0 || i+j >= hi {
			return n
		}
		p := i + j
		i = p + 1
		// The dictionary: after the last "obj" before p, up to "stream" after it.
		s, e := max(p-256, 0), min(p+256, len(b))
		if k := bytes.LastIndex(b[s:p], []byte("obj")); k >= 0 {
			s += k + 3
		}
		if k := bytes.Index(b[p:e], []byte("stream")); k >= 0 {
			e = p + k
		}
		for w := b[s:e]; ; {
			k := bytes.Index(w, []byte("/N"))
			if k < 0 {
				break
			}
			w = w[k+2:]
			if len(w) > 0 && (isSpace(w[0]) || w[0] >= '0' && w[0] <= '9') {
				if v, ok := intAfter(w); ok {
					n += min(v, limit+1)
					break
				}
			}
		}
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0
}

// numBefore parses the unsigned integer that ends at p, past any white space,
// and returns where it starts.
func numBefore(b []byte, p int) (v int64, start int, ok bool) {
	e := p
	for e > 0 && isSpace(b[e-1]) {
		e--
	}
	s := e
	for s > 0 && e-s < 10 && b[s-1] >= '0' && b[s-1] <= '9' {
		s--
	}
	if s == e {
		return 0, 0, false
	}
	v, _ = strconv.ParseInt(string(b[s:e]), 10, 64)
	return v, s, true
}

// indexFrom returns the offset of the first occurrence of pat that starts in
// [from, limit), or -1. It reads in fixed chunks through one buffer.
func indexFrom(rs io.ReadSeeker, pat []byte, from, limit int64) int64 {
	const chunk = 64 << 10
	buf := make([]byte, chunk+len(pat)-1)
	for from >= 0 && from < limit {
		if _, err := rs.Seek(from, io.SeekStart); err != nil {
			return -1
		}
		m, _ := io.ReadFull(rs, buf[:min(int64(len(buf)), limit-from+int64(len(pat))-1)])
		if i := bytes.Index(buf[:m], pat); i >= 0 && from+int64(i) < limit {
			return from + int64(i)
		}
		if m < len(pat) {
			return -1
		}
		from += chunk
	}
	return -1
}

// readAt returns up to n bytes at off, or nil.
func readAt(rs io.ReadSeeker, off int64, n int) []byte {
	if _, err := rs.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	b := make([]byte, n)
	m, _ := io.ReadFull(rs, b)
	return b[:m]
}

// lastIndex returns the offset of the last occurrence of pat that starts before
// limit, searching backwards in fixed chunks of a file of the given size, or -1.
func lastIndex(rs io.ReadSeeker, pat []byte, limit, size int64) int64 {
	const chunk = 64 << 10
	buf := make([]byte, chunk+len(pat)-1)
	for limit > 0 {
		start := max(limit-chunk, 0)
		b := buf[:min(limit+int64(len(pat))-1, size)-start]
		if _, err := rs.Seek(start, io.SeekStart); err != nil {
			return -1
		}
		m, _ := io.ReadFull(rs, b)
		b = b[:m]
		if i := bytes.LastIndex(b, pat); i >= 0 {
			return start + int64(i)
		}
		limit = start
	}
	return -1
}

func sizeIn(b []byte) (int64, bool) {
	i := bytes.Index(b, []byte("/Size"))
	if i < 0 {
		return 0, false
	}
	return intAfter(b[i+len("/Size"):])
}

func intAfter(b []byte) (int64, bool) {
	b = bytes.TrimLeft(b, " \t\r\n")
	n := 0
	for n < len(b) && b[n] >= '0' && b[n] <= '9' {
		n++
	}
	v, err := strconv.ParseInt(string(b[:n]), 10, 64)
	return v, err == nil
}

// decode decodes sd under the stream caps. page counts the
// bytes toward the current page's running total.
func (d *doc) decode(sd *types.StreamDict, page bool) error {
	max, name, capMax := d.lim.StreamBytes(), "stream-bytes", d.lim.StreamBytes()
	if page {
		if rem := d.lim.PageStreamBytes() - d.pageBytes; rem < max {
			max, name, capMax = rem, "page-stream-bytes", d.lim.PageStreamBytes()
		}
	}
	if max <= 0 {
		return &limits.LimitError{Name: name, Got: d.pageBytes + 1, Max: capMax}
	}
	err := sd.DecodeWithLimit(max)
	// An unfiltered stream is returned as is, whatever its size, so the length
	// is checked as well as the filter's own refusal.
	if errors.Is(err, filter.ErrDecodeLimitExceeded) || (err == nil && int64(len(sd.Content)) > max) {
		return &limits.LimitError{Name: name, Got: d.pageBytes + max + 1, Max: capMax}
	}
	if err == nil && page {
		d.pageBytes += int64(len(sd.Content))
	}
	return err
}

// isLimit reports whether err is a cap or budget refusal, which no repair or
// per-element drop may swallow.
func isLimit(err error) bool {
	return errors.Is(err, limits.ErrLimit) || errors.Is(err, limits.ErrMemoryLimit)
}

// Err returns the first limit breach a lookup that can only answer "no" (a
// form XObject, say) had to swallow, or nil.
func (d *doc) Err() error { return d.limitErr }

// Limits is the caps the document was opened under; content.Walk reads its
// image-list caps from here.
func (d *doc) Limits() limits.Limits { return d.lim }

// imageDecodeBytes is an upper bound on a declared image's decoded size:
// four components at the declared depth, plus a predictor tag byte per row.
func imageDecodeBytes(w, h, bpc int) int64 {
	if bpc < 1 {
		bpc = 8
	}
	row := (int64(w)*4*int64(bpc)+7)/8 + 1
	return row * int64(h)
}
