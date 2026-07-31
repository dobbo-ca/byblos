package jbig2

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestBeatsMMROnSpecFigure is the tool-free half of the comparison. T.88 Annex
// H.1 encodes Figure H.6 twice: as MMR in segment 4 (26 bytes of region data)
// and as an arithmetic generic region in segment 11 (9 bytes). Both numbers
// come from the standard, so this assertion needs nothing installed.
func TestBeatsMMROnSpecFigure(t *testing.T) {
	const specMMRBytes = 26
	got := len(EncodeGenericRegion(figureH6(), true))
	if got != 9 {
		t.Fatalf("Figure H.6 encoded to %d bytes; want 9 (T.88 Annex H.1 segment 11)", got)
	}
	if got >= specMMRBytes {
		t.Errorf("generic region (%d bytes) did not beat the spec's own MMR encoding (%d bytes)",
			got, specMMRBytes)
	}
	t.Logf("Figure H.6: generic region %d bytes vs MMR %d bytes (%.2fx)",
		got, specMMRBytes, float64(specMMRBytes)/float64(got))
}

// writePBM writes b as a binary PBM, which is what ImageMagick reads.
func writePBM(path string, b *Bitmap) error {
	out := append([]byte(fmt.Sprintf("P4\n%d %d\n", b.W, b.H)), b.Pix...)
	return os.WriteFile(path, out, 0o644)
}

// stripByteCountsRE matches tiffdump's single-strip form, `... 1<139>`. A
// multi-strip TIFF prints `... 3<100 200 300>`, which this deliberately does
// NOT match: g4PayloadBytes then fails loudly rather than silently reporting
// the first strip and inflating the ratio.
var stripByteCountsRE = regexp.MustCompile(`StripByteCounts \(279\)[^<]*<(\d+)>`)

// lookImageMagick finds the ImageMagick CLI. ImageMagick 7 installs it as
// `magick`; ImageMagick 6, which is still what Debian and Ubuntu package,
// installs it as `convert`. Both take the arguments used below unchanged.
//
// Only the `magick` (IM7) path has been measured. If `convert` produces a
// materially different StripByteCounts, log both and raise it -- do not relax
// the bound to accommodate it.
func lookImageMagick() (string, error) {
	for _, name := range []string{"magick", "convert"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("neither magick nor convert is installed (brew install imagemagick)")
}

// g4PayloadBytes compresses b as a CCITT Group 4 TIFF and returns the size of
// the compressed strip, excluding TIFF container overhead.
func g4PayloadBytes(t *testing.T, magick, tiffdump string, b *Bitmap) int {
	t.Helper()
	dir := t.TempDir()
	pbm := filepath.Join(dir, "in.pbm")
	tif := filepath.Join(dir, "out.tif")
	if err := writePBM(pbm, b); err != nil {
		t.Fatalf("writing %s: %v", pbm, err)
	}
	if out, err := exec.Command(magick, pbm, "-compress", "Group4", tif).CombinedOutput(); err != nil {
		t.Fatalf("magick failed: %v\n%s", err, out)
	}
	out, err := exec.Command(tiffdump, tif).CombinedOutput()
	if err != nil {
		t.Fatalf("tiffdump failed: %v\n%s", err, out)
	}
	m := stripByteCountsRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no single-strip StripByteCounts in tiffdump output:\n%s", out)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parsing StripByteCounts %q: %v", m[1], err)
	}
	return n
}

// TestBeatsCCITTG4OnCorpus is the byb-b2 compression acceptance criterion.
//
// Every fixture must beat G4 at all. The text page carries a much tighter bound
// because it is the case that matters for Kleio and the case the design spec
// makes a promise about. Measured while this plan was written, on the fixtures
// below: text 14.10x (jbig2 870 B, G4 12268 B), empty 9.25x, full 3.00x,
// border 2.44x, noise 2.11x.
func TestBeatsCCITTG4OnCorpus(t *testing.T) {
	magick, err := lookImageMagick()
	if err != nil {
		t.Skipf("%v", err)
	}
	tiffdump, err := exec.LookPath("tiffdump")
	if err != nil {
		t.Skipf("tiffdump not installed (brew install libtiff): %v", err)
	}

	// Fixtures too small for the comparison to mean anything: at these sizes the
	// MQ flush terminator and the G4 EOFB dominate the measurement.
	skip := map[string]bool{"single": true, "odd": true, "row": true, "column": true}

	for name, b := range fixtureBitmaps() {
		if skip[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			b.MaskPadding()
			ours := len(EncodeGenericRegion(b, true))
			g4 := g4PayloadBytes(t, magick, tiffdump, b)
			ratio := float64(g4) / float64(ours)
			t.Logf("%s (%dx%d): jbig2 %d bytes, ccitt-g4 %d bytes, %.2fx", name, b.W, b.H, ours, g4, ratio)
			if ratio <= 1.0 {
				t.Errorf("%s: jbig2 generic region (%d bytes) did not beat CCITT G4 (%d bytes)",
					name, ours, g4)
			}
			// 14.10x was measured on this exact fixture. The bound is set at 8x:
			// low enough that ordinary encoder churn cannot trip it, high enough
			// that passing it actually establishes the design spec section 5
			// claim of "roughly 2-4x better compression than CCITT G4" instead of
			// sitting below it. A bound of 1.5x here would be unfalsifiable.
			if name == "text" && ratio < 8 {
				t.Errorf("text page: only %.2fx better than CCITT G4; expected at least 8x "+
					"(14.10x was measured while this plan was written, jbig2 870 B vs G4 12268 B). "+
					"Do not widen this bound -- work through the order in Task 10 Step 2 first, "+
					"and re-measure against a real scan before relaxing anything.", ratio)
			}
		})
	}
}
