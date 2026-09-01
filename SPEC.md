# vise — v0 specification

Status: DRAFT (2026-09-01). The contract below is the product; the implementation serves it.

## 0. Design doctrine — built for the agent in the driver's seat

vise's primary user is a coding agent mid-loop: context-poor, liable to be killed mid-turn, prone to circling on long campaigns and to rationalizing its own failures. Every interface decision below serves that user. The doctrine, in order of force:

1. **One perception act.** `vise status` renders the entire situation — wiring, lockfile freshness, last verdict, metric trajectory, journal tail — in one bounded read. An agent's first move in any session is `status`; it never reconstructs state by exploring.
2. **Machine-decidable, not merely machine-readable.** Every command supports `--json`; every outcome carries a typed **class** and a `next` field naming the remedy. The agent branches on exit code and class — it never parses prose to decide.
3. **Typed verdicts = typed next actions.** The exit-code vocabulary is shared by all commands, one code per *distinct next action*: `0` proceed · `1` behavior diff → revert · `2` harness broken → fix manifest/probe, not code · `3` nondeterminism → quarantine the probe, don't chase ghosts · `4` not initialized → record first · `5` metric regression (under `no-regress`) → the change "worked" but made things worse. Conflating any two would force the agent to investigate what the tool already knows.
4. **Bounded output, always.** Context is the agent's scarcest resource. Green is one line. Red shows first divergence + counts, never dumps. Blobs stay on disk; the transcript gets hashes and minimal diffs. No command's output grows with repo size — only with divergence.
5. **Every failure names its remedy.** The error message is the documentation, delivered at the moment of need: what happened · what it means · the exact next command. No manual required mid-loop.
6. **Crash-safe by contract.** Agents die mid-turn. Every write is atomic (temp + rename); an interrupted command leaves either the old state or the new state, never a hybrid. Every command is idempotent to re-run.
7. **The tool is the campaign's memory.** The journal (§6) is an append-only JSONL event log keyed to commit SHAs — the anti-circling device. `status` renders it as a trajectory ("14 green gates, complexity 148→131, 2 reds reverted"), so a resuming agent reads the arc, not the history.
8. **Accretive by mechanism, not intention.** Progress locks in mechanically: the **ratchet** (§2.1) makes the best-seen metric the new floor; **every escaped defect becomes a probe** (a behavior change that slipped past the gate is a coverage gap — the fix is a probe, recorded before the bug is fixed); the journal makes each session smarter than the last.
9. **A tower, not a toolbox.** Five layers, each consuming only the one below, each independently legible: **probe** (atomic observation) → **lockfile** (frozen truth) → **gate** (one bit, typed) → **journal** (trajectory) → **status** (situation). The agent enters at the altitude its task needs: tight loop lives at the gate; session start at status; debugging descends to a single probe (`vise run`).
10. **The evaluator stays outside the loop.** The gated agent can read everything and change nothing that judges it: manifest and lockfile are operator-territory (§5), and verify/gate output carries the manifest+lockfile hash so a harness can detect tampering.

## 1. Concepts

- **Probe** — one deterministic observation of the codebase's behavior: a command run under declared stubs, producing an output the lockfile can hash and diff.
- **Manifest** (`vise.toml`) — the repo's declared probe set. Human-authored, committed, read-only to a gated agent.
- **Lockfile** (`vise.lock`) — the frozen behavior: one record per probe, written only by `vise record`. Committed, read-only to a gated agent.
- **The gate** — `vise verify` semantics with a hard exit code. Green means: every probe replayed byte-identical to the lockfile. Anything else is red — including a probe that failed to run.

## 2. Manifest — `vise.toml`

```toml
[vise]
version = 1

[stubs]                       # defaults applied to every probe
tz = "UTC"                    # TZ env
lang = "C"                    # LANG/LC_ALL
seed = "1729"                 # exported as VISE_SEED; the project wires it to its RNG
network = "refuse"            # "refuse" (default) | "allow" — refuse fails the probe on any network attempt where enforceable, and is always declared in the lockfile

[[probe]]
id = "cli-help"               # unique, stable; renaming = a new probe
run = "./mytool --help"      # executed with `sh -c` in the repo root
timeout = 30                  # seconds; timeout = probe failure, never a hang

[[probe]]
id = "convert-fixture"
run = "./mytool convert fixtures/in.csv"
files = ["out/result.json"]  # artifacts to hash after the run, in addition to stdout/stderr/exit
```

