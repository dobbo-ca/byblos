// Package jbig2 implements a lossless JBIG2 codec: generic regions in both
// directions, and symbol mode for reading.
//
// Scope is deliberately narrow. It implements only:
//
//   - the MQ arithmetic coder of ITU-T T.88 (02/2000) | ISO/IEC 14492:2001,
//     Annex E.2 for encoding and Annex E.3 for decoding, and the arithmetic
//     integer decoding procedure of Annex A;
//   - the generic region coding procedure of T.88 6.2 with GBTEMPLATE 0,
//     nominal AT pixels, and TPGDON, in both directions; and for DECODING
//     ONLY, the same procedure over all four templates with arbitrary AT
//     pixels, which is what a symbol dictionary codes its symbols with;
//   - the symbol dictionary procedure of T.88 6.5 and the text region
//     procedure of 6.4, arithmetic variants, for DECODING ONLY;
//   - the segment syntax of T.88 7.2, 7.4.1, 7.4.3, 7.4.4, 7.4.6 and 7.4.8,
//     parsed in the embedded file organization that ISO 32000-1:2008 7.4.7
//     requires of the PDF JBIG2Decode filter, and the two segment types of it
//     that the encoder emits.
//
// It implements NO refinement region, NO halftone region, NO Huffman symbol
// coding and NO MMR fallback, in either direction, and it WRITES no symbol
// dictionary. Every coding procedure here is bit-exact by construction: the
// decoded bitmap is always identical to the encoded one, so no character can
// ever be substituted for another. Lossy symbol matching -- the mechanism
// behind the 2013 Xerox scanner defect -- is rejected outright for this reason,
// not merely deferred. See FUTURE.md.
//
// THE ASYMMETRY IS THE POINT, NOT AN OMISSION (byb-9v0). Reading symbol mode
// carries none of the risk of writing it: a decoder places the symbols the
// stream tells it to place, and cannot substitute one glyph for another because
// it never decides that two glyphs are alike. It is the ENCODER that would have
// to make that judgement, and that judgement is what FUTURE.md rejects.
//
// THE TWO DIRECTIONS ARE NOT SYMMETRIC IN WHAT THEY OWE THE CALLER. The encoder
// chooses its own parameters, so it can simply not implement what it does not
// want to emit. The decoder is handed bytes by somebody else, and the MQ
// decoder returns a decision for ANY input: run it over a Huffman or MMR stream
// and it does not fail, it fills a bitmap with noise. Everything the decoder
// cannot code for is therefore rejected explicitly, by inspection of the segment
// headers, BEFORE any coded bit is read -- see ErrUnsupportedFeature. Those
// rejections are load-bearing, not defensive tidiness: without them this
// package's failure mode is a wrong raster rather than an error.
//
// This package is original work written from the published specifications. It
// is not a translation of jbig2enc, jbig2dec, OCRmyPDF, or any other
// implementation.
package jbig2
