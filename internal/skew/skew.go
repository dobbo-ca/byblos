// Package skew estimates the angle of the text lines INSIDE a raster.
//
// It exists for byb-16j.1, which has to separate two different angles that both
// make a page look crooked and that have nothing to do with each other:
//
//   - PLACEMENT SKEW is the raster's paint matrix, and byblos already reports it
//     as ImageRef.Placement. Reading it costs no decoding at all.
//   - CONTENT SKEW is the angle of the marks inside the raster. A scanner that
//     fed the page crooked writes an AXIS-ALIGNED placement of a raster whose
//     content is rotated, so the placement angle is zero and the page is still
//     crooked. Nothing in byblos can see it. This package can.
//
// IT IS A MEASURING INSTRUMENT, NOT A CORRECTION. Nothing here rotates, resamples
// or writes anything; byblos still never renders (design spec section 2). The
// estimate is a number a census divides pages by, and byb-16j gates the whole
// straightening tree on that census.
//
// # The method, and why this one
//
// PROJECTION PROFILE, scored by the variance of the profile. For a candidate
// angle t, every ink pixel is projected onto the axis NORMAL to t and counted
// into a one-pixel bin. When t matches the angle of the text lines, each line's
// pixels land in the same few bins and the profile is a comb of tall spikes
// separated by empty gaps; at any other angle the lines smear across each other
// and the profile flattens. Sum of squares is maximal at the comb, because the
// bin total is the ink count at EVERY angle and only its distribution moves.
//
// It is chosen over the alternatives on cost and on what the corpus is:
//
//   - A HOUGH TRANSFORM over edge pixels finds lines, which is more than is
//     wanted: a page's dominant straight lines can be a table rule or a scan
//     border rather than its text, and both are common in the sample.
//   - AN FFT of the whole raster costs O(n log n) in the PIXEL count. This costs
//     O(ink points) per angle over a subsampled point set, and text is sparse.
//   - A NEAREST-NEIGHBOUR CHAIN over connected components needs a component
//     labeller and degrades on the touching, broken glyphs that a 1-bit archive
//     scan is full of.
//
// It is also the method byb-divert already used by hand, on four pages of
// 005393.pdf, to establish that that file's stored raster is the raw skewed scan
// and its CTM carries the deskew. That file is this package's only real-world
// ground truth, and it is a strong one: the CTM's angle is measured by a
// completely different route, from numbers in the content stream that no image
// analysis touches. TestSkewInstrumentAgainstMeasuredDeskew (root package,
// skew_probe_test.go) is where the two are put side by side, because checking it
// needs a PDF reader and this package deliberately has none.
//
// # What it cannot see
//
// A page with no line structure -- a photograph, a blank scan, a full-page
// halftone -- has no comb to find, and the score curve over t is flat. Such a
// page MUST report OK false rather than the argmax of noise, which is a real
// angle-shaped number that means nothing. Confidence is the prominence of the
// peak over the rest of the curve and Estimate refuses below minConfidence; see
// that constant for how the threshold was placed.
//
// The estimate is MODULO 90 DEGREES and the search range says so: a page turned
// a quarter turn has a perfectly good comb and reads as 0. Quarter turns are
// /Rotate's business (byb-yul.4) and are not skew.
package skew

import (
	"image"
	"math"
	"sort"
)

