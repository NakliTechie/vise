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

Raw GPT-3.5 produces correct refactorings 26–33% of the time, GPT-4 only slightly better; with a verification layer in front of it, the shipped-to-production rate is 98% ([sources](RESEARCH.md)). The value is in the verification layer, and nothing ships it on its own: what exists nearby is a transform *engine* (OpenRewrite, codemods), a per-language golden-master *library* written for humans (ApprovalTests), a harness that baselines *the agent* rather than the app, or — closest of all, and the reason to be careful — an agent pipeline whose equivalence check is another model judging whether behavior was preserved. vise is the net itself — language-agnostic, CLI-first, built to sit inside an agent's loop and be read by a machine.

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

v0.3, developed in the open, not yet packaged. Build from this checkout. The command surface is implemented and exercised by a committed harness; Service probes, network enforcement, partial recording, and `vise map` are non-goals for v0 ([SPEC §8](SPEC.md#8-non-goals-v0)); Windows is out of scope for a different reason, the POSIX process-group kill the probe lifecycle depends on ([SPEC §2.2](SPEC.md#22-probe-execution-contract)).

```sh
go install ./cmd/vise     # Go 1.25.13+, git, a POSIX /bin/sh
vise version              # vise 0.3.0-dev
```

## Five minutes

This runs. Every line is meant to be typed, in order, inside the repository
whose behavior you want to hold still.

```sh
vise status               # tells you what to do next; exit 0 for any call it understands
vise init                 # writes vise.toml, AGENTS.md, and the local-state ignores
```

`init` writes a commented manifest. Replace it with a probe of your own — a
command whose output is identical every time it runs. Here the program under
test is a one-line script, so the walkthrough is self-contained; yours is
whatever you already build.

```sh
mkdir -p bin fixtures
printf 'col\n1\n2\n' > fixtures/in.csv
printf '#!/bin/sh\nprintf "rows: %%s\\n" "$(tail -n +2 "$1" | wc -l | tr -d " ")"\n' > bin/mytool
chmod +x bin/mytool
```

Now **replace** `vise.toml` — do not append to it, or `[vise]` is defined twice
and the manifest will not parse:

```toml
[vise]
version = 1

[stubs]                   # applied to every probe
tz = "UTC"
lang = "C"
seed = "1729"             # exported as VISE_SEED; your app wires it to its RNG
network = "declared-off"  # a promise probes make; vise does not enforce it

[[probe]]
id = "count-rows"
run = "./bin/mytool fixtures/in.csv"
timeout = 30
deps = ["fixtures/in.csv"]  # files this probe reads: fixtures and wrappers,
                            # never the code under test
```

`deps` is what the probe *consumes*. vise freezes their hashes, so editing one
after recording is reported as harness drift rather than as a behavior change —
which is right, because the probe's inputs moved and not the program. Declare
only what the probe actually reads; a `deps` entry a probe never opens turns an
unrelated edit into a failure on a probe it cannot affect.

Commit everything the probe touches, freeze the baseline, commit that too.
`record` needs a clean tree, so anything the probe reads has to be committed
first — its `deps`, and the program itself:

```sh
git add vise.toml AGENTS.md .gitignore bin fixtures
git commit -m "Add vise probes"
vise record
git add vise.lock .vise/blobs && git commit -m "Record behavior baseline"
```

If a probe writes an artifact you want hashed, declare it with `files` and
ignore it — a declared artifact must be untracked, and an untracked file that
git does not ignore makes the next `record` refuse a dirty tree:

```sh
echo 'out/' >> .gitignore     # before adding files = ["out/result.json"]
```

Now run the gate after every focused change:

```sh
vise gate --quiet
```

```text
GATE GREEN — 7/7
GATE RED [behavior] — 6/7: count-rows
GATE INDETERMINATE [flake] — 6/7: convert-fixture
```

`vise verify` explains a red gate, printing a diff for every probe that
diverged. `vise run <probe>` executes one probe and streams its output.
[GUIDE.md](GUIDE.md) walks a whole campaign with the real output of every
command, including the recovery paths.

## Built for the agent in the driver's seat

vise's primary user is a coding agent mid-loop: context-poor, liable to be killed mid-turn, prone to rationalizing its own failures. Every interface decision follows from that.

- **One perception act.** `vise status` renders the entire situation — manifest, lockfile, environment drift, baseline drift, rerun state, journal tail — in one bounded read. It exits 0 whatever it finds, because it reports a red repository rather than failing red. A usage error is exit 2 like anywhere else: that is a complaint about the command line, not a report about the repository.
- **Machine-decidable, not merely machine-readable.** `--json` on every command; every failing outcome carries a typed class, and every outcome carries one `next.action` from a closed vocabulary. A green outcome carries no classes and no failures, because there are none. The agent branches on the exit code and the class, never on prose.
- **The exit code is the branch; `next.action` is the instruction.** They are not one-to-one and the difference matters: exit 2 asks the agent to repair a probe it broke, or to stop because the repair is in a file it may not write, and only `next.action` separates those. Exits 1 and 5 both say revert, and what to revert differs. Branch on the code, then read the action.
- **Bounded output, always.** A green gate is the verdict line plus the lockfile hash, and `--quiet` drops the hash to leave one line. Red names the probes that failed and the counts, never a dump; `vise verify` is where the diff lives, one per diverging probe, each showing the first line that stopped matching. Output grows with divergence, never with repository size — and a long line is clipped around the differing column, so a probe that prints one 8,000-character line still renders a diff you can read.
- **Every failure names its remedy.** The error message is the documentation, delivered when it is needed.
- **Fail closed.** A flaky probe makes the verdict *indeterminate*, never green, and never leaves the denominator. An agent cannot eject a judge by making it flaky, and after a probe has flaked twice at one commit the third run is refused.

| exit | meaning | `next.action` | what the agent does |
|---|---|---|---|
| 0 | green | `proceed` | next step |
| 1 | behavior differs | `revert` | revert, or ask an operator to accept a new baseline |
| 2 | harness broken, or a usage error | `fix_probe` / `human` / `fix_invocation` | `fix_probe`: repair the probe your change broke. `human`: stop — the repair is in a file you may not write. `fix_invocation`: your command line was wrong — correct it and rerun, the repository is untouched |
| 3 | flaky, indeterminate | `quarantine_ack` | stop unless your policy tolerates indeterminate |
| 4 | no baseline | `record_first` | an operator records one |
| 5 | metric regressed | `revert` | behavior held, quality did not |

## Commands

| command | purpose |
|---|---|
| `vise init` | Write whatever of the starter manifest, ignore rules, and agent contract (`AGENTS.md`) is missing. Never overwrites what is there, and safe to rerun. |
| `vise record` | Two full suite passes, then atomically freeze the baseline. |
| `vise record --preview` | Show the candidate diff and its digest. Writes no baseline state. |
| `vise record --accept <digest>` | Freeze exactly the candidate that was previewed. |
| `vise verify [--probe ID]` | Replay and diagnose the suite, or one probe. |
| `vise gate [--probe ID] [--quiet]` | The refactor-loop verdict, journaled. |
| `vise run <probe-id>` | Run one probe without comparing it to the baseline, streaming its output to your terminal. `--json` reports what it observed: hashes, sizes, artifacts. The lifecycle still applies — artifacts are deleted first, the work-tree and evaluator-state checks still run. Exit mirrors the probe, except a timeout, a refused artifact or a lingering pipe holder, which have no probe exit to mirror and are exit 2. |
| `vise status` | The whole situation in one bounded read. Exit 0 for any call it understands. |
| `vise doctor` | Check the repository is fit to hand to an agent. Read-only; exit 0 whatever it finds. |
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
written rules for the agent, a declared artifact somebody committed with `git add -A`, and an untracked, unignored file set large enough
to make every gate slow for no visible reason. Every finding names its remedy. It runs no probe,
writes nothing, and exits 0 whatever it finds — a usage error is exit 2 like anywhere else, because that is a complaint about the command line rather than a report about the repository.

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

**vise is not an adversary.** A probe runs with `VISE=1`, inside the repository, where the baseline is a committed file it can read. A program written to return its recorded output when `VISE=1` and its new behavior otherwise gates green forever, and no probe can catch that, because the probe is what it is lying to. vise defends a cooperative agent against its own mistakes — that is the whole claim. What catches deliberate deception is the same thing that catches an edited lockfile: a human reading the diff, and CI recomputing the hash from a trusted branch.

**The judge lives outside the loop.** `vise.toml`, `vise.lock`, `.vise/blobs/`, and the local `.vise/journal.jsonl` are operator territory: the gated agent reads them and never writes them. vise cannot authenticate its caller and says so — your harness policy must deny the agent writes to those paths and deny `vise record` mid-campaign.

**Containment ends at the session boundary.** Probes run in their own process group; vise kills that group on timeout and again when the shell exits, bounds the wait on output pipes, refuses any run that touched evaluator state, and compares the work tree, tracked and untracked alike, around every judged run. A child that starts a *new session* escapes all of it. That limit is stated in [SPEC §2.2](SPEC.md#22-probe-execution-contract) rather than papered over.

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

vise ships no analyzer and provisions nothing. A missing tool is exit 2, naming the tool and what to do about it. The metric's definition is frozen with its baseline, and an enforcing metric must declare a `version_cmd` — so swapping the analyzer is harness drift, never a free improvement. Without that declaration the swap is invisible and the guarantee does not hold, which is why vise refuses to enforce a metric that does not name its analyzer.

## Not a refactorer

vise carries no model and edits no code. Pull the AI out and it is a plain golden-master regression tool. The agent is whatever you already use; vise is the stop it cannot argue with.

## Development

```sh
scripts/verify verify          # the committed harness: 10 features, from any directory
scripts/verify verify baseline # one feature
go test -race ./... && go vet ./... && shellcheck scripts/verify scripts/dogfood && govulncheck ./...
```

The feature inventory and the traps each one guards live in [`verify/features/`](verify/features/README.md).

```sh
scripts/dogfood /tmp/vise-dogfood     # a gated clone of this checkout, ready for an agent
```

A gated clone plus a coding agent is also the cheapest audit available. Give
the agent one file and a narrow extraction task, point it at `AGENTS.md`, and
read the two sections of its report that matter: what the gate did not check,
and what looked wrong. Six runs against six files here produced seventeen
defects that had survived every direct audit ([GUIDE §11.6](GUIDE.md)).

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
