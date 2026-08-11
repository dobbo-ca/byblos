package byblos

// Poppler adjudication for byb-e04: for every page a fill-only paint tolerance
// would release, ask poppler what it paints OUTSIDE the raster byblos would
// return. If poppler shows real ink there, extracting the page loses it and the
// tolerance that released it is too loose.
//
// Poppler is the specification, not an oracle of convenience (byb-3jq, byb-62t).
// The distribution of overhangs says how MANY pages a number releases; only a
// render says whether releasing them is correct.
//
// It skips unless BYBLOS_ADJ_TSV is set. Inputs:
//
//	BYBLOS_ADJ_TSV   the clause probe's TSV (all corpora concatenated)
//	BYBLOS_ADJ_OUT   TSV to write
//	BYBLOS_ADJ_DPI   render resolution, default 300
//	BYBLOS_ADJ_MAX   only adjudicate pages whose req_tol_fill is <= this, default 2.0
//	BYBLOS_JOBS      workers, default 6
//
// The measurement is a band: the page minus the raster's own box. A raster is
// antialiased along its edge, so a pixel just outside the box can be the
// raster's own ink rather than the escaping paint. Counts are therefore reported
// at three guard widths (0, 1 and 2 pixels of inset away from the box) and the
// decision reads the guarded ones.

import (
	"bufio"
	"context"
	"fmt"
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

// inkThreshold is the grey level below which a pixel counts as ink. pdftoppm
// writes 255 for white; anything visibly marked lands well under 250.
const inkThreshold = 250

type adjRow struct {
	path     string
	page     int
	reqTol   float64
	bindOp   string
	fill     string
	bandArea [3]int // pixels outside the raster box, at guard 0, 1, 2
	ink      [3]int // of those, how many carry ink
	minGrey  int
	rot      int
	note     string
}

// pgm is a decoded binary PGM (P5).
type pgm struct {
	w, h int
	px   []byte
}

func readPGM(path string) (*pgm, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	// A P5 header is a magic number then three integers, with arbitrary
	// whitespace and #-comments between them.
	field := func() (string, error) {
		var b []byte
		for {
			c, err := r.ReadByte()
			if err != nil {
				return "", err
			}
			if c == '#' {
				for c != '\n' {
					if c, err = r.ReadByte(); err != nil {
						return "", err
					}
				}
				continue
			}
			if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				if len(b) > 0 {
					return string(b), nil
				}
				continue
			}
			b = append(b, c)
		}
	}
	magic, err := field()
	if err != nil || magic != "P5" {
		return nil, fmt.Errorf("not a binary PGM: %q", magic)
	}
	ws, err := field()
	if err != nil {
		return nil, err
	}
	hs, err := field()
	if err != nil {
		return nil, err
	}
	if _, err := field(); err != nil { // maxval
		return nil, err
	}
	w, err := strconv.Atoi(ws)
	if err != nil {
		return nil, err
	}
	h, err := strconv.Atoi(hs)
	if err != nil {
		return nil, err
	}
	px := make([]byte, w*h)
	if _, err := readFull(r, px); err != nil {
		return nil, err
	}
	return &pgm{w: w, h: h, px: px}, nil
}

