# Failure modes

- What exists: stable behavior differences exit 1, harness drift exits 2, flaky observations exit 3, missing baselines exit 4, metric regressions exit 5, and a rerun limit that refuses the third consecutive gate for one commit, lock, and probe set (exit 2, `human`) until a record, a judged green or red verdict covering the set (verify and gate alike), or a new commit or lock.
- User route: run `vise gate --json` and branch on `next.action`.
- Harness route: `scripts/verify verify failure-modes`.
- What usually lies: a flaky probe being excluded, dependency drift mislabeled as behavior, repeated reruns bypassing the operator, or a timeout being recordable as expected behavior.
