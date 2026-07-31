// Package jbig2 implements a lossless JBIG2 generic-region encoder.
//
// Scope is deliberately narrow. It implements only:
//
//   - the MQ arithmetic coder of ITU-T T.88 (02/2000) | ISO/IEC 14492:2001,
//     Annex E.2;
//   - the generic region coding procedure of T.88 6.2 with GBTEMPLATE 0,
//     nominal AT pixels, and TPGDON, run in the encoding direction;
//   - the segment syntax of T.88 7.2, 7.4.1, 7.4.6 and 7.4.8, emitted in the
//     embedded file organization that ISO 32000-1:2008 7.4.7 requires of the
//     PDF JBIG2Decode filter.
//
// It implements NO symbol dictionary, NO text region, NO refinement region, NO
// halftone region and NO MMR fallback. Generic-region coding is bit-exact by
// construction: the decoded bitmap is always identical to the encoded one, so
// no character can ever be substituted for another. Lossy symbol matching --
// the mechanism behind the 2013 Xerox scanner defect -- is rejected outright
// for this reason, not merely deferred. See FUTURE.md.
//
// This package is original work written from the published specifications. It
// is not a translation of jbig2enc, jbig2dec, OCRmyPDF, or any other
// implementation.
package jbig2
