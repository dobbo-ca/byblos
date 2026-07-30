#!/usr/bin/env bash
set -uo pipefail
S=~/work/dobbo-ca/.byblos-sample
UA="byblos-corpus/1.0 (chris@dobbo.ca)"
for spec in \
  "DTIC_ADA383635/DTIC_ADA383635.pdf" \
  "DTIC_ADA134285/DTIC_ADA134285.pdf" \
  "municipaldocume00masgoog/municipaldocume00masgoog.pdf" \
  "revistadasocied03portgoog/revistadasocied03portgoog.pdf" \
  "journalfrtechni13erdmgoog/journalfrtechni13erdmgoog.pdf" \
  "06043926.cn/06043926.cn.pdf" ; do
  id="${spec%%/*}"; fn="${spec##*/}"
  out="$S/anchors/pdfs/ia-$fn"
  [ -s "$out" ] && { echo "HAVE $fn"; continue; }
  code=$(curl -sS -L -A "$UA" --retry 5 --retry-delay 5 --retry-all-errors \
       -o "$out.part" -w '%{http_code}' "https://archive.org/download/$spec")
  if [ "$code" = 200 ] && [ "$(head -c 5 "$out.part")" = "%PDF-" ]; then
    mv "$out.part" "$out"; echo "OK $code $(wc -c < "$out" | tr -d ' ') $fn"
  else
    rm -f "$out.part"; echo "FAIL $code $fn"
  fi
done
