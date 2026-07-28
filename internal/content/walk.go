package content

import (
	"errors"
	"fmt"
	"io"
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

// XObject is a resolved /XObject resource. ID is caller-assigned identity
// echoed back in Placement.ID; pdfdoc uses the PDF object number, so that an
// image named Im0 inside a form is never confused with the page's own Im0.
type XObject struct {
	Image   bool
	ID      int
	Content []byte // form only: the decoded content stream
	Matrix  Matrix // form only: its /Matrix, Identity when absent
	Scope   int    // form only: the scope handle for its own resources
}

// Env resolves XObject resource names encountered during a walk. Scopes are
// opaque handles into the caller's resource tree; the caller chooses the
// numbering and Walk only passes them back.
type Env interface {
	XObject(scope int, name string) (XObject, bool)
}

// Placement is one painting of an image XObject.
type Placement struct {
	Name string // resource name at the point of use, for diagnostics
	ID   int
	CTM  Matrix
	Box  Box
}

// Scan is what a content-stream walk observed, including everything reached
// through Form XObjects.
type Scan struct {
	Images     []Placement
	TextChars  int      // bytes shown by Tj, TJ, ' and "
	TextOps    int      // number of text-showing operators
	PaintOps   int      // path-painting operators; clipping alone does not count
	ShadingOps int      // sh
	InlineImgs int      // BI ... EI
	Unresolved []string // Do operands that did not resolve
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
// Known simplification: a Form XObject's /BBox clips its content, and Walk
// ignores that clip. A form whose BBox crops an oversized image will therefore
// report an oversized placement. This errs toward accepting a page as
// page-covering; revisit if the divert-rate instrumentation shows it matters.
func Walk(src []byte, scope int, env Env) (*Scan, error) {
	s := &Scan{}
	if err := walk(src, scope, env, Identity, 0, s); err != nil {
		return nil, err
	}
	return s, nil
}

func walk(src []byte, scope int, env Env, ctm Matrix, depth int, s *Scan) error {
	if depth > maxFormDepth {
		return fmt.Errorf("content: form XObject nesting deeper than %d", maxFormDepth)
	}
	l := NewLexer(src)
	var stack []Matrix
	var ops []Token
	for {
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
			ops = ops[:0]
			continue
		}
		if tok.Kind != KindKeyword {
			if len(ops) < maxOperands {
				ops = append(ops, tok)
			}
			continue
		}

		switch string(tok.Text) {
		case "q":
			stack = append(stack, ctm)
		case "Q":
			if n := len(stack); n > 0 {
				ctm = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if m, ok := matrixOperands(ops); ok {
				ctm = m.Mul(ctm)
			}
		case "Do":
			if err := doXObject(ops, scope, env, ctm, depth, s); err != nil {
				return err
			}
		case "Tj", "'", "\"":
			s.TextOps++
			for i := len(ops) - 1; i >= 0; i-- {
				if ops[i].Kind == KindString {
					s.TextChars += len(ops[i].Text)
					break
				}
			}
		case "TJ":
			s.TextOps++
			for _, o := range ops {
				if o.Kind == KindString {
					s.TextChars += len(o.Text)
				}
			}
		case "S", "s", "f", "F", "f*", "B", "B*", "b", "b*":
			s.PaintOps++
		case "sh":
			s.ShadingOps++
		}
		ops = ops[:0]
	}
}

func doXObject(ops []Token, scope int, env Env, ctm Matrix, depth int, s *Scan) error {
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
		s.Images = append(s.Images, Placement{Name: name, ID: xo.ID, CTM: ctm, Box: ctm.UnitSquareBox()})
		return nil
	}
	return walk(xo.Content, xo.Scope, env, xo.Matrix.Mul(ctm), depth+1, s)
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
