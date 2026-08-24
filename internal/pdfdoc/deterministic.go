package pdfdoc

// Deterministic pdfcpu output (byb-c53).
//
// pdfcpu v0.13.0's writer injects two per-run values on EVERY write, with no
// Configuration knob gating either: ensureInfoDict (info.go) stamps
// CreationDate/ModDate with time.Now() at second resolution plus a Producer
// line, and fileID (crypto.go) hashes time.Now() nanoseconds into the
// trailer's /ID. The same input written twice therefore yields two different
// files with two different hashes — which breaks deduplication,
// content-addressed storage and any integrity check that re-derives a
// document and compares (byblos is a library for an archive that stores what
// it produces).
//
// Determinism is the DEFAULT, not an option: nondeterminism here is never a
// feature anyone asks for, every caller of this package benefits, and an
// opt-in flag would leave the archive's default behaviour broken. The Producer
// stamp is a constant per pdfcpu version and needs no pin.
//
// The mechanism is a post-write in-place patch. pdfcpu offers no pin, so
// writePinned serializes to a buffer and overwrites the stamps there:
//
//   - Both date stamps are fixed-length plaintext (verified against v0.13.0:
//     types.DateString always renders 23 bytes for a 4-digit year, and the
//     Info dict is written as a top-level plaintext object — writeObjects
//     runs writeDocumentInfoDict after every object-stream phase has been
//     stopObjectStream'd, so the dates never land inside a compressed
//     stream). They are found by exact value: the writer stamped some second
//     in [from, to], so only a date equal to one of those candidate seconds
//     is touched — an input document's own dates elsewhere (annotation
//     /CreationDate, say) cannot match. Each stamp is replaced with the
//     input document's own date when it had one, else with pinnedDate.
//
//   - The /ID pair is plaintext hex in the trailer (or xref-stream) dict.
//     Each freshly minted 32-hex-digit value is replaced with the md5 of the
//     pinned buffer itself (ID digits zeroed first), so /ID remains what ISO
//     32000-1 14.4 wants — a fingerprint of the file — while being a pure
//     function of the bytes. When the input carried an /ID, pdfcpu keeps
//     element one and replaces only element two (ensureFileID), and so does
//     the patch; which case applies is captured from the context BEFORE the
//     write, because it cannot be recovered from the bytes (measured: one
//     write can mint two DIFFERENT fresh values, ensureFileID runs more than
//     once, so fresh elements need not be equal).
//
// Every edit is same-length, so no xref offset moves. Anything unexpected —
// a stamp that is not where it should be, a hex literal of the wrong shape,
// an /Encrypt dict (the /ID feeds the encryption key, and encrypted strings
// cannot match a date candidate anyway) — fails open: the bytes are left
// exactly as pdfcpu wrote them, trading determinism for never corrupting a
// document.

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"io"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pinnedDate is the CreationDate/ModDate written when the input document had
// none of its own: one documented constant instead of the wall clock. The
// value is arbitrary but load-bearing — changing it changes every produced
// document's bytes and hence its content hash.
const pinnedDate = "D:20000101000000+00'00'"

// infoDates returns ctx's Info-dictionary CreationDate and ModDate as raw
// date strings, "" for absent or unreadable. Callers must read these BEFORE
// the context is written: pdfcpu's ensureInfoDict overwrites both values in
// the live dict on every write.
func infoDates(ctx *model.Context) (creation, mod string) {
	if ctx.Info == nil {
		return "", ""
	}
	d, err := ctx.DereferenceDict(*ctx.Info)
	if err != nil || d == nil {
		return "", ""
	}
	return rawDate(ctx, d["CreationDate"]), rawDate(ctx, d["ModDate"])
}

func rawDate(ctx *model.Context, o types.Object) string {
	o, err := ctx.Dereference(o)
	if err != nil {
		return ""
	}
	switch v := o.(type) {
	case types.StringLiteral:
		if s, err := types.StringLiteralToString(v); err == nil {
			return s
		}
	case types.HexLiteral:
		if s, err := types.HexLiteralToString(v); err == nil {
			return s
		}
	}
	return ""
}

// pinDate normalizes raw to pdfcpu's fixed 23-byte D:YYYYMMDDHHMMSS±HH'MM'
// form (any valid PDF date has one, and it preserves the date's own UTC
// offset), falling back to pinnedDate when raw is absent, unparseable, or —
// out-of-range years — would not render at the stamp's length.
func pinDate(raw string) string {
	if raw != "" {
		if t, ok := types.DateTime(raw, true); ok {
			if s := types.DateString(t); len(s) == len(pinnedDate) {
				return s
			}
		}
	}
	return pinnedDate
}