func readFull(r *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// adjudicate renders one page and counts ink outside the raster box. inset
// shrinks the box by that many points on every side before measuring, which is
// the control: a "no ink outside" reading means nothing unless the same code
// FINDS ink when the band is moved over territory the raster demonstrably
// paints. It is 0 for a real run.
func adjudicate(row *adjRow, dpi, inset float64, tmp string) {
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
	// inert -- the same construction the clause probe uses for "held only by".
	noPaint := *s
	noPaint.Paints = nil
	idx, reason := classify(p.CropBox, &noPaint, d.ImageInfo)
	if reason != "" {
		row.note = "not-held-only: " + reason
		return
	}
	box := noPaint.Images[idx].Box
	box = content.Box{LLX: box.LLX + inset, LLY: box.LLY + inset, URX: box.URX - inset, URY: box.URY - inset}

	// The fill colour of the first marking paint carrying the binding operator. A
	// white wash escaping the raster paints nothing a reader would miss; a
	// coloured one does. This is a diagnostic column, not the decision -- the
	// render is the decision -- so matching on the operator alone is enough.
	for _, pt := range s.Paints {
		if _, marks := pt.Ink(); marks && pt.Op == row.bindOp {
			row.fill = fmt.Sprintf("%s%v", pt.Fill.Space, pt.Fill.Comps)
			break
		}
	}

	prefix := filepath.Join(tmp, fmt.Sprintf("adj-%d", row.page))
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

	// Poppler renders the CropBox AND applies the page's /Rotate. byblos does not
	// -- /Rotate is a display attribute and Box is the raster as stored
	// (extract.go:231-233) -- so the box has to be carried through the same
	// rotation before it can be compared against pixels. Mapping all four corners
	// and taking the extent covers every quarter turn without a case per angle.
	sc := dpi / 72
	cw, ch := p.CropBox.URX-p.CropBox.LLX, p.CropBox.URY-p.CropBox.LLY
	rot := ((p.Rotate % 360) + 360) % 360
	disp := func(x, y float64) (float64, float64) {
		u, v := x-p.CropBox.LLX, y-p.CropBox.LLY
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
	wantW, wantH := cw, ch
	if rot == 90 || rot == 270 {
		wantW, wantH = ch, cw
	}
	// If this does not hold, the pixels and the box are in different frames and
	// every count below is meaningless. It is the check that catches a rotation,
	// a CropBox poppler disagrees about, or a wrong page being rendered.
	if math.Abs(float64(im.w)-wantW*sc) > 2 || math.Abs(float64(im.h)-wantH*sc) > 2 {
		row.note = fmt.Sprintf("frame-mismatch: render %dx%d want %.0fx%.0f rot=%d",
			im.w, im.h, wantW*sc, wantH*sc, rot)
		return
	}
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{box.LLX, box.LLY}, {box.URX, box.LLY}, {box.LLX, box.URY}, {box.URX, box.URY}} {
		dx, dy := disp(c[0], c[1])
		x0, x1 = math.Min(x0, dx*sc), math.Max(x1, dx*sc)
		y0, y1 = math.Min(y0, dy*sc), math.Max(y1, dy*sc)
	}
	row.rot = rot
	row.minGrey = 255
	for g := 0; g < 3; g++ {
		gx0, gx1 := x0-float64(g), x1+float64(g)
		gy0, gy1 := y0-float64(g), y1+float64(g)
		for y := 0; y < im.h; y++ {
			fy := float64(y) + 0.5
			inY := fy >= gy0 && fy <= gy1
			for x := 0; x < im.w; x++ {
				fx := float64(x) + 0.5
				if inY && fx >= gx0 && fx <= gx1 {
					continue // inside the raster box (plus guard)
				}
				row.bandArea[g]++
				v := int(im.px[y*im.w+x])
				if v < inkThreshold {
					row.ink[g]++
					if g == 1 && v < row.minGrey {
						row.minGrey = v
					}
				}
			}
		}
	}
}

func TestPopplerAdjudication(t *testing.T) {
	tsv := os.Getenv("BYBLOS_ADJ_TSV")
	if tsv == "" {
		t.Skip("set BYBLOS_ADJ_TSV to the clause probe's TSV to run the byb-e04 poppler adjudication")
	}
	outPath := os.Getenv("BYBLOS_ADJ_OUT")
	if outPath == "" {
		t.Fatal("set BYBLOS_ADJ_OUT")
	}
	dpi := 300.0
	if v, err := strconv.ParseFloat(os.Getenv("BYBLOS_ADJ_DPI"), 64); err == nil && v > 0 {
		dpi = v
	}
	maxTol := 2.0
	if v, err := strconv.ParseFloat(os.Getenv("BYBLOS_ADJ_MAX"), 64); err == nil && v > 0 {
		maxTol = v
	}
	inset := 0.0
	if v, err := strconv.ParseFloat(os.Getenv("BYBLOS_ADJ_INSET"), 64); err == nil && v > 0 {
		inset = v
	}
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
	var rows []adjRow
	for _, ln := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		c := strings.Split(ln, "\t")
		if len(c) < 17 || c[2] != "vector-paint" || c[3] != "EXTRACTS" || c[7] == "inf" {
			continue
		}
		tol, err := strconv.ParseFloat(c[7], 64)
		if err != nil || tol > maxTol {
			continue
		}
		pg, err := strconv.Atoi(c[1])
		if err != nil {
			continue
		}
		rows = append(rows, adjRow{path: c[0], page: pg, reqTol: tol, bindOp: c[8], minGrey: 255})
	}
	if len(rows) == 0 {
		t.Fatalf("no candidate pages in %s at req_tol_fill <= %g", tsv, maxTol)
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
				adjudicate(&rows[i], dpi, inset, dir)
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
	var dirty int
	for _, r := range rows {
		if r.ink[1] > 0 {
			dirty++
		}
		fmt.Fprintf(out, "%s\t%d\t%.4f\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			r.path, r.page, r.reqTol, r.bindOp, r.fill,
			r.bandArea[0], r.ink[0], r.bandArea[1], r.ink[1], r.bandArea[2], r.ink[2], r.rot,
			r.minGrey, r.note)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", outPath, err)
	}
	t.Logf("adjudicated=%d dpi=%g inset=%g maxTol=%g pages-with-ink-outside(guard=1)=%d", len(rows), dpi, inset, maxTol, dirty)
}
