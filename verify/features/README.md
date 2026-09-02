# Verification feature map

Run features in this order:

1. [`orientation.md`](orientation.md) — help, version, JSON, and status without a repository.
2. [`initialization.md`](initialization.md) — cold repository setup and the first remedy.
3. [`baseline.md`](baseline.md) — record, status, run, verify, gate, artifacts, and dependencies.
4. [`failure-modes.md`](failure-modes.md) — behavior, harness, flake, and rerun-limit branches.
5. [`metrics.md`](metrics.md) — metric baseline, delta, and enforcement.
6. [`review.md`](review.md) — preview and accept an overwrite; frozen metric definitions.
7. [`dogfood.md`](dogfood.md) — vise gates a source change in a clone of itself.
8. [`internal-checks.md`](internal-checks.md) — unit, integration, and static checks.

Use `scripts/verify doctor`, `scripts/verify verify <feature>`, or `scripts/verify verify` from any checkout. The harness derives isolated binary, module-cache, build-cache, and fixture paths from the checkout path.
