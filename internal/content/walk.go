package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
)

// Matrix is a PDF transformation matrix [a b c d e f] in the row-vector
// convention of ISO 32000-1 section 8.3.3:
//
//	[ a b 0 ]
//	[ c d 0 ]
//	[ e f 1 ]
type Matrix [6]float64

// Identity is the identity transform.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

// Mul returns the matrix that applies m first and then n.
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// Apply maps a point through m.
func (m Matrix) Apply(x, y float64) (float64, float64) {
	return x*m[0] + y*m[2] + m[4], x*m[1] + y*m[3] + m[5]
}

// Box is an axis-aligned rectangle in PDF user space: points, origin
// lower-left, y increasing upward.
type Box struct{ LLX, LLY, URX, URY float64 }

// UnitSquareBox returns the bounding box of the unit square mapped through m.
// An image XObject always occupies the unit square in its own space
// (ISO 32000-1 section 8.9.5.2), so this is exactly where the image lands.
func (m Matrix) UnitSquareBox() Box {
	x0, y0 := m.Apply(0, 0)
	x1, y1 := m.Apply(1, 0)
	x2, y2 := m.Apply(0, 1)
	x3, y3 := m.Apply(1, 1)
	return Box{
		LLX: min(min(x0, x1), min(x2, x3)),
		LLY: min(min(y0, y1), min(y2, y3)),
		URX: max(max(x0, x1), max(x2, x3)),
		URY: max(max(y0, y1), max(y2, y3)),
	}
}

// Quad is a quadrilateral's four vertices, x then y, in ring order:
// (0,0) (1,0) (1,1) (0,1) mapped through some matrix. Unlike Box, it is not
// axis-aligned -- a rotated matrix leaves it a true parallelogram rather than
// collapsing it to a bounding box.
type Quad [8]float64

// UnitSquareQuad returns the unit square's four corners mapped through m, in
// ring order. An image XObject always occupies the unit square in its own
// space (ISO 32000-1 8.9.5.2), so this is exactly where the image's true
// outline lands -- UnitSquareBox is the same mapping with the ring order lost
// to an axis-aligned box.
func (m Matrix) UnitSquareQuad() Quad {
	x0, y0 := m.Apply(0, 0)
	x1, y1 := m.Apply(1, 0)
	x2, y2 := m.Apply(1, 1)
	x3, y3 := m.Apply(0, 1)
	return Quad{x0, y0, x1, y1, x2, y2, x3, y3}
}

// shoelace returns twice q's signed area: positive for a counter-clockwise
// ring, negative for clockwise, zero for a degenerate (collinear or
// zero-area) one.
func (q Quad) shoelace() float64 {
	var twoA float64
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		twoA += q[2*i]*q[2*j+1] - q[2*j]*q[2*i+1]
	}
	return twoA
}

// containsPoints reports whether every point in pts (x,y pairs) lies inside
// q, allowing each up to tol points of perpendicular distance outside an
// edge. It is the one half-plane test the box and quad containment wrappers
// below both build on.
//
// A zero-area q (collinear or coincident vertices) contains nothing: at page
// scale |2*area| is on the order of 1e6 pt^2, so an exact-zero result is
// float noise only when the quad was already degenerate, and false matches
// the package's other degenerate guards.
//
// The winding direction is measured, not assumed. A reflected matrix (a
// mirrored placement) produces a clockwise ring, and testing against a fixed
// counter-clockwise assumption would invert every mirror's containment
// answer.
func (q Quad) containsPoints(pts []float64, tol float64) bool {
	twoA := q.shoelace()
	if twoA == 0 {
		return false
	}
	s := 1.0
	if twoA < 0 {
		s = -1.0
	}
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		px, py := q[2*i], q[2*i+1]
		ex, ey := q[2*j]-px, q[2*j+1]-py
		l := math.Hypot(ex, ey)
		if l == 0 {
			return false
		}
		for k := 0; k+1 < len(pts); k += 2 {
			tx, ty := pts[k], pts[k+1]
			cross := ex*(ty-py) - ey*(tx-px)
			// Dividing by l turns cross (a scaled area) into a perpendicular
			// distance in points, which is what makes tol mean points rather
			// than something that scales with the edge's length.
			if s*cross/l < -tol {
				return false
			}
		}
	}
	return true
}

// ContainsBox reports whether b's four corners all lie inside q, within tol
// points.
func (q Quad) ContainsBox(b Box, tol float64) bool {
	return q.containsPoints([]float64{
		b.LLX, b.LLY,
		b.URX, b.LLY,
		b.URX, b.URY,
		b.LLX, b.URY,
	}, tol)
}

// ContainsQuad reports whether o's four vertices all lie inside q, within tol
// points.
func (q Quad) ContainsQuad(o Quad, tol float64) bool {
	return q.containsPoints(o[:], tol)
}

