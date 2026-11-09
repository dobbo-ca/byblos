# Orientation metadata: well-formed files match img2pdf, ambiguous ones rotate 0

`EXIFRotation` / `EXIFOrientation` (embed.go) read a JPEG's or PNG's orientation
for `BuildPage.Rotate`.

**Well-formed files** get exactly what img2pdf (Pillow) makes of them
(`testdata/img2pdf/oracle.json`).

**Ambiguous files** get rotation 0 and `Orientation.Ambiguous = true`, whatever
img2pdf makes of them (owner decision 2026-11-09). Ambiguous means the
orientation metadata is:

- duplicated: more than one eXIf chunk, `exif` tEXt, `Raw profile type exif`
  text in a PNG; more than one Exif APP1 segment in a JPEG; the Orientation tag (0x0112) twice in one Exif IFD0 (JPEG
  or PNG), whatever its values;
- conflicting: the Exif orientation and the XMP orientation differ;
- an XMP packet whose bytes contain "Orientation" and that is not normal. It is
  normal only if it parses completely as XML (encoding/xml, at most 1<<17 tokens
  and 1024 deep; NUL padding is not XML), holds exactly one distinct
  `tiff:Orientation` value (namespace `http://ns.adobe.com/tiff/1.0/`, any
  prefix, attribute or element, references decoded, trimmed of XML whitespace)
  that is a single digit 1..8 (not `+6` or `06`), and Pillow's regex digit is absent or equal
  to it. A packet without the word is ignored (so is one in a PNG or JPEG
  that holds a normal one as well); two normal packets (the XMP inside Exif tag
  700 included), or a normal one that disagrees with Exif, are ambiguous; with
  no Exif orientation the XMP value is used. Non-XML packets that mention
  Orientation (a bare `tiff:Orientation="6"`) are malformed, so the eight
  synthetic oracle files built that way (xmp-attr2/6/8-300dpi.jpg, png-xmp2/6.png
  and their rgba twins, xmp6-exif-noorient.jpg) are ambiguous, not img2pdf's
  rotation; img2pdf parity is required only for well-formed files;
- placed after the first IDAT (any of those sources);
- an iTXt chunk (of a key we read) whose language, translated key or text is
  not valid UTF-8.

kleio records it as `ingest.orientation=ambiguous` in `inproc_route`.

**Parked** (an error, as img2pdf refuses the file for a real defect): a bad CRC
on a chunk before the first IDAT; PNG text past `MaxPNGTextBytes` (64 MiB;
every zTXt and compressed iTXt counts); a zTXt or iTXt keyed `exif` (img2pdf
raises a TypeError); a block that is not a TIFF; an Exif orientation outside 1-8
(an XMP value outside 1-8 is ambiguous; mirrored 2, 4, 5, 7 are `ErrMirroredOrientation`). xmp-attr9.jpg is therefore ambiguous, no longer
an img2pdf error.

JPEG orientation keeps Pillow's rules unchanged except the ambiguous cases above.