// Estimate is what one raster's marks say about their own angle.
//
// Deg is positive for content rotated COUNTER-CLOCKWISE as the page is read,
// which is the sign convention ImageRef.Placement's atan2(b, a) already has, so
// a census can add the two angles without a sign table. Note that raster rows
// run DOWNWARD while PDF user space runs upward, and the conversion is done here
// rather than left to the caller (see estimate).
//
// OK false means the raster carries no measurable line structure and Deg is
// meaningless. Callers must check it: the argmax of a flat curve is a number
// like any other and nothing downstream can tell it from a measurement.
type Estimate struct {
	Deg        float64 // the content angle in degrees, or 0 when !OK
	Confidence float64 // peak prominence over the coarse curve; see minConfidence
	OK         bool    // false when the raster has no line structure to measure
	InkFrac    float64 // share of working-resolution pixels judged ink
	InkPoints  int     // ink points at working resolution, before any decimation
	Threshold  uint8   // the luminance cut Otsu chose, for diagnosis
	// AltDeg is the same angle chosen by a DIFFERENT score function -- the
	// profile's first differences instead of the profile itself. It is not a
	// better answer and must not be substituted for Deg. It is there so that a
	// census over real pages can report how often the two disagree, which is the
	// only check on this instrument that does not run on a page I drew myself.
	AltDeg float64
	// Railed says the search settled on the END of its own range, which means the
	// true angle is outside it or there is no peak to find. Such an estimate is
	// the boundary constant and not a measurement, so OK is false; the flag is
	// separate so a census can count how often it happens instead of losing the
	// pages silently.
	Railed bool

	// THE KEYSTONE HALF. byb-16j.1 asks for "pages whose four detected page-edge
	// corners are NOT a parallelogram", and a rotation is exactly the
	// transform that keeps a parallelogram one. These four numbers are that test,
	// applied to the TEXT BLOCK rather than to the page edge, because an archive
	// scan is usually cropped to the page and has no visible edge to detect. See
	// bands and edges for what each can and cannot see.
	//
	// A keystone has two axes and neither field sees both:
	//
	//   TOP EDGE WIDER THAN THE BOTTOM tilts the page about its horizontal axis.
	//   Text lines stay parallel to that axis, so BandSpread stays 0 -- but the
	//   left and right sides of the text block converge, so Converge does not.
	//
	//   ONE SIDE WIDER THAN THE OTHER tilts it about its vertical axis. Now the
	//   text lines themselves fan towards a vanishing point and BandSpread is the
	//   signal, while the sides stay parallel.
	//
	// A page needs BOTH to be near zero to be a parallelogram, which is why both
	// are reported and neither is combined into a single verdict here.
	BandDeg    [bandCount]float64 // per-band angle, top band first
	BandOK     [bandCount]bool
	BandSpread float64 // widest disagreement between OK bands; -1 when under two
	// Converge is the left edge's angle minus the right edge's, measured after
	// the whole raster is turned back by Deg. Zero for a parallelogram at any
	// rotation; non-zero exactly when the sides are not parallel.
	Converge  float64
	LeftDeg   float64 // the sides' own residual angles, kept so a page that is
	RightDeg  float64 // simply narrower at one end can be told from one that leans
	EdgesOK   bool
	EdgeResid float64 // worst of the two straight-line fits, in working pixels
}

// The search. Range and steps are constants rather than options because a census
// that lets each caller pick its own grid cannot pool its rows.
const (
	// searchDeg is the half-range. Chris asked for 1 to 10 degrees and the census
	// must count what is past that as well, so the grid reaches 15 and anything
	// beyond it is reported AT the rail rather than wrapped -- see estimate.
	searchDeg = 15.0
	// coarseStep sweeps the whole range. A tenth of a degree would find the peak
	// directly and cost 300 passes; half a degree costs 61 and the fine pass then
	// costs 41 more, which is a quarter of the work for the same answer.
	coarseStep = 0.5
	// fineStep is the resolution of the reported angle. Below this the score
	// difference between neighbouring angles is smaller than the quantisation of
	// the ink points themselves, so a finer grid reports precision the pixels do
	// not have.
	fineStep = 0.025
	// fineSpan is how far either side of the coarse peak the fine pass sweeps. It
	// is one coarse step, so the fine grid cannot miss a peak the coarse grid
	// bracketed.
	fineSpan = coarseStep
)

