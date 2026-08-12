#!/usr/bin/env bash
# Exercise attempt.sh and compact.sh against a throwaway remote.
#
#   log_test.sh
#
# Every test builds its own bare repository and points BENCH_LOG_REMOTE at it,
# so a test run can never reach the real bench-attempts branch. The scripts are
# only ever run, never sourced: what is under test is the command line the
# routine and the workflow actually type.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ATTEMPT="$HERE/attempt.sh"
COMPACT="$HERE/compact.sh"
PASS=0
FAIL=0

ok() { PASS=$((PASS + 1)); echo "ok   $1"; }
no() {
  FAIL=$((FAIL + 1))
  echo "FAIL $1"
  echo "     ${2//$'\n'/$'\n'     }"
}

# A bare repository holding an empty orphan bench-attempts, exactly as task 10
# step 1 creates it on the real remote.
new_remote() {
  local d
  d="$(mktemp -d)"
  git init -q --bare "$d/remote.git"
  local t c
  t="$(git -C "$d/remote.git" hash-object -w -t tree /dev/null)"
  c="$(git -C "$d/remote.git" commit-tree "$t" -m 'chore(bench): start the attempt log')"
  git -C "$d/remote.git" update-ref refs/heads/bench-attempts "$c"
  # file:// rather than a plain path, so --depth is honoured as it is on the
  # real remote instead of silently ignored for a local clone.
  echo "file://$d/remote.git"
}

# Check out the log so a test can read what was pushed.
readback() {
  local remote="$1" d
  d="$(mktemp -d)"
  git clone -q --branch bench-attempts "$remote" "$d/log" 2>/dev/null
  echo "$d/log"
}

field() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2]))' "$1" "$2"; }

# --- write ------------------------------------------------------------------

test_write_creates_the_record() {
  local remote log out rec
  remote="$(new_remote)"
  if ! out="$(BENCH_LOG_REMOTE="$remote" "$ATTEMPT" write \
    --capability jbig2-generic \
    --hypothesis 'the generic probe dominates the encode' \
    --mechanism 'hoist the probe out of the pixel loop' \
    --pr 142 --date 2026-08-12 --token a3f1 2>&1)"; then
    no "write creates the record" "$out"
    return
  fi
  log="$(readback "$remote")"
  rec="$log/2026/08/2026-08-12-jbig2-generic-a3f1.json"
  if [ ! -f "$rec" ]; then
    no "write creates the record" "no file at 2026/08/2026-08-12-jbig2-generic-a3f1.json; tree:
$(find "$log" -name '*.json' -not -path '*/.git/*')"
    return
  fi
  local bad=""
  [ "$(field "$rec" date)" = 2026-08-12 ] || bad+="date=$(field "$rec" date) "
  [ "$(field "$rec" capability)" = jbig2-generic ] || bad+="capability=$(field "$rec" capability) "
  [ "$(field "$rec" hypothesis)" = 'the generic probe dominates the encode' ] || bad+="hypothesis=$(field "$rec" hypothesis) "
  [ "$(field "$rec" mechanism)" = 'hoist the probe out of the pixel loop' ] || bad+="mechanism=$(field "$rec" mechanism) "
  [ "$(field "$rec" pr)" = 142 ] || bad+="pr=$(field "$rec" pr) "
  # Not yet scored: the workflow fills these, not the routine.
  [ "$(field "$rec" outcome)" = None ] || bad+="outcome=$(field "$rec" outcome) "
  [ "$(field "$rec" score)" = None ] || bad+="score=$(field "$rec" score) "
  [ "$(field "$rec" deltas)" = None ] || bad+="deltas=$(field "$rec" deltas) "
  if [ -n "$bad" ]; then no "write creates the record" "wrong fields: $bad"; else ok "write creates the record"; fi
}

test_write_prints_the_record_path() {
  local remote out
  remote="$(new_remote)"
  out="$(BENCH_LOG_REMOTE="$remote" "$ATTEMPT" write --capability quantize-png \
    --hypothesis h --mechanism m --pr 7 --date 2026-08-13 --token 7c22 2>/dev/null)"
  if [ "$out" = "2026/08/2026-08-13-quantize-png-7c22.json" ]; then
    ok "write prints the record path"
  else
    no "write prints the record path" "printed: $out"
  fi
}

