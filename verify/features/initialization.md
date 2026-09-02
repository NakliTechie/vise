# Initialization

- What exists: `vise init` writes a commented manifest, the embedded agent contract (`AGENTS.md`), and idempotent local-state ignore entries.
- User route: enter a Git repository, run `vise init`, declare a probe, commit the harness, then record.
- Harness route: `scripts/verify verify initialization`.
- What usually lies: overwriting an existing manifest, reporting an empty starter manifest as ready, or omitting journal and lock-file ignores.
