# Failure modes

- What exists: stable behavior differences exit 1, harness drift exits 2, flaky observations exit 3, missing baselines exit 4, and metric regressions exit 5.
- User route: run `vise gate --json` and branch on `next.action`.
- Harness route: `scripts/verify verify failure-modes`.
- What usually lies: a flaky probe being excluded, dependency drift mislabeled as behavior, repeated reruns bypassing the operator, or a timeout being recordable as expected behavior.
