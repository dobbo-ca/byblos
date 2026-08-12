#!/usr/bin/env bash
# Exercise bench_set.sh.
#
#   bench_set_test.sh
#
# The refusal tests build their own sample root, so they need nothing on disk.
# The build tests symlink the real anchors rather than copying them, and are
# skipped when the local sample is absent -- the archive can only be assembled
# where the documents are.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BUILD="$HERE/bench_set.sh"
TSV="$HERE/ids/bench-v1.tsv"
SAMPLE="${BYBLOS_SAMPLE:-$HOME/work/dobbo-ca/.byblos-sample}/anchors/pdfs"
PASS=0
FAIL=0
SKIP=0

ok() { PASS=$((PASS + 1)); echo "ok   $1"; }
no() {
  FAIL=$((FAIL + 1))
  echo "FAIL $1"
  echo "     ${2//$'\n'/$'\n'     }"
}
skip() { SKIP=$((SKIP + 1)); echo "skip $1 -- $2"; }

names() { /usr/bin/grep -v '^#' "$TSV" | tail -n +2 | cut -f1; }

# A sample root holding symlinks to the real documents. Nothing is copied, so
# a test costs no disk and no time.
linked_sample() {
  local root name
  root="$(mktemp -d)"
  mkdir -p "$root/anchors/pdfs"
  while read -r name; do
    ln -s "$SAMPLE/$name" "$root/anchors/pdfs/$name"
  done < <(names)
  echo "$root"
}

test_refuses_a_missing_document() {
  local root out rc outdir
  root="$(mktemp -d)"
  mkdir -p "$root/anchors/pdfs"
  outdir="$(mktemp -d)"
  out="$(BYBLOS_SAMPLE="$root" "$BUILD" "$outdir" 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    no "refuses a missing document" "exited 0"
  elif [ -n "$(ls -A "$outdir")" ]; then
    no "refuses a missing document" "wrote output anyway: $(ls "$outdir")"
  elif ! echo "$out" | /usr/bin/grep -qF "missing"; then
    no "refuses a missing document" "no 'missing' in: $out"
  else
    ok "refuses a missing document"
  fi
}

test_refuses_a_mismatched_document() {
  local root out rc outdir victim
  if [ ! -d "$SAMPLE" ]; then
    skip "refuses a document whose sha256 moved" "no local sample at $SAMPLE"
    return
  fi
  root="$(linked_sample)"
  victim="$(names | head -1)"
  rm "$root/anchors/pdfs/$victim"
  echo "not the document it claims to be" > "$root/anchors/pdfs/$victim"
  outdir="$(mktemp -d)"
  out="$(BYBLOS_SAMPLE="$root" "$BUILD" "$outdir" 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    no "refuses a document whose sha256 moved" "exited 0"
  elif [ -n "$(ls -A "$outdir")" ]; then
    no "refuses a document whose sha256 moved" "wrote output anyway: $(ls "$outdir")"
  elif ! echo "$out" | /usr/bin/grep -qF "$victim"; then
    no "refuses a document whose sha256 moved" "did not name $victim: $out"
  else
    ok "refuses a document whose sha256 moved"
  fi
}

test_builds_twelve_entries() {
  local root outdir out entries recorded actual
  if [ ! -d "$SAMPLE" ]; then
    skip "builds the archive and its checksum" "no local sample at $SAMPLE"
    return
  fi
  root="$(linked_sample)"
  outdir="$(mktemp -d)"
  if ! out="$(BYBLOS_SAMPLE="$root" "$BUILD" "$outdir" 2>&1)"; then
    no "builds the archive and its checksum" "$out"
    return
  fi
  entries="$(tar -tf "$outdir/bench-v1.tar.zst" | /usr/bin/grep -c .)"
  recorded="$(cut -d' ' -f1 "$outdir/bench-v1.tar.zst.sha256")"
  actual="$(shasum -a 256 "$outdir/bench-v1.tar.zst" | cut -d' ' -f1)"
  local bad=""
  [ "$entries" = 12 ] || bad+="archive holds $entries entries, wanted 12; "
  [ "$recorded" = "$actual" ] || bad+="recorded checksum $recorded is not the archive's $actual; "
  # Flat, because byblos-bench globs DIR/*.pdf in the directory it is given.
  tar -tf "$outdir/bench-v1.tar.zst" | /usr/bin/grep -q "/" && bad+="archive has a directory prefix; "
  if [ -n "$bad" ]; then
    no "builds the archive and its checksum" "$bad"
  else
    ok "builds the archive and its checksum"
  fi
}

test_builds_the_same_bytes_twice() {
  local root a b
  if [ ! -d "$SAMPLE" ]; then
    skip "builds the same bytes twice" "no local sample at $SAMPLE"
    return
  fi
  root="$(linked_sample)"
  a="$(mktemp -d)"
  b="$(mktemp -d)"
  BYBLOS_SAMPLE="$root" "$BUILD" "$a" >/dev/null 2>&1
  BYBLOS_SAMPLE="$root" "$BUILD" "$b" >/dev/null 2>&1
  if [ ! -s "$a/bench-v1.tar.zst" ] || [ ! -s "$b/bench-v1.tar.zst" ]; then
    no "builds the same bytes twice" "one of the two builds produced no archive"
    return
  fi
  # A rebuild that moves the bytes would invalidate the -benchset fingerprint
  # recorded in the baseline, so the archive must not carry a build time.
  if [ "$(shasum -a 256 "$a/bench-v1.tar.zst" | cut -d' ' -f1)" = "$(shasum -a 256 "$b/bench-v1.tar.zst" | cut -d' ' -f1)" ]; then
    ok "builds the same bytes twice"
  else
    no "builds the same bytes twice" "two builds of the same input differ"
  fi
}

test_refuses_a_missing_document
test_refuses_a_mismatched_document
test_builds_twelve_entries
test_builds_the_same_bytes_twice
echo
echo "$PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
