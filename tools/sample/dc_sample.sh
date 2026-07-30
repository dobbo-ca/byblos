#!/usr/bin/env bash
# Draw a reproducible sample of public DocumentCloud documents.
#
#   dc_sample.sh <n> <outdir> [seed]
#
# Credentials come from .env at the repo root (see .env.example); the
# environment wins if both are set. .env is gitignored.
#
# This is the "dc_random" set the B1 beads measured. It is FOIA and court
# material, which is the population most likely to carry redaction stamps and
# signature widgets -- the annotations byb-b1.11 exists to count -- so a null
# result without this leg is weak evidence rather than strong.
#
# AUTHENTICATION. DocumentCloud has no API token to copy out of the UI. You
# exchange your MuckRock username and password at accounts.muckrock.com for a
# JWT, and that JWT is good for FIVE MINUTES. A sample of this size takes far
# longer, so this re-mints on a timer; a run that authenticates once dies
# partway through with 403s.
#
# RATE LIMITS. Three things keep this cheap. Enumeration costs one call per 100
# documents and is cached in ids/.ids-documentcloud.txt, so a second run makes
# ZERO enumeration calls -- delete that file only if you want a fresh
# population. Downloads come from public S3 and need no API call at all. And
# DC_MAX_API_CALLS is a hard ceiling that aborts the run rather than continuing
# past it. A preflight call checks the credentials before any of that, so a
# typo costs one call instead of eighty.
set -euo pipefail

N="$1"; OUT="$2"; SEED="${3:-20260730}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [ -f "$ROOT/.env" ]; then
  set -a; . "$ROOT/.env"; set +a
fi
: "${DOCUMENTCLOUD_USER:?set DOCUMENTCLOUD_USER in .env (your MuckRock login email)}"
: "${DOCUMENTCLOUD_PASS:?set DOCUMENTCLOUD_PASS in .env (your MuckRock password)}"
MAX_CALLS="${DC_MAX_API_CALLS:-80}"
PAGES="${DC_PAGES:-60}"   # 100 documents each; the enumeration frame

UA="byblos-corpus/1.0 (chris@dobbo.ca)"
IDS_DIR="$(dirname "$OUT")/../ids"
mkdir -p "$OUT" "$IDS_DIR"
IDS="$IDS_DIR/.ids-documentcloud.txt"

CALLS=0
spend() {
  CALLS=$((CALLS+1))
  if [ "$CALLS" -gt "$MAX_CALLS" ]; then
    echo "ABORT: hit DC_MAX_API_CALLS=$MAX_CALLS. Nothing further requested." >&2
    exit 1
  fi
}

ACCESS=""; MINTED=0
mint() {
  local now; now=$(date +%s)
  # Re-mint at four minutes. The token dies at five; the margin covers a slow
  # request already in flight.
  [ -n "$ACCESS" ] && [ $((now - MINTED)) -lt 240 ] && return 0
  spend
  # Form-encoded, not a hand-built JSON body. The endpoint accepts either, and
  # "{'username':...,'password':...}" inside a nested command substitution gets
  # brace-expanded by the shell on the comma before python ever sees it — the
  # dict arrives as two separate arguments with the braces stripped.
  # --data-urlencode also means the password is never pasted into a string this
  # script has to quote correctly.
  ACCESS=$(curl -sS -X POST "https://accounts.muckrock.com/api/token/" \
    --data-urlencode "username=$DOCUMENTCLOUD_USER" \
    --data-urlencode "password=$DOCUMENTCLOUD_PASS" \
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
  spend
  curl -sS -A "$UA" -H "Authorization: Bearer $ACCESS" "$1" -o "$2"
}

# Preflight: prove the credentials work before spending the enumeration budget.
mint
echo "auth ok (token minted; $CALLS/$MAX_CALLS calls used)" >&2

if [ ! -s "$IDS" ]; then
  # Staged through .part and moved only when the walk finishes. An enumeration
  # that stops early -- the call cap, a 429, a dropped connection -- otherwise
  # leaves a short list that the next run reuses as though it were the whole
  # population, and every later draw is silently from a truncated frame.
  : > "$IDS.part"
  # CURSOR pagination, not &page=N. The API accepts a page parameter and
  # ignores it: page=1 and page=2 return byte-identical results, so walking it
  # re-fetches the same 100 documents forever. The first attempt at this drew
  # 6,000 rows holding 101 distinct ids and nobody would have noticed from the
  # row count alone. The cursor is in each response's "next" URL, which already
  # carries the query and per_page, so it is followed verbatim.
  url="https://api.www.documentcloud.org/api/documents/search/?q=%2Baccess%3Apublic&per_page=100"
  page=1
  while [ -n "$url" ] && [ "$page" -le "$PAGES" ]; do
    api "$url" "$IDS_DIR/.dc-page.json"
    url=$(python3 -c "
import json,sys
d=json.load(open(sys.argv[1]))
rs=d.get('results',[])
with open(sys.argv[2],'a') as f:
    for r in rs:
        f.write('%s\t%s\n' % (r['id'], r.get('slug','')))
sys.stderr.write('page %s: %d results\n' % (sys.argv[3], len(rs)))
print(d.get('next') or '')" "$IDS_DIR/.dc-page.json" "$IDS.part" "$page")
    page=$((page+1))
    sleep 0.5
  done
  # Guard the invariant the page walk violated silently. A frame with far fewer
  # distinct ids than rows means pagination is repeating itself again, and a
  # draw from it is not the sample it claims to be.
  python3 -c "
import sys
rows=[l.split('\t')[0] for l in open(sys.argv[1]) if l.strip()]
u=len(set(rows))
sys.stderr.write('enumerated %d rows, %d distinct\n' % (len(rows), u))
if u < 0.9*len(rows):
    sys.stderr.write('ABORT: pagination is repeating; refusing to cache this frame\n')
    sys.exit(1)" "$IDS.part"
  mv "$IDS.part" "$IDS"
else
  echo "reusing cached enumeration ($(wc -l < "$IDS") ids); 0 API calls" >&2
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
  # Assets are on public S3 and need no Authorization header, so downloads do
  # not touch the API quota at all.
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

echo "done: $CALLS/$MAX_CALLS API calls used" >&2
