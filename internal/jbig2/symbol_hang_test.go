package jbig2

// Termination, which is a property no other test in this package has had to
// assert.
//
// Every earlier decode loop in this package is bounded by a HEADER FIELD: a
// generic region decodes exactly w*h pixels and stops. A symbol dictionary is
// bounded by SDNUMNEWSYMS only if every height class it codes makes progress
// towards that count, and nothing in the format requires one to. Combined with
// the MQ decoder's habit of yielding decisions forever past the end of the data
// -- which is not a bug but T.88 E.3.4, and is what keeps a truncated stream a
// decode error instead of a crash -- the iteration count of that loop is set by
// the coded data rather than by anything bounded.
//
// WHAT THIS TEST ASSERTS IS THE REFUSAL, NOT AN OBSERVED HANG, and the
// difference is worth stating. Removing the guard does not make the fixture
// below spin: its all-1s tail decodes to a negative height delta, which the
// positive-height check catches on the second iteration. Nothing chooses that
// sign -- it is where those context states happen to converge -- so it is not a
// bound, and the termination argument in decodeSymbolDict does not rest on it.
// The timeout here is a regression guard for the day some other input does
// converge the other way. The resource budget cannot cover this: it bounds
// pixels and bytes, and an empty height class allocates neither.

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// TestSymbolDictionaryWithNoSymbolsInAHeightClassTerminates builds the smallest
// stream that reaches the loop: a dictionary declaring one new symbol whose
// coded data is a height class delta followed by OOB, and then nothing.
//
// The decoder reads past the end from there, and what it reads is whatever the
// all-1s tail decodes to -- so this asserts termination, not a particular error.
func TestSymbolDictionaryWithNoSymbolsInAHeightClassTerminates(t *testing.T) {
	e := newEncoder()
	iadh := intEnc{e, newIntContexts()}
	iadw := intEnc{e, newIntContexts()}
	iadh.write(8, false)
	iadw.write(0, true) // OOB: the class ends having coded no symbol
	coded := e.flush()

	d := make([]byte, 0, 18+len(coded))
	d = binary.BigEndian.AppendUint16(d, 0)
	d = append(d, nominalATTemplate0...)
	d = binary.BigEndian.AppendUint32(d, 1)
	d = binary.BigEndian.AppendUint32(d, 1)
	d = append(d, coded...)

	syms := []*Bitmap{glyph(5, 7, 1)}
	text := buildTextRegion(textParams{w: 60, h: 40}, syms, []instance{{0, 10, 20}})
	s := symbolStream(60, 40, d, text)

	done := make(chan error, 1)
	go func() {
		_, err := DecodeEmbeddedStream(s)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("decoded a dictionary whose only height class carries no symbol")
		}
		if !strings.Contains(err.Error(), "containing no symbols") {
			t.Errorf("error = %v; want the empty height class to be what refuses it", err)
		}
	case <-time.After(10 * time.Second):
		// Deliberately not t.Fatal from this goroutine's sibling: the decode is
		// still running and will keep a core busy for the rest of the run. The
		// failure is worth reporting anyway, because the alternative is a test
		// binary that never exits.
		t.Fatalf("DecodeEmbeddedStream did not return within 10s on a %d-byte stream; the symbol "+
			"dictionary loop makes no progress on an empty height class", len(s))
	}
}

// The same shape reached through the byblos entry point, since that is where an
// archive meets it: one page of a document must not be able to stop a run.
func TestSymbolDictionaryHangIsNotReachableThroughPageSize(t *testing.T) {
	e := newEncoder()
	iadh := intEnc{e, newIntContexts()}
	iadw := intEnc{e, newIntContexts()}
	iadh.write(8, false)
	iadw.write(0, true)
	coded := e.flush()

	d := make([]byte, 0, 18+len(coded))
	d = binary.BigEndian.AppendUint16(d, 0)
	d = append(d, nominalATTemplate0...)
	d = binary.BigEndian.AppendUint32(d, 1)
	d = binary.BigEndian.AppendUint32(d, 1)
	d = append(d, coded...)

	syms := []*Bitmap{glyph(5, 7, 1)}
	text := buildTextRegion(textParams{w: 60, h: 40}, syms, []instance{{0, 10, 20}})
	s := symbolStream(60, 40, d, text)

	// PageSize reads headers only, so it must return whatever the dictionary
	// does -- it never enters the loop at all.
	done := make(chan struct{})
	go func() { _, _, _ = PageSize(s); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PageSize did not return within 10s; it is supposed to read headers and no coded data")
	}
}