### 2.1 Metric probes — the quality half of the loop

Refactoring is two-sided: unchanged behavior *and* improved internal quality. Behavior probes enforce the first; **metric probes** measure the second. A metric probe's output is a single number tracked as a **delta**, never diffed for equality:

```toml
[[metric]]
id = "complexity"                       # total cyclomatic complexity via the house linter
run = "oxlint --format=json . | jq '[.diagnostics[] | select(.code==\"complexity\")] | length'"
direction = "down"                      # which way is better: "down" | "up"
enforce = "none"                        # "none" (report only, default) | "no-regress" (worsening turns the gate red)
```

- `run` must print exactly one number on stdout; anything else is a probe error (exit 2 class).
- vise ships **no analyzer** — the metric is whatever tool the repo already trusts (oxlint, gocyclo, wc, a bundle-size check). vise only runs, records, and compares.
- `verify`/`gate` report metrics as a delta line: `complexity: 148 → 131 (−17) ✓ improving`. With `enforce = "no-regress"`, a worsening metric is a gate failure with its own message class — distinguishable from a behavior diff.
- Lockfile records the baseline value per metric; `record`'s determinism self-test applies (same number twice, or the metric is flaky and the freeze fails).
- **The ratchet** (opt-in: `enforce = "ratchet"`): the best value seen across green gates becomes the new floor, persisted in the journal — quality can only improve or hold, mechanically. The accretion device for long campaigns: a good state, once reached, cannot be silently lost.

Rules (behavior probes):
- **Probes run against the working tree as it stands** — the working tree *is* the thing under verification; vise never stashes, checks out, or copies. Mid-refactor dirty trees are the normal case, not an edge case.
- **Isolation is behavioral, not physical**: probes must be order-independent — no probe reads state another probe wrote. A probe may write only to (a) paths it declares in `files` and (b) scratch space (`$VISE_TMP`, provided per-probe, wiped after). vise runs probes sequentially in manifest order but the contract permits any order; `record`'s double-run self-test structurally catches most order-dependence (a probe polluted by a predecessor differs across passes).
- **Environment is sanitized, not inherited**: probes get a minimal fixed env — `PATH`, `HOME`, the stub set (`TZ`, `LANG`/`LC_ALL`, `VISE_SEED`, `SOURCE_DATE_EPOCH=0`, `VISE=1`), and `VISE_TMP` — nothing else. Repo-specific vars are declared per-probe (`env = { FOO = "bar" }`) so the manifest, not the operator's shell, defines the probe.
- **POSIX `sh -c` only in v0.** Cross-platform shells are a portability tarpit; Windows support is a Parked question, not a silent half-support.
- `run` must be self-contained from the repo root; no probe depends on another probe's side effects.
- A probe that cannot be made deterministic under the declared stubs does not belong in the manifest. `vise record` run twice back-to-back MUST produce identical lockfiles; if it doesn't, record fails loudly naming the flaky probe (**the self-test**: determinism is verified, not assumed).
- Env for every probe: `TZ`, `LANG`/`LC_ALL`, `VISE_SEED`, `SOURCE_DATE_EPOCH` (fixed), plus `VISE=1` so app code can gate its own stub seams (the LocalMind monkeypatch-seam pattern).

## 3. Lockfile — `vise.lock`

One TOML/JSON document (single file, v0 — per-probe splitting is a Parked question), written atomically by `record` only:

```
[meta]      vise_version · recorded_at (from SOURCE_DATE_EPOCH honesty: wall time recorded but excluded from diffing) · stub set
[probe.<id>]
  run_hash      = sha256 of the probe's manifest entry (detects probe edits)
  exit          = 0
  stdout_sha    = sha256   # full text stored under .vise/blobs/<sha> for diffing
  stderr_sha    = sha256
  files         = { "out/result.json" = sha256, ... }
  duration_ms   = 412      # informational, never diffed
```

- Full outputs are stored as blobs under `.vise/blobs/<sha256>` (gitignored, content-addressed, re-derivable); the lockfile carries hashes so the committed artifact stays small and diff-noise-free. A missing blob degrades gracefully: verify still judges (hashes suffice) but the human-readable diff says `blob absent — re-run vise record on the recorded commit to regenerate`. `record` prunes blobs unreferenced by the new lockfile (content-addressing makes this safe).
- **Lockfile granularity: one file** (resolves the open question). Per-probe files would ease partial re-record diffs but fragment the tamper hash and multiply atomic-write surfaces; a single lockfile keeps "the frozen truth" one artifact with one hash. Partial re-record (`--probe`) rewrites the one file atomically.
- `run_hash` mismatch (manifest edited since record) = red at `verify`, with a distinct message: "probe changed, not behavior — re-record required."