// XObject is a resolved /XObject resource. ID is caller-assigned identity
// echoed back in Placement.ID; pdfdoc uses the PDF object number, so that an
// image named Im0 inside a form is never confused with the page's own Im0.
type XObject struct {
	Image   bool
	ID      int
	Content []byte // form only: the decoded content stream
	Matrix  Matrix // form only: its /Matrix, Identity when absent
	Scope   int    // form only: the scope handle for its own resources
	// BBox is a form's /BBox, in the form's own coordinate system, or nil when
	// the form has none (malformed, or unread by the caller). A nil BBox
	// applies no BBox-derived clipping at all -- it does not mean "clip to
	// nothing" -- so a form missing it behaves exactly as it did before
	// byb-b1.12.
	BBox *Box
}

// Env resolves resource names encountered during a walk. Scopes are opaque
// handles into the caller's resource tree; the caller chooses the numbering and
// Walk only passes them back.
type Env interface {
	XObject(scope int, name string) (XObject, bool)
	// ExtGStateOpaque reports whether the named /ExtGState leaves painting
	// fully opaque: no /ca or /CA below 1 and no soft mask. A name that does
	// not resolve must be reported as not opaque — a walk cannot vouch for a
	// dictionary it could not read.
	ExtGStateOpaque(scope int, name string) bool
	// Font resolves the named /Font resource to a caller-assigned identity,
	// echoed back in TextShow.FontID; pdfdoc uses the PDF object number. A
	// name that does not resolve reports false, and shows made with it carry
	// FontID 0 — the show is still recorded, so "text whose font I could not
	// read" stays visible rather than silently empty.
	Font(scope int, name string) (int, bool)
}

// Placement is one painting of an image XObject, or of an inline image (see
// Inline).
type Placement struct {
	Name string // resource name at the point of use, for diagnostics
	ID   int
	CTM  Matrix
	Box  Box
	// Inline reports a BI ... ID ... EI operator rather than a Do naming an
	// image XObject. An inline image has no resource name and no object
	// number -- Name is "" and ID is 0 -- because ISO 32000-1 8.9.7 embeds it
	// in the content stream itself; the box is still the CTM's unit square,
	// the same convention 8.9.5.1 gives an XObject image, which is why no
	// other field here needs a separate inline path.
	Inline bool
	// Clip is the clip in effect at the moment of painting -- the running
	// intersection of every W/W* n path and Form /BBox on gstate, in device
	// space -- or nil when nothing clipped this placement. It is the clip
	// itself, not Box: Box is already Clip intersected with CTM.UnitSquareBox,
	// so a caller wanting to know whether Clip actually narrowed anything
	// compares Box against CTM.UnitSquareBox() directly.
	Clip *Box
	// Opaque reports the graphics state at the moment of painting, and nothing
	// else: no /ca, /CA or soft mask was in effect. The image's own /SMask,
	// /Mask and /ImageMask are dictionary facts a walk never sees, so a caller
	// deciding whether this placement hides what is under it has to check those
	// separately.
	Opaque bool
	// Index is this painting's position in the page's painting order, counted
	// across images and paths together. Scan.Images is already in paint order,
	// so this adds nothing between two images; what it adds is the comparison
	// against Paint.Index, which is how a background wash is told from an
	// overlay.
	Index int
}

// Color is the colour a paint operator was issued with, exactly as the content
// stream stated it: the operands of the last colour operator, and the name of
// the colour space when cs or CS named one.
//
// It is deliberately not resolved to device values. Resolving means following
// /ColorSpace resources through ICCBased, Indexed, Separation and DeviceN, and
// the components alone do not say what they mean — `1 1 1 scn` is white in an
// ICCBased RGB space and nearly black in a three-ink DeviceN, and the page
// byb-b1.5 was measured on writes exactly that. Occlusion is decided by
// painting order, geometry and opacity, which need none of it; this is recorded
// so the question can be measured before anyone builds the machinery.
type Color struct {
	Space string    // "DeviceGray", "DeviceRGB", "DeviceCMYK", or the cs/CS resource name
	Comps []float64 // as written; empty when a space was named but no colour set
}

// Paint is one path-painting operator: where its path landed in device space,
// and the colours in force when it was issued.
type Paint struct {
	Op     string // the painting operator, for diagnostics
	Box    Box    // device-space bounds of the path, stroke spread included
	Fill   Color
	Stroke Color
	Index  int // painting order across the page, shared with Placement.Index
	// Clip is the clip in effect at the moment of painting, in device space, or
	// nil when nothing clipped this path. It is the same running intersection
	// Placement.Clip records, captured at the path's own moment rather than the
	// raster's -- the two differ whenever a Form /BBox trims one and not the
	// other. Box is deliberately NOT narrowed by it; see Ink.
	Clip *Box
	// CTM is the graphics-state matrix in effect when the path was painted.
	// Box is only ever an axis-aligned bounding box of the path's true (possibly
	// rotated) shape, so a caller comparing Box against a rotated placement's
	// true quadrilateral needs to know whether this path shares that rotation
	// (byb-2mt): if it does, Box's corners are not real points of the path and
	// testing them against the quad is a category error (see inkHidden).
	CTM Matrix
}

