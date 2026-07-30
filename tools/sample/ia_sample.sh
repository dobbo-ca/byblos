#!/usr/bin/env bash
# Draw a reproducible sample of scanned PDFs from one archive.org collection.
#
#   ia_sample.sh <collection> <n> <outdir> [seed]
#
# The collection's identifiers are enumerated once into ids/.ids-<collection>.txt
# and reused, so a re-run redraws the same sample rather than re-scraping. The
# id list is the part worth keeping: archive.org's scrape API makes no guarantee
# that a later enumeration returns the same population, so the file is the
# record of what was actually drawn from, and the seed alone would not
# reproduce it.
#
# Only items offering an "Image Container PDF" are taken. That format is the
# scanned-page PDF; the "Text PDF" alongside it is a re-rendered derivative.
set -euo pipefail

COLL="$1"; N="$2"; OUT="$3"; SEED="${4:-20260730}"
UA="byblos-corpus/1.0 (chris@dobbo.ca)"
IDS_DIR="$(dirname "$OUT")/../ids"
mkdir -p "$OUT" "$IDS_DIR"
IDS="$IDS_DIR/.ids-$COLL.txt"

# Keyed on the collection. Sharing one cache across collections silently
# redraws the first collection's sample for the second and downloads nothing.
if [ ! -s "$IDS" ]; then
  cursor=""; : > "$IDS"
  while :; do
    curl -sS -A "$UA" -G "https://archive.org/services/search/v1/scrape" \
      --data-urlencode "q=collection:${COLL} AND format:\"Image Container PDF\"" \
      --data-urlencode 'count=10000' --data-urlencode 'fields=identifier' \
      ${cursor:+--data-urlencode "cursor=$cursor"} -o "$IDS_DIR/.page-$COLL.json"
    python3 -c "
import json,sys
d=json.load(open(sys.argv[1]))
print('\n'.join(i['identifier'] for i in d.get('items',[])))" "$IDS_DIR/.page-$COLL.json" >> "$IDS"
    cursor=$(python3 -c "
import json,sys
print(json.load(open(sys.argv[1])).get('cursor',''))" "$IDS_DIR/.page-$COLL.json")
    [ -z "$cursor" ] && break
  done
fi
echo "population $COLL: $(wc -l < "$IDS")" >&2

python3 -c "
import random,sys
ids=[l.strip() for l in open(sys.argv[1]) if l.strip()]
random.seed(int(sys.argv[3]))
print('\n'.join(random.sample(ids, min(int(sys.argv[2]), len(ids)))))
" "$IDS" "$N" "$SEED" > "$IDS_DIR/.sample-$COLL.txt"

while read -r id; do
  [ -s "$OUT/ia-$id.pdf" ] && continue
  name=$(curl -sS -A "$UA" "https://archive.org/metadata/$id" | python3 -c "
import json,sys
d=json.load(sys.stdin)
c=[f for f in d.get('files',[]) if f.get('format')=='Image Container PDF']
print(c[0]['name'] if c else '')")
  [ -z "$name" ] && { echo "SKIP $id: no Image Container PDF" >&2; continue; }

  url="https://archive.org/download/$id/$name"
  # Stage through .part and check the magic before accepting. An HTTP error
  # body is a short blob of HTML, and left at the final name the [ -s ] resume
  # guard above would treat it as a complete download forever.
  code=$(curl -sS -L -A "$UA" --retry 5 --retry-delay 5 --retry-all-errors \
         -o "$OUT/ia-$id.pdf.part" -w '%{http_code}' "$url")
  if [ "$code" != 200 ] || [ "$(head -c 5 "$OUT/ia-$id.pdf.part")" != "%PDF-" ]; then
    rm -f "$OUT/ia-$id.pdf.part"; echo "FAIL $code $id" >&2; continue
  fi
  mv "$OUT/ia-$id.pdf.part" "$OUT/ia-$id.pdf"
  printf '%s\t%s\t%s\t%s\n' "$OUT/ia-$id.pdf" "$url" \
    "$(wc -c < "$OUT/ia-$id.pdf" | tr -d ' ')" \
    "$(shasum -a 256 "$OUT/ia-$id.pdf" | cut -d' ' -f1)"
  sleep 0.3
done < "$IDS_DIR/.sample-$COLL.txt"