# Assert the command fails, for the stated reason, and that the log gains no
# commit. The reason is matched because a refusal test that passes for the
# wrong reason -- an unimplemented subcommand, a typo in the flags -- reads
# exactly like a working guard.
refuses() {
  local name="$1" remote="$2" expect="$3"
  shift 3
  local before after out rc
  before="$(git ls-remote "$remote" refs/heads/bench-attempts | cut -f1)"
  out="$(BENCH_LOG_REMOTE="$remote" "$@" 2>&1)"
  rc=$?
  after="$(git ls-remote "$remote" refs/heads/bench-attempts | cut -f1)"
  if [ "$rc" -eq 0 ]; then
    no "$name" "exited 0, expected a refusal; output: $out"
  elif [ "$before" != "$after" ]; then
    no "$name" "refused but still pushed: $before -> $after"
  elif ! echo "$out" | /usr/bin/grep -qF "$expect"; then
    no "$name" "refused for the wrong reason; wanted '$expect', got: $out"
  else
    ok "$name"
  fi
}

test_write_refuses_a_verdict() {
  local remote
  remote="$(new_remote)"
  # The workflow writes the verdict, never the agent that proposed the change.
  refuses "write refuses to record its own verdict" "$remote" \
    "records only outcome no-candidate" \
    "$ATTEMPT" write --capability jbig2-generic --hypothesis h \
    --outcome accepted --date 2026-08-12 --token 0001
}

test_write_refuses_no_candidate_with_a_pr() {
  local remote
  remote="$(new_remote)"
  refuses "write refuses no-candidate carrying a pr" "$remote" \
    "cannot carry --pr" \
    "$ATTEMPT" write --capability jbig2-generic --hypothesis h \
    --outcome no-candidate --pr 9 --date 2026-08-12 --token 0002
}

test_write_accepts_no_candidate() {
  local remote log rec out
  remote="$(new_remote)"
  out="$(BENCH_LOG_REMOTE="$remote" "$ATTEMPT" write --capability linearize \
    --hypothesis 'nothing worth trying' --outcome no-candidate \
    --reason 'the object streams are already shared' \
    --date 2026-08-14 --token 5d10 2>&1)" || {
    no "write accepts an empty day" "$out"
    return
  }
  log="$(readback "$remote")"
  rec="$log/2026/08/2026-08-14-linearize-5d10.json"
  if [ "$(field "$rec" outcome)" = no-candidate ] &&
    [ "$(field "$rec" pr)" = None ] &&
    [ "$(field "$rec" reason)" = 'the object streams are already shared' ]; then
    ok "write accepts an empty day"
  else
    no "write accepts an empty day" "$(cat "$rec" 2>&1)"
  fi
}

test_write_needs_a_capability_and_a_hypothesis() {
  local remote
  remote="$(new_remote)"
  refuses "write refuses a missing capability" "$remote" \
    "needs --capability" \
    "$ATTEMPT" write --hypothesis h --date 2026-08-12 --token 0003
  refuses "write refuses a missing hypothesis" "$remote" \
    "needs --hypothesis" \
    "$ATTEMPT" write --capability jbig2-generic --date 2026-08-12 --token 0004
}

test_write_refuses_a_capability_that_is_a_path() {
  local remote
  remote="$(new_remote)"
  # The routine is a language model choosing this string. A capability that
  # escapes its directory must never reach the filesystem.
  refuses "write refuses a capability containing a path" "$remote" \
    "is not a name" \
    "$ATTEMPT" write --capability ../../etc/passwd --hypothesis h \
    --date 2026-08-12 --token 0005
}

test_write_refuses_to_overwrite() {
  local remote out
  remote="$(new_remote)"
  out="$(BENCH_LOG_REMOTE="$remote" "$ATTEMPT" write --capability jbig2-generic \
    --hypothesis first --date 2026-08-12 --token dupe 2>&1)" || {
    no "write refuses to overwrite a record" "first write failed: $out"
    return
  }
  refuses "write refuses to overwrite a record" "$remote" \
    "already exists" \
    "$ATTEMPT" write --capability jbig2-generic --hypothesis second \
    --date 2026-08-12 --token dupe
}

