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
// follows it. ok is false when there is no startxref or no /Size; pdfcpu then
// reads or rebuilds the table under its own object-count cap.
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
	if i := bytes.Index(readAt(rs, 0, 1024), []byte("%PDF-")); i > 0 {
		off += int64(i)
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
