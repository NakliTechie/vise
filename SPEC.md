# vise — v0.3 specification

Status: DRAFT (2026-09-01), post two-family cold review (plan/2026-09-01-review-{claude,codex}.md). The contract below is the product; the implementation serves it. Changes from v0.2: fail-closed verdict model, flake decision table, journal made local, environment fingerprint, probe lifecycle pinned, crash-safety ordering, blobs committed, exit-expectation table, dependency closure, ergonomics fixes, scope trim.

## 0. Design doctrine — built for the agent in the driver's seat

vise's primary user is a coding agent mid-loop: context-poor, liable to be killed mid-turn, prone to circling on long campaigns and to rationalizing its own failures. Every interface decision below serves that user. The doctrine, in order of force:

1. **One perception act.** `vise status` renders the entire situation in one bounded read. An agent's first move in any session is `status`; it never reconstructs state by exploring.
2. **Machine-decidable, not merely machine-readable.** Every command supports `--json`; every outcome carries a typed **class** and a `next` field naming the remedy. The agent branches on exit code and class — never on prose.
3. **Typed verdicts = typed next actions.** One exit code per *distinct next action* (§4). Conflating any two forces the agent to investigate what the tool already knows.
4. **Bounded output, always.** Green is one line. Red shows first divergence + counts, never dumps. JSON reports failures and counts — never an entry per passing probe. No output grows with repo or probe count — only with divergence.
5. **Every failure names its remedy.** The error message is the documentation, delivered at the moment of need.
6. **Crash-safe by contract.** Every multi-artifact write follows the ordering protocol (§3.1); an interrupted command leaves the old state or the new state, never a hybrid. Idempotency means no state hybrid — an event log appending one event per run is correct, not a violation.
7. **The tool is the campaign's memory.** The journal (§6) is local, append-only JSONL keyed to commit SHAs — the anti-circling device `status` renders as a trajectory.
8. **Accretive by mechanism.** Every escaped defect becomes a probe — via the proposal flow (§5.1), so the doctrine has a gesture, not just an intention.
9. **A tower, not a toolbox.** **probe** → **lockfile** → **gate** → **journal** → **status**; each layer consumes only the one below. Tight loop at the gate; debugging descends to one probe; sessions open at status.
10. **The evaluator stays outside the loop — fail closed.** The gated agent can read everything and change nothing that judges it. When judgment itself is compromised (flaky probe, broken harness, environment drift), the gate is **indeterminate, never green**: a judge that can be ejected is not a judge. The full evaluator surface is manifest + lockfile + every input probes consume (§5.2).

## 1. Concepts

- **Probe** — one deterministic observation: a command run under declared stubs, its outputs hashed and diffed.
- **Manifest** (`vise.toml`) — the declared probe set. Committed; operator-territory.
- **Lockfile** (`vise.lock`) — the frozen truth, written only by `vise record`. Committed; operator-territory. Its content is a pure function of the probes' behavior — no wall-clock timestamps inside (record events with times live in the journal).
- **Verdict** — three-valued, fail-closed: **green** (every declared behavior probe judged *pass*), **red** (≥1 probe judged *behavior diff*, all others judged), **indeterminate** (≥1 probe unjudgable: flaky, harness-broken, or environment-drifted). Green requires *every* declared probe; nothing is excluded from the denominator, ever.

## 2. Manifest — `vise.toml`

```toml
[vise]
version = 1

[stubs]                       # defaults applied to every probe
tz = "UTC"
lang = "C"
seed = "1729"                 # exported as VISE_SEED; the app wires it to its RNG (a convention the app honors — vise intercepts nothing)
network = "declared-off"      # v0: a declaration probes promise to honor, NOT enforced by vise (enforcement is platform-specific — Parked)

[env]                         # optional: environment fingerprint, captured at record, re-checked at verify
fingerprint = ["sh --version | head -1", "jq --version", "node --version"]

[[probe]]
id = "cli-help"               # unique, stable; renaming = a new probe
run = "./mytool --help"      # executed with POSIX `sh -c` from the repo root
timeout = 30                  # seconds (default 30); killed via process group; output pipes close 1 s after the shell exits, so a detached process left holding them is a harness failure
deps = ["fixtures/in.csv", "scripts/strip-nonce"]   # optional: files this probe consumes — hashed into the probe record (§5.2)
files = ["out/result.json"]  # artifacts to hash after the run; MUST be untracked (vise deletes them before every run and refuses a tracked path as harness class)
```

