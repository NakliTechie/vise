# vise

> Holds the workpiece steady while the agent cuts.

vise freezes what your code **does** into a lockfile, then judges every later change against it. A coding agent refactors; `vise gate` says green or red in one line and one exit code. It never edits code, never calls a model, and never guesses — it replays commands you declared and compares bytes.

**Tier: Tool.** A local CLI. No server, no account, no telemetry.

```
vise record          # freeze — before anything moves
<agent refactors, one transform per commit>
vise gate            # 0 → next step · 1 → revert, the diff says what changed
```

## Why

Raw AI refactoring fails roughly 60% of the time; paired with a verification layer, correctness climbs to about 98% ([sources](RESEARCH.md)). The value is in the verification layer, and nothing ships it on its own: what exists nearby is a transform *engine* (OpenRewrite, codemods), a per-language golden-master *library* written for humans (ApprovalTests), or a harness that baselines *the agent* rather than the app. vise is the net itself — language-agnostic, CLI-first, built to sit inside an agent's loop and be read by a machine.

## Status

v0.3, developed in the open, not yet packaged. Build from this checkout. The command surface is implemented and exercised by a committed harness; Windows, service probes, network enforcement, partial recording, and `vise map` are non-goals for v0 ([SPEC §8](SPEC.md#8-non-goals-v0)).

```sh
go install ./cmd/vise     # Go 1.25.8+, git, a POSIX /bin/sh
vise version              # vise 0.3.0-dev
```

## Five minutes

Inside the repository whose behavior you want to hold still:

```sh
vise status               # always exit 0; tells you what to do next
vise init                 # writes a commented vise.toml and local-state ignores
```

Declare at least one probe — a command whose output is identical every time it runs:

```toml
[vise]
version = 1

[stubs]                   # applied to every probe
tz = "UTC"
lang = "C"
seed = "1729"             # exported as VISE_SEED; your app wires it to its RNG
network = "declared-off"  # a promise probes make; vise does not enforce it

[[probe]]
id = "cli-help"
run = "./bin/mytool --help"
timeout = 30
deps  = ["fixtures/in.csv"]     # inputs this probe consumes, hashed with it
files = ["out/result.json"]     # artifacts to hash; must be untracked
```

Commit the harness, freeze the baseline, commit that too:

```sh
git add vise.toml .gitignore && git commit -m "Add vise probes"
vise record
git add vise.lock .vise/blobs && git commit -m "Record behavior baseline"
```

Now run the gate after every focused change:

```sh
vise gate --quiet
```

```text
GATE GREEN — 7/7
GATE RED [behavior] — 6/7: cli-help
GATE INDETERMINATE [flake] — 6/7: convert-fixture
```

`vise verify` explains a red gate with the first divergence. `vise run <probe>` executes one probe raw. [GUIDE.md](GUIDE.md) walks a whole campaign with the real output of every command, including the recovery paths.

## Built for the agent in the driver's seat

vise's primary user is a coding agent mid-loop: context-poor, liable to be killed mid-turn, prone to rationalizing its own failures. Every interface decision follows from that.

- **One perception act.** `vise status` renders the entire situation — manifest, lockfile, environment drift, baseline drift, rerun state, journal tail — in one bounded read. It always exits 0.
- **Machine-decidable, not merely machine-readable.** `--json` on every command; every outcome carries a typed class and one `next.action` from a closed vocabulary. The agent branches on the exit code and the class, never on prose.
- **One exit code per distinct next action.** Conflating two would force the agent to investigate what the tool already knows.
- **Bounded output, always.** Green is one line. Red shows the first divergence and counts, never a dump. Output grows with divergence, never with repository size.
- **Every failure names its remedy.** The error message is the documentation, delivered when it is needed.
- **Fail closed.** A flaky probe makes the verdict *indeterminate*, never green, and never leaves the denominator. An agent cannot eject a judge by making it flaky, and after two reruns at one commit the third is refused.

| exit | meaning | `next.action` | what the agent does |
|---|---|---|---|
| 0 | green | `proceed` | next step |
| 1 | behavior differs | `revert` | revert, or ask an operator to accept a new baseline |
| 2 | harness broken | `fix_probe` / `human` | repair the harness — never the code under test |
| 3 | flaky, indeterminate | `quarantine_ack` | stop unless your policy tolerates indeterminate |
| 4 | no baseline | `record_first` | an operator records one |
| 5 | metric regressed | `revert` | behavior held, quality did not |

## Commands

| command | purpose |
|---|---|
| `vise init` | Write a starter manifest and ignore rules. Never overwrites. |
| `vise record` | Two full suite passes, then atomically freeze the baseline. |
| `vise record --preview` | Show the candidate diff and its digest. Writes no baseline state. |
| `vise record --accept <digest>` | Freeze exactly the candidate that was previewed. |
| `vise verify [--probe ID]` | Replay and diagnose the suite, or one probe. |
| `vise gate [--quiet]` | The refactor-loop verdict, journaled. |
| `vise run <probe-id>` | Execute one probe raw. Exit mirrors the probe. |
| `vise status` | The whole situation in one bounded read. Always exit 0. |

## What it holds, and what it cannot

**Behavioral equivalence, not semantic equivalence.** Full equivalence is undecidable; same-outputs-on-a-defined-input-set is the production standard. The lockfile *is* that input set — it holds what you declared, and nothing else.

**Determinism is the price of entry.** A probe that cannot be made deterministic gets normalized or it is not a probe. [RUNTIMES.md](RUNTIMES.md) catalogues the traps per language and runtime.

**The judge lives outside the loop.** `vise.toml`, `vise.lock`, `.vise/blobs/`, and the local `.vise/journal.jsonl` are operator territory: the gated agent reads them and never writes them. vise cannot authenticate its caller and says so — your harness policy must deny the agent writes to those paths and deny `vise record` mid-campaign.

**Containment ends at the session boundary.** Probes run in their own process group; vise kills that group on timeout and again when the shell exits, bounds the wait on output pipes, refuses any run that touched evaluator state, and compares the tracked tree around every judged run. A child that starts a *new session* escapes all of it. That limit is stated in [SPEC §2.2](SPEC.md#22-probe-execution-contract) rather than papered over.

**Observations are bounded.** vise hashes and counts every byte a probe produces but holds only the first 256 KiB, so a probe that prints a gigabyte cannot exhaust its own judge. Judgment is the hash; the retained prefix is for rendering.

## Beyond behavior: metric probes

Refactoring is two-sided — behavior must not change, quality should improve. A metric probe prints one number, tracked as a delta rather than diffed for equality:

```toml
[[metric]]
id = "complexity"
run = "oxlint --format=json . | jq '[.diagnostics[] | select(.code==\"complexity\")] | length'"
direction = "down"
enforce = "no-regress"       # worse → exit 5, after behavior held
version_cmd = "oxlint --version"
```

vise ships no analyzer and provisions nothing. A missing tool is exit 2 with the remedy in the message. The metric's definition is frozen with its baseline, so swapping the analyzer is harness drift, never a free improvement.

## Not a refactorer

vise carries no model and edits no code. Pull the AI out and it is a plain golden-master regression tool. The agent is whatever you already use; vise is the stop it cannot argue with.

## Development

```sh
scripts/verify verify          # the committed harness: 8 features, from any directory
scripts/verify verify baseline # one feature
go test -race ./... && go vet ./... && shellcheck scripts/verify && govulncheck ./...
```

The feature inventory and the traps each one guards live in [`verify/features/`](verify/features/README.md).

## Documents

- [GUIDE.md](GUIDE.md) — one campaign end to end, with the real output of every command.
- [SPEC.md](SPEC.md) — the contract: manifest, lockfile, exit vocabulary, flake protocol, crash-safety ordering.
- [RUNTIMES.md](RUNTIMES.md) — per-runtime determinism traps and where the boundary sits.
- [RESEARCH.md](RESEARCH.md) — the evidence the design rests on.
