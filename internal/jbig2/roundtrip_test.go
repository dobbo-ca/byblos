package jbig2

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

var updateGoldens = flag.Bool("update", false,
	"regenerate testdata/jbig2 goldens; requires jbig2dec and verifies the round trip first")

func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "jbig2", name+".jb2")
}

// decodePBM parses a binary PBM (P4): the magic, width and height as
// whitespace-separated tokens, a single whitespace byte, then MSB-first packed
// rows with 1 = black -- the same packing and polarity as Bitmap.
func decodePBM(raw []byte) (*Bitmap, error) {
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

	i := 0
	// nextToken skips runs of whitespace and '#' comments, then returns the
	// following token. The PBM header ends at the single whitespace byte after
	// the height, which the caller consumes.
	nextToken := func() (string, error) {
		for i < len(raw) {
			if isSpace(raw[i]) {
				i++
				continue
			}
			if raw[i] == '#' {
				for i < len(raw) && raw[i] != '\n' {
					i++
				}
				continue
			}
			break
		}
		start := i
		for i < len(raw) && !isSpace(raw[i]) && raw[i] != '#' {
			i++
		}
		if start == i {
			return "", fmt.Errorf("pbm: unexpected end of header at offset %d", start)
		}
		return string(raw[start:i]), nil
	}

	magic, err := nextToken()
	if err != nil {
		return nil, err
	}
	if magic != "P4" {
		return nil, fmt.Errorf("pbm: magic = %q; want P4", magic)
	}
	var dims [2]int
	for d := range dims {
		tok, err := nextToken()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return nil, fmt.Errorf("pbm: dimension %q: %w", tok, err)
		}
		dims[d] = n
	}
	if i >= len(raw) || !isSpace(raw[i]) {
		return nil, fmt.Errorf("pbm: header is not terminated by whitespace at offset %d", i)
	}
	i++ // exactly one whitespace byte separates the header from the raster

	w, h := dims[0], dims[1]
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("pbm: bad dimensions %dx%d", w, h)
	}
	b := NewBitmap(w, h)
	if len(raw[i:]) < len(b.Pix) {
		return nil, fmt.Errorf("pbm: body is %d bytes; want %d", len(raw[i:]), len(b.Pix))
	}
	copy(b.Pix, raw[i:])
	return b, nil
}

// decodeWithJBIG2Dec runs the external decoder over an embedded-organization
// stream and returns the bitmap it produced.
func decodeWithJBIG2Dec(t *testing.T, bin string, stream []byte) *Bitmap {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.jb2")
	out := filepath.Join(dir, "out.pbm")
	if err := os.WriteFile(in, stream, 0o644); err != nil {
		t.Fatalf("writing %s: %v", in, err)
	}
	cmd := exec.Command(bin, "-e", "-t", "pbm", "-o", out, in)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jbig2dec failed: %v\n%s", err, combined)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading %s: %v", out, err)
	}
	got, err := decodePBM(raw)
	if err != nil {
		t.Fatalf("parsing jbig2dec output: %v", err)
	}
	return got
}

func assertBitmapsIdentical(t *testing.T, name string, got, want *Bitmap) {
	t.Helper()
	if got.W != want.W || got.H != want.H {
		t.Errorf("%s: decoded %dx%d; want %dx%d", name, got.W, got.H, want.W, want.H)
		return
	}
	var diff int
	var firstX, firstY int
	for y := 0; y < want.H; y++ {
		for x := 0; x < want.W; x++ {
			if got.Get(x, y) != want.Get(x, y) {
				if diff == 0 {
					firstX, firstY = x, y
				}
				diff++
			}
		}
	}
	if diff != 0 {
		t.Errorf("%s: round trip is NOT lossless -- %d of %d pixels differ, first at (%d,%d)",
			name, diff, want.W*want.H, firstX, firstY)
	}
}

// TestRoundTripBitIdentical is the byb-b2 acceptance criterion. Encoding is
// lossless, so this is an exact check: every pixel must survive.
func TestRoundTripBitIdentical(t *testing.T) {
	bin, err := exec.LookPath("jbig2dec")
	if err != nil {
		t.Skipf("jbig2dec not installed (brew install jbig2dec): %v", err)
	}
	for name, want := range fixtureBitmaps() {
		t.Run(name, func(t *testing.T) {
			want.MaskPadding()
			stream, err := EmbeddedStream(want)
			if err != nil {
				t.Fatalf("EmbeddedStream() error = %v", err)
			}
			assertBitmapsIdentical(t, name, decodeWithJBIG2Dec(t, bin, stream), want)
		})
	}
}

// TestEncoderGoldens keeps CI honest on a machine with no decoder installed: it
// pins the exact bytes the encoder produces for every fixture. A golden is only
// ever written by -update, which refuses to run without a successful live round
// trip, so every committed golden is a stream jbig2dec confirmed lossless.
func TestEncoderGoldens(t *testing.T) {
	if *updateGoldens {
		bin, err := exec.LookPath("jbig2dec")
		if err != nil {
			t.Fatalf("-update requires jbig2dec: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenPath("x")), 0o755); err != nil {
			t.Fatalf("creating golden directory: %v", err)
		}
		for name, b := range fixtureBitmaps() {
			// A sub-test per fixture so t.Failed() reflects only this fixture:
			// a golden is written if and only if its own round trip passed.
			t.Run(name, func(t *testing.T) {
				b.MaskPadding()
				stream, err := EmbeddedStream(b)
				if err != nil {
					t.Fatalf("EmbeddedStream() error = %v", err)
				}
				assertBitmapsIdentical(t, name, decodeWithJBIG2Dec(t, bin, stream), b)
				if t.Failed() {
					t.Fatal("refusing to write a golden that does not round-trip")
				}
				if err := os.WriteFile(goldenPath(name), stream, 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				t.Logf("wrote %s (%d bytes)", goldenPath(name), len(stream))
			})
		}
		return
	}

	for name, b := range fixtureBitmaps() {
		b.MaskPadding()
		want, err := os.ReadFile(goldenPath(name))
		if err != nil {
			t.Errorf("%s: golden missing (regenerate with: go test ./internal/jbig2/ -update): %v", name, err)
			continue
		}
		got, err := EmbeddedStream(b)
		if err != nil {
			t.Errorf("%s: EmbeddedStream() error = %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: encoder output changed: %d bytes now, %d in the golden. "+
				"If this is intentional, re-run with -update on a machine with jbig2dec.",
				name, len(got), len(want))
		}
	}
}
