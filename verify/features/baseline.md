# Baseline lifecycle

- What exists: deterministic double-pass recording, canonical lock persistence, raw probe descent, judged verification, bounded gating, artifacts, dependencies, fingerprints, blobs, status with drift detection, and a pending-proposals count read from the agent-writable `.vise/proposals.toml`.
- User route: `vise record` → `vise status` → `vise run <id>` → `vise verify` → `vise gate`.
- Harness route: `scripts/verify verify baseline`.
- What usually lies: stale artifacts, missing expected blobs, inherited environment values, uncommitted harness inputs, and a green summary that omits a declared probe.