// Ink is the part of the path that can deposit marks: Box intersected with the
// clip that was in force, and whether anything is left.
//
// Box and Clip are kept apart so this question is asked once, by the caller that
// needs it, rather than baked into the record. byb-7aq is why the question
// exists: byb-b1.12 narrowed Placement.Box to the visible rectangle and left
// Paint.Box unnarrowed, so a consumer comparing the two compared a clipped
// rectangle against an unclipped one. Four govdocs1 pages stopped extracting on
// that mismatch alone, each a full-bleed wash drawn oversized and then clipped
// back to the page by the very clip that also bounds the raster.
//
// Two separate things bound no ink, and they are not the same question.
//
// A clip DISJOINT from the path removes the path entirely, whatever operator
// painted it. Nothing is left to mark, so a stroke gets no exception here.
//
// A path that bounds no area on its OWN is a degenerate fill -- `0 842 m 0 842
// l f`, which govdocs1/050104.pdf p2 opens with -- and marks nothing either.
// The exception is a stroke: recordPaint has already spread a stroke's box by
// half the line width, so a stroked path is zero-area only when the pen is zero
// wide, and ISO 32000-1 8.4.3.2 makes THAT the thinnest line the device can
// render -- a hairline, which marks. Reporting it as no ink would extract a
// page carrying a visible line the raster does not.
//
// Testing disjointness against Box rather than against the intersection is what
// keeps the two apart: intersectBox clamps a disjoint pair to a degenerate box,
// so by the time it has run, "clipped away" and "a hairline" look identical.
// Strokes reports whether this operator lays ink along the path as well as, or
// instead of, inside it.
//
// A caller needs this to tell a stroke's box from a fill's, because the two
// carry different guarantees: recordPaint has already spread a stroke's box by
// half the line width, so a stroke's Box includes ink the path itself does not
// bound, while a fill's Box is the path. The operator vocabulary lives here,
// with the walk that writes it, rather than being restated by every caller.
func (p Paint) Strokes() bool { return strokingOps[p.Op] }

func (p Paint) Ink() (Box, bool) {
	box := p.Box
	if p.Clip == nil {
		return box, strokingOps[p.Op] || (box.URX > box.LLX && box.URY > box.LLY)
	}
	c := *p.Clip
	if c.URX <= box.LLX || c.LLX >= box.URX || c.URY <= box.LLY || c.LLY >= box.URY {
		return intersectBox(c, box), false
	}
	ink := intersectBox(c, box)
	return ink, strokingOps[p.Op] || (ink.URX > ink.LLX && ink.URY > ink.LLY)
}

// TextShow is one string shown by Tj, TJ, ' or ": the bytes exactly as
// stored, and the text state in force when they were shown. The walk decodes
// nothing — mapping Raw to Unicode is byb-lez.5's business and word
// boundaries are byb-lez.7's — it only records where each string started and
// with what parameters.
//
// Trm is the text matrix composed with the CTM at the START of this string:
// text-space (0,0) through Trm is the string's origin. It deliberately does
// not fold in Size, Hscale or Rise; a consumer wanting the full rendering
// matrix of ISO 32000-1 9.4.4 builds it from those fields. The walk advances
// the text matrix through every positioning operator and every TJ numeric
// adjustment — which is why each element of a TJ carries its own Trm — but
// NOT through the glyphs of the string itself: glyph advances need the font's
// width tables, which live behind FontID and are byb-lez.5's to read.
type TextShow struct {
	Raw         []byte  // string bytes as stored, undecoded
	Font        string  // Tf resource name, "" when no Tf was seen
	FontID      int     // Env.Font's identity for it, 0 when unresolved
	Size        float64 // Tf's size operand (Tfs)
	CharSpacing float64 // Tc, in unscaled text space units
	WordSpacing float64 // Tw
	Hscale      float64 // Tz / 100, initially 1
	Rise        float64 // Ts
	Mode        int     // rendering mode (Tr); see inksGlyphs
	Trm         Matrix
	Index       int // painting order, shared with Placement and Paint
}

