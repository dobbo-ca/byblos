# Weighted benchmark map and the daily improvement routine

**Date:** 2026-08-11
**Status:** design, approved; planned
**Tracking:** byb-om7 (epic, eleven children)
**Plan:** `docs/superpowers/plans/2026-08-12-bench-map.md`

## 1. Purpose

Byblos measures correctness heavily and cost almost not at all. The repository
carries a poppler oracle, jbig2dec round-trip goldens, a generated corpus, an
adjudicated divergence list, and sixty-odd test files. It carries two Go
benchmarks, both in `internal/jbig2`, and no measurement at all of the bytes it
emits.

This design adds the missing half: a weighted map of what each shipped
capability costs, a harness that measures it, a mechanical scorer that decides
whether a candidate change is an improvement, and a daily agent that proposes
candidates against the map.

The routine proposes. It never lands anything. Implementation stays on the
maintainer's machine; the pull request the routine opens is evidence for a
decision, not a delivery.

## 2. The objective

Optimisation targets, in the maintainer's stated order of importance, with the
weights the scorer uses:

| Rank | Metric | Weight | Measured as | Deterministic |
|---|---|---|---|---|
| 1 | Output file size | 0.40 | bytes of the produced artifact | yes |
| 2 | Latency | 0.25 | wall time, median of 5 repetitions | **no** |
| 3 | CPU and memory | 0.15 | `runtime.MemStats.TotalAlloc` | yes |
| 4 | Disk space used during operation | 0.10 | `/proc/self/io` `wchar`, bytes written | yes |
| 5 | Disk write IOPS | 0.06 | `/proc/self/io` `syscw` | yes |
| 6 | Disk read IOPS | 0.04 | `/proc/self/io` `syscr` | yes |

The weights sum to 1.0.

The determinism column is load-bearing, and it is why memory is scored on total
allocated bytes rather than on peak resident set size. Peak RSS depends on when
the garbage collector happened to run; total allocation does not. Scoring the
deterministic one costs nothing in signal — a change that allocates less is the
change worth having — and it moves 0.15 of the weight from the noisy column to
the exact one.

That leaves **0.75 of the total weight measurable exactly and reproducibly, and
only wall time, at 0.25, dependent on the machine.** Section 5.3 turns that into
the committed baseline. Peak RSS is still recorded and printed, as information
for the reader. It never scores.

Two hard constraints govern every candidate:

- Functionality must not change. `make test` must be green on the candidate.
- A test may be changed only with a justification in the pull request body, and
  a changed test is surfaced to the reader before any score is.

Output file size, the highest-weighted metric, is the only one of the six with
no measurement noise. Byblos is deterministic, so a size delta is exact rather
than statistical. The design leans on that.

## 3. The map

### 3.1 Enrolment

The register of what a build can do already exists: `buildCapabilities` in
`provenance.go`. It holds nine strings today, and `upgrade.go` documents the
rule that entries are appended as each epic lands and never removed. Two tests
already keep other vocabularies bound to it — `TestEveryCapabilityHasARule` in
`upgrade_test.go` and `TestEveryNotImplementedNamesAKnownCapability`.

The benchmark map binds to the same list, enforced the same way. This satisfies
the requirement that newly finished code enrols itself: a capability appended to
`buildCapabilities` without a benchmark target reddens the suite.

### 3.2 Shape

`go.mod` has two direct dependencies and no configuration parser, so the map is
a Go file rather than TOML or YAML. It lives at `internal/bench/map.go`.

```go
// Target names the benchmark that measures one capability's cost.
type Target struct {
	Capability string  // must appear in buildCapabilities
	Entry      string  // the exported call exercised, e.g. "EncodeJBIG2Generic"
	Corpus     string  // which slice of the bench set feeds it
	Override   float64 // hand multiplier on the measured weight; default 1.0
	Why        string  // required, and only permitted, when Override != 1.0
}
```

