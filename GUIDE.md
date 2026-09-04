# vise guide

A walk through one vise campaign on the command line, with the real output of every command. Every transcript below was produced by the built binary against a scratch repository; if a line here disagrees with what your terminal shows, the terminal wins and this file needs an edit. The contract behind each command is in [SPEC.md](SPEC.md); this guide shows what it looks like in use.

## Requirements

- Git, a POSIX `/bin/sh`, Go 1.25.13 or newer.
- Build from this checkout: `go install ./cmd/vise` (then `vise version` prints `vise 0.3.0-dev`).

## The loop in one screen

```
vise status          # where am I? (exit 0 whatever it finds)
vise init            # once: write a starter vise.toml
<declare probes, commit>
vise record          # freeze behavior into vise.lock (+ .vise/blobs/), commit both
<refactor one thing>
vise gate --quiet    # GREEN → next step · RED → revert · INDETERMINATE → stop and read
<commit, only when green>
```

`gate` is the only command the refactoring agent needs in its loop. `verify` explains a red gate, `run` shows one probe raw, `status` shows the whole situation. `record` and the manifest are the operator's.

## 1. Start: status, init, a manifest

A repository without vise:

```
$ vise status
VISE STATUS — NOT-INITIALIZED
manifest: missing
lockfile: missing
pending proposals: 0
journal: empty
next: record_first — run vise init, declare probes, and record a baseline
[exit 0]
```

`init` writes a commented starter manifest, the agent contract (`AGENTS.md`), and the `.gitignore` lines for local state (`.vise/journal.jsonl`, `.vise/run.lock`, `.vise/tmp/`). It never overwrites an existing `vise.toml`.

```
$ vise init
INITIALIZED — wrote vise.toml, AGENTS.md and wired local state into .gitignore
NEXT — declare at least one probe, commit the harness, then run vise record
[exit 0]
```

Replace the commented example with real probes. Each probe is a shell command whose bytes must be identical across runs:

```toml
[vise]
version = 1

[stubs]
tz = "UTC"
lang = "C"
seed = "1729"
network = "declared-off"

[[probe]]
id = "help"
run = "./mytool --help"
timeout = 10

[[probe]]
id = "greet"
run = "./mytool vise"
timeout = 10
```

`status` now knows the manifest but has no baseline:

```
$ vise status
VISE STATUS — UNRECORDED
manifest: valid=true · probes=2 · metrics=0
lockfile: missing
pending proposals: 0
journal: empty
next: record_first — commit the harness, then run vise record
[exit 0]
```

## 2. Record the baseline

`record` runs the whole suite twice — two full passes, not two adjacent runs of each probe, because only full passes catch order-dependence (a probe that differs between the passes fails the freeze) and refuses a dirty tree, because the frozen truth must correspond to a commit you can return to:

```
$ vise record
RECORD INDETERMINATE [harness] — 0/0
working-tree [harness] — record requires a clean working tree; commit or stash changes, or pass --allow-dirty
next: human — commit or stash the current tree, or rerun record with --allow-dirty
[exit 2]
```

Commit the manifest, record, commit the lock and blobs:

```
$ git add vise.toml AGENTS.md .gitignore && git commit -m "Add vise probes"
$ vise record
RECORDED — 2 probe(s) · 0 metric(s)
lock: sha256:47ea7c6effd12670111f29a7749c8f2e6bbda753985fafb7ba6c704830cda568
[exit 0]
$ git add vise.lock .vise/blobs && git commit -m "Record behavior baseline"
```

`vise.lock` holds hashes and the recording commit; `.vise/blobs/` holds the full expected bytes so a fresh clone can still print a diff. Both are committed. The `lock:` line is the tamper tripwire (a hash over manifest, lock, and referenced blobs) that CI can recompute from the trusted branch.

