package render

// byb-8b9.7's acceptance harness: over a NAMED sample of documents whose
// page 1 embeds no font program, render page 1 through byblos (with the 4f
// substitute faces) and compare against pdftoppm at 400px -- the size the
// actual consumer uses. POPULATION BEFORE PERCENTAGE: the test logs the
// population it measured and the agreement distribution; the recorded numbers
// go in the bd comment on byb-8b9.7.
//
// THE SAMPLE, stated exactly: every 7th row of $BYBLOS_SAMPLE/manifest.tsv
// (the same 811-document selection scan.sh used for the byb-8b9.6 decision
// measurement), kept when pdffonts reports >= 1 page-1 font and NONE
// embedded. Documents whose page carries /Rotate are excluded and counted:
// render.Page does not apply page rotation yet, so the comparison would
// measure rotation, not fonts.
//
// GATED ON BYBLOS_SAMPLE and slow (minutes): build once and run the binary
// directly, per the harness_sample_test.go convention:
//
//	go test -c -o render.harness ./internal/render
//	BYBLOS_SAMPLE=~/work/dobbo-ca/.byblos-sample ./render.harness \
//	  -test.run TestSubstituteSampleAgainstPdftoppm -test.v -test.timeout 60m
import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// pdffontsAllNonEmbedded reports (fonts>0, none embedded) for page 1.
func pdffontsAllNonEmbedded(bin, path string) (bool, error) {
	out, err := exec.Command(bin, "-f", "1", "-l", "1", path).Output()
	if err != nil {
		return false, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) <= 2 {
		return false, nil
	}
	n := 0
	for _, ln := range lines[2:] {
		f := strings.Fields(ln)
		if len(f) < 5 {
			continue
		}
		n++
		if f[len(f)-5] != "no" { // emb is the 5th field from the end
			return false, nil
		}
	}
	return n > 0, nil
}

// mismatchOverIntersection compares the two rasters over their overlapping
// pixels (canvas rounding may differ by a pixel; more than 2 refuses -- a
// real placement disagreement, not rounding).
//
// BOTH RASTERS ARE BOX-DOWNSAMPLED 2x FIRST, and that choice was forced by
// mutation-testing the instrument: under a strict 400px per-pixel diff,
// STARVING the substitution (drawing no glyph at all) scored slightly BETTER
// than drawing Liberation glyphs (median 7.3% vs 8.0% across the same
// population). The probe on govdocs1/200614.pdf shows why: at 400px a body-
// text stroke is 1-2 device pixels, poppler renders it antialiased mid-grey
// and byblos hard black-on-white, so EVERY text pixel breaks a 40/255
// channel threshold in both arms and the metric cannot tell any glyph from
// no glyph. Averaging 2x2 blocks equalises the antialiasing (byblos's full-
// black half-covered strokes and poppler's grey average meet) and lands the
// comparison at the 200px scale byb-8b9.6 measured as still shape-
// discriminating (box glyphs scored 14.8% there, obviously wrong). Under
// this metric the starved arm scores decisively worse than the substituted
// arm on the same documents, which is what makes the reported agreement mean
// something; the byb-8b9.7 bd comment records both arms.
func mismatchOverIntersection(a *image.RGBA, b image.Image) (float64, bool) {
	ab, bb := a.Bounds(), b.Bounds()
	if abs(ab.Dx()-bb.Dx()) > 2 || abs(ab.Dy()-bb.Dy()) > 2 {
		return 0, false
	}
	w, h := min(ab.Dx(), bb.Dx())/2, min(ab.Dy(), bb.Dy())/2
	if w == 0 || h == 0 {
		return 0, false
	}
	avg := func(img image.Image, minX, minY, x, y int) [3]int {
		var s [3]uint32
		for dy := 0; dy < 2; dy++ {
			for dx := 0; dx < 2; dx++ {
				r, g, bl, _ := img.At(minX+2*x+dx, minY+2*y+dy).RGBA()
				s[0] += r >> 8
				s[1] += g >> 8
				s[2] += bl >> 8
			}
		}
		return [3]int{int(s[0] / 4), int(s[1] / 4), int(s[2] / 4)}
	}
	bad := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pa := avg(a, ab.Min.X, ab.Min.Y, x, y)
			pb := avg(b, bb.Min.X, bb.Min.Y, x, y)
			for i := 0; i < 3; i++ {
				if d := pa[i] - pb[i]; d > 40 || d < -40 {
					bad++
					break
				}
			}
		}
	}
	return float64(bad) / float64(w*h), true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

type sampleResult struct {
	path string
	frac float64
	note string // non-empty: excluded from the agreement stats, with why
}