// Scan is what a content-stream walk observed, including everything reached
// through Form XObjects.
type Scan struct {
	// Images is in paint order: index 0 is painted first and the last index
	// lands on top. Form XObjects are walked where they are invoked, so a
	// placement inside a form sits between the placements around the Do that
	// reached it. Classification reads occlusion straight off this order.
	Images []Placement
	// InlineImages is a BI ... EI placement per InlineImgs, kept separate from
	// Images so the many callers that treat Images as "the page's image
	// XObjects" (extract.go's classify, optimize.go's recompress,
	// byblos-ctm-census, byblos-annots) are unaffected by byb-js5.6: Inspect
	// merges the two back into paint order via Index, but nothing else has to.
	InlineImages []Placement
	TextChars    int // bytes shown by Tj, TJ, ' and "
	TextOps      int // number of text-showing operators
	// InkedTextOps counts only the text-showing operators that were in a
	// rendering mode which actually paints glyphs. Almost all text on a scanned
	// page is an invisible OCR layer (3 Tr) and deposits nothing; classification
	// wants this count, not TextOps.
	InkedTextOps int
	// TextShows is in paint order too, one entry per string shown — a TJ
	// contributes one per string element, because the numeric adjustments
	// between them move the text matrix. TextChars, TextOps and InkedTextOps
	// above are unchanged by it: they are the counters classification already
	// reads, and this is the record byb-lez.5/.7 and the renderer read.
	TextShows []TextShow
	// Paints is in paint order too, and interleaves with Images through the
	// shared Index. Clipping alone does not appear here.
	Paints     []Paint
	ShadingOps int      // sh
	InlineImgs int      // BI ... EI
	Unresolved []string // Do operands that did not resolve

	// order counts painting events, so that Paint.Index and Placement.Index can
	// be compared. It is walk state, not part of what a walk reports.
	order int
}

const (
	// maxFormDepth bounds Form XObject recursion. Real documents nest two or
	// three deep; anything beyond this is malformed or hostile.
	maxFormDepth = 8
	// maxOperands bounds the pending-operand buffer. Only a TJ array comes
	// close; truncating one costs a little TextChars precision on absurd input
	// and nothing else.
	maxOperands = 8192
)

// Walk interprets a decoded content stream, resolving resource names in scope
// through env.
//
// byb-b1.12 fixed the Placement half of a known simplification: a Form
// XObject's /BBox, mapped through its /Matrix and the enclosing CTM, and any
// clip path set with W/W* n, now narrow every Placement.Box painted inside
// them to the intersection with gstate.clip. A form whose BBox crops an
// oversized image, or a page that clips a placement to a corner, now reports
// the visible box rather than the raster's own oversized placement.
//
// Paint.Box is still never narrowed by a clip, and TestWalkClipDoesNotNarrowPaintBoxes
// still pins that. What byb-b1.12 got wrong was leaving the clip UNRECORDED for
// a path while recording it for a placement: a consumer then had a clipped
// rectangle on one side of its comparison and an unclipped one on the other,
// and byb-7aq measured what that costs — four govdocs1 pages that had extracted
// stopped, every one of them a full-bleed wash drawn oversized and clipped back
// to the page by the same clip that bounds the raster.
//
// "Errs toward diverting" was the argument for the gap, and it does not survive
// contact with the pages: diverting a page whose only vector mark is invisible
// is not conservative, it is wrong, and it costs an archive the page. So
// Paint.Clip now records the clip in force, and Paint.Ink composes the two.
// The record stays literal — where the path went, and what was cutting it — and
// the visibility question is answered where it is asked.
//
// Known simplification, still open after byb-b1.12: a text clipping mode (4
// Tr through 7 Tr, ISO 32000-1 9.3.6/table 106) adds glyph outlines to the
// clipping path instead of, or in addition to, painting them, but Walk only
// tracks gs.tr for inksGlyphs' painted/not-painted question — it never folds
// a text clip into gs.clip. A page whose only mark is glyphs shown in a
// clipping-only mode (7 Tr) clips everything away and shows nothing, but
// Walk reports the placement underneath as unclipped by it. This overstates
// what is visible, the same safe-but-inaccurate direction as every other gap
// named here.
// ctx is checked at the operator loop boundary, which is byb-fem. Before that
// bead Walk took no context, so a single page carrying a multi-million-operator
// content stream was uninterruptible for the whole walk: measured on a 396 KB
// single-page PDF the walk was 95.4% of an ExtractPageRasterContext call, and
// InspectContext -- which byb-xyn classes interruptible -- ignored a cancel for
// 665 ms and then returned nil. "Interruptible" meant "between pages".
//
// This is an internal package, so ctx is a plain first parameter and no
// exported signature moved; the ADD NEVER CHANGE constraint on the v0.1.0
// surface does not reach here.
// THE SCAN IS ALWAYS RETURNED, error or not. A content stream that stops
// half-way still describes the part of the page it reached, and that partial
// answer is what poppler paints for such a page -- on 050734 page 8 it is 182
// characters and 1,260 inked pixels, out of the 1,156 bytes that decode before
// the stream is damaged. Discarding it made a readable half-page indexed as
// nothing (byb-3jq). A caller that cannot use a partial page checks the error,
// which is unchanged.
func Walk(ctx context.Context, src []byte, scope int, env Env) (*Scan, error) {
	s := &Scan{}
	// ISO 32000-1 section 8.4.1's initial graphics state, for the parts tracked
	// here. The line width matters: a stroke that never sets one still spreads
	// half a point either side of its path, not nothing.
	err := walk(ctx, src, scope, env, gstate{
		ctm: Identity, opaque: true, lineWidth: 1,
		tm: Identity, tlm: Identity, hscale: 1,
	}, 0, s)
	return s, err
}

