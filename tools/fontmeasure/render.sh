#!/bin/bash
# byb-8b9.6 measurement harness.
#
# Every arm goes through the SAME rasteriser (poppler), the SAME page, the SAME
# size. The only variable is the face fontconfig hands poppler for a font the
# PDF does not embed. Rendering one arm in Go and one in poppler would have
# measured two rasterisers instead of two font strategies, so it is not done.
#
#   real     -- host fontconfig untouched (on macOS: genuine Helvetica)
#   box-*    -- option (b): fontconfig starved to one synthetic face whose
#               every glyph is a rectangle at the correct Helvetica advance
#   open-*   -- option (a): fontconfig starved to one metric-compatible OPEN
#               font, which is what byblos actually bundles
#
# Usage: render.sh <pdf> <outdir> <scale-to-px> [facesdir]
set -u
PDF="$1"; OUT="$2"; PX="${3:-400}"
D="$(cd "$(dirname "$0")" && pwd)"
FACES="${4:-$D/faces}"
mkdir -p "$OUT"

pdftoppm -png -f 1 -l 1 -scale-to "$PX" "$PDF" "$OUT/real" 2>"$OUT/real.log"
for f in "$OUT"/real-*.png; do [ -e "$f" ] && mv "$f" "$OUT/real.png"; done

for ttf in "$FACES"/*.ttf; do
  [ -e "$ttf" ] || continue
  arm=$(basename "$ttf" .ttf)
  root="$OUT/fc-$arm"
  mkdir -p "$root/fonts" "$root/cache"
  cp "$ttf" "$root/fonts/"
  cat > "$root/fonts.conf" <<EOF
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
  <dir>$root/fonts</dir>
  <cachedir>$root/cache</cachedir>
</fontconfig>
EOF
  env FONTCONFIG_FILE="$root/fonts.conf" \
    pdftoppm -png -f 1 -l 1 -scale-to "$PX" "$PDF" "$OUT/$arm" 2>"$OUT/$arm.log"
  for f in "$OUT/$arm"-*.png; do [ -e "$f" ] && mv "$f" "$OUT/$arm.png"; done
  # An identical render means poppler ignored fontconfig and the arm measured
  # nothing. 850793.pdf failed exactly this way and looked like a real result,
  # so this check is not optional.
  if cmp -s "$OUT/real.png" "$OUT/$arm.png"; then
    echo "WARNING: $arm is byte-identical to real -- the face was NOT used" >&2
  fi
done