func measureOne(pdftoppm string, path string, dir string, i int) sampleResult {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sampleResult{path, 0, "unreadable: " + err.Error()}
	}
	d, err := pdfdoc.Open(bytes.NewReader(raw))
	if err != nil {
		return sampleResult{path, 0, "pdfdoc.Open: " + err.Error()}
	}
	p, err := d.Page(1)
	if err != nil {
		return sampleResult{path, 0, "Page(1): " + err.Error()}
	}
	if p.Rotate%360 != 0 {
		return sampleResult{path, 0, "rotated page (render.Page does not rotate yet)"}
	}
	box := content.Box{LLX: p.CropBox.LLX, LLY: p.CropBox.LLY, URX: p.CropBox.URX, URY: p.CropBox.URY}
	long := box.URX - box.LLX
	if h := box.URY - box.LLY; h > long {
		long = h
	}
	if !(long > 0) {
		return sampleResult{path, 0, "degenerate crop box"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	got, err := Page(ctx, p.Content, box, 400/long, pdfdocImages(d, p), pdfdocFonts(d, p))
	if err != nil {
		return sampleResult{path, 0, "render.Page: " + err.Error()}
	}
	// Zero-padded and hyphen-globbed: a bare "o1*" glob also matches o12's
	// output when workers overlap, which read as "pdftoppm wrote 2 pages".
	prefix := filepath.Join(dir, fmt.Sprintf("o%04d", i))
	out, err := exec.Command(pdftoppm, "-f", "1", "-l", "1", "-scale-to", "400", "-png", path, prefix).CombinedOutput()
	if err != nil {
		return sampleResult{path, 0, fmt.Sprintf("pdftoppm: %v: %s", err, out)}
	}
	matches, _ := filepath.Glob(prefix + "-*.png")
	if len(matches) != 1 {
		return sampleResult{path, 0, fmt.Sprintf("pdftoppm wrote %d pages", len(matches))}
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return sampleResult{path, 0, err.Error()}
	}
	oracle, err := png.Decode(f)
	f.Close()
	os.Remove(matches[0])
	if err != nil {
		return sampleResult{path, 0, "png: " + err.Error()}
	}
	frac, ok := mismatchOverIntersection(got, oracle)
	if !ok {
		return sampleResult{path, 0, fmt.Sprintf("size mismatch: byblos %v vs poppler %v", got.Bounds(), oracle.Bounds())}
	}
	return sampleResult{path, frac, ""}
}

func TestSubstituteSampleAgainstPdftoppm(t *testing.T) {
	sample := os.Getenv("BYBLOS_SAMPLE")
	if sample == "" {
		t.Skip("BYBLOS_SAMPLE not set")
	}
	pdffonts, err := exec.LookPath("pdffonts")
	if err != nil {
		t.Skipf("pdffonts not on PATH: %v", err)
	}
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skipf("pdftoppm not on PATH: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(sample, "manifest.tsv"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var paths []string
	for i, ln := range strings.Split(string(manifest), "\n") {
		if i%7 != 0 || ln == "" { // every 7th row, scan.sh's NR%7==1
			continue
		}
		p := strings.SplitN(ln, "\t", 2)[0]
		if !filepath.IsAbs(p) {
			p = filepath.Join(sample, p)
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	t.Logf("sample: %d documents (every 7th manifest row)", len(paths))

	// Population: page-1 fonts exist and NONE are embedded.
	var population []string
	for _, p := range paths {
		if all, err := pdffontsAllNonEmbedded(pdffonts, p); err == nil && all {
			population = append(population, p)
		}
	}
	t.Logf("population: %d of %d documents have >=1 page-1 font and embed no font program", len(population), len(paths))
	if len(population) == 0 {
		t.Fatal("empty population; the harness measured nothing")
	}

	dir := t.TempDir()
	results := make([]sampleResult, len(population))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, p := range population {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = measureOne(pdftoppm, p, dir, i)
		}(i, p)
	}
	wg.Wait()

	var fracs []float64
	excluded := map[string]int{}
	for _, r := range results {
		if r.note != "" {
			key := strings.SplitN(r.note, ":", 2)[0]
			excluded[key]++
			t.Logf("EXCLUDED %s: %s", r.path, r.note)
			continue
		}
		fracs = append(fracs, r.frac)
		t.Logf("%s\t%.4f", r.path, r.frac)
	}
	sort.Float64s(fracs)
	if len(fracs) == 0 {
		t.Fatalf("no document rendered end to end; exclusions: %v", excluded)
	}
	count := func(tol float64) int {
		n := 0
		for _, f := range fracs {
			if f <= tol {
				n++
			}
		}
		return n
	}
	t.Logf("compared: %d documents; excluded: %v", len(fracs), excluded)
	t.Logf("median mismatch: %.2f%%; p90: %.2f%%", 100*fracs[len(fracs)/2], 100*fracs[len(fracs)*9/10])
	for _, tol := range []float64{0.05, 0.10, 0.15, 0.20} {
		t.Logf("agree at <=%.0f%% pixel mismatch: %d/%d (%.1f%%)",
			tol*100, count(tol), len(fracs), 100*float64(count(tol))/float64(len(fracs)))
	}
}