Weights are not stored in the map. They are measured (section 5.2). What the map
stores is the corpus slice a capability is exercised against, and an optional
hand override that must state its reason.

### 3.3 Initial targets

| Capability | Entry point | Notes |
|---|---|---|
| `inspect` | `Inspect` | produces no bytes; scored on latency, allocation and IO only |
| `extract-raster` | `ExtractPageRaster` | produces no bytes either — `PageRaster.Image` is an `image.Image`, not an encoded stream. Encoding it to measure a size would measure the encoder, not extraction |
| `build-pdf` | `BuildPDF` | fed from rasters extracted in a prior step |
| `jbig2-generic` | `EncodeJBIG2Generic` | expected to dominate the size weight on bitonal input |
| `quantize-png` | `QuantizePNG`, `QuantizeIndexed` | |
| `downsample` | `Downsample`, `DownsampleDeclaredBPC` | |
| `jpeg-recompress` | `Optimize` with `RecompressJPEG` | lossy; quality pinned per run so a size win cannot come from lowering it |
| `linearize` | `Optimize` with `Linearize` | |
| `text-layer` | `StampTextLayer` | |

`jpeg-recompress` needs its own guard. It is the only lossy pass in the library,
and lowering `JPEGQuality` would show as a large size win while degrading the
image. The harness pins the quality value and the scorer rejects any candidate
that changes it.

`Optimize` already returns `min(input, rewritten)` and records which branch ran
on `Provenance.Optimized`. A candidate that improves `Optimize` must not change
which branch is taken for a document without saying so, because the pass-through
branch returns input bytes verbatim and would otherwise show a fabricated zero
delta.

## 4. The benchmark corpus

### 4.1 Store

The bench set is a tar archive attached to a versioned GitHub release on
`dobbo-ca/byblos`, fetched by the workflow and held in `actions/cache`, keyed on
the archive's sha256.

Release assets were chosen over Git LFS. Byblos is a public repository. LFS
bandwidth is billed and every public clone draws on the same quota, so a daily
CI pull plus outside clones would need a paid data pack for no benefit. Release
asset bandwidth is not billed. The per-file limit is 2 GB; the archive is split
if it ever exceeds that.

### 4.2 Contents

`bench-v1` is drawn from the thirteen anchor documents already pinned in
`tools/sample/`: seven govdocs1 files and six archive.org files, 94 MB total,
each with a recorded source URL and sha256 in the sample manifest. They were
selected as the documents whose measured facts specific beads quote, so their
correctness is checkable rather than assumed. One of the thirteen is excluded on
rights grounds by section 4.3, leaving **twelve documents, roughly 89 MB**.

Twelve documents is thin for representativeness and adequate for determinism.
Because size deltas carry no noise, twelve real documents give twelve exact data
points. Widening the set to a stratified draw from the enumerations in
`tools/sample/ids/` is deferred until the loop has run long enough to show
whether the narrow set is misleading.

The 1.8 GB DocumentCloud leg is excluded. Those documents are user-uploaded and
of mixed provenance, and republishing them from a public repository under the
project's name is not defensible.

### 4.3 Rights, checked 2026-08-11

The seven govdocs1 files are public-domain United States government documents,
distributed by digitalcorpora for exactly this purpose.

The six archive.org items were checked individually against the archive.org
metadata API on 2026-08-11:

| Identifier | Date | Rights statement | Verdict |
|---|---|---|---|
| `journalfrtechni13erdmgoog` | 1837 | `possible-copyright-status: NOT_IN_COPYRIGHT` | include |
| `municipaldocume00masgoog` | 1895 | `possible-copyright-status: NOT_IN_COPYRIGHT` | include |
| `revistadasocied03portgoog` | 1881 | `possible-copyright-status: NOT_IN_COPYRIGHT` | include |
| `DTIC_ADA134285` | 1983 | US government work, `usgovernmentmirrors` | include |
| `DTIC_ADA383635` | 2000 | US government work, `usgovernmentmirrors` | include |
| `06043926.cn` | absent | **absent** | **exclude** |

