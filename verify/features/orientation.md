# Orientation

- What exists: repository-independent help and version output plus bounded `status` diagnostics; `status` runs the manifest's fingerprint commands (operator-declared) and writes no vise state.
- User route: run `vise --help`, `vise version --json`, or `vise status --json` before initialization.
- Harness route: `scripts/verify verify orientation`.
- What usually lies: help that requires repository state, malformed JSON, and status commands that fail nonzero while reporting a problem.
