package byblos

// Stencil-wash measurement for byb-oxf: for every page held out of extraction
// ONLY by opaqueCover's /ImageMask clause, ask poppler whether the wash beneath
// the stencil is actually visible.
//
// byb-b1.8 decided a stencil cannot be called an opaque cover, because it paints
// only through its 1 bits and whatever lies under the 0 bits still shows. The
// reasoning is sound and this probe does not question it. It prices it: on the
// 66 measured pages, is there anything under those 0 bits to see?
//
// THIS IS NOT byb-e04's QUESTION AND THE ADJUDICATOR DOES NOT ANSWER IT.
// poppler_adjudicate_test.go asks "is there ink OUTSIDE the raster byblos would
// return", which is a tolerance question. The stencil pages need "is there ink
// INSIDE the box that the raster's own bits do not carry", and the two counts
// share no code beyond the plumbing. The PGM reader, the pdftoppm call, the
// rotation mapping and the frame assertion are reused verbatim through
// pageFrame; the counting is new.
//
// It skips unless BYBLOS_WASH_TSV is set, so `go test ./...` is unaffected.
//
//	BYBLOS_WASH_TSV   the clause probe's TSV; rows are filtered to clause C,
//	                  vector-paint, held-only
//	BYBLOS_WASH_OUT   TSV to write
//	BYBLOS_WASH_DPI   render resolution, default 300
//	BYBLOS_WASH_GUARD erosion radius in RASTER CELLS, default 2
//	BYBLOS_WASH_INVERT set to 1 to flip the stencil's polarity -- the control
//	BYBLOS_JOBS       workers, default 6
//
// THE ACCOUNTING. Every pixel poppler paints is put in exactly one of four
// buckets, and only two of them are losses:
//
//	deep-ink   the raster's own bits, eroded by the guard. CARRIED.
//	edge       within the guard of a bit either way. AMBIGUOUS, not counted:
//	           a 1696x2200 stencil and a 2550x3300 render do not share a grid,
//	           so a glyph edge is a resampling artefact, not evidence.
//	clear      inside the box, guard-distance from every bit. LOST if inked.
//	outside    beyond the box entirely. LOST if inked -- this is byb-e04's
//	           band, kept because clause C short-circuits before the geometry
//	           test, so no one has ever checked whether these washes also
//	           escape the raster.
//
// THE CONTROLS ARE NOT OPTIONAL AND THEY ARE FREE.
//
//   - deep_dark/deep is the polarity and mapping check. It compares two
//     INDEPENDENT sources -- byblos's decode of the stencil against poppler's
//     render of the page -- so it catches a /Decode inversion, a transposed
//     axis and an off-by-a-rotation at once. Near 1.0 means the map is right.
//     Near 0.0 means it is inverted, and every clear count is then measuring
//     the stencil's own ink.
//   - BYBLOS_WASH_INVERT=1 flips the decoded bits. Under it, "clear" becomes
//     the stencil's own territory and clear_ink MUST go large. A run whose
//     inverted counts are also zero is measuring nothing, whatever the
//     uninverted run said.
//
// deep_mean is a third reading and it prices a DIFFERENT loss. byblos hands back
// the stencil's bits, never the fill colour they were painted in, so a stencil
// issued under a non-black fill is flattened by extraction whatever the wash
// under it does. The walk does not record that colour -- Placement carries no
// Color -- so it is read off the render instead: a mean near 0 over the eroded
// ink is a black stencil, and anything higher is a page that loses colour even
// when its wash is invisible.
//
// Colour is deliberately read from the pixels and never from the content
// stream. content.Color is unresolved by design (walk.go) and `1 1 1 scn` is
// white in an ICCBased RGB space and nearly black in a three-ink DeviceN -- the
// wash on the very file this bead is about writes exactly that. The fill column
// here is a diagnostic; the render is the decision.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// offWhiteThreshold is a second, laxer ink threshold. inkThreshold (250) is
// what byb-e04 called a visible mark; a wash can be a pale tint that no reader
// would call ink but that a lossless extractor still drops, so both are
// reported and the decision reads inkThreshold.
const offWhiteThreshold = 254

// pageFrame is the mapping between PDF page coordinates and the pixel grid
// pdftoppm renders, including the /Rotate poppler applies and byblos does not
// (extract.go:231-233).
//
// It is factored out of poppler_adjudicate_test.go's adjudicate rather than
// copied. byb-e04's first reading was wrong by exactly this mapping -- three of
// four pages it called content-losing were a /Rotate artefact -- so a second
// copy of it drifting from the first is the specific instrument failure this
// bead was warned about.
type pageFrame struct {
	sc     float64 // pixels per point
	rot    int
	disp   func(x, y float64) (dx, dy float64) // PDF space -> display points, y down
	undisp func(dx, dy float64) (x, y float64) // and back
}