`06043926.cn` is a Yuan-dynasty poetry collection digitised by the
China-America Digital Academic Library through Zhejiang University Library. The
underlying work is far out of copyright; the scan carries no rights statement
and no date field, so there is nothing on which to base republication. It is
excluded from the published archive and remains in the local sample only.

`bench-v1` is therefore twelve documents, roughly 89 MB.

Excluding it costs the bench set its only CJK document. CJK glyph density is
unlike Latin, and `jbig2-generic` is the capability most likely to behave
differently on it, so the set is measurably narrower for the highest-weighted
capability. A redistributable CJK scan should be found and added in `bench-v2`;
this is recorded as a known gap rather than accepted as unimportant.

## 5. The harness

### 5.1 Measurement

`cmd/byblos-bench` runs one subprocess per target. A subprocess
per measurement is what makes peak memory and the disk counters attributable to
the work rather than to the test framework, and it is why this is a command
rather than a set of `go test -bench` functions.

Output is JSON on stdout: one record per capability, per corpus document.

Repetition applies to wall time and to nothing else. The five deterministic
metrics are measured once, because a second identical run of deterministic code
over an identical input produces an identical number and repeating it buys
nothing. Wall time is measured five times and reduced to its median. This is the
answer to "when would repetition matter": only for the one metric that varies,
which is also the only metric section 5.3 cannot serve from a stored file.

All three disk metrics come from `/proc/self/io`: `wchar` for bytes written,
`syscw` and `syscr` for the write and read syscall counts.

Disk space used was originally specified as the peak size of the run's temp
directory. That was changed during planning because a peak requires a sampling
goroutine, and a sampled peak is not deterministic — which would have put the
fourth metric back in the noisy column and out of the committed baseline.
Cumulative bytes written is deterministic, needs no poller, and is the closer
measure of disk cost anyway.

These three metrics will read zero for most capabilities. Byblos works from an
`io.ReadSeeker` to an `io.Writer` and touches disk mainly where pdfcpu does.
That is the expected result, not a broken measurement, and it is why they sit at
the bottom of the ladder.

`/proc/self/io` is Linux-only. On macOS all three are recorded as absent rather
than zero, so a local run cannot silently claim a disk improvement the CI runner
would not reproduce.

### 5.2 Weighting

A capability's weight for a metric is its measured share of that metric's total
across the whole bench set:

    w_cap,metric = capability's share of that metric's total, taken from
                   internal/bench/baseline.json x the capability's Override

Shares come from the baseline, never from the candidate's own numbers. A
candidate therefore cannot inflate the weight of the thing it changed. Because
the shares for a metric sum to 1, the final score reads as a weighted percentage
improvement across the pipeline rather than as an arbitrary number.

Latency shares come from the baseline too, even though the baseline stores no
wall time. What it stores for latency is the *share* each capability held when
the baseline was measured, not the absolute milliseconds. A share is a ratio
between capabilities measured in one run on one machine, so it survives being
carried to a different runner in a way an absolute duration does not.

### 5.3 The committed baseline

Re-measuring the base commit on every pull request wastes most of the run. A
baseline is committed to the repository at `internal/bench/baseline.json` and is the
comparison target for the five deterministic metrics. Those metrics — 0.75 of
the total weight — are never re-measured during a pull request.

Only wall time needs a live base run, and only for the capabilities the diff
actually touches. A pull request that changes `internal/jbig2` re-times
`jbig2-generic` and nothing else.

    per pull request, before      per pull request, after
      full base run                 head run, all capabilities
      full head run                 base re-timed, touched capabilities only
      full base run again

#### Validity, which is the whole condition you named

A stored baseline is only comparable if what produced it is the same as what
produces the head numbers. The file therefore carries a fingerprint, and the
scorer refuses to use it unless every field matches:

