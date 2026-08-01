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
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Optimize runs pdfcpu's structural optimize pass over rs and writes the
// result to w. It validates rs first (api.Optimize is
// ReadValidateAndOptimize + WriteContext underneath), so a malformed or
// non-PDF rs is reported here rather than producing corrupt output.
func Optimize(rs io.ReadSeeker, w io.Writer) (err error) {
	defer catchPanic("optimize", &err)
	if err := api.Optimize(rs, w, model.NewDefaultConfiguration()); err != nil {
		return fmt.Errorf("byblos/pdfdoc: optimize: %w", err)
	}
	return nil
}