## 4. CLI contract

Global: shared exit-code vocabulary (§0.3): `0` ok · `1` behavior diff · `2` harness error · `3` nondeterminism · `4` not initialized · `5` metric regression.

**Failure precedence** (one exit code even when classes co-occur): `4 > 2 > 3 > 1 > 5` — you can't trust a behavior verdict from a broken harness, you can't trust a diff from a flaky probe, and a metric regression only matters once behavior holds. All co-occurring classes still appear in the output/JSON; only the exit code collapses to the dominant one.

**`--json`** replaces the human rendering (never both) with exactly one object on stdout:

```json
{
  "v": 1,                       // schema version
  "cmd": "gate",
  "exit": 1,                    // mirrors the process exit code
  "verdict": "red",             // "green" | "red" | "ok" (non-judging cmds)
  "classes": ["behavior"],      // all co-occurring classes, dominant first
  "probes": {
    "cli-help": { "class": "behavior", "expect": {"exit": 0}, "got": {"exit": 1},
                   "diff": "…first-divergence unified diff, truncated…" },
    "convert-fixture": { "class": "pass" }
  },
  "quarantined": ["flaky-probe-id"],
  "metrics": { "complexity": { "base": 148, "now": 131, "delta": -17, "floor": 131 } },
  "lock": "sha256:…",           // manifest+lockfile hash (tamper evidence)
  "next": { "action": "revert", "detail": "unintended behavior change in cli-help" }
}
```

**The `next.action` vocabulary is closed** — an agent branches on it without NLP: `proceed` · `revert` · `fix_probe` (harness error: the named probe/manifest key) · `rerun` (transient environment suspicion — at most once) · `record_first` · `quarantine_ack` (flake journaled; continue, human re-records later) · `human` (anything vise cannot safely prescribe — includes all intended-change acceptance). Free text lives only in `next.detail`.

### `vise status`
The single perception act (§0.1). Renders: wired or not (manifest present, valid), lockfile state (present · probes covered · manifest hash match · recorded-at commit), last verdict, metric trajectory (baseline → current, over N gates), journal tail (last 5 events), and quarantined probes. Bounded: never more than ~30 lines / one JSON object regardless of repo or history size. Read-only, always exit 0 (status *reports* red, it doesn't *fail* red).

### `vise record`
Freeze current behavior. Runs every probe twice (the determinism self-test), writes `vise.lock` + blobs atomically. Fails without writing if any probe is flaky, errors, or times out — a lockfile is never partially written.
- `--probe <id>` — re-record one probe (still self-tested).
- Exit: 0 recorded · 2 probe error · 3 determinism self-test failed.

### `vise verify`
Replay all probes against the working tree; diff against the lockfile. Output one block per failing probe, **agent-legible**:

```
FAIL cli-help [behavior]
  exit: expected 0, got 1
  stdout: differs (line 3)
    - Usage: mytool [convert|check] <file>
    + Usage: mytool [convert] <file>
  next: unintended → revert the last change. intended → a human runs `vise record`.
```

Minimal unified diff, first divergence first, truncated with a count when large. No color, no spinner, stable ordering. Metric lines as deltas (§2.1). Output includes `lock: <manifest+lockfile hash>` so a harness can detect gate-tampering.
- Exit: per the shared vocabulary — `1` behavior · `2` harness · `3` flake (a probe that differs from the lockfile *and* from its own re-run is classed nondeterministic, not a behavior diff) · `5` metric regression under `no-regress`.

### `vise gate`
`verify` with a one-line verdict (`GATE GREEN — 7/7 probes · complexity −17` / `GATE RED [behavior] — 2/7 differ: cli-help, convert-fixture`) and the same exit codes. The command a refactor loop calls between commits and CI calls before merge. `--quiet` prints the verdict only.

### `vise run <probe-id>`
The debugging descent (§0.9): execute one probe raw — stdout/stderr/exit to the terminal, lockfile untouched, no verdict. For poking a red probe cheaply instead of re-running the world. Exit mirrors the probe's own exit.

### `vise init`
Adoption in one move: draft a `vise.toml` **deterministically from repo facts** — package.json scripts, Makefile targets, bin entries, existing test commands — as commented-out probe candidates plus the stub block. Draft only: it never records, never overwrites an existing manifest; the operator uncomments what's real and runs `record`. (Deterministic derivation, no model — same repo, same draft.)

## 5. The re-record gesture (evaluator outside the loop)

An *intended* behavior change mid-refactor must not open the gate to the agent:
- `record` refuses to overwrite an existing lockfile unless (a) the working tree is clean of unstaged changes to `vise.toml`, and (b) the flag `--i-reviewed-the-diff` is present, which first prints the full behavior diff (old lockfile vs new run) and the probe list it will rewrite.
- The flag name is deliberately human-shaped; harness policy (hooks / permission rules / CLAUDE.md) instructs agents that `vise record` is operator-only. vise itself cannot verify who typed it — the enforcement seam is the caller's harness, and SPEC says so honestly rather than pretending a CLI can authenticate intent.
- Every overwrite appends a `record` event to the journal (§6) — the audit trail a morning report can read.

## 6. The journal — `.vise/journal.jsonl`

Append-only JSONL, one event per command that changes or judges state. The campaign's memory (§0.7) and the substrate `status` renders. Committed (it's small — one line per event).

