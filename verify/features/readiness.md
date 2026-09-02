# Readiness

- What exists: `vise doctor`, the operator's static check that a repository is fit to hand to a coding agent — six findings, each carrying its remedy — plus the rule that `status` and `doctor` take no state lock and that an unrecognized command is refused before one is reached.
- User route: run `vise doctor` before assigning an agent any work; run `vise init` to close the two findings it is responsible for; run `vise status` while a gate is in progress.
- Harness route: `scripts/verify verify readiness`.
- What usually lies: a readiness check that reports a problem by failing nonzero, so a caller cannot distinguish advice from a verdict; a read-only command that quietly waits on the lock a running suite holds, which looks like a hang rather than a queue; and a typo that waits out a full probe suite before anyone tells it that it was a typo.