// gstate is the part of the PDF graphics state a walk tracks. q and Q save and
// restore it as a unit, which is what the spec says and what a separate CTM
// stack would get wrong for opacity.
//
// Known simplification: opacity only ever falls within a q...Q pair. An
// /ExtGState that restores /ca to 1 after an earlier one lowered it leaves the
// state reported as not opaque, so a page doing that diverts. No producer has
// been seen to do it, and the error is in the safe direction.
//
// tr is the text rendering mode. It lives in the text state (table 104), which
// is part of the graphics state, so BT/ET does not reset it: BT resets the text
// matrix only. A Form XObject inherits it at the Do and discards it on return,
// which the pass-by-value recursion in doXObject gives for free.
//
// lineWidth, fill and stroke are what a path-painting operator needs: how far
// its ink spreads, and in what colour. Dash patterns, line joins, blend modes
// and soft masks are a renderer's business (design spec section 2).
//
// clip is the running intersection of every clip path (W/W* n) and Form
// /BBox in effect, in device space, or nil when nothing has clipped yet. It
// lives on gstate for the same reason opacity does: q/Q save and restore
// gstate as a unit (case "q"/"Q" below), so nesting and restoring the clip
// falls out of that for free rather than needing its own stack.
// The text state (Tf, Tc, Tw, Tz, TL, Ts) is part of the graphics state too
// (table 52), so q/Q saving it as a unit is the spec's behaviour. The text
// matrix and line matrix are NOT — the spec scopes them to a BT...ET pair —
// but they live here anyway, because pass-by-value recursion into forms and
// q/Q nesting then handle them for free. Known simplification: a stream that
// runs `Tm ... q ... Td ... Q` INSIDE one text object gets the Td undone by
// the Q, where a strict reader would keep it. No producer has been seen to
// position text across a q/Q boundary like that, and BT resets both matrices
// anyway.
type gstate struct {
	ctm       Matrix
	opaque    bool
	tr        int
	lineWidth float64
	fill      Color
	stroke    Color
	clip      *Box

	tm, tlm  Matrix  // text matrix and line matrix; BT resets both
	font     string  // Tf resource name
	fontID   int     // Env.Font's identity for font, 0 when unresolved
	fontSize float64 // Tfs
	charSp   float64 // Tc
	wordSp   float64 // Tw
	hscale   float64 // Tz / 100, initially 1
	leading  float64 // TL
	rise     float64 // Ts
}

// nextLine moves to the start of the next line, offset by (tx, ty) from the
// start of the current one: Td, TD, T*, and the implicit advance of ' and "
// (ISO 32000-1 9.4.2). The line matrix moves and the text matrix follows it.
func (gs *gstate) nextLine(tx, ty float64) {
	gs.tlm = Matrix{1, 0, 0, 1, tx, ty}.Mul(gs.tlm)
	gs.tm = gs.tlm
}

// pathBox accumulates the device-space bounds of the path under construction.
//
// A curve contributes its control points rather than the curve itself. A Bezier
// lies inside the convex hull of its control points, so this can only
// over-estimate — and over-estimating makes a page divert rather than extract,
// the safe direction for a rule whose job is to prove a paint is hidden.
type pathBox struct {
	box Box
	set bool
}

func (p *pathBox) add(x, y float64) {
	if !p.set {
		p.box, p.set = Box{LLX: x, LLY: y, URX: x, URY: y}, true
		return
	}
	p.box.LLX = min(p.box.LLX, x)
	p.box.LLY = min(p.box.LLY, y)
	p.box.URX = max(p.box.URX, x)
	p.box.URY = max(p.box.URY, y)
}

func (p *pathBox) reset() { *p = pathBox{} }

// inksGlyphs reports whether text shown in rendering mode tr deposits any ink.
// ISO 32000-1 table 106: 3 is invisible and 7 adds to the clipping path without
// painting; every other listed mode fills, strokes, or both. A mode outside the
// table is treated as inking, because diverting a page Byblos does not
// understand is the safe direction.
func inksGlyphs(tr int) bool { return tr != 3 && tr != 7 }

