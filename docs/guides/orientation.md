# Orientation metadata: well-formed files match img2pdf, ambiguous ones rotate 0

`EXIFRotation` / `EXIFOrientation` (embed.go) read a JPEG's or PNG's orientation
for `BuildPage.Rotate`.

**Well-formed files** get exactly what img2pdf (Pillow) makes of them
(`testdata/img2pdf/oracle.json`).

**Ambiguous files** get rotation 0 and `Orientation.Ambiguous = true`, whatever
img2pdf makes of them (owner decision 2026-11-09). Ambiguous means the
orientation metadata is:

- duplicated: more than one eXIf chunk, `exif` tEXt, `Raw profile type exif`
  text or XMP packet in a PNG;
- conflicting: the Exif orientation and the XMP orientation differ;
- placed after the first IDAT (any of those sources);
- an iTXt chunk (of a key we read) whose language, translated key or text is
  not valid UTF-8.

kleio records it as `ingest.orientation=ambiguous` in `inproc_route`.

**Parked** (an error, as img2pdf refuses the file for a real defect): a bad CRC
on a chunk before the first IDAT; PNG text past `MaxPNGTextBytes` (64 MiB;
every zTXt and compressed iTXt counts); a zTXt or iTXt keyed `exif` (img2pdf
raises a TypeError); a block that is not a TIFF; an orientation outside 1-8
(mirrored 2, 4, 5, 7 are `ErrMirroredOrientation`).

JPEG orientation keeps Pillow's rules unchanged except the Exif/XMP conflict.