// Working resolution and the ink guards.
const (
	// maxWorkingDim caps the long side. A 300 DPI Letter scan is 2550x3300 and
	// reduces by 3; the comb this method looks for is line-spacing-scale
	// structure, about 50 pixels at 300 DPI, and survives that reduction with
	// room to spare. It bounds the cost of the worst page rather than the median
	// one, which is the point: the sample holds 600 DPI JBIG2 base layers.
	maxWorkingDim = 1200
	// maxInkPoints bounds the per-angle cost. A dense page at working resolution
	// is a few hundred thousand ink points; past this they are decimated by a
	// fixed stride, which is deterministic and so keeps two runs of the census
	// identical.
	maxInkPoints = 120000
	// minInkPoints is the floor below which there is nothing to measure. Fewer
	// points than this is a blank scan or a stamp on an empty page.
	minInkPoints = 500
	// minInkFrac and maxInkFrac reject the two degenerate rasters: a page with
	// almost no marks, and a page that is almost entirely dark. Neither has a
	// profile with gaps in it.
	minInkFrac = 0.002
	maxInkFrac = 0.60
	// bandCount is how many full-width horizontal bands the raster is split into
	// to see whether the text lines are parallel to each other.
	//
	// BANDS AND NOT A GRID, and the reason is what a fan actually looks like.
	// Lines converging on a vanishing point have an angle that varies with
	// HEIGHT, so a full-width band has one well-defined angle and a vertical
	// strip has every angle in the page at once and averages them away. Three is
	// the fewest that can show a trend rather than a difference, and each band
	// still holds a third of the page's ink.
	bandCount = 3
	// minBandPoints is the ink a band needs before its own angle is believed. A
	// band that is mostly margin has a handful of points and a meaningless peak.
	minBandPoints = 400
	// edgeSlices is how finely the text block's sides are sampled to fit a line
	// to each. Enough slices to fit through, few enough that each holds ink.
	edgeSlices = 24
	// minSlicePoints is the ink one slice needs to contribute a left and right
	// extreme. Below it the slice is a gap between paragraphs, not an edge.
	minSlicePoints = 40
	// edgeQuantile is how far in from the extreme the side is measured. The
	// literal leftmost ink pixel of a slice is a speckle, a marginal note or a
	// hole punch; the fifth percentile is the text block.
	edgeQuantile = 0.05
	// minEdgeSlices is how many slices must survive before a side is fitted at
	// all.
	minEdgeSlices = 8
	// maxEdgeResidFrac is how far the slices may sit off the fitted side, as a
	// share of the working image's width, before the side is not a line and
	// EdgesOK is false.
	//
	// A GATE IS REQUIRED, NOT OPTIONAL, and the first census run is why. Without
	// one, 176 real extracted pages reported a median convergence of 2.27 degrees
	// with residuals up to 318 working pixels on a page about 800 wide -- fits
	// through points that are not on any line, reported beside fits that are. The
	// same pages under a tight gate report a median of 0.66 and a maximum of
	// 1.42. Converge is only a measurement where the sides are actually straight,
	// and this is what says where that is.
	//
	// A fraction rather than a pixel count because the working image is whatever
	// maxWorkingDim left of a page that could be Letter or a folio.
	maxEdgeResidFrac = 0.02
	// minConfidence is where a peak stops being structure and starts being noise.
	//
	// IT IS CALIBRATED, NOT CHOSEN, and it is placed with a margin on BOTH sides.
	// TestConfidenceSeparatesTextFromNoise builds pages that have lines and pages
	// that cannot have them -- uniform noise, a flat field, a photograph-shaped
	// gradient -- and asserts the two populations fall either side of it. Move
	// the constant and that test says which side stopped clearing it.
	//
	// THE HARDEST TEXT PAGE SETS IT, NOT THE EASIEST. Confidence falls as a page
	// gets DENSER, because the comb's gaps are what the score measures: the
	// synthetic pages score 0.71 at 35% ink and 4.01 at 10%, and real text is
	// nearer the second. Noise, a gradient and a solid blob all score 0.03-0.04.
	// 0.15 is about four times the noise and about five times below the worst
	// text. An earlier 0.35 was only twice below it, and a fanned page -- which
	// smears its own comb -- scored 0.37 against it.
	//
	// A CENSUS MUST NOT LEAN ON THIS NUMBER ANYWAY. Estimate.Confidence is
	// reported on every row whatever OK says, so a distribution can be shown to
	// be insensitive to where the cut is rather than asserted to be.
	minConfidence = 0.15
)

