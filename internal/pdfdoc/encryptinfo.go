package pdfdoc

// Encrypt-census reads. This file exists so cmd/byblos-encrypt-census can
// classify a document's /Encrypt shape without importing pdfcpu itself: design
// spec section 3 keeps pdfcpu behind this package's own types, and
// TestOnlyPdfdocImportsPdfcpu enforces it. Same split as fontcensus.go.

import (
	"errors"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
)

// EncryptInfo is what a document Open already accepted says about its own
// /Encrypt dictionary.
type EncryptInfo struct {
	Encrypted bool
	P, V, R   int
}

// EncryptInfo reports d's /Encrypt shape. It reads the SAME context Open
// already parsed -- no second read of the source.
func (d *doc) EncryptInfo() EncryptInfo {
	if d.ctx.XRefTable.Encrypt == nil {
		return EncryptInfo{}
	}
	info := EncryptInfo{Encrypted: true}
	if e := d.ctx.XRefTable.E; e != nil {
		info.P, info.V, info.R = e.P, e.V, e.R
	}
	return info
}

// IsWrongPassword reports whether err is pdfcpu's own refusal to open a
// document because neither an empty user nor an empty owner password
// authenticated -- as opposed to any other reason Open can fail (a malformed
// file, an unsupported /V, ...), which is not evidence of a password.
func IsWrongPassword(err error) bool {
	return errors.Is(err, pdfcpu.ErrWrongPassword)
}
