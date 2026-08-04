// Package jbig2 implements a lossless JBIG2 generic-region codec.
//
// Scope is deliberately narrow. It implements only:
//
//   - the MQ arithmetic coder of ITU-T T.88 (02/2000) | ISO/IEC 14492:2001,
//     Annex E.2 for encoding and Annex E.3 for decoding;
//   - the generic region coding procedure of T.88 6.2 with GBTEMPLATE 0,
//     nominal AT pixels, and TPGDON, in both directions;
//   - the segment syntax of T.88 7.2, 7.4.1, 7.4.6 and 7.4.8, emitted and
//     parsed in the embedded file organization that ISO 32000-1:2008 7.4.7
//     requires of the PDF JBIG2Decode filter.
//
// It implements NO symbol dictionary, NO text region, NO refinement region, NO
// halftone region and NO MMR fallback. Generic-region coding is bit-exact by
// construction: the decoded bitmap is always identical to the encoded one, so
// no character can ever be substituted for another. Lossy symbol matching --
// the mechanism behind the 2013 Xerox scanner defect -- is rejected outright
// for this reason, not merely deferred. See FUTURE.md.
//
// THE TWO DIRECTIONS ARE NOT SYMMETRIC IN WHAT THEY OWE THE CALLER. The encoder
// chooses its own parameters, so it can simply not implement what it does not
// want to emit. The decoder is handed bytes by somebody else, and the MQ
// decoder returns a decision for ANY input: run it over a symbol-mode or MMR
// stream and it does not fail, it fills a bitmap with noise. Everything the
// decoder cannot code for is therefore rejected explicitly, by inspection of
// the segment headers, BEFORE any coded bit is read -- see
// ErrUnsupportedFeature. Those rejections are load-bearing, not defensive
// tidiness: without them this package's failure mode is a wrong raster rather
// than an error.
//
// This package is original work written from the published specifications. It
// is not a translation of jbig2enc, jbig2dec, OCRmyPDF, or any other
// implementation.
package jbig2