func walk(ctx context.Context, src []byte, scope int, env Env, gs gstate, depth int, s *Scan) error {
	if depth > maxFormDepth {
		return fmt.Errorf("content: form XObject nesting deeper than %d", maxFormDepth)
	}
	l := NewLexer(src)
	var stack []gstate
	var path pathBox
	var ops []Token
	// pendingClip marks that W or W* was seen since the current path was
	// started: ISO 32000-1 8.5.4, "It merely sets a flag... that shall be
	// examined by the path-painting operator that follows immediately
	// after". It is local like path, not part of gstate, because it too is
	// current-path state rather than something q/Q ever needs to save.
	var pendingClip bool
	for {
		// byb-fem's boundary. Per token rather than per painting operator: the
		// pathological stream this bounds is pathological in TOKENS, and a
		// stream of four million operands with no operator would otherwise be
		// as uninterruptible as before.
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, err := l.Next()
		if err != nil {
			// End of stream is the normal exit. Match the sentinel, never the
			// error text.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if tok.Kind == KindInlineImage {
			s.InlineImgs++
			box := gs.ctm.UnitSquareBox()
			if gs.clip != nil {
				box = intersectBox(*gs.clip, box)
			}
			s.order++
			s.InlineImages = append(s.InlineImages, Placement{
				CTM: gs.ctm, Box: box, Clip: gs.clip, Opaque: gs.opaque,
				Index: s.order, Inline: true,
			})
			ops = ops[:0]
			continue
		}
		if tok.Kind != KindKeyword {
			if len(ops) < maxOperands {
				ops = append(ops, tok)
			}
			continue
		}

		op := string(tok.Text)
		switch op {
		case "q":
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
		case "gs":
			if len(ops) > 0 && ops[len(ops)-1].Kind == KindName {
				gs.opaque = gs.opaque && env.ExtGStateOpaque(scope, string(ops[len(ops)-1].Text))
			}
		case "Tr":
			if n := len(ops); n > 0 && ops[n-1].Kind == KindNumber {
				gs.tr = int(ops[n-1].Num)
			}
		case "w":
			if v, ok := lastNumber(ops); ok {
				gs.lineWidth = v
			}

		// Text state (ISO 32000-1 9.3, 9.4.2). BT resets the text and line
		// matrices to identity; ET has nothing to undo, because the matrices
		// are only meaningful between the two and the next BT resets them
		// again. The remaining parameters live on gstate, so q/Q and form
		// recursion handle them like every other graphics-state entry.
		case "BT":
			gs.tm, gs.tlm = Identity, Identity
		case "Tf":
			gs.font = lastName(ops)
			if v, ok := lastNumber(ops); ok {
				gs.fontSize = v
			}
			gs.fontID = 0
			if id, ok := env.Font(scope, gs.font); ok {
				gs.fontID = id
			}
		case "Td":
			if n := numberOperands(ops); len(n) >= 2 {
				gs.nextLine(n[len(n)-2], n[len(n)-1])
			}
		case "TD":
			// Like Td, but also sets the leading to -ty (table 108).
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
			// The operand is a percentage (table 105).
			if v, ok := lastNumber(ops); ok {
				gs.hscale = v / 100
			}
		case "Ts":
			if v, ok := lastNumber(ops); ok {
				gs.rise = v
			}

		// Colour. The lower-case operator sets the nonstroking colour and the
		// upper-case one the stroking colour (ISO 32000-1 section 8.6.8). cs and
		// CS name a space and reset the colour to that space's initial value,
		// which is why the components are dropped rather than kept.
		case "g":
			gs.fill = Color{Space: "DeviceGray", Comps: numberOperands(ops)}
		case "G":
			gs.stroke = Color{Space: "DeviceGray", Comps: numberOperands(ops)}
		case "rg":
			gs.fill = Color{Space: "DeviceRGB", Comps: numberOperands(ops)}
		case "RG":
			gs.stroke = Color{Space: "DeviceRGB", Comps: numberOperands(ops)}
		case "k":
			gs.fill = Color{Space: "DeviceCMYK", Comps: numberOperands(ops)}
		case "K":
			gs.stroke = Color{Space: "DeviceCMYK", Comps: numberOperands(ops)}
		case "cs":
			gs.fill = Color{Space: lastName(ops)}
		case "CS":
			gs.stroke = Color{Space: lastName(ops)}
		case "sc", "scn":
			gs.fill.Comps = numberOperands(ops)
		case "SC", "SCN":
			gs.stroke.Comps = numberOperands(ops)

		// Path construction. Every one of these takes its operands as coordinate
		// pairs — m and l one point, v and y two, c three — so pairing them off
		// handles all of them. h closes the subpath and introduces no new point.
		case "m", "l", "c", "v", "y":
			addPoints(&path, numberOperands(ops), gs.ctm)
		case "re":
			addRect(&path, numberOperands(ops), gs.ctm)
		case "W", "W*":
			// Neither variant paints or modifies the path; the fill rule they
			// name only matters for a self-intersecting path's interior, which a
			// bounding box can never see (ISO 32000-1 8.5.4). Both just arm the
			// flag examined below.
			pendingClip = true
		case "n":
			// n ends the path without painting it. W/W* before it still sets the
			// clip from that path, captured in device space now, before reset
			// discards it.
			if pendingClip {
				gs.clip = intersectClipPath(gs.clip, path)
				pendingClip = false
			}
			path.reset()

		case "Do":
			if err := doXObject(ctx, ops, scope, env, gs, depth, s); err != nil {
				return err
			}
		case "Tj", "'", "\"":
			s.TextOps++
			if inksGlyphs(gs.tr) {
				s.InkedTextOps++
			}
			if op == "\"" {
				// " sets the word and character spacing from its two leading
				// operands, then behaves as ' (ISO 32000-1 9.4.3 table 109).
				if n := numberOperands(ops); len(n) >= 2 {
					gs.wordSp, gs.charSp = n[len(n)-2], n[len(n)-1]
				}
			}
			if op != "Tj" {
				// ' and " advance to the next line BEFORE showing.
				gs.nextLine(0, -gs.leading)
			}
			for i := len(ops) - 1; i >= 0; i-- {
				if ops[i].Kind == KindString {
					s.TextChars += len(ops[i].Text)
					recordText(s, gs, ops[i].Text)
					break
				}
			}
		case "TJ":
			s.TextOps++
			if inksGlyphs(gs.tr) {
				s.InkedTextOps++
			}
			for _, o := range ops {
				switch o.Kind {
				case KindString:
					s.TextChars += len(o.Text)
					recordText(s, gs, o.Text)
				case KindNumber:
					// A TJ number is subtracted from the displacement, in
					// thousandths of unscaled text space (9.4.3): it moves the
					// text matrix, scaled by the font size and horizontal
					// scaling, and leaves the line matrix alone.
					gs.tm = Matrix{1, 0, 0, 1, -o.Num / 1000 * gs.fontSize * gs.hscale, 0}.Mul(gs.tm)
				}
			}
		case "S", "s", "f", "F", "f*", "B", "B*", "b", "b*":
			// The clip takes effect after WHICHEVER path-painting operator
			// follows W/W*, not just n (ISO 32000-1 8.5.4), so this captures it
			// from the path before recordPaint resets it -- and, deliberately,
			// never narrows the Paint this same operator records (see Walk's
			// doc comment).
			//
			// That last clause is why the gstate is copied. Paint.Clip records
			// the clip in force when the operator was ISSUED, and a path that
			// paints and clips in one operator is not clipped by its own W: the
			// copy is taken before the line below installs it. Reordering
			// instead is not open, because recordPaint resets the path that
			// intersectClipPath still needs.
			//
			// The difference is only visible on a stroke, and there it is the
			// whole ballgame. recordPaint spreads a stroke's box by half the
			// pen, while the clip comes from the UN-spread path, so clipping the
			// paint by its own W would shave exactly that spread off --
			// `20 w 100 100 400 600 re W S` would report a 10pt band of ink as
			// landing inside a raster it actually straddles. See
			// TestWalkAPathIsNotClippedByItsOwnW.
			painted := gs
			if pendingClip {
				gs.clip = intersectClipPath(gs.clip, path)
				pendingClip = false
			}
			recordPaint(s, &path, painted, op)
		case "sh":
			s.ShadingOps++
		}
		ops = ops[:0]
	}
}