// Options is the little that a caller may vary.
//
// Bitonal says the source declared one bit per component. It skips Otsu, whose
// between-class variance is undefined on an image with two luminance values and
// whose answer on a widened bitonal raster is an arbitrary point between them.
// It is a DECLARATION from the image dictionary, exactly as PageRaster.Bitonal
// is, and must not be sniffed from the pixels.
type Options struct {
	Bitonal bool
}

// Measure reports the angle of the line structure in img.
//
// It never returns an error. A raster it cannot measure comes back with OK
// false and a filled-in InkFrac, which is what a census row wants: the reason a
// page has no estimate is itself a measurement.
func Measure(img image.Image, opt Options) Estimate {
	pts, w, h, est := inkPoints(img, opt)
	est.BandSpread = -1
	if !est.OK {
		return est
	}
	deg, conf, alt, railed := search(pts, w, h)
	est.Deg, est.Confidence, est.AltDeg, est.Railed = deg, conf, alt, railed
	est.OK = conf >= minConfidence && !railed
	if !est.OK {
		est.Deg, est.AltDeg = 0, 0
		return est
	}
	bands(pts, w, h, &est)
	// The sides are measured in the frame the page's own lines define, so
	// -Deg turns the raster back to square first. Deg is already in user-space
	// sign, and the points are in row space, so the turn is by +Deg.
	edges(pts, w, est.Deg, &est)
	return est
}

// bands measures each full-width horizontal band's own line angle, so that a
// page whose lines are not parallel to each other can be told from one that is
// simply rotated.
//
// A band's points are the whole raster's points, filtered by y. The threshold,
// the reduction and the decimation are therefore shared with the whole-page
// estimate, which is what lets the two be compared at all: a band that ran its
// own Otsu could differ from the page for a reason that has nothing to do with
// geometry.
func bands(pts []point, w, h int, est *Estimate) {
	edge := h / bandCount
	buckets := make([][]point, bandCount)
	for _, p := range pts {
		i := min(int(p.y)/max(edge, 1), bandCount-1)
		buckets[i] = append(buckets[i], p)
	}
	lo, hi, n := math.Inf(1), math.Inf(-1), 0
	for i, b := range buckets {
		if len(b) < minBandPoints {
			continue
		}
		deg, conf, _, railed := search(b, w, h)
		if conf < minConfidence || railed {
			continue
		}
		est.BandDeg[i], est.BandOK[i] = deg, true
		lo, hi, n = min(lo, deg), max(hi, deg), n+1
	}
	if n >= 2 {
		est.BandSpread = hi - lo
	}
}

