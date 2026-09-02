# Operator review

- What exists: `record --preview` runs both passes and returns the candidate's behavior diff and digest without writing baseline state; `record --accept <digest>` writes only that candidate; metric definitions are frozen as `run_hash` and a changed definition is harness drift, never an improvement.
- User route: change a probe or metric definition, `vise record --preview`, read the diff, `vise record --accept <digest>`, then `vise gate`.
- Harness route: `scripts/verify verify review`.
- What usually lies: a preview that writes, an accept that writes a different candidate than the one reviewed, a weakened metric policy reported as "no behavior changed", and a replaced analyzer reported as an improvement.