test_write_refuses_a_malformed_date_or_token() {
  local remote
  remote="$(new_remote)"
  refuses "write refuses a malformed date" "$remote" \
    "is not a date" \
    "$ATTEMPT" write --capability jbig2-generic --hypothesis h \
    --date 12/08/2026 --token 0006
  refuses "write refuses a token containing a path" "$remote" \
    "is not a token" \
    "$ATTEMPT" write --capability jbig2-generic --hypothesis h \
    --date 2026-08-12 --token ../../x
}

# --- amend ------------------------------------------------------------------

# A record as the routine leaves it: proposed, scored by nobody yet.
seed_attempt() {
  local remote="$1" pr="$2" token="$3"
  BENCH_LOG_REMOTE="$remote" "$ATTEMPT" write \
    --capability jbig2-generic \
    --hypothesis 'the generic probe dominates the encode' \
    --mechanism 'hoist the probe out of the pixel loop' \
    --pr "$pr" --date 2026-08-12 --token "$token" 2>&1
}

test_amend_fills_the_verdict() {
  local remote log rec out
  remote="$(new_remote)"
  seed_attempt "$remote" 142 a3f1 >/dev/null || {
    no "amend fills the verdict" "seed failed"
    return
  }
  out="$(BENCH_LOG_REMOTE="$remote" "$ATTEMPT" amend --pr 142 \
    --outcome rejected --score -0.42 \
    --deltas '{"size": 0.1, "time": -3.2}' \
    --reason 'the probe is not the cost; the MQ renormalise loop is' 2>&1)" || {
    no "amend fills the verdict" "$out"
    return
  }
  log="$(readback "$remote")"
  rec="$log/2026/08/2026-08-12-jbig2-generic-a3f1.json"
  local bad=""
  [ "$(field "$rec" outcome)" = rejected ] || bad+="outcome=$(field "$rec" outcome) "
  [ "$(field "$rec" score)" = -0.42 ] || bad+="score=$(field "$rec" score) "
  [ "$(field "$rec" reason)" = 'the probe is not the cost; the MQ renormalise loop is' ] || bad+="reason=$(field "$rec" reason) "
  # deltas must survive as an object, not as the string it arrived as.
  local deltas
  deltas="$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["deltas"], sort_keys=True))' "$rec")"
  [ "$deltas" = '{"size": 0.1, "time": -3.2}' ] || bad+="deltas=$deltas "
  # and the routine's half of the record must be untouched
  [ "$(field "$rec" hypothesis)" = 'the generic probe dominates the encode' ] || bad+="hypothesis=$(field "$rec" hypothesis) "
  [ "$(field "$rec" mechanism)" = 'hoist the probe out of the pixel loop' ] || bad+="mechanism=$(field "$rec" mechanism) "
  [ "$(field "$rec" pr)" = 142 ] || bad+="pr=$(field "$rec" pr) "
  [ "$(field "$rec" date)" = 2026-08-12 ] || bad+="date=$(field "$rec" date) "
  if [ -n "$bad" ]; then no "amend fills the verdict" "wrong fields: $bad"; else ok "amend fills the verdict"; fi
}

test_amend_by_file() {
  local remote rel log rec out
  remote="$(new_remote)"
  rel="$(seed_attempt "$remote" 143 b4c2)" || {
    no "amend addresses a record by path" "seed failed"
    return
  }
  out="$(BENCH_LOG_REMOTE="$remote" "$ATTEMPT" amend --file "$rel" --outcome accepted --score 1.5 2>&1)" || {
    no "amend addresses a record by path" "$out"
    return
  }
  log="$(readback "$remote")"
  rec="$log/$rel"
  if [ "$(field "$rec" outcome)" = accepted ] && [ "$(field "$rec" score)" = 1.5 ]; then
    ok "amend addresses a record by path"
  else
    no "amend addresses a record by path" "$(cat "$rec" 2>&1)"
  fi
}