// newPageFrame builds the mapping for p and asserts the render agrees with it.
// A non-nil error means the pixels and the page box are in different frames and
// every count taken against them is meaningless: a rotation, a CropBox poppler
// disagrees about, or the wrong page rendered.
func newPageFrame(cropBox pdfdoc.Rect, rotate, imW, imH int, dpi float64) (*pageFrame, error) {
	sc := dpi / 72
	cw, ch := cropBox.URX-cropBox.LLX, cropBox.URY-cropBox.LLY
	rot := ((rotate % 360) + 360) % 360
	f := &pageFrame{sc: sc, rot: rot}
	f.disp = func(x, y float64) (float64, float64) {
		u, v := x-cropBox.LLX, y-cropBox.LLY
		switch rot {
		case 90:
			return v, u
		case 180:
			return cw - u, v
		case 270:
			return ch - v, cw - u
		default:
			return u, ch - v
		}
	}
	f.undisp = func(dx, dy float64) (float64, float64) {
		switch rot {
		case 90:
			return cropBox.LLX + dy, cropBox.LLY + dx
		case 180:
			return cropBox.LLX + cw - dx, cropBox.LLY + dy
		case 270:
			return cropBox.LLX + cw - dy, cropBox.LLY + ch - dx
		default:
			return cropBox.LLX + dx, cropBox.LLY + ch - dy
		}
	}
	wantW, wantH := cw, ch
	if rot == 90 || rot == 270 {
		wantW, wantH = ch, cw
	}
	if math.Abs(float64(imW)-wantW*sc) > 2 || math.Abs(float64(imH)-wantH*sc) > 2 {
		return nil, fmt.Errorf("frame-mismatch: render %dx%d want %.0fx%.0f rot=%d",
			imW, imH, wantW*sc, wantH*sc, rot)
	}
	return f, nil
}

// pixelBox is b's extent on the pixel grid.
func (f *pageFrame) pixelBox(b content.Box) (x0, y0, x1, y1 float64) {
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{b.LLX, b.LLY}, {b.URX, b.LLY}, {b.LLX, b.URY}, {b.URX, b.URY}} {
		dx, dy := f.disp(c[0], c[1])
		x0, x1 = math.Min(x0, dx*f.sc), math.Max(x1, dx*f.sc)
		y0, y1 = math.Min(y0, dy*f.sc), math.Max(y1, dy*f.sc)
	}
	return
}

// dilate grows the marked cells by r in each direction. Out of range is
// unmarked, so the grid's own border never grows inward.
func dilate(src []bool, w, h, r int) []bool {
	out := make([]bool, w*h)
	if r <= 0 {
		copy(out, src)
		return out
	}
	tmp := make([]bool, w*h)
	for y := range h {
		row := y * w
		for x := range w {
			for dx := -r; dx <= r; dx++ {
				if nx := x + dx; nx >= 0 && nx < w && src[row+nx] {
					tmp[row+x] = true
					break
				}
			}
		}
	}
	for y := range h {
		for x := range w {
			for dy := -r; dy <= r; dy++ {
				if ny := y + dy; ny >= 0 && ny < h && tmp[ny*w+x] {
					out[y*w+x] = true
					break
				}
			}
		}
	}
	return out
}

// erode keeps only cells whose whole r-neighbourhood is marked. Out of range is
// unmarked, so a cell on the grid's border never survives -- the raster's outer
// edge is exactly where a resampling artefact would live.
func erode(src []bool, w, h, r int) []bool {
	out := make([]bool, w*h)
	if r <= 0 {
		copy(out, src)
		return out
	}
	tmp := make([]bool, w*h)
	for y := range h {
		row := y * w
		for x := range w {
			keep := true
			for dx := -r; dx <= r && keep; dx++ {
				nx := x + dx
				keep = nx >= 0 && nx < w && src[row+nx]
			}
			tmp[row+x] = keep
		}
	}
	for y := range h {
		for x := range w {
			keep := true
			for dy := -r; dy <= r && keep; dy++ {
				ny := y + dy
				keep = ny >= 0 && ny < h && tmp[ny*w+x]
			}
			out[y*w+x] = keep
		}
	}
	return out
}

