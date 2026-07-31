package pdfdoc

// The Info-dictionary property seam (byb-0dz). WriteProperties/ReadProperties
// are a thin wrapper over pdfcpu's own api.AddProperties/api.Properties: the
// byblos package marshals a Provenance record to JSON and hands it here as a
// single key/value pair, so this package never needs to know that record's
// shape (design spec section 3 — pdfcpu stays behind pdfdoc alone).
//
// Unlike Open, these validate. api.AddProperties and api.Properties both call
// ReadValidateAndOptimize internally and neither offers a non-validating path
// — that is pdfcpu's choice, not this package's. model.NewDefaultConfiguration
// already sets ValidationMode to ValidationRelaxed (the same mode Open would
// use if it validated at all), so this is the least strict pdfcpu allows, but
// a document Open accepts is not guaranteed to survive here.

import (
	"io"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// WriteProperties adds properties to rs's Info dictionary and writes the
// result to w, replacing any existing entries with the same keys.
func WriteProperties(rs io.ReadSeeker, w io.Writer, properties map[string]string) error {
	return api.AddProperties(rs, w, properties, model.NewDefaultConfiguration())
}

// ReadProperties returns rs's Info-dictionary properties. A key WriteProperties
// never wrote is simply absent from the result, not an error.
func ReadProperties(rs io.ReadSeeker) (map[string]string, error) {
	return api.Properties(rs, model.NewDefaultConfiguration())
}