// edges fits a straight line to the left and to the right side of the text
// block, after turning the raster back by the angle the lines themselves gave.
//
// AFTER THE TURN, A PARALLELOGRAM HAS TWO VERTICAL SIDES. That is the whole
// test: a rotation moves both sides by the same angle and the turn takes it back
// out, so any angle LEFT OVER is a side that was never parallel to the other one
// -- which is a trapezoid, which is a keystone. Converge is their difference and
// is the number a census counts.
//
// WHAT IT CANNOT SEE, and a count made from it must say so:
//   - Ragged-right text has a right side that is not a line at all. EdgeResid is
//     reported so that a page whose fit is poor can be excluded rather than
//     counted as converging.
//   - Centred text, a title page, a page of figures and a single-column page
//     beside a wide table all have sides that are honest lines and are not the
//     page's edge. They converge for typographic reasons, not optical ones.
//   - A page cropped INSIDE its own text block has no side to find there.
//
// So Converge over a corpus is an UPPER BOUND on the keystone population, and
// its value is that a near-zero distribution refutes the population outright
// while a wide one only says the question needs a renderer to settle.
func edges(pts []point, w int, deg float64, est *Estimate) {
	rad := deg * math.Pi / 180
	sin, cos := math.Sin(rad), math.Cos(rad)
	// Turning by +deg in row space undoes the -deg the search reported.
	type slice struct{ xs []float64 }
	var lo, hi float64 = math.Inf(1), math.Inf(-1)
	rx := make([]float64, len(pts))
	ry := make([]float64, len(pts))
	for i, p := range pts {
		x, y := float64(p.x), float64(p.y)
		rx[i] = x*cos - y*sin
		ry[i] = x*sin + y*cos
		lo, hi = min(lo, ry[i]), max(hi, ry[i])
	}
	if hi-lo < edgeSlices {
		return
	}
	slices := make([]slice, edgeSlices)
	span := (hi - lo) / edgeSlices
	for i := range rx {
		j := min(int((ry[i]-lo)/span), edgeSlices-1)
		slices[j].xs = append(slices[j].xs, rx[i])
	}

	var ly, lx, ry2, rx2 []float64
	for j, s := range slices {
		if len(s.xs) < minSlicePoints {
			continue
		}
		sort.Float64s(s.xs)
		k := int(edgeQuantile * float64(len(s.xs)))
		mid := lo + (float64(j)+0.5)*span
		ly, lx = append(ly, mid), append(lx, s.xs[k])
		ry2, rx2 = append(ry2, mid), append(rx2, s.xs[len(s.xs)-1-k])
	}
	if len(ly) < minEdgeSlices {
		return
	}
	lm, lr := fitLine(ly, lx)
	rm, rr := fitLine(ry2, rx2)
	est.LeftDeg = math.Atan(lm) * 180 / math.Pi
	est.RightDeg = math.Atan(rm) * 180 / math.Pi
	est.Converge = est.LeftDeg - est.RightDeg
	est.EdgeResid = max(lr, rr)
	// A fit through points that are not on a line is not a side. Reported
	// through EdgesOK rather than by returning early, so EdgeResid still says how
	// far off it was and a census can show its own gate is not load-bearing.
	est.EdgesOK = est.EdgeResid <= maxEdgeResidFrac*float64(w)
}

// fitLine fits x against y and returns the slope with the median absolute
// residual. y is the free variable because the sides of a text block are
// near-vertical after the turn, and a fit of y against x would be fitting an
// infinite slope.
//
// IT IS A THEIL-SEN FIT AND NOT LEAST SQUARES, and the first run of the census
// over real pages is the reason. On a page of ragged-right typescript the last
// line of every paragraph stops short, so the right-hand extreme of the slice
// holding it sits far inside the margin. Least squares gives every such slice
// full weight and swings the fitted side by tens of degrees: over 176 real
// extracted pages it reported a median convergence of 9.6 degrees, on flatbed
// scans that are certainly flat, with RMS residuals of 140 to 350 working pixels
// on a page about 800 wide. The median of the pairwise slopes ignores a minority
// of short lines entirely.
//
// The residual is the MEDIAN absolute deviation for the same reason: an RMS
// residual is dominated by the same outliers the slope was made immune to, so it
// could not be used to gate them.
func fitLine(y, x []float64) (slope, resid float64) {
	n := len(y)
	if n < 2 {
		return 0, math.Inf(1)
	}
	slopes := make([]float64, 0, n*(n-1)/2)
	for i := range n {
		for j := i + 1; j < n; j++ {
			if dy := y[j] - y[i]; dy != 0 {
				slopes = append(slopes, (x[j]-x[i])/dy)
			}
		}
	}
	if len(slopes) == 0 {
		return 0, math.Inf(1)
	}
	slope = median(slopes)
	// The intercept is the median of x - slope*y, which is the Theil-Sen
	// companion to the slope and keeps the whole fit robust.
	inter := make([]float64, n)
	for i := range n {
		inter[i] = x[i] - slope*y[i]
	}
	c := median(inter)
	dev := make([]float64, n)
	for i := range n {
		dev[i] = math.Abs(x[i] - (slope*y[i] + c))
	}
	return slope, median(dev)
}