test_amend_refuses_an_unknown_pr() {
  local remote
  remote="$(new_remote)"
  seed_attempt "$remote" 142 a3f1 >/dev/null
  # Silence here would let the workflow report a verdict it never recorded.
  refuses "amend refuses a pr with no record" "$remote" \
    "no record for pull request 999" \
    "$ATTEMPT" amend --pr 999 --outcome rejected --score -0.1
}

test_amend_refuses_an_unknown_outcome() {
  local remote
  remote="$(new_remote)"
  seed_attempt "$remote" 142 a3f1 >/dev/null
  refuses "amend refuses an outcome outside the three" "$remote" \
    "outcome must be one of" \
    "$ATTEMPT" amend --pr 142 --outcome maybe --score -0.1
}

test_amend_refuses_an_ambiguous_pr() {
  local remote
  remote="$(new_remote)"
  seed_attempt "$remote" 142 a3f1 >/dev/null
  seed_attempt "$remote" 142 b5d3 >/dev/null
  refuses "amend refuses a pr matching two records" "$remote" \
    "two records claim pull request 142" \
    "$ATTEMPT" amend --pr 142 --outcome rejected --score -0.1
}

test_amend_refuses_malformed_deltas() {
  local remote
  remote="$(new_remote)"
  seed_attempt "$remote" 142 a3f1 >/dev/null
  refuses "amend refuses deltas that are not an object" "$remote" \
    "deltas must be a JSON object" \
    "$ATTEMPT" amend --pr 142 --outcome rejected --score -0.1 --deltas 'size: 0.1'
}

# --- compact ----------------------------------------------------------------

# A complete record: proposed by the routine, then scored by the workflow.
seed_scored() {
  local remote="$1" date="$2" cap="$3" token="$4" pr="$5"
  BENCH_LOG_REMOTE="$remote" "$ATTEMPT" write --capability "$cap" \
    --hypothesis "hypothesis-$token" --mechanism "mechanism-$token" \
    --pr "$pr" --date "$date" --token "$token" >/dev/null || return 1
  BENCH_LOG_REMOTE="$remote" "$ATTEMPT" amend --pr "$pr" --outcome rejected \
    --score -0.42 --deltas '{"size": 0.1}' --reason "reason-$token" >/dev/null || return 1
}

test_compact_folds_old_records() {
  local remote log
  remote="$(new_remote)"
  seed_scored "$remote" 2026-01-05 jbig2-generic old1 101 || {
    no "compact folds a record past ninety days" "seed failed"
    return
  }
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1 || {
    no "compact folds a record past ninety days" "compact failed"
    return
  }
  log="$(readback "$remote")"
  local bad=""
  [ -f "$log/summary/jbig2-generic.md" ] || bad+="no summary/jbig2-generic.md; "
  [ ! -f "$log/2026/01/2026-01-05-jbig2-generic-old1.json" ] || bad+="raw record survived; "
  if [ -f "$log/summary/jbig2-generic.md" ]; then
    /usr/bin/grep -qF "hypothesis-old1" "$log/summary/jbig2-generic.md" || bad+="summary lost the hypothesis; "
    /usr/bin/grep -qF "reason-old1" "$log/summary/jbig2-generic.md" || bad+="summary lost the reason; "
    /usr/bin/grep -qF "2026-01-05" "$log/summary/jbig2-generic.md" || bad+="summary lost the date; "
    # The numbers are what compaction drops.
    /usr/bin/grep -qF -- "-0.42" "$log/summary/jbig2-generic.md" && bad+="summary kept the score; "
    /usr/bin/grep -qF "101" "$log/summary/jbig2-generic.md" && bad+="summary kept the pr; "
  fi
  if [ -n "$bad" ]; then
    no "compact folds a record past ninety days" "$bad
$(cat "$log/summary/jbig2-generic.md" 2>&1)"
  else
    ok "compact folds a record past ninety days"
  fi
}

