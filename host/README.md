> **Lifecycle:** living — bundle foundation implemented; host runtime pending

# Local reference host

An optional, caller-neutral trusted controller for Vise, with a fixed local
Docker executor and independent delivery authority. The standalone CLI remains
usable without it. This directory currently records the contract, not a usable
host or a certified containment boundary. The pure bundle module is implemented
with unit tests; it does not yet import a workspace or launch candidate code.

## Status

- [x] Accepted scope and independently reviewed lifecycle design folded into spec.
- [x] Phase 1 — canonical source-bundle foundation.
- [ ] Phase 2 — trusted materialization, persistent session and receipt schemas.
- [ ] Phase 3 — fixed executor and scoped container recovery.
- [ ] Phase 4 — evaluation, progress, operator acceptance and delivery.
- [ ] Phase 5 — two-client runtime evidence and support matrix.

No host runtime quickstart is advertised until it executes the legitimate
construction and refusal matrix. Unit-level bundle work cannot establish H01–H08.

## Check the foundation

From the repository root, with Python 3.11 or newer:

```sh
python3 -B -m unittest -v host.test_bundle
```

The full `scripts/verify verify` also runs these tests in `internal-checks`.
That development check requires Python; the standalone Go Vise CLI does not.
The twelve frozen root observations do not directly cover the bundle module.

## Related documents

- [SPEC.md](SPEC.md) — authority, exact identity, persistent lifecycle and checks
- [SESSION.md](SESSION.md) — phase-two materialization, Git identity and recovery contract
- [walkthroughs.md](walkthroughs.md) — accepted scope decisions
- [DEFERRED.md](DEFERRED.md) — later required slices and revisit triggers
- [Portable advisory examples](../examples/agent-ready/README.md)

Implementation branch: `codex/caller-neutral-roadmap`. This is not a release or
permission to merge, publish, spend or replace the installed judge.