```json
{
  "bench_set_sha256": "sha256 of the bench-v1 archive",
  "harness_sha256":   "sha256 over cmd/byblos-bench and internal/bench/map.go",
  "commit":           "the main commit these numbers were measured at",
  "go_version":       "1.26.4",
  "goos_goarch":      "linux/amd64",
  "runner":           "ubuntu-24.04, informational only",
  "metrics":          { },
  "latency_shares":   { }
}
```

The four checks:

1. `bench_set_sha256` equals the archive the workflow just verified. Different
   corpus, different numbers.
2. `harness_sha256` equals the current harness. Changing how a thing is measured
   invalidates every stored measurement of it, and this is the check that stops
   a harness edit from silently rebasing the comparison.
3. `commit` is an ancestor of the pull request's merge base. A baseline from a
   diverged line is not this branch's base.
4. `go_version` and `goos_goarch` match. A compiler change moves output bytes.

On any mismatch the workflow does not guess. It falls back to a full live base
run, labels the comment "baseline stale, measured live", and the run costs what
it costs.

Runner identity is recorded but is deliberately **not** one of the four checks.
No absolute duration is stored, so a different runner does not invalidate the
file. That is the property that makes a committed baseline safe here, and would
make it unsafe if wall time in milliseconds were stored in it.

`latency_shares` is the one field that is not fully machine-independent: a
different CPU could shift the ratio between two capabilities even though it
shifts no absolute number the scorer reads. The effect is second-order — it
moves how much a latency win is worth, never whether one occurred — and it is
accepted rather than corrected. If it ever matters, the fix is to recompute
shares in the same job, which costs a full base timing run and gives back
exactly what section 5.3 set out to save.

#### Refresh

A job on every push to `main` recomputes the deterministic metrics. If they
differ from `internal/bench/baseline.json`, it opens or updates a pull request titled
`bench: refresh baseline` carrying the diff.

Main is never failed for drift, because ordinary feature work legitimately
changes output bytes. But the drift is never silent either, and this gives the
project something it does not have today: any commit that changes what byblos
emits produces a reviewable diff saying by how much.

## 6. Scoring

`cmd/byblos-bench score base.json head.json` produces a markdown table and a
verdict.

    score = SUM over capabilities SUM over metrics
                w_metric  x  w_cap,metric  x  (-delta_percent)

    w_metric = size 0.40, time 0.25, memory 0.15,
               disk 0.10, write iops 0.06, read iops 0.04

Three rules override the arithmetic:

1. **Regression ceiling.** Any capability-and-metric pair worse than +10% fails
   the candidate outright, whatever the score.
2. **Noise floor on latency.** Base and head are timed in the same job on the
   same runner, five repetitions each, for the capabilities the diff touches. A
   wall-time delta smaller than the spread of the base repetitions is recorded
   as noise and scored as zero. Shared runners vary enough that without this the
   scorer would reward scheduling luck. A capability the diff does not touch
   contributes zero to the latency term, rather than a stale comparison.
3. **Lossy-parameter freeze.** A candidate that changes `JPEGQuality`, or any
   other parameter governing how much information is discarded, fails
   regardless of score.

### 6.1 Why the verdict is unbiased

No model votes. The scorer is a Go program reading two JSON files. The test
suite must be green on the candidate. The single place judgement enters is a
changed test file, and that is handled by disclosure rather than by scoring: the
pull request comment opens with a banner naming every changed test and the diff
of its assertions, above the table, so a weakened assertion cannot reach a
verdict unread.

## 7. The workflow

`.github/workflows/bench.yml`, triggered by `pull_request: types: [labeled]` on
the label `bench`.

Steps, in order:

1. Refuse to run unless the gates in section 7.1 all pass.
2. Restore the bench set from `actions/cache`; on a miss, download the release
   asset and verify its sha256.
3. Validate `internal/bench/baseline.json` against the four fingerprint checks in section
   5.3. Record whether the baseline is usable or stale.
4. Run `make test` on head. A red suite fails the run immediately, before any
   benchmark work.