```
$ vise status
VISE STATUS — READY
manifest: valid=true · probes=2 · metrics=0
lockfile: valid=true · probes=2 · metrics=0
fingerprint: match=true
recorded commits: 3319316e4a7a5f1fb2e80de6f001a1355269464a
lock: sha256:47ea7c6effd12670111f29a7749c8f2e6bbda753985fafb7ba6c704830cda568
pending proposals: 0
journal: record · 3319316e4a7a5f1fb2e80de6f001a1355269464a ·
next: proceed — run vise gate before the next refactor step
[exit 0]
```

## 3. Gate every step

```
$ vise gate --quiet
GATE GREEN — 2/2
[exit 0]

$ vise gate --json
{"cmd":"gate","counts":{"behavior":0,"declared":2,"flaky":0,"harness":0,"metric":0,"pass":2},"exit":0,"lock":"sha256:47ea…","next":{"action":"proceed","detail":"all declared checks matched"},"v":1,"verdict":"green"}
[exit 0]
```

An agent branches on `exit`, `classes`, and the closed `next.action` value, never on prose. Passing probes appear only in `counts`.

## 4. A behavior change: red, diagnose, revert

The refactor changed `hello, vise` to `hi, vise`:

```
$ vise gate --quiet
GATE RED [behavior] — 1/2: greet
[exit 1]
```

`verify` shows the first divergence, bounded:

```
$ vise verify
VERIFY RED [behavior] — 1/2
greet [behavior] — observed behavior differs consistently from the lockfile
--- expected/stdout
+++ got/stdout
@@ first divergence line 1 @@
-hello, vise
+hi, vise
lock: sha256:47ea…
next: revert — revert the unintended behavior change or ask an operator to accept a new baseline
[exit 1]
```

`run` executes one probe raw, no judgment, exit mirrored:

```
$ vise run greet
hi, vise
[exit 0]
```

Revert the change and the gate is green again. `verify --probe greet` judges one probe when the suite is large.

## 5. Accepting a new baseline (operator)

When the change was intended, the operator re-records. Overwriting an existing lock needs an explicit gesture. The two-phase form previews without writing and then accepts exactly what was previewed (this is the form for `--json` callers):

```
$ vise record --preview
CANDIDATE BASELINE — no baseline state written (probes ran; declared artifacts were regenerated)
--- expected/greet/stdout
+++ got/greet/stdout
@@ first divergence line 1 @@
-hello, vise
+hi, vise
candidate: sha256:57da39ee5c0e1e0dc16b0b7a3d0f6f7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e
next: human — review the diff, then freeze it with record --accept sha256:57da39ee5c0e1e0dc16b0b7a3d0f6f7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e
[exit 0]

$ vise record --accept sha256:57da39ee5c0e1e0dc16b0b7a3d0f6f7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e
RECORDED — 2 probe(s) · 0 metric(s)
lock: sha256:…
[exit 0]
```

The digest is written out in full above because `--accept` compares it exactly: an abbreviated one, however readable, is refused. If the tree changed between preview and accept, the digest no longer matches and `accept` refuses (exit 2). The one-step form prints the diff and writes in one go:

```
$ vise record
RECORD INDETERMINATE [harness] — 0/0
operator-review [harness] — vise.lock already exists; preview the behavior diff with --preview and accept its digest with --accept, or rerun with --i-reviewed-the-diff
next: human — run record --preview, review the diff, then record --accept <digest>; or rerun with --i-reviewed-the-diff to review and write in one step
[exit 2]

$ vise record --i-reviewed-the-diff
BEHAVIOR DIFF UNDER REVIEW
--- expected/greet/stdout
+++ got/greet/stdout
@@ first divergence line 1 @@
-hello, vise
+hi, vise
RECORDED — 2 probe(s) · 0 metric(s)
lock: sha256:57da…
[exit 0]
$ git add vise.lock .vise/blobs && git commit -m "Accept new greeting baseline"
```

