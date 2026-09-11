# Pin lifecycle

- What exists: a probe whose expected bytes are a human-written spec file, frozen by `record` before the program exists; the `unmet` class, exit 6, and `next.action: build`; acceptance stamped only by a clean-tree record that saw the spec met, shown as a transition line in the preview, and never revoked by a later record; spec drift routed to `human`; an accepted pin judged as a preserve probe.
- User route: write `spec/<id>.stdout` → add `expect.stdout` to the probe → `vise record` (unmet) → `vise gate` (exit 6, `build`) → build → `vise gate` (green, `passing_unaccepted`) → `vise record --preview` / `--accept` (accepted) → `vise gate`.
- Harness route: `scripts/verify verify pin`.
- What usually lies: a gate that accepts a pin (only record may); a spec edited to match the program's output gating green (its hash is in the lockfile — the answer is `human`); an unmet pin reported as `behavior` or a regressed accepted pin reported as `unmet`; a record that quietly demotes an accepted pin instead of refusing; an unrun metric counted as a pass while a pin is unmet.
- Shown to fail (2026-09-12): `ExitUnmet` set to 1 → `expected vise exit 6, got 1`; the spec-drift check deleted → `expected vise exit 2, got 6`.
