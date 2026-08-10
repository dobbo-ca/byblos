#!/bin/bash
# Keep only candidates whose page 1 has NO image at all -- pure vector text.
#
# WHY: the first pick, 850793.pdf, scored 12,030 page-1 characters and 12/12
# non-embedded fonts, and rendered BYTE-IDENTICAL under a box font. It is a
# scanned newspaper: the characters are an invisible OCR text layer and no
# glyph is ever painted. Selecting on "has text" measures nothing. The font
# strategy can only show up where glyphs are actually drawn.
IN="$1"; OUT="$2"
: > "$OUT"
while IFS=$'\t' read -r path nonemb tot chars names; do
  n=$(pdfimages -list -f 1 -l 1 "$path" 2>/dev/null | /usr/bin/tail -n +3 | /usr/bin/grep -c . )
  if [ "${n:-1}" = "0" ]; then
    printf '%s\t%s\t%s\t%s\t%s\n' "$path" "$nonemb" "$tot" "$chars" "$names" >> "$OUT"
  fi
done < "$IN"
echo "vector-text pages: $(/usr/bin/grep -c . "$OUT") of $(/usr/bin/grep -c . "$IN")" >&2