5. Run the head benchmark: every capability, all six metrics.
6. Re-time the base for the capabilities the diff touches, on this same runner.
   If the baseline was stale, run the full base benchmark instead.
7. Score, and post the table as a single pull request comment.
8. Amend this attempt's record on `bench-attempts` with the outcome, score and
   deltas (section 9.3).
9. On a pass, post to Slack. On a fail, close the pull request.

### 7.1 Access control

The workflow executes candidate code and holds a token that can close a pull
request and post to Slack. The public must not be able to run it.

- **Label trigger.** Applying a label requires triage permission or above, so a
  stranger cannot fire the workflow at all.
- **No forks.** The job runs only when
  `github.event.pull_request.head.repo.full_name == github.repository`. Fork
  code never sees a secret.
- **Actor allowlist.** An explicit `if` on the job restricting it to the
  maintainer and the routine's bot identity.
- **Never `pull_request_target`.** That trigger runs head code with a write
  token against the base repository, which is the precise hole these gates
  exist to close.

The routine authenticates as a GitHub App with `contents: write` (section 7.4).
A token with that permission can otherwise push anywhere, so "the routine never
lands anything" must be enforced by the repository rather than by the routine's
prompt:

- **A ruleset on `main`** requiring a pull request and blocking direct pushes.
  This is the mechanical guarantee behind the whole design, and it is worth
  having regardless of this project.
- `bench-attempts` is deliberately left unprotected. It carries data, never
  source, and both the routine and the workflow push to it.

### 7.2 Which runner, and what it costs

GitHub-hosted `ubuntu-24.04`, matching the existing `ci.yml`.

