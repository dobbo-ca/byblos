package jbig2

import (
	"bytes"
	"testing"
)

// Table E.1 has exactly 47 rows. Index 46 is the fixed 0.5 estimate: it is its
// own NMPS and NLPS, so once reached the state never leaves.
func TestQeTableShape(t *testing.T) {
	if len(qeTable) != 47 {
		t.Fatalf("len(qeTable) = %d; want 47", len(qeTable))
	}
	for i, e := range qeTable {
		if e.qe == 0 || e.qe > 0x5601 {
			t.Errorf("qeTable[%d].qe = %#04x; want a value in (0, 0x5601]", i, e.qe)
		}
		if int(e.nmps) >= len(qeTable) {
			t.Errorf("qeTable[%d].nmps = %d; out of range", i, e.nmps)
		}
		if int(e.nlps) >= len(qeTable) {
			t.Errorf("qeTable[%d].nlps = %d; out of range", i, e.nlps)
		}
		if e.swch > 1 {
			t.Errorf("qeTable[%d].swch = %d; want 0 or 1", i, e.swch)
		}
	}
	// SWITCH is set on exactly the three rows T.88 marks: 0, 6 and 14.
	for _, i := range []int{0, 6, 14} {
		if qeTable[i].swch != 1 {
			t.Errorf("qeTable[%d].swch = 0; want 1", i)
		}
	}
	if qeTable[46].nmps != 46 || qeTable[46].nlps != 46 {
		t.Errorf("qeTable[46] transitions = (%d,%d); want (46,46)", qeTable[46].nmps, qeTable[46].nlps)
	}
}

// TestMQConformanceVector is the T.88 Annex H.2 test sequence: 32 bytes of
// decisions (256 decisions, MSB first) coded through a single context starting
// at I=0, MPS=0, producing exactly 30 bytes.
//
// On failure, do NOT adjust the expected bytes. Instrument encode() to log
// (I, MPS, Qe, A, C, CT, B) after every decision and diff against Table H.1
// (T.88 doc p. 143-146), which traces all 257 events. The first row that
// differs is the bug.
func TestMQConformanceVector(t *testing.T) {
	in := []byte{
		0x00, 0x02, 0x00, 0x51, 0x00, 0x00, 0x00, 0xC0,
		0x03, 0x52, 0x87, 0x2A, 0xAA, 0xAA, 0xAA, 0xAA,
		0x82, 0xC0, 0x20, 0x00, 0xFC, 0xD7, 0x9E, 0xF6,
		0xBF, 0x7F, 0xED, 0x90, 0x4F, 0x46, 0xA3, 0xBF,
	}
	want := []byte{
		0x84, 0xC7, 0x3B, 0xFC, 0xE1, 0xA1, 0x43, 0x04,
		0x02, 0x20, 0x00, 0x00, 0x41, 0x0D, 0xBB, 0x86,
		0xF4, 0x31, 0x7F, 0xFF, 0x88, 0xFF, 0x37, 0x47,
		0x1A, 0xDB, 0x6A, 0xDF, 0xFF, 0xAC,
	}

	cx := make(contexts, 1)
	e := newEncoder()
	for _, b := range in {
		for i := 7; i >= 0; i-- {
			e.encode(cx, 0, int(b>>uint(i))&1)
		}
	}
	got := e.flush()

	if !bytes.Equal(got, want) {
		t.Fatalf("MQ output mismatch\ngot  (%d): % 02X\nwant (%d): % 02X",
			len(got), got, len(want), want)
	}
}

// Every MQ stream terminates with the 0xFF 0xAC marker written by FLUSH.
func TestMQFlushTerminator(t *testing.T) {
	cx := make(contexts, 1)
	e := newEncoder()
	for i := 0; i < 40; i++ {
		e.encode(cx, 0, i&1)
	}
	got := e.flush()
	if len(got) < 2 || got[len(got)-2] != 0xFF || got[len(got)-1] != 0xAC {
		t.Fatalf("stream tail = % 02X; want ... FF AC", got)
	}
}

// Byte stuffing after 0xFF emits only 7 bits, so no 0xFF byte may ever be
// followed by a byte above 0x8F inside the coded data. Violating this would
// create a marker sequence a decoder must treat as terminating the stream.
func TestMQNoMarkerSequenceInData(t *testing.T) {
	cx := make(contexts, 1<<8)
	e := newEncoder()
	s := uint32(1)
	for i := 0; i < 20000; i++ {
		s = s*1664525 + 1013904223
		e.encode(cx, int(s>>24)&0xFF, int(s>>16)&1)
	}
	got := e.flush()
	body := got[:len(got)-2] // exclude the FF AC terminator
	for i := 0; i+1 < len(body); i++ {
		if body[i] == 0xFF && body[i+1] > 0x8F {
			t.Fatalf("marker sequence FF %02X at offset %d", body[i+1], i)
		}
	}
}

// An encoder that has coded nothing still flushes a well-formed terminator.
func TestMQFlushWithNoDecisions(t *testing.T) {
	got := newEncoder().flush()
	if len(got) == 0 {
		t.Fatal("flush() with no decisions returned no bytes")
	}
	if got[len(got)-2] != 0xFF || got[len(got)-1] != 0xAC {
		t.Fatalf("flush() = % 02X; want a stream ending FF AC", got)
	}
}