test_compact_keeps_recent_records() {
  local remote log
  remote="$(new_remote)"
  seed_scored "$remote" 2026-08-01 jbig2-generic new1 102 || {
    no "compact keeps a record inside ninety days" "seed failed"
    return
  }
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1
  log="$(readback "$remote")"
  if [ -f "$log/2026/08/2026-08-01-jbig2-generic-new1.json" ] && [ ! -f "$log/summary/jbig2-generic.md" ]; then
    ok "compact keeps a record inside ninety days"
  else
    no "compact keeps a record inside ninety days" "$(find "$log" -not -path '*/.git*' -type f | sed "s|$log/||")"
  fi
}

test_compact_separates_capabilities() {
  local remote log
  remote="$(new_remote)"
  seed_scored "$remote" 2026-01-05 jbig2-generic old1 101 || return
  seed_scored "$remote" 2026-01-06 quantize-png old2 102 || return
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1
  log="$(readback "$remote")"
  if /usr/bin/grep -qF "hypothesis-old1" "$log/summary/jbig2-generic.md" 2>/dev/null &&
    /usr/bin/grep -qF "hypothesis-old2" "$log/summary/quantize-png.md" 2>/dev/null &&
    ! /usr/bin/grep -qF "hypothesis-old2" "$log/summary/jbig2-generic.md" 2>/dev/null; then
    ok "compact keeps one summary per capability"
  else
    no "compact keeps one summary per capability" "$(find "$log/summary" -type f 2>&1)"
  fi
}

test_compact_is_idempotent() {
  local remote log before after lines
  remote="$(new_remote)"
  seed_scored "$remote" 2026-01-05 jbig2-generic old1 101 || return
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1
  before="$(git ls-remote "$remote" refs/heads/bench-attempts | cut -f1)"
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1
  after="$(git ls-remote "$remote" refs/heads/bench-attempts | cut -f1)"
  log="$(readback "$remote")"
  lines="$(/usr/bin/grep -cF "hypothesis-old1" "$log/summary/jbig2-generic.md")"
  if [ "$before" = "$after" ] && [ "$lines" = 1 ]; then
    ok "compact with nothing to fold changes nothing"
  else
    no "compact with nothing to fold changes nothing" "commit $before -> $after, hypothesis appears $lines times"
  fi
}

test_compact_appends_to_an_existing_summary() {
  local remote log lines
  remote="$(new_remote)"
  seed_scored "$remote" 2026-01-05 jbig2-generic old1 101 || return
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1
  seed_scored "$remote" 2026-02-05 jbig2-generic old3 103 || return
  BENCH_LOG_REMOTE="$remote" "$COMPACT" --today 2026-08-12 --days 90 >/dev/null 2>&1
  log="$(readback "$remote")"
  lines="$(/usr/bin/grep -c "^- " "$log/summary/jbig2-generic.md")"
  if [ "$lines" = 2 ] &&
    /usr/bin/grep -qF "hypothesis-old1" "$log/summary/jbig2-generic.md" &&
    /usr/bin/grep -qF "hypothesis-old3" "$log/summary/jbig2-generic.md"; then
    ok "compact appends to a summary it wrote before"
  else
    no "compact appends to a summary it wrote before" "$(cat "$log/summary/jbig2-generic.md")"
  fi
}

run() {
  test_write_creates_the_record
  test_write_prints_the_record_path
  test_write_refuses_a_verdict
  test_write_refuses_no_candidate_with_a_pr
  test_write_accepts_no_candidate
  test_write_needs_a_capability_and_a_hypothesis
  test_write_refuses_a_capability_that_is_a_path
  test_write_refuses_to_overwrite
  test_write_refuses_a_malformed_date_or_token
  test_amend_fills_the_verdict
  test_amend_by_file
  test_amend_refuses_an_unknown_pr
  test_amend_refuses_an_unknown_outcome
  test_amend_refuses_an_ambiguous_pr
  test_amend_refuses_malformed_deltas
  test_compact_folds_old_records
  test_compact_keeps_recent_records
  test_compact_separates_capabilities
  test_compact_is_idempotent
  test_compact_appends_to_an_existing_summary
  echo
  echo "$PASS passed, $FAIL failed"
  [ "$FAIL" -eq 0 ]
}

run