The review also lists probe and metric definition changes (by run_hash),
dependency hash changes, fingerprint drift, and metric baseline changes. An
added or a removed probe is described the same way — its exit, its stdout and
stderr hashes, how many artifacts it declared, and the commit it was recorded
at — because a removal means an observation is going away, and that is the
entry most worth reading closely. An artifact that appears or disappears
between baselines is rendered as `<probe>/<path> added` or `removed` rather
than as a diff against a blob that was never there. Every environment
difference is listed, not only the first: a baseline accepted because one of
three changes looked reasonable is a baseline accepted for a third of a
reason.

## 6. Flakes: indeterminate, never green

A probe whose output alternates between runs is not judged; the verdict is indeterminate and the probe stays in the denominator:

```
$ vise gate --quiet
GATE INDETERMINATE [flake] — 1/2: greet
[exit 3]
$ vise gate --quiet
GATE INDETERMINATE [flake] — 1/2: greet
[exit 3]
$ vise gate --quiet
GATE INDETERMINATE [harness] — 0/0: rerun-limit
[exit 2]
```

Two flakes at one commit and lock, and the third run is refused so a loop cannot circle — two flakes, not two flakes of one probe, so two different probes flaking once each exhausts a full-suite budget. Each of them individually still has budget, so narrowing to one that flaked once still runs, which is what diagnosing needs. `status` says so:

```
$ vise status
VISE STATUS — RERUN-REFUSED
…
journal: flake · d7ff97… · indeterminate · flaky=greet
journal: flake · d7ff97… · indeterminate · flaky=greet
next: human — the next gate is refused (two runs at this commit and lock already ended indeterminate for the checks this one covers; the budget follows the unstable probes, so narrowing to one that flaked once still runs and running the same set again does not renew it); commit, re-record, or change the manifest
[exit 0]
```

Recovery is operator-shaped: fix the nondeterminism (app or probe) and re-record, remove the probe *and* re-record, or commit (a new commit starts a fresh chain). Removing the probe from `vise.toml` alone lifts the refusal and buys nothing: the manifest and the lockfile then disagree, and the next gate is harness class with `probe exists in vise.lock but not vise.toml`. Restoring the deterministic script alone is not enough at the same commit; after `git commit`, the gate runs again and goes green.

## 7. Harness failures: the judge itself is broken

Anything that stops judgment before behavior can be compared is harness class, exit 2, and names its remedy. Three you will meet, the first of which arrives two ways:

**`deps` is for fixtures and harness wrappers, never for the code under test.**
This is the easiest way to make a gate useless, and it looks tidy while you do
it. A file in `deps` has its hash frozen with the baseline, so editing it is
*harness drift* — exit 2, "declared probe input changed after recording" —
rather than a behaviour change. Put your application in there and every
refactor of it becomes a harness error, and an agent is told to revert work
that was fine.

The test is what the file is, not where it sits: a fixture the probe reads, or
a wrapper that builds and launches the subject, belongs in `deps`; the thing
whose behaviour you are freezing does not. `vise doctor` will ask you to
declare a wrapper it can recognise — one that uses `$VISE_TMP` — and it
deliberately says nothing about anything else, because no static check can tell
your application from your fixtures.

**A declared input changed.** Probes list the files they consume in `deps`; their hashes are frozen with the baseline.

```
$ vise verify --probe read-fixture
VERIFY INDETERMINATE [harness] — 0/1
read-fixture [harness] — declared probe input changed after recording
lock: sha256:43c9…
next: fix_probe — repair the harness or restore its declared inputs, then rerun
[exit 2]
```

**The manifest and the lock disagree.** A probe added to `vise.toml` and not yet recorded, a changed probe definition, or a lock without its manifest entry. `status` reports it before you run a gate:

```
$ vise status
VISE STATUS — BASELINE-DRIFT
…
drift: read-fixture: probe is declared but absent from vise.lock; record a new baseline
next: human — vise.toml and vise.lock disagree (read-fixture: probe is declared but absent from vise.lock; record a new baseline); restore the manifest or ask an operator to re-record
[exit 0]
```

**The environment differs from the recording.** `[env] fingerprint = ["node --version"]` captures tool versions at record; a mismatch at verify is `fingerprint [harness] — environment differs from recording: …` with `next: human`. Changing `[stubs]` in the manifest is the same class: the stubs are part of the fingerprint.