`[[service]]` blocks (long-lived processes shared by probes — the Rails shape) are specified for v1 and land with the first server-shaped dogfood; v0 implements shell probes only. (Design retained from v0.2: `run` + `ready` poll + `$VISE_PORT`; note the port must be normalized out of recorded bytes by the probe — RUNTIMES.md tier 2.)

### 2.1 Metric probes

Refactoring is two-sided: unchanged behavior *and* improved internal quality. Metric probes measure the second — a single number tracked as a **delta**, never diffed for equality:

```toml
[[metric]]
id = "complexity"
run = "oxlint --format=json . | jq '[.diagnostics[] | select(.code==\"complexity\")] | length'"
direction = "down"            # "down" | "up"
enforce = "none"              # "none" (report only, default) | "no-regress" (worsening → exit 5)
version_cmd = "oxlint --version"   # optional; captured at record — a mismatch at verify is HARNESS class, not a quality regression
```

- `run` must print exactly one number; anything else is harness class.
- vise ships **no analyzer** and **never provisions tools** — no downloads, no network. A missing tool is exit 2 with `next: {action: "fix_probe", detail: "oxlint not on PATH — install it, then re-run"}`; the driving agent installs via its own tooling, guided at the moment of failure. `vise init` writes a static stub with one commented example metric; tool and ecosystem detection is parked (§8).
- A metric probe that yields different numbers across the flake re-run (§4.1) is classed flake → indeterminate, same as behavior probes.
- The metric definition (`run`, `direction`, `enforce`, `timeout`, `env`, `version_cmd`) is frozen as `run_hash` in the lockfile, like a probe's; a changed definition at verify is **harness class**, never an improvement. A lock recorded before definitions were frozen reports "metric definition was not frozen by this baseline; re-record".
- ~~ratchet~~ deferred to v1 with the journal-integrity redesign (the floor must live inside an operator-protected artifact, not a plain local file).

### 2.2 Probe execution contract