// writePinned serializes ctx like api.WriteContext and pins pdfcpu's per-run
// stamps in the result. creation, mod and hadID describe the INPUT document
// (from infoDates and ctx.ID != nil, read before any write mutates them).
func writePinned(ctx *model.Context, w io.Writer, creation, mod string, hadID bool) error {
	var buf bytes.Buffer
	from := time.Now()
	if err := api.WriteContext(ctx, &buf); err != nil {
		return err
	}
	to := time.Now()
	b := buf.Bytes()
	pinStamps(b, from, to, pinDate(creation), pinDate(mod), hadID)
	_, err := w.Write(b)
	return err
}

// pinStamps patches pdfcpu's write-time stamps in buf in place. [from, to]
// brackets the api.WriteContext call, so the date the writer stamped is
// types.DateString of one of those seconds.
func pinStamps(buf []byte, from, to time.Time, creation, mod string, hadID bool) {
	for sec := from.Unix(); sec <= to.Unix(); sec++ {
		cand := types.DateString(time.Unix(sec, 0))
		pinDateKey(buf, "/CreationDate", cand, creation)
		pinDateKey(buf, "/ModDate", cand, mod)
	}
	pinFileID(buf, hadID)
}

// pinDateKey overwrites every occurrence of key valued exactly (stamped)
// with pin, which must be the same length — otherwise it does nothing.
func pinDateKey(buf []byte, key, stamped, pin string) {
	if len(pin) != len(stamped) {
		return
	}
	pat := []byte(key + "(" + stamped + ")")
	for off := 0; ; {
		i := bytes.Index(buf[off:], pat)
		if i < 0 {
			return
		}
		copy(buf[off+i+len(key)+1:], pin)
		off += i + len(pat)
	}
}

// pinFileID replaces the freshly minted /ID value(s) with the md5 of the
// buffer itself. It patches the trailing /ID[<..><..>] only, and only when
// the fresh elements have fileID's exact shape (32 hex digits): both
// elements when the input document carried no /ID (pdfcpu mints both), the
// second alone when it did (pdfcpu keeps the input's first, which is already
// deterministic).
func pinFileID(buf []byte, hadID bool) {
	if bytes.Contains(buf, []byte("/Encrypt")) {
		return // the /ID feeds the encryption key; leave it alone
	}
	i := bytes.LastIndex(buf, []byte("/ID["))
	if i < 0 {
		return
	}
	p := i + len("/ID[")
	h1s, h1e, ok := hexSpan(buf, &p)
	if !ok {
		return
	}
	h2s, h2e, ok := hexSpan(buf, &p)
	if !ok {
		return
	}
	if p = skipWS(buf, p); p >= len(buf) || buf[p] != ']' {
		return
	}
	if h2e-h2s != 2*md5.Size || !isHexDigits(buf[h2s:h2e]) {
		return
	}
	if !hadID && (h1e-h1s != 2*md5.Size || !isHexDigits(buf[h1s:h1e])) {
		return
	}
	fill(buf[h2s:h2e], '0')
	if !hadID {
		fill(buf[h1s:h1e], '0')
	}
	sum := md5.Sum(buf)
	id := strings.ToUpper(hex.EncodeToString(sum[:]))
	copy(buf[h2s:h2e], id)
	if !hadID {
		copy(buf[h1s:h1e], id)
	}
}

// hexSpan parses a <...> hex literal at buf[*p] after optional whitespace and
// returns the span of the digits between the brackets, advancing *p past it.
func hexSpan(buf []byte, p *int) (start, end int, ok bool) {
	i := skipWS(buf, *p)
	if i >= len(buf) || buf[i] != '<' {
		return 0, 0, false
	}
	start = i + 1
	end = bytes.IndexByte(buf[start:], '>')
	if end < 0 {
		return 0, 0, false
	}
	end += start
	*p = end + 1
	return start, end, true
}

// skipWS advances past ISO 32000-1 white-space characters.
func skipWS(buf []byte, i int) int {
	for i < len(buf) && strings.IndexByte(" \t\r\n\f\x00", buf[i]) >= 0 {
		i++
	}
	return i
}

func isHexDigits(b []byte) bool {
	for _, c := range b {
		if !('0' <= c && c <= '9' || 'A' <= c && c <= 'F' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func fill(b []byte, c byte) {
	for i := range b {
		b[i] = c
	}
}
