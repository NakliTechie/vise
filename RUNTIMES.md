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
- Nonces & digests: CSRF tokens, asset fingerprints, `SECRET_KEY_BASE`-derived output → probe-owned normalization (the SPEC §2 example).
- **Spring preloader is a false-green machine**: it serves stale code after edits — probes must run with `DISABLE_SPRING=1` (recommend it in the manifest for any Rails repo).
- Database state *is* behavior: a probe that reads the DB must own its setup (`db:reset` + deterministic seed inside `run`, or transactional fixtures). Auto-increment IDs and `created_at` in output → normalize or seed fixed.
- Parallel test runners randomize ordering — pin `--seed` where the framework supports it.
- Boot logs interleave on stdout → the `[[service]]` `ready` pattern exists so probes hit HTTP, not boot output.

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

## The boundary rule

If a behavior can't be made deterministic at reasonable cost (GPU output, TCC-gated paths, wall-clock-coupled flows), it is **not a vise probe** — it belongs to the live-check layer. vise guards what can be frozen; pretending to freeze the unfreezable produces quarantine noise that erodes trust in the gate.
