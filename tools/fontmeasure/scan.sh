#!/bin/bash
# Find worst-case pages for byb-8b9.6: page 1 uses fonts with NO embedded
# program, and carries a lot of text. Emits TSV:
#   path  nonembedded_uses  total_uses  page1_chars  fontnames
#
# NOTE: macOS has no `timeout`/`gtimeout`. An earlier version wrapped every
# poppler call in `timeout 10 ... || continue`, which failed on EVERY document
# and silently produced an empty file that looked like "the corpus has no
# non-embedded fonts". Do not reintroduce it without checking the binary exists.
S=~/work/dobbo-ca/.byblos-sample
OUT="$1"
: > "$OUT"
n=0
while IFS=$'\t' read -r path _rest; do
  [ -f "$path" ] || continue
  n=$((n+1))
  ff=$(pdffonts -f 1 -l 1 "$path" 2>/dev/null)
  [ -z "$ff" ] && continue
  body=$(printf '%s\n' "$ff" | tail -n +3)
  [ -z "$body" ] && continue
  total=$(printf '%s\n' "$body" | grep -c .)
  # emb is always the 5th field from the END (emb sub uni objID gen), which
  # survives multi-word type names like "Type 1" and "CID TrueType".
  nonemb=$(printf '%s\n' "$body" | awk 'NF>=5 && $(NF-4)=="no"' | grep -c .)
  [ "$nonemb" -eq 0 ] && continue
  chars=$(pdftotext -f 1 -l 1 "$path" - 2>/dev/null | tr -d '[:space:]' | wc -c | tr -d ' ')
  names=$(printf '%s\n' "$body" | awk 'NF>=5 && $(NF-4)=="no" {print $1}' | paste -sd, -)
  printf '%s\t%s\t%s\t%s\t%s\n' "$path" "$nonemb" "$total" "$chars" "$names" >> "$OUT"
done < <(awk -F'\t' 'NR%7==1 {print}' "$S/manifest.tsv")
echo "scanned $n documents, $(grep -c . "$OUT") with non-embedded page-1 fonts" >&2
