# vise

> Holds the workpiece steady while the agent cuts. A language-agnostic CLI safety net for agent-led refactoring: freeze a codebase's observable behavior into a deterministic **behavior lockfile**, then gate any coding agent's refactor loop against it.

**Tier: Tool.**

## Current state

The v0.3 development CLI is implemented for Git repositories on POSIX systems. It supports shell-command behavior probes, metric probes, deterministic recording, typed verification, fail-closed gating, raw probe runs, bounded status, proposal discovery, and initialization. Windows, service probes, network enforcement, partial recording, ratchets, and `vise map` remain parked by [SPEC.md §8](SPEC.md#8-non-goals-v0).

vise is not publicly packaged yet. Build it from this checkout while the command contract is still marked draft.

## Requirements and build

- Git
- A POSIX `/bin/sh`
- Go 1.25.8 or newer; an older Go command with automatic toolchain switching can fetch the module’s declared 1.25.8 toolchain

From this repository:

```sh
go install ./cmd/vise
vise version
```

If `vise` is not on `PATH`, add `$(go env GOPATH)/bin` to `PATH` or invoke that binary by its full path.

## First campaign

Enter the Git repository whose behavior you want to freeze:

```sh
cd /path/to/your/repository
vise status
vise init
```

[GUIDE.md](GUIDE.md) walks one full campaign with the real output of every command, including the recovery paths (flakes, drift, accepting a new baseline).

`vise init` writes a commented `vise.toml`. Replace the example with at least one deterministic probe. A minimal CLI probe looks like this:

```toml
[vise]
version = 1

[stubs]
tz = "UTC"
lang = "C"
seed = "1729"
network = "declared-off"

[[probe]]
id = "cli-help"
run = "./bin/mytool --help"
timeout = 30
```

The probe command must already exist and must produce identical bytes across runs. Normalize legitimate timestamps, random IDs, absolute paths, and unstable ordering in the probe command; [RUNTIMES.md](RUNTIMES.md) catalogs common runtime traps.

Commit the evaluator inputs before recording. `record` refuses a dirty tree unless an operator explicitly passes `--allow-dirty`.

```sh
git add vise.toml .gitignore
git commit -m "Add vise behavior probes"
vise record
git add vise.lock .vise/blobs
git commit -m "Record behavior baseline"
vise status
```

Now run the gate after each focused refactor commit:

```sh
vise gate --quiet
```

The compact outcomes drive the loop:

```text
GATE GREEN — 1/1
GATE RED [behavior] — 0/1: cli-help
GATE INDETERMINATE [flake] — 0/1: cli-help
```

Use `vise verify` for the first bounded divergence, `vise verify --probe cli-help` to judge one probe, and `vise run cli-help` for raw debugging output. Every command accepts `--json`; gate consumers branch on `exit`, `classes`, and the closed `next.action` value.

`vise.toml`, `vise.lock`, `.vise/blobs/`, and the local `.vise/journal.jsonl` are operator territory. vise cannot authenticate its caller. The agent harness must deny the gated agent writes to those paths and deny `vise record` during a refactor campaign; the journal is on the list because the rerun limit (§4.1 of the spec) is derived from it.

## The problem

Unverified LLM refactoring fails roughly 60% of the time; the same refactoring gated by a verification layer approaches 98% correctness. The verification layer is where the value lives — yet no shipping tool packages it. What exists nearby is either a transform *engine* (OpenRewrite, codemods), a per-language golden-master *library* for humans (ApprovalTests), or a harness that baselines *the agent's* behavior rather than the app's. Nobody ships the net itself: language-agnostic, CLI-first, built to sit inside an agent's loop.

## What vise is

A tower of five abstractions, each legible to an agent on its own: **probe** (one deterministic observation) → **lockfile** (frozen truth) → **gate** (one bit, typed) → **journal** (the campaign's trajectory) → **status** (the whole situation in one bounded read). The driver's loop: perceive with `status`, cut with your own tools, be judged by `gate`, accrete through the journal and the metric deltas. Built agent-first: typed exit codes (one per distinct next action), `--json` everywhere, every failure naming its remedy, output bounded by divergence rather than repo size, every write crash-safe. See SPEC.md §0 for the doctrine.

The core commands:

- **`vise record`** — freeze the current behavior: run the project's declared probes (commands, entry points, HTTP calls, rendered output) under a pinned deterministic environment (fixed epoch, locale/tty pins, an environment fingerprint, plus seeded-RNG and no-network *conventions the app honors*) and write the results to a **behavior lockfile** (`vise.lock` — golden outputs, hashed and diffable). Recording runs everything twice — a flaky probe fails the freeze loudly instead of producing a lockfile that lies.
- **`vise verify`** — replay every probe against the current working tree and diff against the lockfile. Output is **agent-legible**: exact probe, expected vs got, minimal diff — written for a model to act on, not a human to squint at.
- **`vise gate`** — `verify` with a hard exit code and a one-line verdict. The thing a refactor loop calls between every micro-step; the thing a CI job calls before merge.

Around them: **`vise status`** (the session-opening perception act), **`vise run <probe>`** (debug one probe raw), **`vise init`** (write a stub manifest + gitignore wiring), and **metric probes** (cyclomatic complexity et al. as tracked deltas — the gate holds behavior constant while the metrics prove the refactor actually improved something). The gate is **fail-closed**: a flaky probe makes the verdict *indeterminate*, never green — an agent cannot eject a judge by making it flaky.

The refactor loop it enables, for any agent:

```
vise record          # freeze — before touching anything
<agent refactors, one transform type per commit>
vise gate            # green → next step; red → revert, the diff says exactly what changed
```

## What vise is not

**Not a refactorer.** vise never edits code, never calls a model, and works as a plain golden-master regression tool with no AI anywhere near it (removable-AI by design). The agent is whatever you already use; vise is the deterministic stop it cannot argue with.

## Design anchors (from the research — see RESEARCH.md)

- **Behavioral equivalence over semantic equivalence.** Full equivalence is undecidable; same-outputs-on-a-defined-input-set is the production standard. The lockfile *is* the defined input set.
- **Determinism first.** A probe that can't be made deterministic (time, randomness, network) gets stubbed or it doesn't qualify as a probe.
- **Closed-loop micro-iterations.** The gate is cheap enough to run between every commit, because that cadence is what moves success from ~60% to ~98%.
- **The verifier lives outside the loop.** The agent being gated can run `vise gate` but must not be able to edit `vise.lock` or the probe definitions mid-run — re-recording is a human act.

## Command surface

| Command | Purpose |
|---|---|
| `vise init` | Write a starter manifest and local-state ignore rules; never overwrite. |
| `vise record [--preview | --accept DIGEST]` | Run two full suite passes and atomically freeze deterministic behavior; preview the candidate and accept its digest to overwrite safely. |
| `vise verify [--probe ID]` | Replay and diagnose the full suite or one judged probe. |
| `vise gate [--quiet]` | Emit the typed refactor-loop verdict and journal the event. |
| `vise run <probe-id>` | Execute one probe raw without judgment or lockfile access. |
| `vise status` | Render manifest, lock, fingerprint, proposals, metrics, flakes, and journal tail in one bounded read. |
| `vise version` | Print the development version. |

Exit codes: `0` green/ok · `1` behavior difference · `2` harness failure · `3` indeterminate flake · `4` baseline missing · `5` enforced metric regression. See [SPEC.md §4](SPEC.md#4-cli-contract) for precedence and JSON fields.

## Development verification

The committed verifier uses checkout-derived state, so primary and linked worktrees do not share binaries, caches, or fixture repositories.

```sh
scripts/verify doctor
scripts/verify verify baseline
scripts/verify verify
go test -race ./...
go vet ./...
shellcheck scripts/verify
govulncheck ./...
```

The full feature inventory and known traps live in [`verify/features/`](verify/features/README.md); the command-by-command walkthrough is [GUIDE.md](GUIDE.md).