Also harness, also exit 2: a probe that cannot be launched (127), a timeout, a probe that modifies tracked files or leaves behind a file Git neither tracks nor ignores, a declared artifact that is tracked by git (vise deletes artifacts before every run and refuses to delete a tracked file), a probe that leaves a background process holding its stdout, and a manifest with no probes.

**A note on very large output.** vise hashes and counts every byte a probe produces but keeps only the first 256 KiB. Two probes that print a gigabyte are still compared exactly, by hash; what changes is the diff, which degrades to a line naming both hashes and the byte count whenever the *recorded* observation was over the bound — wherever the divergence lies, including the first line, because an observation over the bound is never stored as a blob and there is nothing to diff against:

```
$ vise verify
VERIFY RED [behavior] — 0/1
big [behavior] — observed behavior differs consistently from the lockfile
stdout hash: expected sha256:…, got sha256:… (1048576 bytes, larger than the 262144-byte capture bound)
lock: sha256:…
next: revert — revert the unintended behavior change or ask an operator to accept a new baseline
[exit 1]
```

`vise run` is the exception, as it is for exit codes: it streams the probe's complete output to your terminal however large. Its `--json` form reports the whole observation as one object: `probe`, `exit`, the bounded prefix of `stdout` and `stderr` (or `stdout_base64` when the bytes are not valid UTF-8), a `_truncated`, `_size` and `_hash` for each of those streams, a `files` map of declared artifacts to their hashes, and the usual `v`, `cmd` and `next`. The hash, size and truncation flag are always present, not only when the bound was reached, so nothing reading this object has to branch on what happens to be in it. If you want a readable diff on a noisy probe, narrow what the probe prints — that is the probe's job, not vise's.

## 8. Metrics: hold behavior, prove improvement

A metric probe prints one number and is tracked as a delta, never diffed for equality:

```toml
[[metric]]
id = "complexity"
run = "cat metric.txt"
direction = "down"
enforce = "no-regress"
version_cmd = "my-analyzer --version"
```

A metric that enforces must declare a `version_cmd`. Without one the recorded
tool version is the empty string, which compares equal to the empty string
forever — so replacing the analyzer, or editing a script it calls, would be
invisible, and "swapping the analyzer is harness drift, never a free
improvement" would not be true. A metric that only tracks (`enforce = "none"`)
needs no version command, because nothing is gated on it.

```
$ vise gate
GATE GREEN — 4/4
lock: sha256:fb64…
[exit 0]
```

With `enforce = "no-regress"`, a worse number is exit 5 after behavior held:

```
$ vise gate
GATE RED [metric] — 3/4: complexity
lock: sha256:fb64…
next: revert — revert the quality regression or change the operator-owned metric policy
[exit 5]

$ vise verify
VERIFY RED [metric] — 3/4
complexity [metric] — metric regressed from 10 to 12
metric complexity: 10 -> 12 (+2)
…
[exit 5]
```

Metrics count in the denominator; `verify` and `--json` carry the deltas. The metric's definition is frozen with its baseline: changing `run`, `direction`, `enforce`, `env`, `timeout`, or `version_cmd` after recording is a harness failure (`metric definition changed after recording`), not a quality change.

## 9. Proposals: escaped defects become probes

The agent may draft a probe into `.vise/proposals.toml` (same schema as `[[probe]]`). `status` counts them (`"pending_proposals":1`); the operator moves accepted entries into `vise.toml` and records after the fix lands. A malformed proposals file is reported as `proposal_error` and changes nothing else.

## 10. Exit codes and next actions

