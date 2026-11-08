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
// reservation come before pdfcpu allocates anything. ok is false when no /Size
// is found in the tail; pdfcpu then reads or rebuilds the table under its own
// object-count cap.
func trailerSize(rs io.ReadSeeker) (size int64, ok bool) {
	const tail = 64 << 10
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, false
	}
	start := max(end-tail, 0)
	if _, err := rs.Seek(start, io.SeekStart); err != nil {
		return 0, false
	}
	buf := make([]byte, end-start)
	if _, err := io.ReadFull(rs, buf); err != nil {
		return 0, false
	}
	sx := bytes.LastIndex(buf, []byte("startxref"))
	if sx < 0 {
		return 0, false
	}
	if t := bytes.LastIndex(buf[:sx], []byte("trailer")); t >= 0 {
		return sizeIn(buf[t:sx])
	}
	// Cross-reference stream: /Size is in the dictionary at the startxref offset.
	off, ok := intAfter(buf[sx+len("startxref"):])
	if !ok {
		return 0, false
	}
	if off >= start { // inside the tail already
		return sizeIn(buf[off-start:])
	}
	if _, err := rs.Seek(off, io.SeekStart); err != nil {
		return 0, false
	}
	head := make([]byte, 4096)
	n, _ := io.ReadFull(rs, head)
	return sizeIn(head[:n])
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

// imageDecodeBytes is an upper bound on a declared image's decoded size:
// four components at the declared depth, plus a predictor tag byte per row.
func imageDecodeBytes(w, h, bpc int) int64 {
	if bpc < 1 {
		bpc = 8
	}
	row := (int64(w)*4*int64(bpc)+7)/8 + 1
	return row * int64(h)
}
