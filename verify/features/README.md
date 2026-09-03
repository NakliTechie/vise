# Verification feature map

Run features in this order:

1. [`orientation.md`](orientation.md) — help, version, JSON, and status without a repository.
2. [`readiness.md`](readiness.md) — `doctor`'s findings, what `init` closes, and the commands that take no lock.
3. [`initialization.md`](initialization.md) — cold repository setup and the first remedy.
4. [`baseline.md`](baseline.md) — record, status, run, verify, gate, artifacts, and dependencies.
5. [`failure-modes.md`](failure-modes.md) — behavior, harness, flake, and rerun-limit branches.
6. [`metrics.md`](metrics.md) — metric baseline, delta, and enforcement.
7. [`review.md`](review.md) — preview and accept an overwrite; frozen metric definitions.
8. [`example.md`](example.md) — the shipped agent-ready template, instantiated and gated.
9. [`dogfood.md`](dogfood.md) — vise gates a source change in a clone of itself.
10. [`internal-checks.md`](internal-checks.md) — unit, integration, and static checks.

Use `scripts/verify doctor`, `scripts/verify verify <feature>`, or `scripts/verify verify` from any checkout. The harness derives isolated binary, module-cache, build-cache, and fixture paths from the checkout path.
