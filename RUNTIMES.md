# Runtime quirks — the determinism field guide

Guidance, not code: vise's contract (SPEC §2) is that a probe is deterministic under the stub env or it fails the freeze. This file is the accretive catalog of *why* a probe flakes per runtime, so the first `record` passes sooner. It grows with every dogfood — when a quarantine teaches us a new quirk, it lands here.

Three tiers of handling, in order of preference:
1. **vise's sanitized env** pins the universal ones (SPEC §2) — no probe author thinks about them.
2. **Probe-owned normalization** — the probe's own pipeline strips legitimate nondeterminism (`| ./strip-nonce`, `| sort`, `| jq -S`). vise never fuzzy-matches.
3. **The self-test + quarantine** catch everything else, loudly and named.

## Universal (pinned by vise's env, every probe)

| Pin | Kills |
|---|---|
| `TZ=UTC`, `LANG=C`, `LC_ALL=C` | timezone/locale formatting, collation order |
| `SOURCE_DATE_EPOCH=0` | embedded build timestamps (honored by most compilers/packagers) |
| `NO_COLOR=1`, `TERM=dumb`, `COLUMNS=80` | color codes, tty-detection branches, width-dependent wrapping |
| `PYTHONHASHSEED=0` | Python hash randomization (dict/set/str-hash order) — harmless elsewhere |
| `CI=1` | tools that switch to non-interactive, no-spinner output under CI |
| non-tty stdin/stdout | isatty() branches, progress bars, pagers |

## Per-runtime

**Ruby / Rails** (the dogfood class)
- Nonces & digests: CSRF tokens, asset fingerprints, `SECRET_KEY_BASE`-derived output → probe-owned normalization (the SPEC §2 example). Same tier handles **`$VISE_PORT` leaking into recorded bytes** (absolute URLs in a served page): strip or fix the port in the probe pipeline.
- **Spring preloader is a false-green machine**: it serves stale code after edits — probes must run with `DISABLE_SPRING=1` (recommend it in the manifest for any Rails repo).
- Database state *is* behavior: a probe that reads the DB must own its setup (`db:reset` + deterministic seed inside `run`, or transactional fixtures). Auto-increment IDs and `created_at` in output → normalize or seed fixed.
- Parallel test runners randomize ordering — pin `--seed` where the framework supports it.
- Boot logs interleave on stdout → the `[[service]]` `ready` pattern is designed for this (parked for v1, SPEC §2); until it lands, a shell probe must start the server, wait for readiness, and hit HTTP itself, never boot output.

**JavaScript / Node**
- `Math.random`/`Date.now` → the app wires `VISE_SEED`/`SOURCE_DATE_EPOCH` at its stub seam (`VISE=1` gates it).
- Bundler output: content hashes are deterministic *given identical inputs and tool versions* — tool-version drift is the real enemy (see SPEC §2.1 version pinning).
- Test-runner parallelism and scheduler timing → run serial in probes (`--runInBand`-class flags).
- Source-map/absolute paths leaking into output → build with relative paths or normalize.

**Python**
- `PYTHONHASHSEED=0` already pinned. Add `PYTHONDONTWRITEBYTECODE=1` per-probe if `.pyc` churn pollutes `files` hashes.
- `pip`/venv variance is an environment problem, not a probe problem — probes assume the env is built; building it is not a probe.

**Go**
- **Map iteration order is randomized by language design** — any output ranging over a map must sort before printing. This is the #1 Go probe flake and it is always an app-side fix.
- Test caching (`go test` memoizes) → `GOFLAGS=-count=1` in probe env when the run must be fresh.

**Swift / macOS** (summon)
- Dictionary ordering unguaranteed → sort before output, same as Go.
- **TCC prompts hang probes**: anything touching accessibility, mic, screen recording blocks on a system dialog vise cannot dismiss. Such paths are live-check territory (`/live-check-nt`), not vise probes — probe the logic below the permission boundary.
- Codesigning/notarization stamps and DerivedData caches → keep signing out of probes; probe the binary's behavior, not its packaging.

**Browser / DOM** (LocalMind class)
- Font metrics, GPU rasterization, animation timing — why DOM probes are Parked. Today's honest form: probe the app's *logic* via a headless JS entry point; leave pixel/GPU truth to `/live-check-nt`.

## Warnings the host injects into your observation

