#!/usr/bin/env bash
# Write and amend one record in the benchmark attempt log.
#
#   attempt.sh write --capability <c> --hypothesis <h> [--mechanism <m>]
#                    [--pr <n>] [--outcome no-candidate] [--reason <r>]
#                    [--date <YYYY-MM-DD>] [--token <hex>]
#
#   attempt.sh amend (--pr <n> | --file <path>) [--outcome <o>] [--score <s>]
#                    [--deltas <json>] [--reason <r>]
#
# The log is the orphan branch bench-attempts, which shares no history with
# main. Both subcommands work in a shallow clone under a temporary directory,
# so neither touches the caller's working tree or moves its branch.
#
# One file per attempt, named for its date and capability, so two writers never
# touch the same path. A push can still be refused when another writer lands a
# commit first, so the change is re-applied on a fresh clone and retried.
#
# BENCH_LOG_REMOTE overrides the remote. It exists so the tests can run against
# a throwaway repository instead of the real branch.
set -euo pipefail

BRANCH=bench-attempts
REMOTE="${BENCH_LOG_REMOTE:-$(git remote get-url origin)}"

CMD="${1:-}"
shift || true

CAPABILITY=""
HYPOTHESIS=""
MECHANISM=""
PR=""
OUTCOME=""
SCORE=""
DELTAS=""
REASON=""
DATE=""
TOKEN=""
FILE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --capability) CAPABILITY="$2"; shift 2 ;;
    --hypothesis) HYPOTHESIS="$2"; shift 2 ;;
    --mechanism) MECHANISM="$2"; shift 2 ;;
    --pr) PR="$2"; shift 2 ;;
    --outcome) OUTCOME="$2"; shift 2 ;;
    --score) SCORE="$2"; shift 2 ;;
    --deltas) DELTAS="$2"; shift 2 ;;
    --reason) REASON="$2"; shift 2 ;;
    --date) DATE="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --file) FILE="$2"; shift 2 ;;
    *) echo "attempt.sh: unknown argument $1" >&2; exit 2 ;;
  esac
done

die() {
  echo "attempt.sh: $*" >&2
  exit 2
}

WORK=""
# Returns 0 even with nothing to remove: the retry loop calls it under set -e.
cleanup() { [ -n "$WORK" ] && rm -rf "$(dirname "$WORK")"; return 0; }
trap cleanup EXIT

# Re-apply the change on a fresh clone and retry, because a push is refused
# when another writer lands first even though the two touch different paths.
apply_and_push() {
  local msg="$1"
  for _ in 1 2 3; do
    cleanup
    WORK="$(mktemp -d)/log"
    git clone -q --depth 1 --branch "$BRANCH" "$REMOTE" "$WORK"
    mutate "$WORK"
    git -C "$WORK" add -A
    git -C "$WORK" commit -q -m "$msg"
    if git -C "$WORK" push -q origin "HEAD:$BRANCH" 2>/dev/null; then
      return 0
    fi
  done
  echo "attempt.sh: push to $BRANCH refused after three tries" >&2
  return 1
}