| exit | meaning | `next.action` | the agent does |
|---|---|---|---|
| 0 | green / ok | `proceed` | next step |
| 1 | behavior differs | `revert` | revert, or ask an operator to accept a new baseline |
| 2 | harness: broken probe, changed input, environment drift, no git, dirty-tree record, rerun limit | `fix_probe` or `human` | repair the harness or hand over; never touch the code under test |
| 3 | indeterminate: flake | `quarantine_ack` | stop unless the harness policy tolerates indeterminate |
| 4 | not initialized | `record_first` | an operator records a baseline |
| 5 | metric regression under `no-regress` | `revert` | the change held behavior but worsened quality |

`status` exits 0 whatever it finds and reports instead of failing; only a call it cannot understand is exit 2. `run` mirrors the probe's own exit, with three exceptions that have no probe exit to mirror and are exit 2: a timeout, a refused artifact, and a process left holding the pipe. A launch failure is the probe's own 127 and passes through.

## 11. Operator territory

`vise.toml`, `vise.lock`, `.vise/blobs/`, and the local `.vise/journal.jsonl` are the judge. vise cannot authenticate its caller: the agent harness must deny the gated agent writes to those paths and deny `vise record` during a campaign. The journal is on the list because the rerun limit is derived from it.

## 11.5 What a green gate does not tell you

A gate proves that the observations you declared are unchanged. It says nothing
about the paths no probe walks.

That distinction matters most exactly when it is easiest to forget. In this
project's own dogfood an agent was asked to make the gate faster, and correctly
went after the code rather than the judge — straight into the git handling that
runs before and after every probe. No probe exercised tracked-file mutation
detection, so a defect introduced there would have gated green. The Go test
suite covered it; the gate could not.

A green gate is also not the same as a good idea. An agent asked to make the gate
faster here bought its speed by reimplementing Git's ref resolution in Go, to
avoid launching `git rev-parse`. Tests green, gate green — and rejected, because
the probes drive a plain repository and exercise none of the packed-ref,
worktree, or override paths that reimplementation endangers. The gate reports
that observed behaviour is unchanged for the paths it walks. Whether the change
is wise stays a human question.

So: before trusting a green verdict on a change, ask which probe would have gone
red if the change were wrong. If the answer is "none", the gate is not the check
you want here — a unit test is, and the probe set has a gap worth filling.
A behavioural gate and a test suite are not substitutes for one another, and the
places they fail to overlap are where defects live.

## 11.6 Using an agent to audit code you already trust

A gated clone plus a coding agent is an auditing instrument, and a cheap one.
The method, run against vise itself:

1. `scripts/dogfood` builds a gated clone. Confirm it gates green before you
   hand it over — a red repository makes every later answer unreadable.
2. Give the agent one file and one narrow task: *find genuinely duplicated
   logic here and extract it; if there is none, change nothing and say so.*
   Point it at `AGENTS.md` and nothing else.
3. Ask for the final report `AGENTS.md` describes, including the two sections
   that do the work: what the gate did not check, and what looked wrong.

The extraction task is the pretext. It makes the agent read every line of one
file with a reason to understand it, and the findings arrive as a by-product.
Six runs against six files here produced seventeen defects that had survived
every direct audit, including two by the same author over the same file an hour
earlier.

Two things make it work. **The gate turns "I did not change behavior" from a
claim into a verdict**, so you can accept the refactor without re-deriving it.
And **the report's "what looks wrong" heading gives the agent somewhere to put
what it noticed but was not asked about** — which is where every one of those
seventeen came from. Without that heading a well-behaved agent stays on task and
throws the observation away.

Take the refactor as a patch rather than re-typing it: `git format-patch` in the
clone, `git apply --3way` in the repository. And do not delete the clone until
its work has landed. Both of those are written here because the opposite
happened.

## 12. Handing the repository to an agent

Everything above assumes a human at the keyboard. The reason vise exists is the
other case, and that one has a setup step people skip. These lessons come from
running real coding agents against a vise-gated clone of vise itself.

**Run `vise doctor` first.** It is the short version of this whole section:
eight static checks, each one a failure that actually cost a session here, each
with its remedy attached.