// stencilMarks decodes the raster byblos would return and reports, per cell,
// whether it deposits ink -- by byblos's OWN decode path (extract.go:348-401),
// not a second reading of the stream. A cell is marked when the decoded sample
// is dark, which is the convention pdfcpu writes for a stencil; the deep_dark
// control is what proves it rather than assuming it.
func stencilMarks(d pdfdoc.Doc, id int) ([]bool, int, int, error) {
	data, fileType, err := d.RawImage(id)
	if err != nil {
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			return nil, 0, 0, errors.New("unsupported-codec")
		}
		return nil, 0, 0, err
	}
	var img image.Image
	switch fileType {
	case "jbig2":
		img, err = decodeJBIG2Placement(data, d.ImageInfo, id)
	case "jpx":
		return nil, 0, 0, errors.New("unsupported-codec-jpx")
	default:
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode %s: %w", fileType, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, 0, errors.New("empty raster")
	}
	marks := make([]bool, w*h)
	for y := range h {
		for x := range w {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if int((r+g+bl)/3>>8) < inkThreshold {
				marks[y*w+x] = true
			}
		}
	}
	return marks, w, h, nil
}

type washRow struct {
	path   string
	page   int
	imgW   int
	imgH   int
	mask   bool // the returned raster really is an /ImageMask
	decode bool // /Decode is present on it
	annots int
	fill   string
	rot    int

	marked   int // stencil cells that deposit ink
	deep     int // render pixels over eroded stencil ink
	deepDark int // of those, how many poppler renders dark -- the control
	deepSum  int64

	clear      int // render pixels inside the box, guard-clear of every bit
	clearInk   int // of those, below inkThreshold
	clearOff   int // of those, below offWhiteThreshold
	clearMin   int
	clearSum   int64
	outside    int // render pixels beyond the box, 1px guard
	outsideInk int

	cellsPerPixel float64 // the guard has to be at least this, or it guards nothing
	note          string
}

func (r washRow) line() string {
	frac := func(a, b int) string {
		if b == 0 {
			return "-"
		}
		return strconv.FormatFloat(float64(a)/float64(b), 'f', 4, 64)
	}
	mean := func(sum int64, n int) string {
		if n == 0 {
			return "-"
		}
		return strconv.FormatFloat(float64(sum)/float64(n), 'f', 1, 64)
	}
	return strings.Join([]string{
		r.path,
		strconv.Itoa(r.page),
		fmt.Sprintf("%dx%d", r.imgW, r.imgH),
		strconv.FormatBool(r.mask),
		strconv.FormatBool(r.decode),
		strconv.Itoa(r.annots),
		r.fill,
		strconv.Itoa(r.rot),
		strconv.Itoa(r.marked),
		strconv.Itoa(r.deep),
		strconv.Itoa(r.deepDark),
		frac(r.deepDark, r.deep),
		mean(r.deepSum, r.deep),
		strconv.Itoa(r.clear),
		strconv.Itoa(r.clearInk),
		strconv.Itoa(r.clearOff),
		strconv.Itoa(r.clearMin),
		mean(r.clearSum, r.clear),
		strconv.Itoa(r.outside),
		strconv.Itoa(r.outsideInk),
		strconv.FormatFloat(r.cellsPerPixel, 'f', 2, 64),
		r.note,
	}, "\t")
}

