# Self-gating this repository

The root `vise.toml`, `vise.lock`, and `.vise/blobs/` freeze seven behavioral
observations of vise. `scripts/selfgate-probe` builds the current working tree
for each observation. A separately installed vise judges those observations;
do not rebuild that judge automatically during an agent's editing loop.

## Start a session

Use a trusted, clean build of vise that understands this baseline. The initial
judge was built from `70e04d63bbd7910b9708f5d1480412c40b909859` with
`modified: false`. Check `vise version --json` to identify the installed judge.
An operator can build and install a replacement from a reviewed clean checkout
with `GOTOOLCHAIN=go1.25.13 go install ./cmd/vise`; its revision should be recorded
in the handoff. The agent must not replace its judge to make a failed gate pass.

The initial baseline records macOS/arm64, Go 1.25.13, and the output of
`git --version`. Another platform or a changed fingerprint needs an operator's
baseline review. A fresh clone on the recorded environment needs no recording.
Install Go and Git, and run `GOTOOLCHAIN=go1.25.13 go version` once before handing
the checkout to an agent, so the named toolchain is available locally.

```sh
vise version --json
vise status --json
vise doctor --json
vise gate --json
```

The TOML dependency is vendored. Probes build with `-mod=vendor`, `GOPROXY=off`,
`CGO_ENABLED=0`, and an ignored `.gocache/` under the checkout. Binaries and nested
fixtures live in the per-probe `VISE_TMP` and are removed by vise after each run.
The network declaration is still `declared-off`, not OS-enforced isolation.

## What the baseline covers

| Probe | Frozen observation |
| --- | --- |
| `cli-help` | Complete global help output |
| `cli-version` | Human version output, including the release number |
| `cli-invalid-command` | Human and JSON unknown-command errors, including actual exit 2 |
| `cli-status` | Ready status, lock hash, recorded commits, and a record/gate journal tail |
| `cli-doctor` | Ready doctor's output and actual exit 0 on a complete fixture |
| `cli-review` | Preview containing a changed, removed, and added probe, plus actual exit 0 |
| `cli-pin` | Record unmet; gate exit 6; build and gate green before acceptance; preview and accept; gate green after acceptance; edited spec rejected with exit 2 |

These are preservation probes. The pin lifecycle runs inside a disposable
fixture; the root baseline does not claim a newly specified pin was built here.
The fixture's operator commands and intentional spec edit never touch the root
judge. Fixture commits use fixed identities, timestamps, branch, object format,
and empty Git templates. No output normalizers are applied. The subject binary
alone is built without VCS stamps, to keep the parent's changing commit and dirty
state out of observations; the external judge retains its real build identity.

`deps` includes the wrapper, module files, and all initially vendored files.
Changing those requires an operator to review and record the revised harness.
Production source files deliberately are not dependencies: editing them must
produce an observed behavior change, not an input-hash refusal. Newly added
vendor files must be included in `deps` by the operator when vendoring changes.

A green root gate does not cover every branch, Go tests, metric regressions,
performance, documentation, real agent permissions, or build identity. Keep
running `scripts/verify verify` and the additional checks in README for broad
validation; the root gate supplements that verifier. In particular, a release
number change deliberately fails `cli-version` until the operator accepts it.

## Baseline ownership

The operator authorized initial self-gating on 2026-09-12. Bootstrapping requires
committing the new harness before recording, since there is no old root baseline
and `record` needs a clean tree. Once recorded, commit the lock and blobs and
verify a fresh clone. Ordinary future agent sessions follow `AGENTS.md`: never
edit or re-record the judge, and never weaken an observation to pass it.

For an intentional behavior or harness change, the operator inspects
`vise record --preview`, reviews the actual diff, then runs
`vise record --accept <digest>`. The agent reports the proposed change and its
failed observations so the operator can make that decision.

To check whether an observation detects a defect, temporarily change a single
production constant or message while keeping the program compilable, confirm
the edit, and run the gate. Restore precisely that edit and gate again. A root
gate that remains green has not demonstrated coverage of the mutated behavior.
