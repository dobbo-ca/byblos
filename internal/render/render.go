// Package render rasterises PDF vector paths into an RGBA canvas -- stage 4a
// of the renderer epic (byb-8b9, byb-8b9.1).
//
// WHY A SECOND INTERPRETER AND NOT AN EXTENSION OF internal/content's Walk:
// walk.go is a classifier whose path model is deliberately a bounding box
// (pathBox), whose fill-rule discard is deliberate (its W/W* comment), and
// whose Paint/Placement contract is pinned by a large test surface. Rendering
// needs the real path -- every subpath, every flattened curve point, the fill
// rule, and painting at each operator -- none of which classification wants
// to carry. So this package reuses the pieces that ARE shared (the lexer, the
// Matrix/Box vocabulary, the same operand conventions) and interprets the
// path and painting operators itself. The two walkers read the same tokens;
// they answer different questions.
//
// WHY NOT x/image/vector: its rasteriser carries an unresolved "// TODO:
// non-zero vs even-odd winding?" (vector.go:324,360) -- it cannot express
// even-odd at all, and byb-8b9.1 requires BOTH rules exercised. Importing it
// would also mean widening the imagecodecs_arch_test.go allowlist for a
// package that still would not do the job. The winding rule is therefore
// settled here: fills sample pixel centers against half-open scanline spans,
// nonzero sums signed crossings, even-odd takes their parity.
//
// WHY NOT x/image/draw's NearestNeighbor.Transform for stage 4b's image
// draws: it could replace the non-stencil sampling loop, but the stencil
// branch would still need an inverse-mapping loop (or a mask-image adapter),
// and the per-scanline maxFillWork charge would collapse to one up-front
// bounding-box charge -- one loop serving both branches under one budget
// shape is smaller than a loop plus a Transform call.
//
// Stage 4a scope: path construction (m l c v y re h), painting (f F f* B B*
// b b* S s n), DeviceGray/DeviceRGB colour, CTM, and a minimal rectangular
// clip (W/W* intersect the clip with the path's device bounding box, the
// same approximation walk.go uses). Stage 4b adds Do for image XObjects,
// sampled nearest-neighbor under the CTM; the caller supplies the DECODED
// pixels (see ImageFor). Stage 4c adds text: the full text state machinery
// (BT/ET, Tf, Td/TD/Tm/T*, TL/Tc/Tw/Tz/Ts/Tr, Tj/TJ/'/") and embedded
// TrueType glyph outlines (see text.go). Stage 4d adds bare-CFF (Type1C)
// glyph outlines by wrapping the CFF for the same sfnt machinery -- the
// spike outcome and the charstring work gate are documented in cff.go. Form
// XObjects, shading and inline images are later stages and are ignored here.
//
// WHY STAGE 4c MIRRORS walk.go's TEXT OPERATORS instead of consuming
// content.Walk's TextShows: Walk records where each show STARTED (one Trm
// per string), but rendering needs a position per GLYPH -- the /Widths
// advance and TJ adjustments happen inside the show -- and it needs each
// show painted in operator order between the fills around it, where Walk
// hands back a flat record after the fact. Mirroring the dozen text-state
// cases here (the same choice 4a already made for the path operators, and
// the same operator semantics walk.go pins with its own tests) is smaller
// than re-interleaving Walk's output, and the two walkers stay two readers
// of the same tokens. Deviations would show up as byb-8b9.3's oracle test
// disagreeing with walk_test.go's quad fixtures.
//
// Untrusted input: the page box and scale come out of the file, so raster
// dimensions are clamped before allocation; flattened path points and
// scanline work are charged against budgets as they accrue, in the spirit of
// internal/jbig2's decode-time budget -- refuse before allocating, never
// after.
package render

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"sort"

	"github.com/dobbo-ca/byblos/internal/content"
)

const (
	// maxRasterDim and maxRasterPixels clamp the canvas. 20000px is 66in at
	// 300 DPI, beyond any real page; 1<<26 pixels (67M) is 256 MB of RGBA,
	// roughly twice a 600 DPI A4.
	maxRasterDim    = 20000
	maxRasterPixels = 1 << 26

	// maxGStateDepth bounds the q graphics-state stack, byb-9ml. Kept equal
	// to internal/content's cap of the same name rather than imported: the
	// two packages track different gstates and only the BOUND is shared.
	maxGStateDepth = 1024
	// maxCurveDepth bounds recursive Bezier subdivision: at most 2^8 = 256
	// segments per curve, so a hostile stream of `c` operators amplifies its
	// own byte count by a bounded constant.
	maxCurveDepth = 8
	// flatTol is the flattening tolerance in device pixels: subdivision stops
	// when the control points sit within this distance of the chord.
	flatTol = 0.2
)

// Budgets are vars so tests can lower them; production code never writes them.
var (
	// maxPathPoints bounds the flattened device points held for the current
	// path. 2M points is far past any real page's most complex path.
	maxPathPoints int64 = 1 << 21
	// maxFillWork bounds scanline filling, charged one unit per active edge
	// per scanline AND one per painted pixel, across the whole Page call --
	// pixel writes dominate large fills, so a budget that skipped them let a
	// 387 KB stream of full-canvas fills buy ~33 minutes of CPU. 1<<28 units
	// is four full coats of the largest permitted canvas (maxRasterPixels).
	// Image draws charge their destination pixels here too -- the only work
	// drawImage does is one sample per DESTINATION pixel, so no separate
	// source-pixel budget exists (decoding already happened behind ImageFor,
	// under the caller's own budgets).
	maxFillWork int64 = 1 << 28
)

