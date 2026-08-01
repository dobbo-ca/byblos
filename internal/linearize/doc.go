// Package linearize implements ISO 32000-1:2008 Annex F linearization: the
// object partition, the two cross-reference sections, the linearization
// parameter dictionary and the primary hint stream.
//
// It knows nothing about parsing PDF. Everything it needs arrives as a neutral
// representation -- object numbers, outgoing references, and already-serialized
// object bodies -- built by internal/pdfdoc, which is the only package allowed
// to import pdfcpu (arch_test.go). The split is not cosmetic: the hint tables
// are bit-packed, column-major, MSB-first structures where a wrong bit width
// produces a file every viewer accepts and qpdf rejects with "overflow reading
// bit stream". In front of the pdfcpu wall they can be tested against
// hand-decoded byte vectors from real linearized files; behind it they could
// only be tested by writing a PDF and reading it back.
//
// This package must import nothing outside the standard library.
// TestLinearizePackageDependsOnlyOnTheStandardLibrary (linearize_arch_test.go
// in the root package) enforces that, so an implementer cannot resolve a
// compile error by reaching for pdfcpu.
//
// Tracked as byb-1y7.
package linearize
