#!/usr/bin/env bash
# Assemble the bench-v1 archive from the local sample.
#
#   bench_set.sh <outdir>
#
# Writes <outdir>/bench-v1.tar.zst and <outdir>/bench-v1.tar.zst.sha256 from
# the twelve documents pinned in ids/bench-v1.tsv, and refuses to write
# anything at all if one of them is missing or no longer hashes to its recorded
# sha256. A benchmark corpus that quietly gained the wrong document would make
# every later measurement wrong in a way nothing downstream can detect.
#
# The archive is flat: byblos-bench globs DIR/*.pdf, so the consumer extracts
# into a directory of its own choosing.
#
#   mkdir -p bench-v1 && tar --zstd -xf bench-v1.tar.zst -C bench-v1
#
# Every file is staged with one fixed timestamp and no owner, so two builds of
# the same twelve documents produce the same bytes. The baseline records the
# archive's sha256 as its -benchset fingerprint, and a rebuild that moved those
# bytes would invalidate a baseline that is otherwise still good.
#
# BYBLOS_SAMPLE overrides the sample root, for the tests.
set -euo pipefail

OUT="${1:-}"
[ -n "$OUT" ] || {
  echo "bench_set.sh: usage: bench_set.sh <outdir>" >&2
  exit 2
}

HERE="$(cd "$(dirname "$0")" && pwd)"
TSV="$HERE/ids/bench-v1.tsv"
SAMPLE="${BYBLOS_SAMPLE:-$HOME/work/dobbo-ca/.byblos-sample}/anchors/pdfs"
STAMP=202601010000.00

STAGE="$(mktemp -d)"
cleanup() { rm -rf "$STAGE"; return 0; }
trap cleanup EXIT

# Every row is checked before anything is copied, so the report names all the
# faults at once rather than one per run.
faults=0
while IFS=$'\t' read -r name _ want; do
  case "$name" in '#'* | name) continue ;; esac
  [ -n "$name" ] || continue
  src="$SAMPLE/$name"
  if [ ! -f "$src" ]; then
    echo "bench_set.sh: missing $name in $SAMPLE" >&2
    faults=$((faults + 1))
    continue
  fi
  got="$(shasum -a 256 "$src" | cut -d' ' -f1)"
  if [ "$got" != "$want" ]; then
    echo "bench_set.sh: $name hashes to $got, but bench-v1.tsv records $want" >&2
    faults=$((faults + 1))
    continue
  fi
  cp "$src" "$STAGE/$name"
  touch -t "$STAMP" "$STAGE/$name"
done < "$TSV"

if [ "$faults" -gt 0 ]; then
  echo "bench_set.sh: refusing to build, $faults document(s) failed" >&2
  exit 1
fi

count="$(find "$STAGE" -name '*.pdf' | /usr/bin/grep -c .)"
if [ "$count" -eq 0 ]; then
  echo "bench_set.sh: refusing to build, no documents were staged" >&2
  exit 1
fi

mkdir -p "$OUT"
# Sorted names and no owner, so the archive does not carry the machine that
# built it.
(cd "$STAGE" && find . -name '*.pdf' | sed 's|^\./||' | sort |
  tar -cf - --uid 0 --gid 0 --uname "" --gname "" -T -) |
  zstd -19 -q -f -o "$OUT/bench-v1.tar.zst"

(cd "$OUT" && shasum -a 256 bench-v1.tar.zst > bench-v1.tar.zst.sha256)

echo "bench-v1: $count documents, $(shasum -a 256 "$OUT/bench-v1.tar.zst" | cut -d' ' -f1)"