// recordText appends one shown string with the text state in force. Raw
// aliases the lexer's freshly-decoded string buffer, never the source stream.
func recordText(s *Scan, gs gstate, raw []byte) {
	s.order++
	s.TextShows = append(s.TextShows, TextShow{
		Raw: raw, Font: gs.font, FontID: gs.fontID, Size: gs.fontSize,
		CharSpacing: gs.charSp, WordSpacing: gs.wordSp, Hscale: gs.hscale,
		Rise: gs.rise, Mode: gs.tr, Trm: gs.tm.Mul(gs.ctm), Index: s.order,
	})
}

// strokingOps are the painting operators that lay ink along the path, as well
// as or instead of inside it.
var strokingOps = map[string]bool{
	"S": true, "s": true, "B": true, "B*": true, "b": true, "b*": true,
}

// recordPaint appends the path as painted and clears it, which every painting
// operator does (ISO 32000-1 section 8.5.3).
func recordPaint(s *Scan, path *pathBox, gs gstate, op string) {
	defer path.reset()
	if !path.set {
		// A painting operator with no current path marks nothing, and giving it
		// the empty box would put a rectangle at the origin into the record.
		return
	}
	box := path.box
	if strokingOps[op] {
		// Ink spreads half the line width either side of the path. Without this
		// a border stroked along the raster's own edge would look contained by
		// it, when half the ink actually falls outside.
		r := gs.lineWidth * deviceScale(gs.ctm) / 2
		box = Box{LLX: box.LLX - r, LLY: box.LLY - r, URX: box.URX + r, URY: box.URY + r}
	}
	s.order++
	s.Paints = append(s.Paints, Paint{
		Op: op, Box: box, Fill: gs.fill, Stroke: gs.stroke, Index: s.order,
		Clip: gs.clip, CTM: gs.ctm,
	})
}

// deviceScale is how much the CTM magnifies lengths. The square root of the
// determinant is exact for the rotations and uniform scales a page placement
// carries, and for an anisotropic one it is the geometric mean rather than the
// worst axis — close enough for a pen width, and nowhere near the tolerances
// classify decides on.
func deviceScale(m Matrix) float64 {
	return math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
}

// addPoints adds every coordinate pair in nums, mapped through m.
func addPoints(p *pathBox, nums []float64, m Matrix) {
	for i := 0; i+1 < len(nums); i += 2 {
		p.add(m.Apply(nums[i], nums[i+1]))
	}
}

