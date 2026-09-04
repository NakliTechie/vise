# Changelog

All notable changes to vise are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and vise aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-09-04

First tagged release. vise freezes a project's observable behaviour into a
committed `vise.lock` and gates an agent's refactor against it: same behaviour,
verdict green; changed behaviour, a typed non-zero exit and a closed-vocabulary
`next.action` telling the agent exactly what to do. The judge decides; the agent
obeys.

### Added

- **The gate.** `vise record` freezes probe outputs and metric values into
  `vise.lock`; `vise gate` re-runs them against a candidate change and returns a
  fail-closed verdict. `vise verify` reports without judging; `vise status` and
  `vise doctor` read the repository without taking the state lock.
- **Typed exit codes** (0 OK, 1 behaviour, 2 harness, 3 indeterminate, 4
  not-initialised, 5 metric) paired with a **closed `next.action` vocabulary** of
  seven emitted values — `proceed`, `revert`, `fix_probe`, `human`,
  `record_first`, `quarantine_ack`, `fix_invocation` — that an agent can branch on
  without parsing prose. An unrecognised value is a defect in vise, not a case to
  guess at.
- **Whole-work-tree snapshot.** The judged state covers the tracked diff, every
  untracked-unignored file by content, and Git's own resolved state — HEAD's value
  and raw ref, `info/exclude`, `info/attributes`, the resolved config, and the
  resolved excludes file — so a change cannot hide behind Git plumbing.
- **Evaluator-state guard.** vise digests `vise.toml`, `vise.lock`, the journal,
  and the blobs around each judged run — by content, refusing a state file swapped
  for a symlink, and distinguishing an absent file from an empty one — so the
  judged party cannot rewrite the judge mid-run.
- **Agent contract.** `vise init` ships `AGENTS.md`, the operator setup it depends
  on, and a copyable agent-ready example. The contract is pinned byte-for-byte
  against the embedded copy and machine-checked against what the tool actually
  prints.
- **`vise doctor`**, an advisory readiness check that front-loads setup failures
  the gate would otherwise catch at run time.
- **`vise record --preview` / `--accept`** to review a baseline before any write,
  and **enforced metrics** frozen in the lockfile beside behavioural probes.
- **Committed verification harness** (`scripts/verify`) with eleven features, each
  shown to fail against a deliberate break in what it watches, plus a one-command
  dogfood target that gates vise with vise.

### Security

- **Go toolchain pinned to 1.25.13**, the minimal patch bump clearing the 26
  standard-library advisories `govulncheck` had reported; under it `govulncheck`
  finds nothing on a path vise calls.
- **Trust boundary hardened** so the party under judgement cannot influence the
  verdict: blob bytes (not only names) are digested, state files may not be
  symlinks, and every declared command is covered by the evaluator-state guard.

### Notes

- The core — `record`, `gate`, `verify`, the tamper boundary, the judging path —
  was mutation-audited (37 mutations against the verdict-deciding functions; every
  real survivor fixed and tested) and driven end-to-end by eight coding agents
  working real tasks under the gate.
- `vise doctor` is advisory and fails safe: an inspection it cannot complete
  becomes a finding, never a silent pass. A doctor miss costs a surprise, not a
  wrong verdict — the gate remains the guarantee.

[0.3.0]: https://github.com/NakliTechie/vise/commits/v0.3.0