// point is one ink pixel at working resolution. int32 halves the slice against
// int on a 64-bit build and no working image approaches 2^31 on a side.
type point struct{ x, y int32 }

// inkPoints reduces img to the ink points the search runs over, and reports the
// guards that stop a raster having an estimate at all.
//
// THE REDUCTION IS A BLOCK MINIMUM, NOT AN AVERAGE, and that is not a detail. A
// 300 DPI scan's strokes are one or two pixels wide; averaging a 3x3 block
// containing one ink pixel gives 8/9 of the page colour and the threshold then
// discards the stroke. Taking the darkest pixel in the block keeps every stroke
// that existed at full resolution, which is what the comb is made of. It also
// makes the reduction idempotent on a bitonal raster: any ink in the block is
// ink.
func inkPoints(img image.Image, opt Options) ([]point, int, int, Estimate) {
	b := img.Bounds()
	if b.Empty() {
		return nil, 0, 0, Estimate{}
	}
	stride := 1
	for max(b.Dx(), b.Dy())/stride > maxWorkingDim {
		stride++
	}
	w, h := (b.Dx()+stride-1)/stride, (b.Dy()+stride-1)/stride
	if w < 2 || h < 2 {
		return nil, 0, 0, Estimate{}
	}

	lum := make([]uint8, w*h)
	for yy := range h {
		for xx := range w {
			lum[yy*w+xx] = blockMin(img, b, xx*stride, yy*stride, stride)
		}
	}

	cut := uint8(128)
	if !opt.Bitonal {
		cut = otsu(lum)
	}
	// Ink is DARKER than the cut. A raster stored inverted -- white marks on a
	// black field -- reads as a page of ink and is caught by maxInkFrac rather
	// than silently measured upside down, because this package cannot tell an
	// inverted scan from a photograph of a night sky and must not guess.
	pts := make([]point, 0, w*h/8)
	for yy := range h {
		row := lum[yy*w : (yy+1)*w]
		for xx, v := range row {
			if v <= cut {
				pts = append(pts, point{int32(xx), int32(yy)})
			}
		}
	}

	est := Estimate{
		InkFrac:   float64(len(pts)) / float64(w*h),
		InkPoints: len(pts),
		Threshold: cut,
	}
	if len(pts) < minInkPoints || est.InkFrac < minInkFrac || est.InkFrac > maxInkFrac {
		return nil, w, h, est
	}
	// InkPoints stays the count BEFORE decimation, so it and InkFrac describe the
	// same thing. A field that silently means "before the cap" on some pages and
	// "after it" on others cannot be summed over a census.
	if len(pts) > maxInkPoints {
		pts = decimate(pts, maxInkPoints)
	}
	est.OK = true
	return pts, w, h, est
}