// measureWash renders one page and fills in row's counts.
func measureWash(row *washRow, dpi float64, guard int, invert bool, tmp string) {
	f, err := os.Open(row.path)
	if err != nil {
		row.note = "open: " + err.Error()
		return
	}
	defer f.Close()
	d, err := pdfdoc.Open(f)
	if err != nil {
		row.note = "pdfdoc: " + err.Error()
		return
	}
	p, err := d.Page(row.page)
	if err != nil {
		row.note = "page: " + err.Error()
		return
	}
	s, walkErr := content.Walk(context.Background(), p.Content, p.Scope, d)
	if walkErr != nil || s == nil {
		row.note = "walk"
		return
	}
	// The raster byblos would return is the one classify picks with the paint arm
	// inert -- the same construction the clause probe and the adjudicator use.
	noPaint := *s
	noPaint.Paints = nil
	idx, reason := classify(p.CropBox, &noPaint, d.ImageInfo)
	if reason != "" {
		row.note = "not-held-only: " + reason
		return
	}
	placement := noPaint.Images[idx]
	info, ok := d.ImageInfo(placement.ID)
	if !ok {
		row.note = "no-imageinfo"
		return
	}
	// If this is not a stencil the page is not this bead's, and every sentence
	// above about 0 bits is about some other image.
	row.mask, row.decode = info.ImageMask, info.Decode
	if !info.ImageMask {
		row.note = "not-imagemask"
		return
	}
	if as, err := d.Annots(row.page); err == nil {
		row.annots = len(as)
	}
	// EVERY marking paint, not just the first. byb-e04's adjudicator records one
	// because one paint bound its tolerance; here the page is the unit, and
	// govdocs1/500805.pdf p48 is why -- its first wash is a near-white
	// DeviceRGB[0.9 0.92 0.952] and the one that actually colours the page is the
	// second. Reporting the first alone would describe that page as nearly white.
	var fills []string
	for _, pt := range s.Paints {
		if _, marks := pt.Ink(); marks {
			fills = append(fills, fmt.Sprintf("%s%v", pt.Fill.Space, pt.Fill.Comps))
		}
	}
	row.fill = strings.Join(fills, ";")

	marks, iw, ih, err := stencilMarks(d, placement.ID)
	if err != nil {
		row.note = "stencil: " + err.Error()
		return
	}
	row.imgW, row.imgH = iw, ih
	if invert {
		for i := range marks {
			marks[i] = !marks[i]
		}
	}
	for _, m := range marks {
		if m {
			row.marked++
		}
	}
	near := dilate(marks, iw, ih, guard)
	deep := erode(marks, iw, ih, guard)

	prefix := filepath.Join(tmp, fmt.Sprintf("wash-%d", row.page))
	cmd := exec.Command("pdftoppm", "-gray", "-r", strconv.FormatFloat(dpi, 'f', -1, 64),
		"-f", strconv.Itoa(row.page), "-l", strconv.Itoa(row.page), "-singlefile",
		row.path, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		row.note = "pdftoppm: " + err.Error() + " " + strings.TrimSpace(string(out))
		return
	}
	defer os.Remove(prefix + ".pgm")
	im, err := readPGM(prefix + ".pgm")
	if err != nil {
		row.note = "pgm: " + err.Error()
		return
	}
	fr, err := newPageFrame(p.CropBox, p.Rotate, im.w, im.h, dpi)
	if err != nil {
		row.note = err.Error()
		return
	}
	row.rot = fr.rot

	box := placement.Box
	if box.URX-box.LLX <= 0 || box.URY-box.LLY <= 0 {
		row.note = "degenerate box"
		return
	}
	// A cell is found through the INVERSE CTM, never proportionally across Box.
	// Box is Clip intersected with the placement (walk.go:94-99), so on a clipped
	// placement it spans less than the whole raster and a proportional map lands
	// on the wrong cell everywhere. And byblos accepts a fraction of a degree of
	// scanner deskew (extract.go:235-242), which an axis-aligned map does not
	// carry at all: 0.1 degrees over 2200 cells drifts nearly 4 cells, twice the
	// guard. Either error moves the whole grid under the render, which is the
	// frame-mismatch failure again in a second coordinate system.
	m := placement.CTM
	det := m[0]*m[3] - m[1]*m[2]
	if det == 0 {
		row.note = "singular CTM"
		return
	}
	cellAt := func(px, py float64) (int, int, bool) {
		dx, dy := px-m[4], py-m[5]
		u := (m[3]*dx - m[2]*dy) / det
		v := (m[0]*dy - m[1]*dx) / det
		if u < 0 || u >= 1 || v < 0 || v >= 1 {
			return 0, 0, false
		}
		// An image XObject fills the unit square with sample (0,0) at its TOP
		// left (ISO 32000-1 8.9.5.2), so the row axis runs against v.
		return min(int(u*float64(iw)), iw-1), min(int((1-v)*float64(ih)), ih-1), true
	}
	// THE GUARD IS IN CELLS AND THE RENDER IS IN PIXELS, and a guard narrower
	// than one render pixel guards nothing: poppler averages every cell falling
	// under a pixel, so a cell the guard calls clear still contributes the ink of
	// its neighbour. Measured, that is not a rounding error -- at 72 DPI the same
	// 66 pages report 51 losses against 1 at 300, and a guard of 0 reports all
	// 66. Both readings are the instrument, not the corpus, so the condition is
	// stated per page rather than left to whoever picks the flags.
	row.cellsPerPixel = math.Max(
		float64(iw)*72/(dpi*math.Hypot(m[0], m[1])),
		float64(ih)*72/(dpi*math.Hypot(m[2], m[3])))
	if float64(guard) < row.cellsPerPixel {
		row.note = fmt.Sprintf("guard-below-pixel: %d cells < %.2f cells per render pixel",
			guard, row.cellsPerPixel)
	}

	x0, y0, x1, y1 := fr.pixelBox(box)
	row.clearMin = 255
	for y := range im.h {
		fy := float64(y) + 0.5
		for x := range im.w {
			fx := float64(x) + 0.5
			g := int(im.px[y*im.w+x])
			// Outside the box, with byb-e04's 1-pixel guard against the raster's
			// own antialiased edge.
			if fx < x0-1 || fx > x1+1 || fy < y0-1 || fy > y1+1 {
				row.outside++
				if g < inkThreshold {
					row.outsideInk++
				}
				continue
			}
			c, r, inRaster := cellAt(fr.undisp(fx/fr.sc, fy/fr.sc))
			if !inRaster {
				continue // between the box and its axis-aligned pixel extent
			}
			switch k := r*iw + c; {
			case deep[k]:
				row.deep++
				row.deepSum += int64(g)
				if g < inkThreshold {
					row.deepDark++
				}
			case !near[k]:
				row.clear++
				row.clearSum += int64(g)
				if g < row.clearMin {
					row.clearMin = g
				}
				if g < inkThreshold {
					row.clearInk++
				}
				if g < offWhiteThreshold {
					row.clearOff++
				}
			}
		}
	}
}