case "$CMD" in
  write)
    [ -n "$CAPABILITY" ] || die "write needs --capability"
    [ -n "$HYPOTHESIS" ] || die "write needs --hypothesis"
    # The routine is a language model choosing this string, and it lands in a
    # path. A capability that escapes its directory must never reach the disk.
    case "$CAPABILITY" in
      *[!a-z0-9-]*) die "capability '$CAPABILITY' is not a name: use lower-case letters, digits and hyphens" ;;
    esac
    if [ -n "$OUTCOME" ]; then
      # Section 9.3: the verdict is the workflow's. An agent cannot record its
      # own failure as a success, so the only outcome it may write is the
      # empty day it alone knows about.
      [ "$OUTCOME" = no-candidate ] ||
        die "write records only outcome no-candidate; the workflow writes accepted and rejected"
      [ -z "$PR" ] || die "outcome no-candidate cannot carry --pr"
    fi

    DATE="${DATE:-$(date +%F)}"
    TOKEN="${TOKEN:-$(python3 -c 'import secrets; print(secrets.token_hex(2))')}"
    # Both halves of the name reach the filesystem, so both are checked.
    case "$DATE" in
      [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
      *) die "date '$DATE' is not a date: expected YYYY-MM-DD" ;;
    esac
    case "$TOKEN" in
      *[!a-z0-9]*) die "token '$TOKEN' is not a token: use lower-case letters and digits" ;;
    esac
    REL="${DATE:0:4}/${DATE:5:2}/$DATE-$CAPABILITY-$TOKEN.json"

    mutate() {
      local root="$1"
      if [ -e "$root/$REL" ]; then
        die "a record already exists at $REL"
      fi
      mkdir -p "$root/$(dirname "$REL")"
      python3 - "$root/$REL" "$DATE" "$CAPABILITY" "$HYPOTHESIS" "$MECHANISM" \
        "$PR" "$OUTCOME" "$REASON" <<'PY'
import json, sys
path, date, cap, hyp, mech, pr, outcome, reason = sys.argv[1:9]
record = {
    "date": date,
    "capability": cap,
    "hypothesis": hyp,
    "mechanism": mech or None,
    "pr": int(pr) if pr else None,
    "outcome": outcome or None,
    "score": None,
    "deltas": None,
    "reason": reason or None,
}
with open(path, "w") as f:
    json.dump(record, f, indent=2)
    f.write("\n")
PY
    }

    apply_and_push "bench: attempt $DATE $CAPABILITY"
    echo "$REL"
    ;;

  amend)
    [ -n "$PR" ] || [ -n "$FILE" ] || die "amend needs --pr or --file"
    if [ -n "$OUTCOME" ]; then
      case "$OUTCOME" in
        accepted | rejected | no-candidate) ;;
        *) die "outcome must be one of accepted, rejected, no-candidate; got '$OUTCOME'" ;;
      esac
    fi
    if [ -n "$FILE" ]; then
      case "$FILE" in
        /* | *..*) die "record '$FILE' is not a path inside the log" ;;
      esac
    fi

    mutate() {
      local root="$1" found
      if [ -n "$FILE" ]; then
        REL="$FILE"
        if [ ! -f "$root/$REL" ]; then
          die "no record at $REL"
        fi
      else
        found="$(python3 - "$root" "$PR" <<'PY'
import json, os, sys
root, pr = sys.argv[1], int(sys.argv[2])
for dirpath, dirnames, filenames in os.walk(root):
    dirnames[:] = [d for d in dirnames if d != ".git"]
    for name in sorted(filenames):
        if not name.endswith(".json"):
            continue
        full = os.path.join(dirpath, name)
        try:
            with open(full) as f:
                record = json.load(f)
        except ValueError:
            continue
        if record.get("pr") == pr:
            print(os.path.relpath(full, root))
PY
        )"
        case "$(echo "$found" | /usr/bin/grep -c .)" in
          0) die "no record for pull request $PR" ;;
          1) REL="$found" ;;
          # Amending an arbitrary one would file the verdict against the wrong
          # hypothesis, which is worse than filing it nowhere.
          *) die "two records claim pull request $PR: $(echo "$found" | tr '\n' ' ')" ;;
        esac
      fi

      python3 - "$root/$REL" "$OUTCOME" "$SCORE" "$DELTAS" "$REASON" <<'PY'
import json, sys
path, outcome, score, deltas, reason = sys.argv[1:6]
with open(path) as f:
    record = json.load(f)
if outcome:
    record["outcome"] = outcome
if score:
    record["score"] = float(score)
if deltas:
    try:
        parsed = json.loads(deltas)
    except ValueError:
        parsed = None
    if not isinstance(parsed, dict):
        sys.exit("attempt.sh: deltas must be a JSON object, got %r" % deltas)
    record["deltas"] = parsed
if reason:
    record["reason"] = reason
with open(path, "w") as f:
    json.dump(record, f, indent=2)
    f.write("\n")
PY
    }

    apply_and_push "bench: verdict for ${FILE:-pull request $PR}"
    echo "$REL"
    ;;

  *)
    echo "attempt.sh: expected write or amend, got '${CMD:-nothing}'" >&2
    exit 2
    ;;
esac