// blockMin is the darkest luminance in one stride x stride block, clipped to the
// image. The block is read through image.Image's At so that every decoded raster
// byblos can produce is accepted whatever its concrete type; the census decodes
// far more slowly than this loop runs.
func blockMin(img image.Image, b image.Rectangle, x0, y0, stride int) uint8 {
	lo := uint32(0xffff)
	for dy := range stride {
		y := b.Min.Y + y0 + dy
		if y >= b.Max.Y {
			break
		}
		for dx := range stride {
			x := b.Min.X + x0 + dx
			if x >= b.Max.X {
				break
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			// Rec. 601 luma, in the 16-bit space RGBA returns. Alpha is ignored:
			// a decoded page raster is opaque, and a caller handing this a
			// transparent one would rather see the colour than the compositing.
			l := (299*r + 587*g + 114*bl) / 1000
			if l < lo {
				lo = l
			}
		}
	}
	return uint8(lo >> 8)
}

// decimate keeps at most n points by a fixed stride. It is deterministic on
// purpose: a census that draws a random subset cannot be diffed against its own
// previous run, and byb-wj2 is the record of what an unexplained difference
// between two measurement runs costs.
func decimate(pts []point, n int) []point {
	stride := (len(pts) + n - 1) / n
	out := pts[:0:0]
	for i := 0; i < len(pts); i += stride {
		out = append(out, pts[i])
	}
	return out
}

// otsu picks the luminance cut that maximises between-class variance.
//
// A GLOBAL threshold is deliberate. Sauvola (sauvola.go) is better for
// RECOVERING a page's text, because it follows an uneven illumination; it is
// worse here, because it also follows the page's own shading and turns a smooth
// gradient into a field of speckle that has line structure of its own. The comb
// only needs the strokes to be mostly present, and a global cut cannot invent
// structure that the page does not have.
func otsu(lum []uint8) uint8 {
	var hist [256]int
	for _, v := range lum {
		hist[v]++
	}
	total := len(lum)
	var sum float64
	for i, c := range hist {
		sum += float64(i) * float64(c)
	}
	var sumB, wB float64
	best, bestVar := uint8(128), -1.0
	for i := range 256 {
		wB += float64(hist[i])
		if wB == 0 {
			continue
		}
		wF := float64(total) - wB
		if wF == 0 {
			break
		}
		sumB += float64(i) * float64(hist[i])
		mB, mF := sumB/wB, (sum-sumB)/wF
		v := wB * wF * (mB - mF) * (mB - mF)
		if v > bestVar {
			bestVar, best = v, uint8(i)
		}
	}
	return best
}

// search runs the coarse sweep, then a fine sweep around its peak, and reports
// the angle and how prominent that peak was.
//
// CONFIDENCE IS MEASURED ON THE COARSE CURVE, not the fine one. The fine curve
// spans one coarse step and is nearly flat by construction even on a page with
// perfect line structure, so its own peak prominence would say every page is
// noise. The coarse curve spans the whole search and its shape is the thing that
// differs between a page of text and a photograph.
func search(pts []point, w, h int) (deg, conf, alt float64, railed bool) {
	bins := make([]int32, binCount(w, h))
	n := int(2*searchDeg/coarseStep) + 1
	scores := make([]float64, n)
	bestI, bestScore := 0, -1.0
	altI, altScore := 0, -1.0
	for i := range n {
		t := -searchDeg + float64(i)*coarseStep
		s, d := profileScore(pts, bins, w, t)
		scores[i] = s
		if s > bestScore {
			bestScore, bestI = s, i
		}
		if d > altScore {
			altScore, altI = d, i
		}
	}

	// Stepped by an integer index rather than by adding fineStep to a float, so
	// the last angle of the sweep is where the arithmetic says it is.
	steps := int(fineSpan/fineStep) + 1
	coarse := -searchDeg + float64(bestI)*coarseStep
	best := coarse
	altCoarse := -searchDeg + float64(altI)*coarseStep
	altBest := altCoarse
	for i := -steps; i <= steps; i++ {
		if t := coarse + float64(i)*fineStep; t >= -searchDeg && t <= searchDeg {
			if s, _ := profileScore(pts, bins, w, t); s > bestScore {
				bestScore, best = s, t
			}
		}
		if t := altCoarse + float64(i)*fineStep; t >= -searchDeg && t <= searchDeg {
			if _, d := profileScore(pts, bins, w, t); d > altScore {
				altScore, altBest = d, t
			}
		}
	}

	// Prominence against the MEDIAN of the coarse curve rather than its mean.
	// The peak is one of the samples the mean averages, so on a page whose lines
	// are strong it drags the mean towards itself and the prominence is reported
	// lower than it is.
	//
	// IT CHANGES THE NUMBER AND NOT ANY VERDICT, measured: swapping the median
	// for the mean moves confidence by about a sixth on the strongest fixture in
	// this package and does not move a single page across minConfidence, so no
	// test in this file fails when it is swapped. Stated because an unfalsified
	// claim in a comment is worth what it is tested at.
	med := median(scores)
	if med > 0 {
		conf = bestScore/med - 1
	}
	// Raster rows run downward and PDF user space runs upward, so the angle a
	// row-space search finds is the negative of the angle a caller adds to
	// ImageRef.Placement's atan2(b, a). Flipped here, once, rather than in every
	// caller: byb-16j.4 says this axis flip WILL be got wrong at least once, and
	// the fewer places it is written the fewer places it can be wrong.
	// AT THE RAIL IS NOT A MEASUREMENT. A band of a page with little ink, or one
	// whose structure the profile cannot resolve, has a score curve that is still
	// climbing where the sweep stops, and the argmax is then the boundary rather
	// than a peak. The first census run found 45 bands sitting at -15.0000 beside
	// two neighbours near zero, which reads as a fifteen degree fan and is
	// nothing of the kind, and 11 whole pages at +-15.0000 -- 11 of the 18 that
	// looked like more than ten degrees of skew.
	//
	// THE TEST IS ON THE COARSE INDEX, not on how close the fine answer is to the
	// boundary. The coarse sweep is what has to BRACKET the peak; if its argmax
	// is the first or last sample then the far side of the peak was never seen,
	// and the search cannot tell 15 degrees from 40. Testing the returned angle
	// against the boundary instead fails on arithmetic: the fine pass around a
	// peak at 15.0 settles at 14.975, which is inside any sensible tolerance and
	// is just as unbracketed.
	railed = bestI == 0 || bestI == n-1
	return -best, conf, -altBest, railed
}

// profileScore is the sum of squares of the projection profile at angle t.
//
// The profile total is the ink count at every angle, so this is the profile's
// variance up to a constant and is maximal where the ink is most concentrated
// into few bins -- which is where the text lines are parallel to t.
//
// Bins are one working pixel wide and are indexed by the coordinate NORMAL to t:
// p = y*cos(t) - x*sin(t) for image coordinates with y running down. Adding w
// keeps the index non-negative for every point at every angle in range, and
// binCount is sized so the far end cannot run off either -- an index computed
// from a rotation and then used unchecked is exactly the arithmetic that reads
// as a corrupted profile rather than as a panic.
//
// bins is supplied by the caller and cleared here, because search calls this a
// hundred times per page and allocating the profile inside the loop is most of
// the cost of the whole estimate.
// TWO SCORES ARE RETURNED, and the second one is not a spare. sq is the sum of
// squares and is what the angle is chosen by. diff is the sum of squared
// FIRST DIFFERENCES, which is a different statistic with a different weakness:
// it high-passes away any ink that is spread evenly across the profile -- a dark
// gutter band, a scanner's edge shadow, a black margin -- which sq counts as
// concentration and diff does not.
//
// Both peak at the same place on a page whose lines are real, so their
// DISAGREEMENT is a check the census can run on the corpus itself rather than
// only on synthetic pages. That matters here: every fixture in this package is
// one I drew, and a fixture cannot find a case I did not think of.
func profileScore(pts []point, bins []int32, w int, t float64) (sq, diff float64) {
	clear(bins)
	rad := t * math.Pi / 180
	sin, cos := math.Sin(rad), math.Cos(rad)
	off := float64(w) + 1
	for _, p := range pts {
		i := int(float64(p.y)*cos - float64(p.x)*sin + off)
		bins[i]++
	}
	prev := 0.0
	for _, c := range bins {
		v := float64(c)
		sq += v * v
		d := v - prev
		diff += d * d
		prev = v
	}
	return sq, diff
}

// binCount sizes the profile so that no angle in the search range can index off
// either end.
//
// The projection of the image's own corners spans at most h*cos(t) on one axis
// and w*|sin(t)| on the other, and |sin(t)| <= sin(15 deg) < 0.26 over the whole
// range. Allowing a full w either side of the h span is loose by a factor of
// four and costs a few kilobytes; sizing it tightly to the trigonometry would
// make the bound depend on searchDeg staying where it is.
func binCount(w, h int) int { return 2*w + h + 3 }

func median(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}