// addRect adds the four corners of a `re` rectangle. Width or height may be
// negative, and taking all four corners covers that without a special case.
func addRect(p *pathBox, nums []float64, m Matrix) {
	if len(nums) < 4 {
		return
	}
	n := nums[len(nums)-4:]
	x, y, w, h := n[0], n[1], n[2], n[3]
	p.add(m.Apply(x, y))
	p.add(m.Apply(x+w, y))
	p.add(m.Apply(x+w, y+h))
	p.add(m.Apply(x, y+h))
}

func doXObject(ctx context.Context, ops []Token, scope int, env Env, gs gstate, depth int, s *Scan) error {
	if len(ops) == 0 || ops[len(ops)-1].Kind != KindName {
		return nil
	}
	name := string(ops[len(ops)-1].Text)
	xo, ok := env.XObject(scope, name)
	if !ok {
		s.Unresolved = append(s.Unresolved, name)
		return nil
	}
	if xo.Image {
		box := gs.ctm.UnitSquareBox()
		if gs.clip != nil {
			box = intersectBox(*gs.clip, box)
		}
		s.order++
		s.Images = append(s.Images, Placement{
			Name: name, ID: xo.ID, CTM: gs.ctm, Box: box, Clip: gs.clip,
			Opaque: gs.opaque, Index: s.order,
		})
		return nil
	}
	gs.ctm = xo.Matrix.Mul(gs.ctm)
	if xo.BBox != nil {
		// ISO 32000-1 8.10.2: /BBox trims the form to its boundaries. It lives
		// in the form's own coordinate system, which /Matrix composed with the
		// enclosing CTM maps to device space -- the same composition just built
		// above for the recursive walk() call.
		dev := xo.BBox.mapThrough(gs.ctm)
		if gs.clip != nil {
			dev = intersectBox(*gs.clip, dev)
		}
		gs.clip = &dev
	}
	return walk(ctx, xo.Content, xo.Scope, env, gs, depth+1, s)
}

// intersectClipPath folds a just-closed clip path into clip, in device space
// (the path's points were already mapped through the CTM as they were added,
// so no further mapping happens here). A path with no points sets no clip:
// W with no current path marks nothing (ISO 32000-1 8.5.4).
func intersectClipPath(clip *Box, path pathBox) *Box {
	if !path.set {
		return clip
	}
	box := path.box
	if clip != nil {
		box = intersectBox(*clip, box)
	}
	return &box
}

// intersectBox returns the intersection of a and b. The two rectangles may
// not overlap on an axis, which would ordinarily invert that axis (max >
// min); this clamps the far edge to the near one instead, so the result
// always keeps every Box in this package's LLX<=URX, LLY<=URY invariant. A
// disjoint clip therefore reports a zero-area box at the near corner, not an
// inverted one -- see TestWalkDisjointClipReportsAZeroAreaBoxNotAnInvertedOne.
func intersectBox(a, b Box) Box {
	llx, lly := max(a.LLX, b.LLX), max(a.LLY, b.LLY)
	urx, ury := min(a.URX, b.URX), min(a.URY, b.URY)
	if urx < llx {
		urx = llx
	}
	if ury < lly {
		ury = lly
	}
	return Box{LLX: llx, LLY: lly, URX: urx, URY: ury}
}

// mapThrough returns the axis-aligned device-space bounding box of b's four
// corners mapped through m. Unlike UnitSquareBox, b need not be the unit
// square -- a form's /BBox is an arbitrary rectangle in its own space.
func (b Box) mapThrough(m Matrix) Box {
	x0, y0 := m.Apply(b.LLX, b.LLY)
	x1, y1 := m.Apply(b.URX, b.LLY)
	x2, y2 := m.Apply(b.LLX, b.URY)
	x3, y3 := m.Apply(b.URX, b.URY)
	return Box{
		LLX: min(min(x0, x1), min(x2, x3)),
		LLY: min(min(y0, y1), min(y2, y3)),
		URX: max(max(x0, x1), max(x2, x3)),
		URY: max(max(y0, y1), max(y2, y3)),
	}
}

// matrixOperands reads the six numbers a cm operator takes.
func matrixOperands(ops []Token) (Matrix, bool) {
	if len(ops) < 6 {
		return Identity, false
	}
	var m Matrix
	for i := 0; i < 6; i++ {
		t := ops[len(ops)-6+i]
		if t.Kind != KindNumber {
			return Identity, false
		}
		m[i] = t.Num
	}
	return m, true
}

// numberOperands returns the numeric operands in order. Non-numeric tokens are
// dropped rather than rejected: a malformed operand list should cost a little
// precision, not abort the walk of an otherwise readable page.
func numberOperands(ops []Token) []float64 {
	out := make([]float64, 0, len(ops))
	for _, o := range ops {
		if o.Kind == KindNumber {
			out = append(out, o.Num)
		}
	}
	return out
}

func lastNumber(ops []Token) (float64, bool) {
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].Kind == KindNumber {
			return ops[i].Num, true
		}
	}
	return 0, false
}

func lastName(ops []Token) string {
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].Kind == KindName {
			return string(ops[i].Text)
		}
	}
	return ""
}