A probe freezes bytes, and the host writes bytes into them that have nothing to
do with your program. Inside an agent sandbox macOS could not resolve its temp
directory, so every `git` call printed
`git: warning: confstr() failed ... DARWIN_USER_TEMP_DIR` onto the probe's
stderr — invisible in a terminal, eight extra lines in the sandbox, and a red
gate that named a divergence the author could not reproduce.

- vise now pins `TMPDIR` to the per-probe scratch, which removes this particular
  one at the source. It is the shape of the problem that matters: anything the
  host decides to say lands in the observation.
- Normalize or drop host chatter in the probe, and remember that a filter only
  covers the commands you actually pipe through it — the setup commands beside
  it are where the noise gets in.
- If a probe merges stderr into stdout, it has doubled the surface the host can
  write to. Merge deliberately, not by habit.

## A build failure is deterministic; the toolchain's account of it is not

A probe that builds before it runs usually wraps the build so its output is
discarded on success and printed on failure. The second half is the trap. The
compiler's own diagnostics are deterministic — the code compiles or it does
not — but everything else on that stream is not: sandbox permission warnings,
cache notices, download lines, all of which vary by caller and sometimes
between two runs by the same caller.

vise runs a diverging probe twice to tell a consistent difference from an
unstable one. If the two failed builds print different chatter, the observation
differs between the passes and the verdict is **flake**, not red. That is the
worst possible answer: the agent contract tells an agent to stop and report on
a flake, so a plain compile error ends the session instead of telling the agent
to fix the code it just wrote.

Print the compiler's lines and nothing else:

```sh
if ! go build -o "$BIN" ./cmd/app 2>"$VISE_TMP/build.err"; then
  grep -E '(^#|\.go:)' "$VISE_TMP/build.err" >&2 || cat "$VISE_TMP/build.err" >&2
  exit 1
fi
```

The fallback matters: if nothing matches, show everything rather than failing
silently with an empty explanation.

## The normalizer is a probe too

A probe that normalizes its own output is only as deterministic as the
normalizer, and a normalizer that silently stops matching is worse than none:
the probe keeps passing until the day the unmatched value changes.

- **BSD vs GNU `sed`.** macOS `sed` has no `\b`, no `\d`, no `\+`, and treats
  `-i` differently. A pattern like `s/\b[0-9a-f]{40}\b/COMMIT/g` matches
  nothing on macOS and everything you wanted on Linux — a probe that is
  deterministic on CI and flaky on a laptop, or the reverse. Write POSIX ERE
  (`sed -E 's/[0-9a-f]{40}/COMMIT/g'`), or use `awk`, or normalize inside the
  program you are probing.
- **Order your substitutions.** Replace the longest, most specific pattern
  first: a rule for 64-hex digests must run before a rule for 40-hex ones, or
  the second clips the first.
- **Prove the normalizer.** `record` runs two full passes and fails the freeze
  when they differ, which is exactly how a broken normalizer surfaces — the
  divergence names the value that got through. Read it as "my normalizer
  missed this", not "vise is being difficult".
- **`LC_ALL=C` is already pinned** by vise's stub env, so collation in `sort` is
  stable; do not re-set it to something else inside a probe.

## Processes vise cannot reach

Every guard vise has over a running probe is a process-group guard: the timeout kill, the post-exit sweep, the one-second pipe close. An ordinary background child stays in the group and the sweep kills it — `sleep 30 &`, a plain double fork, and `nohup` alike (`nohup` only ignores SIGHUP; it does not change session). What escapes is a child that starts a **new session**: `setsid`, or a daemonize idiom that calls it — the shape a Spring preloader or a detaching `rails server` uses. vise cannot kill that child; it still refuses the run if the run changed evaluator state, and still catches a tracked-file write that lands before the tracked-tree check, but a write that lands after it is invisible. Rules for probe authors: start nothing you do not `wait` for, redirect anything you background (`>/dev/null 2>&1 &`) so it cannot hold the probe's pipes, and never let a probe leave a detached process behind. SPEC §2.2 states the boundary.

## The boundary rule

If a behavior can't be made deterministic at reasonable cost (GPU output, TCC-gated paths, wall-clock-coupled flows), it is **not a vise probe** — it belongs to the live-check layer. vise guards what can be frozen; pretending to freeze the unfreezable produces quarantine noise that erodes trust in the gate.