Actions minutes on standard GitHub-hosted runners are free for public
repositories, and byblos is public. There is no minutes bill to avoid here.
Larger runners are billed even on a public repository; this workflow does not
need one. (Confirm against the billing page before relying on it — this is from
general knowledge of GitHub's terms, not a live check of the account.)

Self-hosted runners through Actions Runner Controller were considered and are
not recommended for this workflow, for two reasons.

The first is that the problem ARC would solve is mostly gone. Stable hardware
buys comparability of wall time, and wall time is 0.25 of the weight, measured
on only the touched capabilities. The other 0.75 is deterministic and does not
care what machine ran it.

The second is that GitHub advises against self-hosted runners on public
repositories, because a pull request can cause arbitrary code to execute on your
own infrastructure. The gates in section 7.1 are designed to close exactly that
path, but running a proposal-writing agent's unreviewed code on a cluster that
also runs other things is a larger blast radius than a disposable cloud VM, for
no measurement gain.

Revisit this only if latency noise is shown to be blocking a decision that the
deterministic 0.75 could not make on its own.

### 7.3 Slack setup

A new repository secret, `SLACK_WEBHOOK_URL`, is required. None exists today.
To create it:

1. At `api.slack.com/apps`, create a new app, "From scratch", in the target
   workspace.
2. Under **Incoming Webhooks**, turn the feature on.
3. Choose **Add New Webhook to Workspace** and select the channel the
   notifications should land in. A webhook posts to that one channel; changing
   channel means a new webhook.
4. Copy the generated URL. It is a bearer credential — anyone holding it can
   post to that channel.
5. Store it, from a checkout of this repository:

   ```sh
   gh secret set SLACK_WEBHOOK_URL --repo dobbo-ca/byblos
   ```

   The command reads the value from standard input, so the URL does not enter
   shell history.

The workflow posts only on a pass, and only a summary line plus the pull request
link. A failing candidate is closed silently, because a daily notification that
says "no improvement found" trains the reader to ignore the channel.

### 7.4 Authentication for the routine

The routine authenticates as a **GitHub App**, not as a personal access token
and not as the Actions `GITHUB_TOKEN`.

This is a correctness requirement, not only a permissions preference. Anything
the Actions `GITHUB_TOKEN` does is deliberately prevented from triggering
further workflow runs, to stop recursion. A pull request opened and labelled
with that token would therefore **never fire the `labeled` trigger**, and the
benchmark would silently never run. An App installation token does trigger
workflows, so the label actually starts the job.

Permissions the App installation needs, and no more:

| Permission | Level | Why |
|---|---|---|
| `contents` | write | push the candidate branch, and write the attempt log on `bench-attempts` |
| `pull_requests` | write | open the pull request |
| `issues` | write | apply the `bench` label — labels on a pull request go through the issues API, which is easy to miss when scoping the App |
| `metadata` | read | required of every App |

Two secrets hold the App credentials: `BENCH_APP_ID` and
`BENCH_APP_PRIVATE_KEY`. The routine mints a short-lived installation token from
them at the start of each run; installation tokens expire after an hour, which
is longer than a run and shorter than a day.

Three constraints follow, all of which are easy to lose during implementation:

1. **The App must not appear in the bypass list of the `main` ruleset.** A
   ruleset can name Apps that skip it, and adding this one there would silently
   undo the guarantee the ruleset exists to provide. The App must be subject to
   the rule like any other actor.
2. **The App's bot login goes in the actor allowlist** of section 7.1, as
   `<app-slug>[bot]`. That is the identity the workflow will see in
   `github.actor`, not a human username.
3. **The workflow keeps using `GITHUB_TOKEN`**, with an explicit `permissions:`
   block granting `contents: write` for the log, `pull-requests: write` to
   comment and close, and nothing else. Only the routine needs the App.

## 8. The daily routine

A scheduled cloud agent. Its whole instruction set:

> Read `internal/bench/map.go` and `internal/bench/baseline.json`. The baseline tells you what
> each capability costs now and therefore what it is worth. Read the attempt log
> (section 9): every per-capability summary, and every raw record from the last
> ninety days.
>
> Choose exactly one capability: the highest-weighted one for which you can form
> a concrete hypothesis, that you have not attempted in the last seven days, and
> that is not in cool-off.
>
> Before you propose anything, name the closest prior attempt in the log and
> state in one sentence how your idea differs from it. If you cannot point to a
> difference, it is the same idea and you must choose again.
>
> Read that capability's implementation. Find one change that reduces output
> bytes first, latency second, memory third. Do not change what the code does.
> Do not weaken a test. Do not change any parameter that governs how much
> information is discarded.
>
> Commit it to a new branch named `bench/<capability>-<short-token>`. Open a pull
> request whose body states, in this order: the capability, the hypothesis, the
> mechanism, and which metric you expect to move and by roughly how much. If you
> changed a test, justify it in one paragraph at the top and state plainly what
> the assertion used to require.
>
> Apply the label `bench`. Write your attempt record to the log. Then stop. Do
> not merge. Do not run the benchmark yourself — the workflow measures, and its
> number is the only one that counts.
>
> If you cannot find a defensible change, open no pull request. Write an attempt
> record with `outcome: no-candidate` and a `reason` saying what you looked at
> and why there was nothing there. An empty day is a correct outcome, but it is
> not a silent one — the reason is what stops you rediscovering the same dead
> end next week.

## 9. The attempt log

### 9.1 Why it is not on `main`

The routine must never land anything on `main`, and the log has to survive a
pull request that gets discarded. Those two rules together mean the log cannot
be a file in the working tree.

It lives on its own orphan branch, `bench-attempts`, sharing no history with
`main`. This copies the pattern beads already uses in this repository, where
issue data syncs over `refs/dolt/data` on the same remote rather than through
the source tree. A branch is used here rather than a bare ref because the log is
meant to be read by a person occasionally, and a branch is browsable in the web
interface.

Nothing on `bench-attempts` is ever merged into `main`. It is data, not source.

### 9.2 One file per attempt

    bench-attempts
      2026/08/2026-08-12-jbig2-generic-a3f1.json
      2026/08/2026-08-13-quantize-png-7c22.json
      summary/jbig2-generic.md
      summary/quantize-png.md

One file per attempt, named for its date and capability, so two writers never
touch the same path and a push never conflicts. Appending lines to a shared file
would conflict; creating files does not.

Record shape:

```json
{
  "date": "2026-08-12",
  "capability": "jbig2-generic",
  "hypothesis": "one sentence, the idea itself",
  "mechanism": "what was actually changed",
  "pr": 142,
  "outcome": "rejected",
  "score": -0.42,
  "deltas": { "size": 0.1, "time": -3.2 },
  "reason": "the probe is not the cost; the MQ renormalise loop is"
}
```

`outcome` is one of `accepted`, `rejected`, or `no-candidate`.

`reason` is the field that does the work. A hypothesis can be rephrased and
proposed again without the routine noticing; a recorded reason tells it why the
whole class of idea failed.

### 9.3 Lifecycle

The routine creates the file when it opens the pull request, or on an empty day
with `outcome: no-candidate` and no `pr`. The workflow amends that same file
with `outcome`, `score` and `deltas` once it has scored. One writer at a time,
so no locking is needed.

The verdict is written by the workflow, not by the agent that proposed the
change. An agent cannot record its own failure as a success.

### 9.4 Compaction

The log is pruned rather than allowed to grow without bound. Records older than
ninety days are folded into `summary/<capability>.md` — one line per attempt,
keeping the hypothesis and the reason and discarding the numbers — and the raw
files are deleted.

The routine therefore reads roughly nine small summary files plus ninety days of
records, which stays flat over time instead of growing by a file a day forever.

### 9.5 Cool-off

Three consecutive `rejected` outcomes on one capability put it out of scope for
thirty days. Without this the routine spends every day on the highest-weighted
capability, which is also the one most likely to have already been optimised.

## 10. Not built

Deliberately excluded from this design:

- **A separate trend file.** `internal/bench/baseline.json` is committed and refreshed on
  drift, so its git history already is the trend for the five deterministic
  metrics. Nothing further is built until there is a question that history
  cannot answer.
- **A stored wall-time history.** Wall time is not in the baseline, by design
  (section 5.3), so there is no latency trend across runners. Tracking latency
  over time would need a fixed machine, which section 7.2 declines.
- **A wider corpus.** Deferred until the thirteen-document set is shown to
  mislead.
- **Benchmarks for unshipped capabilities.** The map covers `buildCapabilities`
  only. `jbig2-symbol`, `ccitt-g4` and the renderer have rules in
  `capabilityRules` but no implementation, and benchmarking absent code measures
  nothing.
- **Automatic merging.** By requirement.

## 11. Risks

- **pdfcpu owns much of the cost, and byb-3iw must land first.** `Optimize`,
  `Linearize` and the provenance writes all run through pdfcpu, so a large share
  of both output bytes and wall time is decided by a dependency. Candidates
  against those capabilities will mostly be about how pdfcpu is called, and some
  headroom is unreachable without changing it.

  Bead byb-3iw is open on adjudicating a pdfcpu v0.14 bump. That bump would move
  output bytes across three of the nine capabilities, which invalidates the
  baseline the day it lands. **Sequencing: resolve byb-3iw before `bench-v1` is
  published and the first baseline is committed.** Building the baseline first
  means measuring a dependency version that is already known to be under
  review.
- **Thirteen documents can be unrepresentative.** A change that wins on the
  anchors and loses on the archive is possible. Section 4.2 accepts that risk in
  exchange for a loop that runs today.
- **Runner variance.** Handled by rule 2, but a persistent hardware change on
  GitHub's runner fleet would shift the noise floor. The double base run detects
  this; nothing corrects for it automatically.
- **Two populations disagreeing.** Bead byb-wj2 records two verified measurement
  lanes differing by 342 pages because their readability predicates were never
  stated. The harness must define, in one place, what it counts and what it
  skips, or it will reproduce that failure.