// Image is a decoded image XObject ready for Do to sample. Decoding is the
// caller's: byblos's extract path already decodes flate/DCT/CCITT/JBIG2, and
// this package only samples the result. For a stencil (/ImageMask true), Data
// is the decoded 1-bit gray by the usual convention -- luminance below one
// half is sample value 0, the value that marks the page under the default
// /Decode (ISO 32000-1 8.9.6.2).
type Image struct {
	Data    image.Image
	Stencil bool // /ImageMask: paint the fill colour through the stencil
	Invert  bool // stencil /Decode [1 0]: paint where the sample is 1, not 0
}

// ImageFor resolves a Do operand to a decoded image. ok=false skips the draw
// cleanly -- an unresolved name, a form XObject, or a codec byblos does not
// decode (JPX) must not stop the rest of the page from rendering. Page calls
// the resolver once per Do, so a stream repeating one name pays the decode
// each time: implementations must cache by name (or bound their own decode
// work), because only the resulting destination writes are budgeted here.
type ImageFor func(name string) (Image, bool)

// Page interprets the vector path operators of the decoded content stream src
// and rasterises them onto a white, opaque canvas. box is the page box in PDF
// points (user space, y up); scale is device pixels per point, so the canvas
// is box's size times scale with device row 0 at the TOP of the page.
// rotate is the page's /Rotate: the degrees the raster turns CLOCKWISE for
// display (ISO 32000-1 table 30), which swaps the canvas for 90 and 270.
// images resolves Do operands and fonts resolves Tf operands; either may be
// nil to render without images or without text.
func Page(ctx context.Context, src []byte, box content.Box, rotate int, scale float64, images ImageFor, fonts FontFor) (*image.RGBA, error) {
	w, h, fw, fh, err := rasterSize(box, scale)
	if err != nil {
		return nil, err
	}
	rot := ((rotate % 360) + 360) % 360
	if rot%90 != 0 {
		return nil, fmt.Errorf("render: /Rotate %d is not a multiple of 90", rotate)
	}
	if rot == 90 || rot == 270 {
		w, h = h, w
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	r := &renderer{img: img, images: images, fonts: fonts}
	// User space to device space: scale, then flip y so user URY lands on
	// device row 0, then turn the whole device space for /Rotate.
	// Row-vector convention, like content.Matrix.
	base := content.Matrix{scale, 0, 0, -scale, -box.LLX * scale, box.URY * scale}.Mul(displayTurn(rot, fw, fh))
	// ISO 32000-1 8.4.1's initial graphics state for the parts tracked here:
	// DeviceGray black for both colours, line width 1.
	ink := colorState{space: "DeviceGray", rgba: color.RGBA{0, 0, 0, 255}}
	err = r.run(ctx, src, gstate{
		ctm: base, lineWidth: 1, fill: ink, stroke: ink,
		tm: content.Identity, tlm: content.Identity, hscale: 1,
	})
	return img, err
}

// displayTurn maps the unrotated device space of a page onto the displayed one
// for /Rotate. An exact quarter turn permutes the device grid onto itself: no
// resampling happens here, only a change of which pixel a mark lands in.
//
// fw and fh are the EXACT unrotated device extents, NOT the ceiled canvas
// dimensions, and that distinction is the whole subtlety. rasterSize rounds the
// canvas UP on both axes, so a page whose size times scale is not an integer
// carries up to a pixel of slack. Poppler leaves that slack on the far edge of
// each axis BEFORE the turn, and translating by the ceiled dimension instead
// puts it on the near edge -- a whole-raster shift of one device pixel for 90,
// 180 and 270, invisible on the integral 240x160 oracle page and present on
// almost every real page at the sample harness's 400/long scale.
func displayTurn(rot int, fw, fh float64) content.Matrix {
	switch rot {
	case 90:
		return content.Matrix{0, 1, -1, 0, fh, 0}
	case 180:
		return content.Matrix{-1, 0, 0, -1, fw, fh}
	case 270:
		return content.Matrix{0, -1, 1, 0, 0, fw}
	}
	return content.Identity
}

// rasterSize returns the canvas in whole pixels AND the exact device extents it
// was rounded up from. displayTurn needs the unrounded pair: see its comment.
func rasterSize(box content.Box, scale float64) (w, h int, fw, fh float64, err error) {
	if !(scale > 0) || math.IsInf(scale, 0) {
		return 0, 0, 0, 0, fmt.Errorf("render: scale %v is not a positive finite number", scale)
	}
	fw = (box.URX - box.LLX) * scale
	fh = (box.URY - box.LLY) * scale
	if !(fw >= 1) || !(fh >= 1) {
		return 0, 0, 0, 0, fmt.Errorf("render: page box %+v at scale %v rasterises below one pixel", box, scale)
	}
	if fw > maxRasterDim || fh > maxRasterDim || fw*fh > maxRasterPixels {
		return 0, 0, 0, 0, fmt.Errorf("render: raster %gx%g exceeds the size clamp", fw, fh)
	}
	return int(math.Ceil(fw)), int(math.Ceil(fh)), fw, fh, nil
}

// gstate is the graphics state this stage tracks, saved and restored as a
// unit by q/Q like walk.go's.
type gstate struct {
	ctm       content.Matrix
	lineWidth float64
	fill      colorState
	stroke    colorState
	// clip is the device-space clip rectangle, or nil when nothing has
	// clipped yet. Rectangular only in this stage: W/W* contribute the
	// path's device bounding box, walk.go's approximation.
	clip *clipRect

	// Text state (ISO 32000-1 9.3). The parameters are graphics state (table
	// 52), so q/Q saving them as a unit is the spec's behaviour; tm and tlm
	// are strictly BT..ET-scoped but live here anyway, walk.go's documented
	// simplification, because q/Q nesting then handles them for free and BT
	// resets both.
	tm, tlm  content.Matrix
	font     *textFont // Tf resolution; nil skips shows entirely
	fontSize float64   // Tfs
	charSp   float64   // Tc
	wordSp   float64   // Tw
	hscale   float64   // Tz / 100, initially 1
	leading  float64   // TL
	rise     float64   // Ts
	tr       int       // Tr rendering mode
}

// nextLine is walk.go's: Td, TD, T*, and the implicit advance of ' and "
// move the line matrix and the text matrix follows it (ISO 32000-1 9.4.2).
func (gs *gstate) nextLine(tx, ty float64) {
	gs.tlm = content.Matrix{1, 0, 0, 1, tx, ty}.Mul(gs.tlm)
	gs.tm = gs.tlm
}

type clipRect struct{ x0, y0, x1, y1 float64 }

// colorState resolves DeviceGray/DeviceRGB operands to a device colour at
// set time. The initial value of both spaces is black, which is also what an
// unsupported space paints as -- the least-wrong mark for a stage that only
// speaks gray and RGB.
type colorState struct {
	space string
	rgba  color.RGBA
}

func clamp01(v float64) float64 { return math.Min(1, math.Max(0, v)) }

func grayColor(nums []float64) color.RGBA {
	g := 0.0
	if len(nums) > 0 {
		g = clamp01(nums[len(nums)-1])
	}
	c := uint8(math.Round(g * 255))
	return color.RGBA{c, c, c, 255}
}

func rgbColor(nums []float64) color.RGBA {
	var v [3]float64
	if len(nums) >= 3 {
		for i := range v {
			v[i] = clamp01(nums[len(nums)-3+i])
		}
	}
	return color.RGBA{
		uint8(math.Round(v[0] * 255)),
		uint8(math.Round(v[1] * 255)),
		uint8(math.Round(v[2] * 255)),
		255,
	}
}

// cmykColor is the naive (1-c)(1-k) conversion, the same shape every viewer
// uses without an ICC profile. ponytail: no colour management, so this is
// wrong under a real CMYK profile (e.g. a press's black generation); accurate
// conversion needs a profile this stage doesn't have and shouldn't grow one.
func cmykColor(nums []float64) color.RGBA {
	var v [4]float64
	if len(nums) >= 4 {
		for i := range v {
			v[i] = clamp01(nums[len(nums)-4+i])
		}
	}
	c, m, y, k := v[0], v[1], v[2], v[3]
	return color.RGBA{
		uint8(math.Round((1 - c) * (1 - k) * 255)),
		uint8(math.Round((1 - m) * (1 - k) * 255)),
		uint8(math.Round((1 - y) * (1 - k) * 255)),
		255,
	}
}

// setComps applies sc/scn operands under the current space.
func (c *colorState) setComps(nums []float64) {
	switch c.space {
	case "DeviceGray":
		c.rgba = grayColor(nums)
	case "DeviceRGB":
		c.rgba = rgbColor(nums)
	case "Pattern", "Indexed", "Separation", "DeviceN":
		// Pattern's scn operand is a pattern name, not colour components.
		// Indexed's single operand is a PALETTE INDEX, not a gray level --
		// treating it as gray would paint index 1 as near-black by
		// accident. Separation/DeviceN are subtractive: 1.0 means full ink
		// (dark), the opposite of gray 1.0 (white), so the operand-count
		// guess below is confidently wrong for them, not just imprecise.
		// cs/CS's direct name only matches these literally when a resource
		// happens to be named after its family (rare -- 8.6.5.2 routes all
		// four through the resource dict, not a bare cs operand), so this
		// is cheap insurance, not the general case. Left at the existing
		// black default (byb-6ty).
	default:
		// Any other space -- almost always an ICCBased resource name, since
		// cs/CS only ever gets a bare name and render.go has no resource
		// dict to resolve it to a real family (content.Color documents the
		// same gap, byb-b1.5). ICCBased profiles are overwhelmingly
		// stand-ins for calibrated Gray/RGB/CMYK, so the operand COUNT
		// alone recovers the intended model without any colour management
		// (byb-6ty). A name operand (a pattern) leaves nums empty and
		// matches no case here, same as before.
		//
		// No case 1: a single operand is exactly where this guess is
		// dangerous rather than merely imprecise. cs/CS in real content
		// streams is a resource name, so the literal-name exclusion above
		// never fires for Indexed/Separation/DeviceN -- they land here.
		// Indexed's one operand is a palette index, not a gray level, and
		// Separation/DeviceN's common one-channel form is subtractive (1.0
		// = full ink = dark), the opposite of gray's 1.0 = white. Measured
		// on the sample corpus (byb-6ty fix stage): guessing gray at 1
		// operand flips real /Separation tints white and regresses 14/811
		// documents against poppler (500542.pdf's masthead ink among
		// them), while 3 and 4 operands stayed a net improvement -- so only
		// those two counts get the heuristic.
		switch len(nums) {
		case 3:
			c.rgba = rgbColor(nums)
		case 4:
			c.rgba = cmykColor(nums)
		}
	}
}

type point struct{ x, y float64 }

// subpath is a run of flattened device-space points. closed records an
// explicit h (or re), which matters for stroking: a closed subpath strokes
// its closing segment and joins at the wrap.
type subpath struct {
	pts    []point
	closed bool
}

// renderer carries the canvas and the per-call budgets.
type renderer struct {
	img      *image.RGBA
	images   ImageFor
	fonts    FontFor
	fontsBy  map[string]*textFont   // Tf resolutions, nil entry = known unusable
	progsBy  map[[32]byte]*fontProg // parsed font programs by content hash
	points   int64                  // flattened points held for the current path
	fillWork int64                  // active-edge x scanline units spent
}

// path is the current path under construction, all points already in device
// space (affine maps preserve Beziers, so mapping control points through the
// CTM and flattening in device space is exact and keeps the tolerance in
// pixels).
type path struct {
	subs []subpath
	cur  point
	has  bool // a current point exists
	open bool // the last subpath accepts more segments
}

func (p *path) reset(r *renderer) {
	r.points -= int64(pathPoints(p.subs))
	*p = path{}
}

func pathPoints(subs []subpath) int {
	n := 0
	for _, s := range subs {
		n += len(s.pts)
	}
	return n
}

func (r *renderer) charge(n int) error {
	r.points += int64(n)
	if r.points > maxPathPoints {
		return fmt.Errorf("render: path exceeds %d points", maxPathPoints)
	}
	return nil
}

func (p *path) moveTo(r *renderer, pt point) error {
	if err := r.charge(1); err != nil {
		return err
	}
	p.subs = append(p.subs, subpath{pts: []point{pt}})
	p.cur, p.has, p.open = pt, true, true
	return nil
}

func (p *path) lineTo(r *renderer, pt point) error {
	if !p.has {
		return nil
	}
	if err := r.charge(1); err != nil {
		return err
	}
	if !p.open {
		// After h, a construction operator continues from the closed
		// subpath's start (ISO 32000-1 8.5.2) in a fresh subpath.
		p.subs = append(p.subs, subpath{pts: []point{p.cur}})
		p.open = true
		if err := r.charge(1); err != nil {
			return err
		}
	}
	s := &p.subs[len(p.subs)-1]
	s.pts = append(s.pts, pt)
	p.cur = pt
	return nil
}

func (p *path) curveTo(r *renderer, c1, c2, end point) error {
	if !p.has {
		return nil
	}
	var pts []point
	flattenCubic(&pts, p.cur, c1, c2, end, maxCurveDepth)
	for _, pt := range pts {
		if err := p.lineTo(r, pt); err != nil {
			return err
		}
	}
	return nil
}

func (p *path) close() {
	if p.open && len(p.subs) > 0 {
		s := &p.subs[len(p.subs)-1]
		if len(s.pts) > 1 {
			s.closed = true
		}
		p.cur = s.pts[0]
		p.open = false
	}
}

// flattenCubic appends line-segment endpoints approximating the cubic
// p0..p3. Subdivision stops when both control points lie within flatTol of
// the chord, or at depth 0 -- the cap that bounds a hostile curve's cost.
func flattenCubic(dst *[]point, p0, c1, c2, p3 point, depth int) {
	if depth == 0 || cubicFlat(p0, c1, c2, p3) {
		*dst = append(*dst, p3)
		return
	}
	// de Casteljau split at t = 1/2.
	m := func(a, b point) point { return point{(a.x + b.x) / 2, (a.y + b.y) / 2} }
	ab, bc, cd := m(p0, c1), m(c1, c2), m(c2, p3)
	abc, bcd := m(ab, bc), m(bc, cd)
	mid := m(abc, bcd)
	flattenCubic(dst, p0, ab, abc, mid, depth-1)
	flattenCubic(dst, mid, bcd, cd, p3, depth-1)
}

func cubicFlat(p0, c1, c2, p3 point) bool {
	// Distance of each control point from the chord p0-p3, against flatTol.
	dx, dy := p3.x-p0.x, p3.y-p0.y
	l := math.Hypot(dx, dy)
	if l == 0 {
		return math.Hypot(c1.x-p0.x, c1.y-p0.y) <= flatTol &&
			math.Hypot(c2.x-p0.x, c2.y-p0.y) <= flatTol
	}
	d1 := math.Abs(dx*(p0.y-c1.y) - dy*(p0.x-c1.x))
	d2 := math.Abs(dx*(p0.y-c2.y) - dy*(p0.x-c2.x))
	return d1 <= flatTol*l && d2 <= flatTol*l
}

// run is the operator loop, shaped like walk.go's: collect operands, switch
// on the keyword, reset. Unknown operators are ignored -- text, images and
// shading belong to later stages, and a viewer keeps rendering past what it
// does not speak.
func (r *renderer) run(ctx context.Context, src []byte, gs gstate) error {
	l := content.NewLexer(src)
	var stack []gstate
	var pth path
	var ops []content.Token
	// pendingClip mirrors walk.go: W/W* arm a flag the next painting
	// operator examines (ISO 32000-1 8.5.4).
	pendingClip := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, err := l.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if tok.Kind != content.KindKeyword {
			if tok.Kind != content.KindInlineImage && len(ops) < maxOperands {
				ops = append(ops, tok)
			}
			continue
		}

		op := string(tok.Text)
		switch op {
		case "q":
			// The same unbounded save byb-9ml capped in the walker, and the
			// same 328-byte-per-two-bytes ratio: this stack held a gstate
			// that is larger still. maxGStateDepth is content's, measured
			// there over 31,705 real page-walks against an observed max of
			// 18.
			if len(stack) >= maxGStateDepth {
				return fmt.Errorf("render: q nesting deeper than %d", maxGStateDepth)
			}
			stack = append(stack, gs)
		case "Q":
			if n := len(stack); n > 0 {
				gs = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if m, ok := matrixOperands(ops); ok {
				gs.ctm = m.Mul(gs.ctm)
			}
		case "w":
			if nums := numberOperands(ops); len(nums) > 0 {
				gs.lineWidth = nums[len(nums)-1]
			}

		case "g":
			gs.fill = colorState{space: "DeviceGray", rgba: grayColor(numberOperands(ops))}
		case "G":
			gs.stroke = colorState{space: "DeviceGray", rgba: grayColor(numberOperands(ops))}
		case "rg":
			gs.fill = colorState{space: "DeviceRGB", rgba: rgbColor(numberOperands(ops))}
		case "RG":
			gs.stroke = colorState{space: "DeviceRGB", rgba: rgbColor(numberOperands(ops))}
		case "cs":
			// Naming a space resets the colour to its initial value, black
			// for both device spaces (ISO 32000-1 8.6.8).
			gs.fill = colorState{space: lastName(ops), rgba: color.RGBA{0, 0, 0, 255}}
		case "CS":
			gs.stroke = colorState{space: lastName(ops), rgba: color.RGBA{0, 0, 0, 255}}
		case "sc", "scn":
			gs.fill.setComps(numberOperands(ops))
		case "SC", "SCN":
			gs.stroke.setComps(numberOperands(ops))

		// Text (stage 4c, byb-8b9.3): the same operator semantics as
		// walk.go's text state, feeding glyph outlines to the fill machinery.
		// ET needs no case -- BT resets both matrices, and the state
		// parameters persist across text objects (table 104).
		case "BT":
			gs.tm, gs.tlm = content.Identity, content.Identity
		case "Tf":
			gs.font = r.resolveFont(lastName(ops))
			if v, ok := lastNumber(ops); ok {
				gs.fontSize = v
			}
		case "Td":
			if n := numberOperands(ops); len(n) >= 2 {
				gs.nextLine(n[len(n)-2], n[len(n)-1])
			}
		case "TD":
			if n := numberOperands(ops); len(n) >= 2 {
				gs.leading = -n[len(n)-1]
				gs.nextLine(n[len(n)-2], n[len(n)-1])
			}
		case "Tm":
			if m, ok := matrixOperands(ops); ok {
				gs.tm, gs.tlm = m, m
			}
		case "T*":
			gs.nextLine(0, -gs.leading)
		case "TL":
			if v, ok := lastNumber(ops); ok {
				gs.leading = v
			}
		case "Tc":
			if v, ok := lastNumber(ops); ok {
				gs.charSp = v
			}
		case "Tw":
			if v, ok := lastNumber(ops); ok {
				gs.wordSp = v
			}
		case "Tz":
			if v, ok := lastNumber(ops); ok {
				gs.hscale = v / 100
			}
		case "Ts":
			if v, ok := lastNumber(ops); ok {
				gs.rise = v
			}
		case "Tr":
			if v, ok := lastNumber(ops); ok {
				gs.tr = int(v)
			}
		case "Tj", "'", "\"":
			if op == "\"" {
				// " sets word and character spacing from its two leading
				// operands, then behaves as ' (ISO 32000-1 table 109).
				if n := numberOperands(ops); len(n) >= 2 {
					gs.wordSp, gs.charSp = n[len(n)-2], n[len(n)-1]
				}
			}
			if op != "Tj" {
				// ' and " advance to the next line BEFORE showing.
				gs.nextLine(0, -gs.leading)
			}
			for i := len(ops) - 1; i >= 0; i-- {
				if ops[i].Kind == content.KindString {
					if err := r.showText(&gs, ops[i].Text); err != nil {
						return err
					}
					break
				}
			}
		case "TJ":
			for _, o := range ops {
				switch o.Kind {
				case content.KindString:
					if err := r.showText(&gs, o.Text); err != nil {
						return err
					}
				case content.KindNumber:
					// Subtracted from the displacement, in thousandths of
					// unscaled text space (9.4.3).
					gs.tm = content.Matrix{1, 0, 0, 1, -o.Num / 1000 * gs.fontSize * gs.hscale, 0}.Mul(gs.tm)
				}
			}

		case "m":
			if pt, ok := lastPoint(ops, gs.ctm); ok {
				if err := pth.moveTo(r, pt); err != nil {
					return err
				}
			}
		case "l":
			if pt, ok := lastPoint(ops, gs.ctm); ok {
				if err := pth.lineTo(r, pt); err != nil {
					return err
				}
			}
		case "c", "v", "y":
			if err := curveOp(r, &pth, op, numberOperands(ops), gs.ctm); err != nil {
				return err
			}
		case "re":
			if err := rectOp(r, &pth, numberOperands(ops), gs.ctm); err != nil {
				return err
			}
		case "h":
			pth.close()

		case "Do":
			if err := r.drawImage(lastName(ops), gs); err != nil {
				return err
			}

		case "W", "W*":
			// Both rules collapse to the same device bounding box here.
			pendingClip = true

		case "n", "f", "F", "f*", "B", "B*", "b", "b*", "S", "s":
			// The paint sees the gstate BEFORE its own W installs a clip: a
			// path that paints and clips in one operator is not clipped by
			// itself (same reasoning as walk.go, where it is load-bearing
			// for a stroke's spread).
			painted := gs
			if pendingClip {
				gs.clip = intersectClip(gs.clip, pth.subs)
				pendingClip = false
			}
			if err := r.paint(op, &pth, painted); err != nil {
				return err
			}
			pth.reset(r)
		}
		ops = ops[:0]
	}
}

// maxOperands matches walk.go's bound on the pending-operand buffer.
const maxOperands = 8192

func curveOp(r *renderer, p *path, op string, nums []float64, m content.Matrix) error {
	need := 6
	if op != "c" {
		need = 4
	}
	if len(nums) < need || !p.has {
		return nil
	}
	n := nums[len(nums)-need:]
	pt := func(i int) point {
		x, y := m.Apply(n[i], n[i+1])
		return point{x, y}
	}
	switch op {
	case "c":
		return p.curveTo(r, pt(0), pt(2), pt(4))
	case "v": // current point doubles as the first control point
		return p.curveTo(r, p.cur, pt(0), pt(2))
	default: // y: the endpoint doubles as the second control point
		return p.curveTo(r, pt(0), pt(2), pt(2))
	}
}

func rectOp(r *renderer, p *path, nums []float64, m content.Matrix) error {
	if len(nums) < 4 {
		return nil
	}
	n := nums[len(nums)-4:]
	x, y, w, h := n[0], n[1], n[2], n[3]
	corner := func(ux, uy float64) point {
		dx, dy := m.Apply(ux, uy)
		return point{dx, dy}
	}
	// re is m l l l h (ISO 32000-1 8.5.2, table 59): a closed subpath whose
	// current point is its start.
	if err := p.moveTo(r, corner(x, y)); err != nil {
		return err
	}
	for _, pt := range []point{corner(x+w, y), corner(x+w, y+h), corner(x, y+h)} {
		if err := p.lineTo(r, pt); err != nil {
			return err
		}
	}
	p.close()
	return nil
}

// paint dispatches one path-painting operator.
func (r *renderer) paint(op string, p *path, gs gstate) error {
	if op == "b" || op == "b*" || op == "s" {
		p.close()
	}
	fill, evenOdd, stroke := false, false, false
	switch op {
	case "f", "F":
		fill = true
	case "f*":
		fill, evenOdd = true, true
	case "B", "b":
		fill, stroke = true, true
	case "B*", "b*":
		fill, evenOdd, stroke = true, true, true
	case "S", "s":
		stroke = true
	}
	clip := r.deviceClip(gs.clip)
	if fill {
		if err := r.fillSubpaths(p.subs, evenOdd, clip, gs.fill.rgba); err != nil {
			return err
		}
	}
	if stroke {
		if err := r.strokeSubpaths(p.subs, gs, clip); err != nil {
			return err
		}
	}
	return nil
}

// deviceClip converts the gstate clip to a canvas-bounded pixel-center test
// range. With no clip it is the whole canvas.
func (r *renderer) deviceClip(c *clipRect) clipRect {
	b := r.img.Bounds()
	full := clipRect{0, 0, float64(b.Dx()), float64(b.Dy())}
	if c == nil {
		return full
	}
	return clipRect{
		math.Max(full.x0, c.x0), math.Max(full.y0, c.y0),
		math.Min(full.x1, c.x1), math.Min(full.y1, c.y1),
	}
}

func intersectClip(clip *clipRect, subs []subpath) *clipRect {
	first := true
	var b clipRect
	for _, s := range subs {
		for _, pt := range s.pts {
			if first {
				b = clipRect{pt.x, pt.y, pt.x, pt.y}
				first = false
				continue
			}
			b.x0 = math.Min(b.x0, pt.x)
			b.y0 = math.Min(b.y0, pt.y)
			b.x1 = math.Max(b.x1, pt.x)
			b.y1 = math.Max(b.y1, pt.y)
		}
	}
	if first {
		return clip
	}
	if clip != nil {
		b.x0 = math.Max(b.x0, clip.x0)
		b.y0 = math.Max(b.y0, clip.y0)
		b.x1 = math.Min(b.x1, clip.x1)
		b.y1 = math.Min(b.y1, clip.y1)
	}
	return &b
}

// edge is one non-horizontal line segment, stored top-down in device space.
type edge struct {
	ytop, ybot float64
	x, dxdy    float64
	dir        int // +1 when the segment ran downward, -1 upward
}

func addEdge(edges []edge, a, b point) []edge {
	if a.y == b.y {
		return edges
	}
	dir := 1
	if a.y > b.y {
		a, b, dir = b, a, -1
	}
	return append(edges, edge{ytop: a.y, ybot: b.y, x: a.x, dxdy: (b.x - a.x) / (b.y - a.y), dir: dir})
}

// fillSubpaths scanline-fills the subpaths (each implicitly closed, as every
// fill operator does) under the chosen winding rule. A pixel is painted when
// its center (x+0.5, y+0.5) is inside: crossings are counted over the
// half-open span [ytop, ybot), which is what keeps a vertex from counting
// twice.
func (r *renderer) fillSubpaths(subs []subpath, evenOdd bool, clip clipRect, col color.RGBA) error {
	var edges []edge
	for _, s := range subs {
		if len(s.pts) < 2 {
			continue
		}
		for i := 0; i+1 < len(s.pts); i++ {
			edges = addEdge(edges, s.pts[i], s.pts[i+1])
		}
		edges = addEdge(edges, s.pts[len(s.pts)-1], s.pts[0])
	}
	return r.fillEdges(edges, evenOdd, clip, col)
}

type crossing struct {
	x   float64
	dir int
}

func (r *renderer) fillEdges(edges []edge, evenOdd bool, clip clipRect, col color.RGBA) error {
	if len(edges) == 0 || clip.x1 <= clip.x0 || clip.y1 <= clip.y0 {
		return nil
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ytop < edges[j].ytop })
	yMin, yMax := edges[0].ytop, edges[0].ybot
	for _, e := range edges {
		yMax = math.Max(yMax, e.ybot)
	}
	y0 := int(math.Ceil(math.Max(yMin, clip.y0) - 0.5))
	y1 := int(math.Ceil(math.Min(yMax, clip.y1) - 0.5))
	px0 := int(math.Ceil(clip.x0 - 0.5))
	px1 := int(math.Ceil(clip.x1 - 0.5))

	var active []edge
	next := 0
	var xs []crossing
	for y := y0; y < y1; y++ {
		yc := float64(y) + 0.5
		for next < len(edges) && edges[next].ytop <= yc {
			active = append(active, edges[next])
			next++
		}
		live := active[:0]
		xs = xs[:0]
		for _, e := range active {
			if e.ybot <= yc {
				continue
			}
			live = append(live, e)
			if e.ytop <= yc {
				xs = append(xs, crossing{x: e.x + (yc-e.ytop)*e.dxdy, dir: e.dir})
			}
		}
		active = live
		r.fillWork += int64(len(active)) + 1
		if r.fillWork > maxFillWork {
			return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
		}
		if len(xs) == 0 {
			continue
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
		// After processing crossing i, winding describes the interval
		// (xs[i].x, xs[i+1].x). Nonzero fills where the sum of signed
		// crossings is nonzero; even-odd fills where their count is odd.
		winding := 0
		for i, c := range xs {
			if evenOdd {
				winding++
			} else {
				winding += c.dir
			}
			inside := winding != 0
			if evenOdd {
				inside = winding%2 != 0
			}
			if !inside || i+1 >= len(xs) {
				continue
			}
			// Clamp in FLOATS before the int conversion, the same way the
			// y bounds above do and for the reason drawImage spells out: on
			// amd64 a device x past 2^63 wraps to MinInt64, and min() then
			// takes it and drops the span. A finite huge CTM reaches this.
			xa := max(px0, int(math.Ceil(math.Max(c.x, clip.x0)-0.5)))
			xb := min(px1, int(math.Ceil(math.Min(xs[i+1].x, clip.x1)-0.5)))
			if xb <= xa {
				continue
			}
			r.fillWork += int64(xb - xa)
			if r.fillWork > maxFillWork {
				return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
			}
			for x := xa; x < xb; x++ {
				r.img.SetRGBA(x, y, col)
			}
		}
	}
	return nil
}

// strokeSubpaths widens each subpath's segments into rectangles and adds a
// join polygon at every shared vertex. Each polygon is filled the moment it
// is built: same-colour opaque fills union pixel-for-pixel with filling the
// whole pile at once, and the edge buffer stays O(1) instead of ~12 edges
// per budgeted path point -- accumulation let MB-scale streams peak at GiB
// of edges before the first budget check, violating the package's
// refuse-before-allocating rule. Caps are butt (the PDF default); joins are
// octagons circumscribing the round join's circle, correct for the common
// case. A width of zero is the thinnest renderable line (ISO 32000-1
// 8.4.3.2), one device pixel, and any width thinner than a pixel still
// marks -- so the half-width floors at 0.5.
func (r *renderer) strokeSubpaths(subs []subpath, gs gstate, clip clipRect) error {
	half := math.Max(gs.lineWidth*deviceScale(gs.ctm)/2, 0.5)
	edges := make([]edge, 0, 8)
	fillRing := func(pts []point) error {
		edges = edges[:0]
		for i := range pts {
			edges = addEdge(edges, pts[i], pts[(i+1)%len(pts)])
		}
		return r.fillEdges(edges, false, clip, gs.stroke.rgba)
	}
	quad := func(a, b point) error {
		dx, dy := b.x-a.x, b.y-a.y
		l := math.Hypot(dx, dy)
		if l == 0 {
			return nil
		}
		nx, ny := -dy/l*half, dx/l*half
		q := [4]point{
			{a.x + nx, a.y + ny}, {b.x + nx, b.y + ny},
			{b.x - nx, b.y - ny}, {a.x - nx, a.y - ny},
		}
		return fillRing(q[:])
	}
	// Octagon circumscribing the radius-half circle.
	join := func(c point) error {
		rad := half / math.Cos(math.Pi/8)
		var q [8]point
		for i := range q {
			a := -2 * math.Pi * float64(i) / 8
			q[i] = point{c.x + rad*math.Cos(a), c.y + rad*math.Sin(a)}
		}
		return fillRing(q[:])
	}
	for _, s := range subs {
		n := len(s.pts)
		if n < 2 {
			continue
		}
		for i := 0; i+1 < n; i++ {
			if err := quad(s.pts[i], s.pts[i+1]); err != nil {
				return err
			}
		}
		for i := 1; i+1 < n; i++ {
			if err := join(s.pts[i]); err != nil {
				return err
			}
		}
		if s.closed {
			if err := quad(s.pts[n-1], s.pts[0]); err != nil {
				return err
			}
			if err := join(s.pts[0]); err != nil {
				return err
			}
			if err := join(s.pts[n-1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// drawImage paints an image XObject under the CTM (stage 4b, byb-8b9.2). The
// image occupies the unit square in its own space (ISO 32000-1 8.9.5.2), so
// each destination pixel center inside the placed square's device bounding
// box is mapped BACK through the CTM's inverse and sampled nearest-neighbor
// -- the inverse mapping is what makes rotation, flip and shear exact, where
// forward-mapping source pixels would leave seams. Source row 0 is the top of
// the unit square (v near 1), the same orientation image.Image uses.
func (r *renderer) drawImage(name string, gs gstate) error {
	if r.images == nil || name == "" {
		return nil
	}
	im, ok := r.images(name)
	if !ok || im.Data == nil {
		return nil // unresolved, undecodable, or not an image: skip the draw
	}
	sb := im.Data.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	m := gs.ctm
	det := m[0]*m[3] - m[1]*m[2]
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return nil // a singular CTM places the image with zero area
	}
	// Destination range: the unit square's device bounding box intersected
	// with the clip, under the same pixel-center convention as fills. The
	// intersection happens IN FLOATS, checked before any int conversion: a
	// huge-but-finite translation (a 301-digit operand passes matrixOperands'
	// Inf/NaN check) would overflow the conversion, which on amd64 wraps to
	// minInt64 and turns the loop below into a ~9e18-iteration hang. After
	// the guard both bounds sit inside the clip, which deviceClip bounded to
	// the canvas, so the conversions are safe; the float comparison also
	// rejects NaN bounds.
	ub := m.UnitSquareBox()
	clip := r.deviceClip(gs.clip)
	fx0 := math.Max(ub.LLX, clip.x0)
	fx1 := math.Min(ub.URX, clip.x1)
	fy0 := math.Max(ub.LLY, clip.y0)
	fy1 := math.Min(ub.URY, clip.y1)
	if !(fx0 < fx1 && fy0 < fy1) {
		return nil
	}
	x0 := int(math.Ceil(fx0 - 0.5))
	x1 := int(math.Ceil(fx1 - 0.5))
	y0 := int(math.Ceil(fy0 - 0.5))
	y1 := int(math.Ceil(fy1 - 0.5))
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	at := samplerFor(im.Data)
	for y := y0; y < y1; y++ {
		r.fillWork += int64(x1 - x0)
		if r.fillWork > maxFillWork {
			return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
		}
		dy := float64(y) + 0.5 - m[5]
		for x := x0; x < x1; x++ {
			// Row-vector inverse: [u v] = [x-e, y-f] * [[a b],[c d]]^-1.
			dx := float64(x) + 0.5 - m[4]
			u := (dx*m[3] - dy*m[2]) / det
			v := (dy*m[0] - dx*m[1]) / det
			if !(u >= 0 && u < 1 && v >= 0 && v < 1) {
				continue
			}
			sx := sb.Min.X + min(int(u*float64(sw)), sw-1)
			sy := sb.Min.Y + min(int((1-v)*float64(sh)), sh-1)
			sr, sg, sbl, sa := at(sx, sy)
			if im.Stencil {
				// The default /Decode [0 1] paints where the decoded sample
				// is 0; Invert (/Decode [1 0]) paints where it is 1
				// (ISO 32000-1 8.9.6.2).
				one := sr+sg+sbl >= 3*0x8000
				if one == im.Invert {
					r.img.SetRGBA(x, y, gs.fill.rgba)
				}
				continue
			}
			r.compose(x, y, sr, sg, sbl, sa)
		}
	}
	return nil
}

// samplerFor returns src's pixels as premultiplied 16-bit channels. The
// concrete fast paths exist because the At interface boxes one color.Color
// heap allocation per call -- measured at exactly one alloc per sampled
// destination pixel, ~2.5 GB of garbage for one budget-ceiling page.
func samplerFor(src image.Image) func(x, y int) (r, g, b, a uint32) {
	switch d := src.(type) {
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			c := d.RGBAAt(x, y)
			return uint32(c.R) * 0x101, uint32(c.G) * 0x101, uint32(c.B) * 0x101, uint32(c.A) * 0x101
		}
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			c := d.NRGBAAt(x, y)
			a := uint32(c.A)
			pm := func(v uint8) uint32 { return uint32(v) * 0x101 * a / 0xff } // color.NRGBA.RGBA's arithmetic
			return pm(c.R), pm(c.G), pm(c.B), a * 0x101
		}
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			g := uint32(d.GrayAt(x, y).Y) * 0x101
			return g, g, g, 0xffff
		}
	default:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return src.At(x, y).RGBA() }
	}
}

// compose source-over composites one premultiplied 16-bit source colour onto
// the opaque canvas -- decoded PNGs can carry alpha even though byblos adds
// no SMask handling in this stage.
func (r *renderer) compose(x, y int, sr, sg, sb, sa uint32) {
	switch sa {
	case 0:
	case 0xffff:
		r.img.SetRGBA(x, y, color.RGBA{uint8(sr >> 8), uint8(sg >> 8), uint8(sb >> 8), 255})
	default:
		d := r.img.RGBAAt(x, y)
		inv := 0xffff - sa
		blend := func(s uint32, dc uint8) uint8 {
			return uint8((s + uint32(dc)*0x101*inv/0xffff) >> 8)
		}
		r.img.SetRGBA(x, y, color.RGBA{blend(sr, d.R), blend(sg, d.G), blend(sb, d.B), blend(sa, d.A)})
	}
}

// deviceScale is walk.go's: how much the CTM magnifies lengths, exact for
// rotations and uniform scales.
func deviceScale(m content.Matrix) float64 {
	return math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
}

// Operand helpers, the same conventions as walk.go's unexported ones.

func numberOperands(ops []content.Token) []float64 {
	var nums []float64
	for _, o := range ops {
		if o.Kind == content.KindNumber {
			nums = append(nums, o.Num)
		}
	}
	return nums
}

func matrixOperands(ops []content.Token) (content.Matrix, bool) {
	nums := numberOperands(ops)
	if len(nums) < 6 {
		return content.Identity, false
	}
	var m content.Matrix
	copy(m[:], nums[len(nums)-6:])
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return content.Identity, false
		}
	}
	return m, true
}

// lastNumber is the last numeric operand, walk.go's convention. Non-finite
// values pass -- like TJ's raw adjustments they can only poison tm, and
// fillGlyph already skips any glyph whose device coordinates come out
// non-finite.
func lastNumber(ops []content.Token) (float64, bool) {
	nums := numberOperands(ops)
	if len(nums) == 0 {
		return 0, false
	}
	return nums[len(nums)-1], true
}

func lastName(ops []content.Token) string {
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].Kind == content.KindName {
			return string(ops[i].Text)
		}
	}
	return ""
}

func lastPoint(ops []content.Token, m content.Matrix) (point, bool) {
	nums := numberOperands(ops)
	if len(nums) < 2 {
		return point{}, false
	}
	x, y := m.Apply(nums[len(nums)-2], nums[len(nums)-1])
	if math.IsNaN(x) || math.IsNaN(y) {
		return point{}, false
	}
	return point{x, y}, true
}