```jsonl
{"e":"record","commit":"06aee75","probes":7,"lock":"sha256:…"}
{"e":"gate","commit":"1652409","verdict":"green","probes":"7/7","metrics":{"complexity":131},"lock":"sha256:…"}
{"e":"gate","commit":"9f21c04","verdict":"red","class":"behavior","failed":["cli-help"],"lock":"sha256:…"}
{"e":"quarantine","probe":"convert-fixture","reason":"nondeterministic on verify (self-diff)"}
{"e":"ratchet","metric":"complexity","floor":131}
```

- Keyed to **commit SHAs, not wall time** — replayable, diffable, and honest under `SOURCE_DATE_EPOCH`. Mid-refactor gates run on dirty trees by design, so every event carries `"commit": "<HEAD sha>"` plus `"dirty": true|false`; a dirty-tree event means "judged the working tree at this base," not "judged this commit."
- **Quarantine**: a probe classed nondeterministic at verify (exit 3) is journaled and excluded from the verdict with a visible count (`7/7 green · 1 quarantined`) until re-recorded — a flaky probe must never be silently green *or* silently block the loop.
- `status` derives everything from journal + lockfile + manifest; it holds no state of its own.

## 7. Harness integration (spec-level)

How the loop actually drives vise, in the three harness shapes that matter here:

- **Inside an agent's refactor loop (the canonical use):** freeze once (`vise record`, operator), then per micro-step: transform → commit → `vise gate --json` → branch on `next.action` (`proceed` → next step; `revert` → `git revert`/restore and journal the dead end; `fix_probe` → repair the harness, never the code, then re-gate; `quarantine_ack` → continue, flag for the human; `human` → park and move on). One transform type per commit; the gate runs *between every commit*, not at the end — the cadence is the value.
- **ntkit:** an `/autopilot-nt` refactor run treats `vise gate` as the per-item verifier and `vise status` as part of the morning report; a `/lab-nt` refactor campaign uses a metric probe as its contract metric with "gate stays green" as the fence. No ntkit changes shipped yet — parked until vise dogfoods (pending.md).
- **CI:** `vise gate --quiet` as a required check; exit code is the verdict, `lock:` hash in the log is the tamper evidence.
- **Hook-level enforcement of operator-only `record`** (Claude Code example, for the README eventually): a PreToolUse hook or permission rule denying `vise record` to the agent while allowing `verify`/`gate`/`status`/`run`. vise's own seam (§5) plus the caller's policy together close the loop.

## 8. Non-goals (v0)

- No transform engine, no model calls, no code edits — ever (README: the net, never the refactorer).
- No probe types beyond shell commands (HTTP/DOM/browser probes are Parked — the shell probe can shell out to `curl`/Playwright today). Metric probes (§2.1) are in scope for v0 — they reuse the same run-and-record machinery.
- No `vise map` yet — a deterministic structural map (tree-sitter/SCIP-class symbol + import inventory) whose diff evidences *what the refactor did* and catches incomplete transformations. v1, after the vertical proves the loop.
- No parallel probe execution, no caching, no daemon. Boring and sequential until the vertical works.