- **Probes run against the working tree as it stands** — the working tree *is* the thing under verification; vise never stashes, checks out, or copies. Mid-refactor dirty trees are the normal case.
- **Environment is sanitized, not inherited**: `PATH`, `HOME`, the stub set (`TZ`, `LANG`/`LC_ALL`, `VISE_SEED`, `SOURCE_DATE_EPOCH=0`, `VISE=1`, `PYTHONHASHSEED=0`, `NO_COLOR=1`, `TERM=dumb`, `COLUMNS=80`, `CI=1`), `VISE_TMP` and `TMPDIR` (both the per-probe scratch under `.vise/tmp/`, wiped after each run — pinning `TMPDIR` keeps every tool's temp files inside the probe's own scratch and stops the host's temp resolution from leaking warnings into the observation) — nothing else. Per-probe `env = { … }` adds declared variables. Non-tty stdio. Per-runtime quirks: RUNTIMES.md.
- **Isolation is behavioral**: probes are order-independent; a probe writes only to declared `files` and `$VISE_TMP`. **A probe must not change the checkout** — vise snapshots the work tree before and after every judged run and classes a violation as probe error. The snapshot covers tracked files by their diff against `HEAD`, and every file Git neither tracks nor ignores by content, because a stray a probe drops into the checkout is invisible to a tracked diff and visible to every later probe and build, which is exactly how order-dependence gets in. **Ignored paths are outside the snapshot on purpose**: a build cache is the one thing a probe is expected to write, and `.gitignore` is where the operator already declared which paths those are. Declared `files` are excluded too — vise deletes and recreates them each run by design, and compares them separately. A stray is reported by name (at most three, then a count). The untracked digest covers modification time as well as content, because a probe that writes a stray fails its first run and then rewrites the same bytes on the next one — a content-only comparison would turn *rerun it* into a way to launder a harness error, and the tracked half of this check has always failed on a mid-run write. The cost is proportional to the untracked, unignored bytes in the checkout — 18 files and 124 KiB in vise's own dogfood target, about 40 ms — and a repository that keeps a large unignored tree should ignore it, which is what `.gitignore` is for.
- **Containment ends at the session boundary, and that limit is stated, not implied.** A probe runs in its own process group; vise kills the group on timeout, kills it again once the shell exits (so an ordinary background child, double fork included, does not outlive the run), bounds the wait on its output pipes (1 s), refuses any run that changed `vise.toml`, `vise.lock`, or the journal or that added or removed a blob, and compares the work tree, tracked and untracked alike, before and after every judged run — each record pass and each verify replay. What escapes all of that is a child that leaves the **session**, not merely the group: `setsid`, or a daemonize idiom that calls it. vise cannot kill such a child, and a write it makes *after* the work-tree check is not attributed to the run. Two things still hold: blob *contents* are content-addressed and re-verified whenever they are read (§5 tamper line), and the residual belongs to the probe author (start nothing you do not wait for; redirect what you background) and to the harness policy. Enforcing more would need a platform containment boundary (a pid namespace, a sandboxed runner) and is deliberately out of scope for a portable, dependency-free CLI (2026-09-02 decision).
- **Lifecycle per run**: delete declared `files` → run under timeout (process-group kill) → hash stdout/stderr/exit/`files`. This holds for record passes and verify replays identically, so appending probes and stale artifacts cannot slip through.
- **Determinism self-test at record**: **two full suite passes** (not adjacent per-probe re-runs — full passes catch order-dependence). Any probe differing across passes fails the freeze loudly, named. Known limitation (stated, not hidden): a probe nondeterministic with sticky luck can pass twice; the verify-side flake protocol (§4.1) is the second net.
- **Git is required.** vise refuses to run outside a git work tree (exit 2): the clean-tree rule, journal keying, and the loop's revert semantics all assume it.
- **POSIX `sh -c` only in v0.** Windows is Parked, not silently half-supported.

## 3. Lockfile — `vise.lock`

**Canonical JSON** (one format, sorted keys, LF, UTF-8; byte-reproducible — resolves the TOML/JSON ambiguity), written atomically by `record` only:

```
{
  "v": 1,
  "fingerprint": { "os": "darwin", "arch": "arm64",
                    "stubs": { "tz": "UTC", "lang": "C", "seed": "1729", "network": "declared-off" },
                    "env": { "sh --version | head -1": "GNU bash 3.2…", "jq --version": "jq-1.7" } },
  "probes": {
    "cli-help": {
      "run_hash": "sha256:…",      // canonical-JSON serialization of the parsed probe entry (whitespace/reordering immune)
      "deps": { "fixtures/in.csv": "sha256:…" },
      "recorded_commit": "06aee75",
      "exit": 0,                    // whatever the probe produced — nonzero is a legitimate expectation (§4.2)
      "stdout": "sha256:…", "stderr": "sha256:…",
      "files": { "out/result.json": "sha256:…" }
    }
  },
  "metrics": { "complexity": { "run_hash": "sha256:…", "value": 148, "tool_version": "oxlint 1.35.0" } }
}
```

- No wall-clock fields — two consecutive `record`s on the same tree MUST produce byte-identical lockfiles (this is itself the self-test's acceptance check).
- `recorded_commit` names the commit an observation was **first** frozen at, not the last time `record` ran. A re-record that observes exactly what the baseline already holds carries the old commit forward, so unchanged behavior yields a byte-identical lockfile and an empty `git diff vise.lock`. Restamping HEAD every time churned the file and the tamper hash under it, which teaches a reviewer to skim the one diff that has to be read.
- **Blobs are committed**: full outputs live in `.vise/blobs/<sha256>` (content-addressed), and `.vise/blobs/` is **tracked** — so the agent-legible diff works in CI and fresh clones, where gates actually run. A blob over 256 KiB is stored hash-only with a lockfile marker (`"stdout_large": true`) and the diff degrades gracefully for that probe alone. `record` prunes blobs unreferenced by the new lockfile.
- **Observations are captured under a bound.** vise never holds a whole observation in memory: stdout, stderr, and each declared artifact are hashed and counted as they are produced while only the first 256 KiB (the same figure as the blob rule) is retained. A probe that prints gigabytes cannot exhaust the judge before its timeout fires. Judgment is the hash and the byte count, so an over-bound observation is compared exactly and rendered as `hash: expected …, got … (N bytes, larger than the 262144-byte capture bound)`. A divergence that lies inside the retained prefix is still rendered as bytes, with a line saying how far the stream ran. `vise run` is unaffected: raw execution streams every byte to the terminal as it is produced (§4), and only its `--json` form reports the bounded prefix plus `stdout_truncated`, `stdout_size`, and `stdout_hash`. An `[env] fingerprint` command must print less than the bound — its output is a lockfile value, and one larger than 256 KiB is a harness error rather than a recorded baseline.
- **Environment fingerprint**: `os`/`arch` always; the `[env] fingerprint` command outputs when declared. Any mismatch at verify → **harness class** (exit 2, `next: human` — "environment differs from recording: re-record on this machine, or restore the recorded toolchain"). This converts the cross-machine false-red (the reviews' top determinism finding) into a named, classed condition.
- Granularity: one file (v0.2 decision stands). Partial re-record (`record --probe`) is **deferred** — it created mixed-provenance lockfiles under one hash; `recorded_commit` is per-probe so the field survives when partial re-record returns.

### 3.1 Crash-safety ordering + concurrency

- One `flock` on `.vise/run.lock` per invocation that writes — `init`, `record`, `verify`, `gate`, `run`. Concurrent vise processes queue, never interleave. `status` takes none: it writes nothing, the lockfile it reads is replaced by atomic rename so it sees one whole generation or the other, and a torn journal tail is already tolerated.
- Write order, always: **blobs first** (content-addressed; orphans are harmless garbage) → **lockfile atomic rename** → **journal append**. A crash at any point leaves a valid prior state plus at worst orphan blobs, pruned at the next record.

## 4. CLI contract

Shared exit-code vocabulary, one code per distinct next action:
`0` ok/green · `1` behavior diff (→ revert) · `2` harness error: broken probe, missing tool, env-fingerprint mismatch, no git, dirty-tree record (→ fix the harness or hand to human — never the refactor) · `3` indeterminate: flake detected (→ quarantine-ack; do NOT treat as pass or fail) · `4` not initialized (→ record first) · `5` metric regression under `no-regress` (→ the change held behavior but worsened quality).

**Failure precedence** when classes co-occur (one exit code; all classes still listed in output): `4 > 2 > 3 > 1 > 5` — no behavior verdict from a broken harness, no diff verdict from a flaky probe, quality only after behavior holds.

**`--json`** replaces the human rendering with exactly one object, on every command including `help`:

```json
{ "v": 1, "cmd": "gate", "exit": 1, "verdict": "red",
  "classes": ["behavior"],
  "counts": { "declared": 7, "pass": 5, "behavior": 2, "flaky": 0, "harness": 0 },
  "failures": {
    "cli-help": { "class": "behavior", "expect": {"exit": 0}, "got": {"exit": 1},
                   "diff": "…first-divergence unified diff, truncated with counts…" } },
  "metrics": { "complexity": { "base": 148, "now": 131, "delta": -17 } },
  "lock": "sha256:…",
  "next": { "action": "revert", "detail": "unintended behavior change in cli-help; if intended, a human runs vise record" } }
```

Passing probes appear only in `counts` (§0.4). `next.action` is a **closed vocabulary**: `proceed` · `revert` · `fix_probe` · `rerun` · `record_first` · `quarantine_ack` · `human` (`rerun` is reserved and not emitted in v0.3). Exactly one action; alternatives (like the intended-change path) live in `detail`. Free text lives only in `detail`.

### 4.1 The flake protocol (fail-closed)

At verify, each probe runs **once**. On any mismatch with the lockfile, that probe runs **once more** (mismatching probes only — the every-commit cadence stays cheap):
- second run **matches the first** → **behavior diff** (consistent new behavior);
- second run **differs from the first** → **flake** → verdict **indeterminate**, exit 3, a `flake` event journaled.

A flaky probe is **never excluded from the verdict** — indeterminate is not green, and there is no quarantine state that shrinks the denominator (this closes the reward-hack both reviews ranked #1: an agent cannot eject a judge by making it flaky). `next: quarantine_ack` means: journal it, tell the operator, and *only* continue if the caller's harness policy explicitly tolerates indeterminate — vise's own exit code stays 3. Resolution is operator-only: fix the nondeterminism (app or probe) and `vise record`, or remove the probe from the manifest — both leave an audit trail (journal + git).

vise itself enforces **`rerun` at most once**: the third consecutive `gate` or `verify` for the same commit, lock, and probe set is refused (journal-checked, exit 2) with `next: human`. The refusal is not journaled and does not reset the chain; a chain ends only at a `record`, a journaled green or red verdict (every judged `verify` or `gate` is journaled) whose probe set covers this one, or a new commit or lock — so a refusal persists until the operator commits, re-records, or changes the manifest. A journal tail that is truncated without reaching such a boundary is refused too.

### 4.2 Exit-expectation table

`record` freezes whatever exit the probe produces — **nonzero is a legitimate expectation** (a probe may assert a failure mode). "Probe error" means only: launch failure (spawn error / 127), timeout, tracked-file mutation, or self-test divergence. At verify:

| Recorded | Observed | Class |
|---|---|---|
| exit E, bytes B | exit E, bytes B | pass |
| exit E, bytes B | different exit or bytes (stable across re-run) | behavior |
| exit E, bytes B | different, unstable across re-run | flake → indeterminate |
| anything | launch failure / timeout | harness |

(`record` refuses to freeze launch failures and timeouts — so at verify they always mean the harness broke, not the code.)

### The commands

**`vise doctor`** — the operator's readiness check, run before the repository is handed to an agent. Six static checks, each derived from a failure observed while running real coding agents against a gated checkout: no `[env] fingerprint` (a toolchain that moves without saying so); a probe `run`, `deps`, or `env` value naming an absolute path, `~`, or `$HOME` (the operator's machine is not the agent's sandbox, and the `[env] fingerprint` command is checked too because that is where an SDK or module-cache path ends up); a file in the repository that a probe runs but does not declare in `deps` (the run string is hashed, the script it calls is not, so editing the script changes what is observed with no harness drift); an uncommitted `vise.lock` or blob directory (a fresh clone, which is where the agent and CI both work, cannot gate); vise's per-checkout state not ignored by Git (every gate then dirties the tree, which the agent reports as a change it did not make); and no `AGENTS.md` (an agent with no written rules discovers them by breaking them). Findings are grouped by cause, not by site, and each carries a remedy. Runs no probe and writes nothing. **Always exit 0**, for the same reason `status` does: it reports a situation to an operator, it does not judge a change, and giving it a code would add a seventh meaning to a vocabulary whose value is that each code names exactly one next action.

**`vise status`** — the single perception act. The identity of the binary answering (`tool`: version, revision, modified — JSON only, so the human rendering stays stable enough to be a probe surface), manifest validity, lockfile state (probes covered, fingerprint match, `recorded_commit` spread), static manifest-versus-lock drift (`lock.drift`, capped at 5 lines in the human view; state `baseline-drift` when the next gate would refuse without running a probe), rerun-limit state (`rerun-refused` when the next gate would be refused, §4.1), last verdict, metric trajectory, journal tail (last 5 events), recent flakes. Bounded ≤ ~30 lines / one object regardless of history. Read-only in the strict sense: it takes no state lock and creates nothing, so a repository that has never run vise is unchanged by asking it what its situation is, and asking is possible *while* a record or gate holds the lock — the moment you most want to ask what is happening is while something is happening. `doctor` is read-only on the same terms. An unrecognized command is refused before the lock is reached, so a typo cannot wait out a running suite. It does execute the manifest's `[env] fingerprint` commands to report live environment drift — they are operator-declared like probes, and a stale drift report would send the agent into a refusing gate; a fingerprint command that writes files is an operator bug, caught by the same work-tree snapshot probes get. **Always exit 0** (the one deliberate exception to the shared vocabulary — status *reports* red, it doesn't *fail* red; stated here so it is contract, not accident).

**`vise record`** — freeze. Two full suite passes (§2.2), byte-identical lockfiles required, then blobs → lockfile → journal (§3.1). Requires a clean working tree (frozen truth must correspond to a returnable commit); `--allow-dirty` overrides and journals `"dirty": true`. **Overwriting an existing lockfile** requires a review gesture. Two-phase, agent-safe: `record --preview` runs both passes and returns the candidate's behavior diff old-vs-new (§5) plus `candidate`, the sha256 of the canonical candidate lockfile, writing no baseline state (no blobs, no lock, no journal event — the probes still run and regenerate their declared artifacts); `record --accept <digest>` runs the passes again and writes only if the candidate still matches — a tree or environment change in between is refused. One-step, human-shaped: `--i-reviewed-the-diff` prints the diff and writes. All of these compose with `--allow-dirty`; all are operator-territory. Exit: 0 · 2 (probe error, dirty tree, no git) · 3 (self-test divergence).

**`vise verify`** — replay all probes, judge per §4.1/§4.2. One block per failing probe: class tag, expected/got, first-divergence unified diff (truncated with counts), `next:` line. Metric deltas. `lock:` hash line. `--probe <id>` — **judged single-probe verify** (the missing rung between raw `run` and the full suite; verdict for that probe only, lockfile untouched, exit codes as usual).

**`vise gate`** — verify + one-line verdict (`GATE GREEN — 8/8` / `GATE RED [behavior] — 6/8: cli-help, convert-fixture` / `GATE INDETERMINATE [flake] — 7/8: convert-fixture`; counts include metrics, and metric deltas appear in `verify` output and in `--json`, not on the gate line) + journal event. Same exits. `--quiet` = verdict line only. Re-running gate appends another journal event — correct for an event log (§0.6).

**`vise run <probe-id>`** — raw single-probe execution: stdout/stderr/exit to the terminal, no judgment, no lockfile. Exit mirrors the probe's own exit (documented exception #2 to the vocabulary — `run` output IS the probe's output). A launch failure is the probe's own exit 127 and passes through; a timeout, a refused artifact, or a lingering pipe holder has no probe exit to mirror and exits 2.

**`vise init`** — writes the agent contract (`AGENTS.md`, embedded in the binary, never overwriting one the project already has — a gate nobody explained is a gate an agent works around) and a **stub manifest** (stubs block + one commented example probe + one commented example metric; no tool or ecosystem detection — parked, §8) and adds `.vise/journal.jsonl` + `.vise/run.lock` + `VISE_TMP` residue to `.gitignore` (`.vise/blobs/` stays tracked). Never records, never overwrites an existing manifest. Multi-ecosystem *probe* detection (package.json scripts etc.) is deferred — the stub delivers the adoption moment.

## 5. Operator territory

- `vise.toml`, `vise.lock`, and `.vise/blobs/` are the judge: the gated agent reads them, never writes them. `.vise/journal.jsonl` is local and gitignored (§6) but joins the protected surface: the rerun limit (§4.1) is derived from it, so an agent that edits the journal can buy itself reruns — vise does not detect that, the harness must prevent it. Enforcement is the caller's harness policy (hook/permission rule denying `vise record` + writes to those four paths to the agent) — vise cannot authenticate who typed a command and says so honestly. `record`'s overwrite gesture (`--i-reviewed-the-diff`, diff shown first, journaled) is deliberately human-shaped.
- The tamper line: verify/gate print `lock: sha256(manifest+lockfile+blobs-index)`. **The trusted anchor is git** — the hash is reproducible from the committed artifacts at the recorded commit, so CI (or the operator) compares the printed hash against one recomputed from `git show` of the trusted branch. Not a cryptographic identity system; an honest tripwire, and stated as such.

### 5.1 The proposal flow (escaped defects become probes)

When the driving agent discovers uncovered behavior — an escaped defect, a gap the diff revealed — it **drafts** a probe into `.vise/proposals.toml` (same schema as `[[probe]]`, agent-writable, judged by nobody, but validated exactly as a probe is — a proposal an operator could not promote is refused when it is drafted). `vise status` reports the count of pending proposals (`pending_proposals`); the ids live in the file. A malformed proposals file is reported as `proposal_error` and never changes the status `state` or `next` action. The operator reviews, moves accepted entries into `vise.toml`, and — for a defect — records **after the fix lands**, as an acknowledged baseline (recording before the fix would freeze the defective output; codex review, finding 9). The doctrine gets a gesture; the judge stays operator-owned.

### 5.2 The dependency closure

A probe's judgment depends on more than the manifest entry: fixtures, normalizer scripts, seed files. Declared `deps` paths are hashed into the probe's lockfile record — a changed dep at verify is **harness class** ("probe input changed, not behavior — re-record or restore"). Undeclared dependencies are the honest residual: the spec states that the evaluator surface includes everything probes consume, and harness policy should protect probe-referenced scripts the same as the manifest.

## 6. The journal — `.vise/journal.jsonl`

**Local, gitignored, append-only** JSONL — operator-side state like blobs' scratch, NOT committed (v0.2 had it committed; that dirtied the tree after every gate, broke one-transform-per-commit, and made merge conflicts by design — both reviews' finding). Cross-machine trajectory is out of scope for v0; the committed artifacts (lockfile, blobs) carry everything judgment needs.

```jsonl
{"e":"record","commit":"06aee75","dirty":false,"counts":{"declared":7,"pass":7},"lock":"sha256:…"}
{"e":"gate","commit":"1652409","dirty":true,"verdict":"green","counts":{"pass":7},"metrics":{"complexity":131},"probe_set":["cli-help","convert-fixture","complexity"],"lock":"sha256:…"}
{"e":"flake","commit":"1652409","dirty":true,"verdict":"indeterminate","flaky":["convert-fixture"],"probe_set":["cli-help","convert-fixture","complexity"],"lock":"sha256:…"}
```

- Keyed to HEAD SHA + `dirty` flag ("judged the working tree at this base", never "judged this commit").
- `status` derives flake history, trajectory, and rerun-refusal state from the journal tail; it never replays unbounded history (bounded scan from the end).

## 7. Harness integration (spec-level)

- **The canonical loop:** operator `record` once → per micro-step: transform → commit → `vise gate --json` → branch on `next.action` (`proceed` · `revert` + journal the dead end · `fix_probe` — repair harness, never code · `quarantine_ack` — continue only if the harness policy tolerates indeterminate, else stop · `human` — park). One transform type per commit; the gate runs between every commit.
- **ntkit:** `/autopilot-nt` refactor runs use `vise gate` as the per-item verifier and `vise status` in the morning report; `/lab-nt` campaigns hill-climb a metric probe with "gate stays green" as the fence. (Parked until dogfood.)
- **CI:** `vise gate --quiet` as a required check; recompute the `lock:` hash from the trusted branch as the tamper tripwire (§5).
- **Operator-only enforcement:** a PreToolUse hook / permission rule denying the agent `vise record` and writes to `vise.toml` / `vise.lock` / `.vise/blobs/` / `.vise/journal.jsonl` (the last one guards the rerun limit, §4.1).

## 8. Non-goals (v0)

- No transform engine, no model calls, no code edits, no tool provisioning, no network — ever.
- No probe types beyond shell commands (HTTP/DOM parked; a shell probe may shell out to `curl`/Playwright). Metric probes are in v0; `[[service]]` lands with the Rails dogfood.
- No `vise map` (v1: tree-sitter/SCIP-class structural diff evidencing what the refactor did).
- Deferred from v0.2 after review: **ratchet** (needs an operator-protected floor), **`record --probe`** (chimera lockfiles), **init ecosystem detection** (stub manifest only).
- No parallel probe execution, no caching, no daemon. Boring and sequential until the vertical works.
- `network = "declared-off"` is a promise probes make, not something vise enforces (platform enforcement Parked; stated to avoid the false-green license the reviews flagged).