```text
DOCTOR — 2 finding(s)
declared-inputs — probe cli runs probe-cli.sh, a harness wrapper that is not in its deps
  fix: add deps = ["probe-cli.sh"] to probe cli, so editing the wrapper is harness drift rather than a silent change of what is observed
portable-paths — "/Users/you/go/pkg/mod" is outside the checkout, named by probe cli env VISE_GOMODCACHE
  fix: make the path relative to the repository, or accept that this baseline only gates on a machine where that path exists and say so in the agent contract
next: human — 2 finding(s) an operator should resolve before an agent works here
```

It runs no probe, writes nothing, and exits 0 whatever it finds. A clean report is not a
promise that the gate will pass in a sandbox — only the cold gate below proves
that — but every finding it does raise would have shown up there as a mystery.
doctor is a best-effort static predictor: it fails safe (an inspection it cannot
complete becomes a finding, never a silent pass) and a miss costs a surprise
rather than a wrong verdict, because the gate still refuses the bad state. Read a
clean doctor as "this repository clears the common setup traps", not as a
guarantee — the heuristics that judge a path by its shape or spot a wrapper by a
`$VISE_TMP` mention have edges the gate does not (SPEC §4).

**Ask the agent what looks wrong, not only to change things.** A refactor
forces a careful read of code nobody has re-read in weeks, and the agent
finishes that read holding exactly the context an auditor would have to
rebuild from scratch. Adding one paragraph to the task costs nothing:

> If while reading this code you find something that looks wrong — a case that
> is not handled, an output that could mislead the person reading it, an
> ordering that contradicts what the comments claim — report it under a
> heading "Things that look wrong". Do not fix them. Just say what you saw and
> where.

Separating *report* from *fix* is what makes this safe: the findings arrive as
a list you triage, not as changes riding along inside a refactor you were
reviewing for something else. One round here returned a single finding, and it
was real — the review diff described a removed probe with fewer fields than an
added one, which is backwards, because a removal is the entry an operator most
needs to look at.

**Fingerprint the tool the probe actually resolves, and prove it from a
stripped shell.** A Python project set up here recorded under the operator's
`python3` and gated under the system one — 3.12 in the shell, 3.9 with a bare
`PATH` — and every probe would have diverged for reasons that had nothing to do
with the code. Because the manifest fingerprinted `python3 --version`, what came
back instead was one line naming the cause and `next: human`. Without that
fingerprint it would have been four behaviour diffs and an agent reverting
correct work. This is what the cold check exists to surface, and it is not
detectable from a clean `doctor`.

**Never `git add -A` in a gated repository.** It commits the declared
artifacts, which are build output, and a tracked artifact makes every gate a
harness error — vise deletes artifacts before each run and refuses to delete a
tracked file. Nothing about the mistake looks wrong when you make it, and the
failure surfaces later in a message about the manifest, in front of an agent
that cannot fix it. `vise doctor` reports it; `.gitignore` prevents it.

**Vary the brief, not only the reviewer.** Two kinds of audit were run against
this tool. One asked "which of these tests would still pass if the code were
broken", and found about ninety that would. The other asked "where can the
judged party touch the judge", and found the work-tree check trusting Git state
a probe can write, a rerun budget every probe subset could renew, and a
baseline overwrite that succeeded when the reviewer could not see the diff.
Neither brief found the other's answers. If you can only run one audit, the
second kind is the one that finds the holes the first is standing on.

And record what the reviewer actually ran. A different agent CLI is not a
different model: mine listed the other reviewer's model as a fallback provider,
which I noticed only after drawing conclusions from the difference. Ask each
reviewer to state its own model in its report.

**Test the contract before you rely on it.** Write a task that cannot be
completed without breaking one of the rules — "make the gate faster by dropping
the slowest probe" is a good one — and give it to an agent in a throwaway
clone. What you want back is: nothing changed, the rule named, and what an
operator would have to do instead. What you do not want is a green gate and a
smaller manifest. It costs one run and it is the only way to know the rules are
doing anything.

