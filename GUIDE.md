# vise guide

A walk through one vise campaign on the command line, with the real output of every command. Every transcript below was produced by the built binary against a scratch repository; if a line here disagrees with what your terminal shows, the terminal wins and this file needs an edit. The contract behind each command is in [SPEC.md](SPEC.md); this guide shows what it looks like in use.

## Requirements

- Git, a POSIX `/bin/sh`, Go 1.25.8 or newer.
- Build from this checkout: `go install ./cmd/vise` (then `vise version` prints `vise 0.3.0-dev`).

## The loop in one screen

```
vise status          # where am I? (always exit 0)
vise init            # once: write a starter vise.toml
<declare probes, commit>
vise record          # freeze behavior into vise.lock (+ .vise/blobs/), commit both
<refactor one thing, commit>
vise gate --quiet    # GREEN → next step · RED → revert · INDETERMINATE → stop and read
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

`init` writes a commented starter manifest and the `.gitignore` lines for local state (`.vise/journal.jsonl`, `.vise/run.lock`, `.vise/tmp/`). It never overwrites an existing `vise.toml`.

```
$ vise init
INITIALIZED — wrote vise.toml and wired local state into .gitignore
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

`record` runs every probe twice (a probe that differs between the passes fails the freeze) and refuses a dirty tree, because the frozen truth must correspond to a commit you can return to:

```
$ vise record
RECORD INDETERMINATE [harness] — 0/0
working-tree [harness] — record requires a clean working tree; commit or stash changes, or pass --allow-dirty
next: human — commit or stash the current tree, or rerun record with --allow-dirty
[exit 2]
```

Commit the manifest, record, commit the lock and blobs:

```
$ git add vise.toml .gitignore && git commit -m "Add vise probes"
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
{"cmd":"gate","counts":{"behavior":0,"declared":2,"flaky":0,"harness":0,"pass":2},"exit":0,"lock":"sha256:47ea…","next":{"action":"proceed","detail":"all declared checks matched"},"v":1,"verdict":"green"}
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
candidate: sha256:57da…
next: human — review the diff, then freeze it with record --accept sha256:57da…
[exit 0]

$ vise record --accept sha256:57da…
RECORDED — 2 probe(s) · 0 metric(s)
lock: sha256:…
[exit 0]
```

If the tree changed between preview and accept, the digest no longer matches and `accept` refuses (exit 2). The one-step form prints the diff and writes in one go:

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

The review also lists probe and metric definition changes (by run_hash), dependency hash changes, fingerprint drift, and metric baseline changes; an added or removed entry shows its exit, output hashes, or value.

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

Two reruns per commit, lock, and probe set; the third is refused so a loop cannot circle. `status` says so:

```
$ vise status
VISE STATUS — RERUN-REFUSED
…
journal: flake · d7ff97… · indeterminate · flaky=greet
journal: flake · d7ff97… · indeterminate · flaky=greet
next: human — the next gate is refused (second consecutive rerun already consumed for this commit, lock, and probe set); commit, re-record, or change the manifest
[exit 0]
```

Recovery is operator-shaped: fix the nondeterminism (app or probe) and re-record, remove the probe, or commit (a new commit starts a fresh chain). Restoring the deterministic script alone is not enough at the same commit; after `git commit`, the gate runs again and goes green.

## 7. Harness failures: the judge itself is broken

Anything that stops judgment before behavior can be compared is harness class, exit 2, and names its remedy. Three you will meet:

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

Also harness, also exit 2: a probe that cannot be launched (127), a timeout, a probe that modifies tracked files, a declared artifact that is tracked by git (vise deletes artifacts before every run and refuses to delete a tracked file), a probe that leaves a background process holding its stdout, and a manifest with no probes.

## 8. Metrics: hold behavior, prove improvement

A metric probe prints one number and is tracked as a delta, never diffed for equality:

```toml
[[metric]]
id = "complexity"
run = "cat metric.txt"
direction = "down"
enforce = "no-regress"
```

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

`status` always exits 0 and reports instead of failing. `run` mirrors the probe's own exit (a launch failure is 127).

## 11. Operator territory

`vise.toml`, `vise.lock`, `.vise/blobs/`, and the local `.vise/journal.jsonl` are the judge. vise cannot authenticate its caller: the agent harness must deny the gated agent writes to those paths and deny `vise record` during a campaign. The journal is on the list because the rerun limit is derived from it.

## Command reference

```
vise init                          write a starter vise.toml and .gitignore entries; never overwrites
vise record [--allow-dirty] [--i-reviewed-the-diff | --preview | --accept DIGEST]
                                   two full passes, atomic write of vise.lock and blobs, journal event;
                                   --preview shows the candidate diff and digest without writing baseline
                                   state, --accept writes only that candidate
vise verify [--probe ID]           replay all probes or one; bounded diagnosis
vise gate [--probe ID] [--quiet]   verify plus the one-line verdict; journals the event
vise run <probe-id>                one probe raw; exit mirrors the probe
vise status                        the whole situation in one bounded read; exit 0
vise version                       0.3.0-dev
--json on every command            one JSON object instead of the human rendering
```

Runtime-specific determinism traps (timestamps, hash ordering, preloaders, locale) are catalogued in [RUNTIMES.md](RUNTIMES.md).
