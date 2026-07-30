#!/usr/bin/env bash
# Draw a reproducible sample of public DocumentCloud documents.
#
#   DOCUMENTCLOUD_USER=you@example.com DOCUMENTCLOUD_PASS=... dc_sample.sh <n> <outdir> [seed]
#
# This is the "dc_random" set the B1 beads measured. It is FOIA and court
# material, which is the population most likely to carry redaction stamps and
# signature widgets -- the annotations byb-b1.11 exists to count -- so a null
# result without this leg is weak evidence rather than strong.
#
# AUTHENTICATION. DocumentCloud has no API token to copy out of the UI. You
# exchange your MuckRock username and password at accounts.muckrock.com for a
# JWT access token, and that token is good for FIVE MINUTES. A sample of this
# size takes far longer than that, so the script re-mints on a timer rather
# than authenticating once at the start; a run that mints once dies partway
# through with 403s.
#
# Anonymous access is not an option here: the unauthenticated quota is 500
# calls per 24 hours and this needs more than that in metadata calls alone.
set -euo pipefail

N="$1"; OUT="$2"; SEED="${3:-20260730}"
: "${DOCUMENTCLOUD_USER:?set DOCUMENTCLOUD_USER (your MuckRock login email)}"
: "${DOCUMENTCLOUD_PASS:?set DOCUMENTCLOUD_PASS (your MuckRock password)}"
UA="byblos-corpus/1.0 (chris@dobbo.ca)"
IDS_DIR="$(dirname "$OUT")/../ids"
mkdir -p "$OUT" "$IDS_DIR"
IDS="$IDS_DIR/.ids-documentcloud.txt"

ACCESS=""; MINTED=0
mint() {
  local now; now=$(date +%s)
  # Re-mint at four minutes. The token expires at five, and the margin covers a
  # slow request already in flight.
  [ -n "$ACCESS" ] && [ $((now - MINTED)) -lt 240 ] && return 0
  ACCESS=$(curl -sS -X POST "https://accounts.muckrock.com/api/token/" \
    -H "Content-Type: application/json" \
    -d "$(python3 -c "
import json,os
print(json.dumps({'username':os.environ['DOCUMENTCLOUD_USER'],
                  'password':os.environ['DOCUMENTCLOUD_PASS']}))")" \
    | python3 -c "
import json,sys
d=json.load(sys.stdin)
if 'access' not in d:
    sys.stderr.write('auth failed: %s\n' % json.dumps(d)); sys.exit(1)
print(d['access'])")
  MINTED=$now
}

api() {  # api <url> <outfile>
  mint
  curl -sS -A "$UA" -H "Authorization: Bearer $ACCESS" "$1" -o "$2"
}

if [ ! -s "$IDS" ]; then
  : > "$IDS"
  page=1
  while [ "$page" -le 60 ]; do
    api "https://api.www.documentcloud.org/api/documents/search/?q=%2Baccess%3Apublic&per_page=100&page=$page" \
        "$IDS_DIR/.dc-page.json"
    n=$(python3 -c "
import json,sys
d=json.load(open(sys.argv[1]))
rs=d.get('results',[])
with open(sys.argv[2],'a') as f:
    for r in rs:
        f.write('%s\t%s\n' % (r['id'], r.get('slug','')))
print(len(rs))" "$IDS_DIR/.dc-page.json" "$IDS")
    [ "$n" = "0" ] && break
    page=$((page+1))
    sleep 0.5
  done
fi
echo "population documentcloud: $(wc -l < "$IDS")" >&2

python3 -c "
import random,sys
rows=[l.strip() for l in open(sys.argv[1]) if l.strip()]
random.seed(int(sys.argv[3]))
print('\n'.join(random.sample(rows, min(int(sys.argv[2]), len(rows)))))
" "$IDS" "$N" "$SEED" > "$IDS_DIR/.sample-documentcloud.txt"

while IFS=$'\t' read -r id slug; do
  [ -z "$id" ] && continue
  [ -s "$OUT/dc-$id.pdf" ] && continue
  # Assets are on public S3 and need no Authorization header; only the API
  # calls above are authenticated.
  url="https://s3.documentcloud.org/documents/$id/$slug.pdf"
  code=$(curl -sS -L -A "$UA" --retry 5 --retry-delay 5 --retry-all-errors \
         -o "$OUT/dc-$id.pdf.part" -w '%{http_code}' "$url")
  if [ "$code" != 200 ] || [ "$(head -c 5 "$OUT/dc-$id.pdf.part")" != "%PDF-" ]; then
    rm -f "$OUT/dc-$id.pdf.part"; echo "FAIL $code $id" >&2; continue
  fi
  mv "$OUT/dc-$id.pdf.part" "$OUT/dc-$id.pdf"
  printf '%s\t%s\t%s\t%s\n' "$OUT/dc-$id.pdf" "$url" \
    "$(wc -c < "$OUT/dc-$id.pdf" | tr -d ' ')" \
    "$(shasum -a 256 "$OUT/dc-$id.pdf" | cut -d' ' -f1)"
  sleep 0.3
done < "$IDS_DIR/.sample-documentcloud.txt"