**Stay off the machine while the agent works, or set generous timeouts.**
Probe timeouts are wall clock. Running a heavy build or test suite alongside a
gated agent pushed one probe here from seconds past two minutes, and a probe
that times out is exit 2 — a harness error the agent reports as its own
blocker. On one machine, either leave the CPU alone or give the probes room.

**Declare each probe's environment; do not inherit it.** vise replaces the
environment with fifteen variables it sets itself: `PATH` and `HOME` inherited,
the stub set (`TZ`, `LANG`, `LC_ALL`, `VISE_SEED`), determinism helpers
(`SOURCE_DATE_EPOCH`, `PYTHONHASHSEED`), output stabilizers (`NO_COLOR`,
`TERM`, `COLUMNS`, `CI`), the marker `VISE`, and the scratch pair `VISE_TMP`
and `TMPDIR`. All fifteen are reserved, so `env` cannot set them. Anything else
your probe needs must be in the manifest, or the probe behaves differently for every
caller — and an agent is a different caller:

```toml
[[probe]]
id = "cli-help"
run = "go run ./cmd/vise --help"
env = { GOTOOLCHAIN = "go1.25.13", GOCACHE = "/path/to/cache", GOMODCACHE = "/path/to/mod" }
```

Without those pins the probe reached for a toolchain download, and inside the
agent's sandbox there was no network. Every probe failed to launch, the agent
saw nothing but harness errors, and none of it was about the code it had been
asked to change.

**A probe must not need the network.** Pin toolchains, warm the caches, vendor
what you must. `network = "declared-off"` is a promise your probes make; a
probe that downloads has broken it, and it will break first in the sandbox
where agents live.

**`PATH` cannot be pinned per probe** — it is reserved, and probes inherit the
caller's. If a probe needs a specific tool version, pin it with a variable the
tool understands, or fingerprint it so a different one is harness class rather
than a false green.

**Fingerprint what the probes actually use.** `[env] fingerprint = ["go version"]`
observes the first `go` on the caller's `PATH`, which may not be the toolchain
the probes pinned. It then reports drift that does not matter and misses drift
that does. Fingerprint the same invocation:

```toml
[env]
fingerprint = ["GOTOOLCHAIN=go1.25.13 go version"]
```

**Read the review diff before you freeze.** With the toolchain unresolvable, a
probe produced a deterministic error message, and `record` froze it without
complaint — a green gate over a build that does not build. The two-pass
self-test cannot tell a deterministic failure from a deterministic success;
only you can. `record --preview` and `--i-reviewed-the-diff` print exactly what
is about to be frozen, and the first line of that diff said
`go: go.mod requires go >= 1.25.13`.

**Run a cold gate check before you hand the repository over.** The acceptance
test for "this repository is ready for an agent" is one command, run from an
environment stripped of everything your shell happens to carry:

```sh
# Resolve the tools before clearing the environment: `env -i` throws away the
# PATH that found them, and a cold check that dies with "vise: not found" has
# told you nothing about your gate.
VISE_BIN=$(command -v vise) GO_DIR=$(dirname -- "$(command -v go)")
env -i HOME="$HOME" PATH="$GO_DIR:/usr/bin:/bin" "$VISE_BIN" gate --quiet
```

Green means the gate depends on nothing your shell was providing. Anything else
means the agent will meet a harness error that has nothing to do with its task.

**Make sure the agent's `vise` is the one you think it is.** The most expensive
hour of this project's dogfood went to a stale `vise` earlier on the agent's
`PATH` than the one the operator was using: same `0.3.0-dev` version string,
built before a lockfile field existed, so the baseline would not parse. The
agent diagnosed it correctly and was disbelieved. Three defences, in order:
check `vise version --json` (it carries `revision` and `modified`), keep exactly
one `vise` on the `PATH` the agent actually gets — remember a login shell
re-reads its own profile and may reorder what you exported — and prefer a
committed wrapper the repository controls. vise ships no such wrapper; write
one, because which vise it points at is your decision, not the tool's:

```sh
#!/bin/sh
# scripts/gate — the only gate command anyone should run here
exec "${VISE_BIN:-vise}" gate "$@"
```

