package pdfdoc

// The structural-optimize seam (byb-b5). Optimize wraps pdfcpu's own
// api.Optimize: a read-validate-optimize-then-write pass that dedupes shared
// resources (optimizeFontAndImages), fixes references to free objects, and
// writes with object/xref streams on by default (model.Configuration's
// WriteObjectStream/WriteXRefStream). The size and provenance policy around
// "is the result actually smaller" lives in byblos.Optimize, not here — this
// seam only runs the pdfcpu pass and turns its panics into errors, the same
// contract every other pdfcpu-facing call in this package keeps.

import (
	"fmt"
	"io"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// Optimize runs pdfcpu's structural optimize pass over rs and writes the
// result to w. It validates rs first (api.Optimize is
// ReadValidateAndOptimize + WriteContext underneath), so a malformed or
// non-PDF rs is reported here rather than producing corrupt output.
//
// Those two calls are made here directly rather than through api.Optimize:
// the context must be visible between read and write so the input's Info
// dates can be captured before pdfcpu's writer stamps over them, and the
// write goes through writePinned for byte-deterministic output (byb-c53,
// deterministic.go).
func Optimize(rs io.ReadSeeker, w io.Writer) (err error) {
	defer catchPanic("optimize", &err)
	ctx, err := api.ReadValidateAndOptimize(rs, defaultConfig())
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: optimize: %w", err)
	}
	creation, mod := infoDates(ctx)
	if err := writePinned(ctx, w, creation, mod, ctx.ID != nil); err != nil {
		return fmt.Errorf("byblos/pdfdoc: optimize: %w", err)
	}
	return nil
}
