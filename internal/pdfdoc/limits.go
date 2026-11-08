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
func objectsIn(rs io.ReadSeeker) int64 {
	size, _ := trailerSize(rs)
	return max(size, objectBound(rs))
}

// sizeOf is the length of rs, or 0 when it cannot seek.
func sizeOf(rs io.ReadSeeker) int64 {
	n, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0
	}
	return n
}

// objectBound is an upper bound on the objects with a header that pdfcpu can
// create from the file, from one pass through a fixed buffer: every "obj" token
// (not "endobj"), with no regard to the number before it. pdfcpu reads a header
// by its own rules (cleanObjProlog), so any reading of the numbers here can
// differ from it; a token count cannot undercount its table inserts. Objects
// packed in object streams have no header and are not counted: pdfcpu holds
// them to MaxObjectStreamTotalCount.
func objectBound(rs io.ReadSeeker) (n int64) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	const ov = 256 // longest look-behind or look-ahead of a match
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
		n += headersIn(b, lo, hi)
		if err != nil {
			return n
		}
		kept = copy(buf, b[len(b)-2*ov:])
	}
}

// headersIn counts the "obj" tokens, "endobj" excepted, starting in [lo, hi) of b.
func headersIn(b []byte, lo, hi int) (n int64) {
	for i := lo; ; {
		j := bytes.Index(b[i:], []byte("obj"))
		if j < 0 || i+j >= hi {
			return n
		}
		p := i + j
		i = p + 3
		if p < 3 || string(b[p-3:p]) != "end" {
			n++
		}
	}
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

// parsedBytes is the cap on the heap of parsed objects: lim's, else three
// quarters of the object table the reservation holds plus the decode slot. The
// table is reserved at 1536 bytes an object, but an object costs about 260 of
// them (measured, null filler) even when it holds nothing, so that quarter is
// already spent and a parsed object cannot have it as well.
func parsedBytes(lim limits.Limits, table int64) int64 {
	if lim.MaxParsedBytes > 0 {
		return lim.MaxParsedBytes
	}
	return table/4*3 + lim.TotalDecodeBytes()
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