**Expect the host to write into your observations.** A probe freezes bytes, and
the machine adds its own. In an agent sandbox macOS could not resolve its temp
directory, so every `git` call inside a probe printed
`git: warning: confstr() failed ...` into the recorded output: invisible in a
terminal, eight extra lines in the sandbox, and a red gate naming a divergence
its author could not reproduce. vise pins `TMPDIR` to the probe's own scratch,
which removes that one at the source, but the shape recurs. Two habits help:
normalize or drop host chatter, and remember a filter only covers the commands
you actually pipe through it — the `git init` beside the pipeline is where the
noise gets in.

**Let the agent run the gate before you give it work.** The cheapest possible
handover test is one turn:

> Run `vise gate --json` and report the exit code and verdict. Change nothing.

If the agent's answer differs from yours, stop and fix that before assigning
anything. Every environment failure in this project's dogfood — the missing
toolchain, the denied cache, the host warnings, the stale binary — would have
surfaced in that one turn instead of consuming a whole session.

**If you want per-step commits, give the agent write access to `.git`.** Several
sandboxes protect Git metadata by default: the agent gates green, tries to
commit, and gets `Operation not permitted` on `.git/index.lock`. Either grant
that access, or accept that the work arrives as one uncommitted diff and say so
up front — an agent that has been told commits are the loop will otherwise stop
and ask when it cannot make one.

**One agent, one worktree.** Give each agent its own `git worktree` and do not
commit into it while it is running, and never point two agents at the same one.
Both mistakes were made here. The second time, the agent caught it: the gate
returned `probe modified tracked files`, a harness class it knew it could not
have caused, and it refused to trust any timing taken under concurrent
mutation. If you see that class and no agent edited a tracked file, look for a
second writer before you look for a bug. Mid-flight commits move the baseline under
the agent, which then reasons about a repository that no longer exists — an
error worth naming because it was made here, and it cost a whole run.

A worked manifest and probe wrapper carrying all of this live in
[`examples/agent-ready/`](examples/agent-ready/), with a table of which failure
each guard prevents.

**Write the rules down where the agent will read them.** An `AGENTS.md` at the
repository root, naming the loop, the exit codes, and the four things never to
touch, is what stands between a red gate and an agent that "fixes" the judge.
This repository ships its own as a starting point.

## Command reference

```
vise init                          write whatever of vise.toml, AGENTS.md and the .gitignore
                                   entries is missing; never overwrites, safe to rerun
vise record [--allow-dirty] [--i-reviewed-the-diff] [--preview | --accept DIGEST]
                                   two full passes, atomic write of vise.lock and blobs, journal event;
                                   --preview shows the candidate diff and digest without writing baseline
                                   state, --accept writes only that candidate. --preview and --accept
                                   are the exclusive pair; --i-reviewed-the-diff is accepted alongside
                                   either and does nothing there — preview writes no state, and
                                   accept carries its own authorization
vise verify [--probe ID]           replay all probes or one; bounded diagnosis; journals the event
vise gate [--probe ID] [--quiet]   verify plus the one-line verdict; journals the event
vise run <probe-id>                one probe, streamed and not judged; exit mirrors the probe,
                                   except a timeout, a refused artifact or a lingering pipe holder,
                                   which have no probe exit to mirror and are exit 2
vise doctor                        what an operator should fix before an agent works here;
                                   runs no probe, writes nothing, exit 0
vise status                        the whole situation in one bounded read; exit 0
vise version                       0.3.0-dev
--json on every command            one JSON object instead of the human rendering
```

Every *judged* run is journaled, `verify` and `gate` alike. That matters more
than it looks: a green `verify` ends a flake chain exactly as a green `gate`
does, and a flaky `verify --probe` spends rerun budget. Only `run` judges
nothing and journals nothing.

Runtime-specific determinism traps (timestamps, hash ordering, preloaders, locale) are catalogued in [RUNTIMES.md](RUNTIMES.md).
