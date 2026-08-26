#!/usr/bin/env bash
# Fold attempt records past ninety days into one line each.
#
#   compact.sh [--days 90] [--today YYYY-MM-DD]
#
# Every record older than the window is appended to summary/<capability>.md as
# a single line keeping the date, the outcome, the hypothesis and the reason,
# and the raw file is deleted. The numbers go: a score and a set of deltas are
# only meaningful against the baseline that produced them, and that baseline
# has moved by the time a record is this old. The reason is what the routine
# still needs, because it is what stops a dead idea coming back.
#
# The log therefore stays at roughly nine summaries plus ninety days of
# records instead of growing by a file a day forever.
#
# Run from the same weekly schedule as the baseline refresh.
#
# --today exists so the tests can place a record on either side of the window
# without waiting ninety days. BENCH_LOG_REMOTE overrides the remote.
set -euo pipefail

BRANCH=bench-attempts
REMOTE="${BENCH_LOG_REMOTE:-$(git remote get-url origin)}"
DAYS=90
TODAY=""

while [ $# -gt 0 ]; do
  case "$1" in
    --days) DAYS="$2"; shift 2 ;;
    --today) TODAY="$2"; shift 2 ;;
    *) echo "compact.sh: unknown argument $1" >&2; exit 2 ;;
  esac
done
TODAY="${TODAY:-$(date +%F)}"

WORK=""
# Returns 0 even with nothing to remove: the retry loop calls it under set -e.
cleanup() { [ -n "$WORK" ] && rm -rf "$(dirname "$WORK")"; return 0; }
trap cleanup EXIT

# Exits 3 when no record is old enough, so the caller can push nothing rather
# than an empty commit.
fold() {
  python3 - "$1" "$TODAY" "$DAYS" <<'PY'
import datetime, json, os, sys

root, today, days = sys.argv[1], sys.argv[2], int(sys.argv[3])
cutoff = datetime.date.fromisoformat(today) - datetime.timedelta(days=days)

folded = {}
for dirpath, dirnames, filenames in os.walk(root):
    dirnames[:] = [d for d in dirnames if d not in (".git", "summary")]
    for name in sorted(filenames):
        if not name.endswith(".json"):
            continue
        full = os.path.join(dirpath, name)
        try:
            with open(full) as f:
                record = json.load(f)
            when = datetime.date.fromisoformat(record["date"])
        except (ValueError, KeyError, TypeError):
            continue
        if when >= cutoff:
            continue
        folded.setdefault(record.get("capability") or "unknown", []).append((record, full))

if not folded:
    sys.exit(3)

for capability, items in sorted(folded.items()):
    path = os.path.join(root, "summary", capability + ".md")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    fresh = not os.path.exists(path)
    with open(path, "a") as f:
        if fresh:
            f.write("# %s\n\n" % capability)
            f.write("Folded attempts. The hypothesis and the reason are kept;\n")
            f.write("the numbers are dropped with the record.\n\n")
        for record, _ in sorted(items, key=lambda item: item[0]["date"]):
            line = "- %s %s: %s" % (
                record["date"],
                record.get("outcome") or "unscored",
                record.get("hypothesis") or "",
            )
            if record.get("reason"):
                line += " -- " + record["reason"]
            f.write(line + "\n")
    for _, full in items:
        os.remove(full)

print("folded %d record(s) into %d summary file(s)" % (
    sum(len(i) for i in folded.values()), len(folded)))
PY
}

# Re-apply the fold on a fresh clone and retry, because a push is refused when
# another writer lands first.
for _ in 1 2 3; do
  cleanup
  WORK="$(mktemp -d)/log"
  git clone -q --depth 1 --branch "$BRANCH" "$REMOTE" "$WORK"

  if fold "$WORK"; then
    :
  else
    rc=$?
    if [ "$rc" -eq 3 ]; then
      echo "compact.sh: nothing older than $DAYS days" >&2
      exit 0
    fi
    exit "$rc"
  fi

  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m "bench: fold attempts older than $DAYS days"
  if git -C "$WORK" push -q origin "HEAD:$BRANCH" 2>/dev/null; then
    exit 0
  fi
done

echo "compact.sh: push to $BRANCH refused after three tries" >&2
exit 1
