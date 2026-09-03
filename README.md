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

### What it catches that a test suite does not

From this project's own dogfood: a coding agent was asked to shorten three of
vise's harness messages. It made the edits, updated the two test assertions that
pinned the old strings, and ran the suite — `go test ./...`, exit 0, both
packages green. Then it ran the gate, which said red: those strings are
observable output, frozen in the baseline. The agent reverted and reported that
only an operator can accept a new baseline.

The tests were not wrong. They were watching what someone had thought to assert;
the lockfile was watching what the program actually does. That gap is the whole
job.

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
git add vise.toml AGENTS.md .gitignore && git commit -m "Add vise probes"
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
- **Machine-decidable, not merely machine-readable.** `--json` on every command; every failing outcome carries a typed class, and every outcome carries one `next.action` from a closed vocabulary. A green outcome carries no classes and no failures, because there are none. The agent branches on the exit code and the class, never on prose.
- **The exit code is the branch; `next.action` is the instruction.** They are not one-to-one and the difference matters: exit 2 asks the agent to repair a probe it broke, or to stop because the repair is in a file it may not write, and only `next.action` separates those. Exits 1 and 5 both say revert, and what to revert differs. Branch on the code, then read the action.
- **Bounded output, always.** Green is one line. Red shows the first divergence and counts, never a dump. Output grows with divergence, never with repository size — and a long line is clipped around the differing column, so a probe that prints one 8,000-character line still renders a diff you can read.
- **Every failure names its remedy.** The error message is the documentation, delivered when it is needed.
- **Fail closed.** A flaky probe makes the verdict *indeterminate*, never green, and never leaves the denominator. An agent cannot eject a judge by making it flaky, and after two reruns at one commit the third is refused.

| exit | meaning | `next.action` | what the agent does |
|---|---|---|---|
| 0 | green | `proceed` | next step |
| 1 | behavior differs | `revert` | revert, or ask an operator to accept a new baseline |
| 2 | harness broken | `fix_probe` / `human` | `fix_probe`: repair the probe your change broke. `human`: stop — the repair is in a file you may not write |
| 3 | flaky, indeterminate | `quarantine_ack` | stop unless your policy tolerates indeterminate |
| 4 | no baseline | `record_first` | an operator records one |
| 5 | metric regressed | `revert` | behavior held, quality did not |

## Commands

| command | purpose |
|---|---|
| `vise init` | Write a starter manifest, ignore rules, and the agent contract (`AGENTS.md`). Never overwrites. |
| `vise record` | Two full suite passes, then atomically freeze the baseline. |
| `vise record --preview` | Show the candidate diff and its digest. Writes no baseline state. |
| `vise record --accept <digest>` | Freeze exactly the candidate that was previewed. |
| `vise verify [--probe ID]` | Replay and diagnose the suite, or one probe. |
| `vise gate [--quiet]` | The refactor-loop verdict, journaled. |
| `vise run <probe-id>` | Execute one probe raw. Exit mirrors the probe. |
| `vise status` | The whole situation in one bounded read. Always exit 0. |
| `vise doctor` | Check the repository is fit to hand to an agent. Read-only, always exit 0. |
| `vise version` | The version, and with `--json` the build revision. |

## Before you hand the repository to an agent

```sh
vise doctor
```

Eight checks, each one a setup failure that cost a session when vise was first
handed to real coding agents: a toolchain nobody fingerprinted, a probe naming
a path that exists only on your machine, a harness wrapper a probe runs
without declaring it as an input, a baseline that was never committed so a fresh clone
cannot gate, vise's own local state left unignored, a repository with no
written rules for the agent, a declared artifact somebody committed with , and an untracked, unignored file set large enough
to make every gate slow for no visible reason. Every finding names its remedy. It runs no probe,
writes nothing, and always exits 0.

The failures are invisible from where you sit and expensive from where the
agent sits: the operator's shell has the toolchain, the caches, and the home
directory that the sandbox does not.

## What it holds, and what it cannot

**A green gate covers the paths your probes walk, and nothing else.** Before
trusting one on a change, ask which probe would have gone red if the change were
wrong. If the answer is none, the gate is not the check you want there — and the
probe set has a gap worth filling. A behavioural gate and a test suite are not
substitutes for each other; defects live where they fail to overlap.

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
scripts/verify verify          # the committed harness: 9 features, from any directory
scripts/verify verify baseline # one feature
go test -race ./... && go vet ./... && shellcheck scripts/verify scripts/dogfood && govulncheck ./...
```

The feature inventory and the traps each one guards live in [`verify/features/`](verify/features/README.md).

```sh
scripts/dogfood /tmp/vise-dogfood     # a gated clone of this checkout, ready for an agent
```

`scripts/dogfood` builds the target vise is tested against: a clone of this
repository with vendored dependencies, agent-ready probes, a recorded baseline,
`vise doctor` clean, and a cold gate green. It exists because every round of
testing vise against real coding agents began by rebuilding that target by hand,
and every rebuild rediscovered the same half-dozen details. Two of the defects
fixed this week were found by the script itself on its first run.

## Documents

- [AGENTS.md](AGENTS.md) — the rules for a coding agent working under the gate. Copy it into any repository you gate.
- [examples/agent-ready/](examples/agent-ready/) — a manifest, probe wrapper, and ignore fragment that survive an agent sandbox, with the failure each guard prevents.
- [GUIDE.md](GUIDE.md) — one campaign end to end, with the real output of every command, and how to hand a repository to an agent (§12).
- [SPEC.md](SPEC.md) — the contract: manifest, lockfile, exit vocabulary, flake protocol, crash-safety ordering.
- [RUNTIMES.md](RUNTIMES.md) — per-runtime determinism traps and where the boundary sits.
- [RESEARCH.md](RESEARCH.md) — the evidence the design rests on.