func TestStencilWashVisibility(t *testing.T) {
	tsv := os.Getenv("BYBLOS_WASH_TSV")
	if tsv == "" {
		t.Skip("set BYBLOS_WASH_TSV to the clause probe's TSV to run the byb-oxf stencil-wash probe")
	}
	outPath := os.Getenv("BYBLOS_WASH_OUT")
	if outPath == "" {
		t.Fatal("set BYBLOS_WASH_OUT")
	}
	dpi := 300.0
	if v, err := strconv.ParseFloat(os.Getenv("BYBLOS_WASH_DPI"), 64); err == nil && v > 0 {
		dpi = v
	}
	guard := 2
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_WASH_GUARD")); err == nil && v >= 0 {
		guard = v
	}
	invert := os.Getenv("BYBLOS_WASH_INVERT") == "1"
	workers := 6
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && v > 0 {
		workers = v
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Fatalf("pdftoppm not on PATH: %v", err)
	}

	raw, err := os.ReadFile(tsv)
	if err != nil {
		t.Fatalf("read %s: %v", tsv, err)
	}
	var rows []washRow
	for _, ln := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		c := strings.Split(ln, "\t")
		// clause C, diverted vector-paint, and held out by the paint arm alone.
		if len(c) < 17 || c[2] != "vector-paint" || c[3] != "EXTRACTS" || c[4] != "C" {
			continue
		}
		pg, err := strconv.Atoi(c[1])
		if err != nil {
			continue
		}
		rows = append(rows, washRow{path: c[0], page: pg, clearMin: 255})
	}
	if len(rows) == 0 {
		t.Fatalf("no clause-C held-only pages in %s", tsv)
	}

	tmp := t.TempDir()
	work := make(chan int)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			dir := filepath.Join(tmp, strconv.Itoa(w))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return
			}
			for i := range work {
				measureWash(&rows[i], dpi, guard, invert, dir)
			}
		}(w)
	}
	for i := range rows {
		work <- i
	}
	close(work)
	wg.Wait()

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	defer out.Close()
	var lost, escaped, noted, mute int
	var worstControl = 1.0
	for _, r := range rows {
		if r.note != "" {
			noted++
		}
		if r.clearInk > 0 {
			lost++
		}
		if r.outsideInk > 0 {
			escaped++
		}
		// A page whose stencil has no interior at this guard cannot speak to the
		// control either way, and counting it as agreement would be counting an
		// empty set. It is reported so the control's COVERAGE is visible, not
		// just its worst value.
		if r.deep == 0 {
			mute++
		} else if f := float64(r.deepDark) / float64(r.deep); f < worstControl {
			worstControl = f
		}
		if _, err := fmt.Fprintln(out, r.line()); err != nil {
			t.Fatalf("write %s: %v", outPath, err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", outPath, err)
	}
	t.Logf("pages=%d dpi=%g guard=%d invert=%v notes=%d wash-visible-under-stencil=%d ink-outside-box=%d control-mute=%d worst-deep-dark-fraction=%.4f",
		len(rows), dpi, guard, invert, noted, lost, escaped, mute, worstControl)
}
