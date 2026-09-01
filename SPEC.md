# vise — v0 specification

Status: DRAFT (2026-09-01). The contract below is the product; the implementation serves it.

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

Rules:
- `run` must be self-contained from the repo root; no probe depends on another probe's side effects (each runs in a scratch copy or after `git stash`-clean state — implementation detail, but isolation is contractual).
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

- Full outputs are stored as blobs (gitignored by default, re-derivable); the lockfile carries hashes so the committed artifact stays small and diff-noise-free.
- `run_hash` mismatch (manifest edited since record) = red at `verify`, with a distinct message: "probe changed, not behavior — re-record required."

## 4. CLI contract

### `vise record`
Freeze current behavior. Runs every probe twice (the determinism self-test), writes `vise.lock` + blobs. Fails without writing if any probe is flaky, errors, or times out — a lockfile is never partially written.
- `--probe <id>` — re-record one probe (still self-tested).
- Exit: 0 recorded · 2 determinism self-test failed · 3 probe error.

### `vise verify`
Replay all probes against the working tree; diff against the lockfile. Output one block per failing probe, **agent-legible**:

```
FAIL cli-help
  exit: expected 0, got 1
  stdout: differs (line 3)
    - Usage: mytool [convert|check] <file>
    + Usage: mytool [convert] <file>
  hint: behavior changed. If unintended, revert the last change. If intended, a human must run `vise record`.
```

Minimal unified diff, first divergence first, truncated with a count when large. No color, no spinner, stable ordering — written for a model mid-loop, parseable by a human.
- Exit: 0 all green · 1 behavior diff · 2 probe/manifest error (distinct so a loop can tell "I broke behavior" from "I broke the harness").

### `vise gate`
`verify` with a one-line verdict (`GATE GREEN — 7/7 probes` / `GATE RED — 2/7 probes differ: cli-help, convert-fixture`) and the same exit codes. The command a refactor loop calls between commits and CI calls before merge. `--quiet` prints the verdict only.

## 5. The re-record gesture (evaluator outside the loop)

An *intended* behavior change mid-refactor must not open the gate to the agent:
- `record` refuses to overwrite an existing lockfile unless (a) the working tree is clean of unstaged changes to `vise.toml`, and (b) the flag `--i-reviewed-the-diff` is present, which first prints the full behavior diff (old lockfile vs new run) and the probe list it will rewrite.
- The flag name is deliberately human-shaped; harness policy (hooks / permission rules / CLAUDE.md) instructs agents that `vise record` is operator-only. vise itself cannot verify who typed it — the enforcement seam is the caller's harness, and SPEC says so honestly rather than pretending a CLI can authenticate intent.
- Every overwrite appends one line to `.vise/journal` (`date · probes rewritten · diff summary`) — the audit trail a morning report can read.

## 6. Non-goals (v0)

- No transform engine, no model calls, no code edits — ever (README: the net, never the refactorer).
- No probe types beyond shell commands (HTTP/DOM/browser probes are Parked — the shell probe can shell out to `curl`/Playwright today).
- No parallel probe execution, no caching, no daemon. Boring and sequential until the vertical works.
