> **Lifecycle:** living — bundle, operator, storage and Git identity foundations implemented; host runtime pending

# Local reference host

An optional, caller-neutral trusted controller for Vise, with a fixed local
Docker executor and independent delivery authority. The standalone CLI remains
usable without it. This directory currently records the contract, not a usable
host or a certified containment boundary. Source bundles, operator generations,
owned-directory I/O, Git identity and private Git layout have tests; the
persistent controller lifecycle is not yet implemented. Git activation publishes only index/HEAD,
not a usable work-tree/current-state generation. These modules do not launch
candidate code.

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
python3 -B -m unittest -v host.test_bundle host.test_operator host.test_storage host.test_storage_tree_publication host.test_git_identity host.test_git_initialization_prestate host.test_git_inventory
```

The full `scripts/verify verify` also runs these tests in `internal-checks`.
That development check requires Python; the standalone Go Vise CLI does not.
The twelve frozen root observations do not directly cover these Python modules.
Storage tests currently require POSIX descriptor-relative I/O and advisory locks;
they are not evidence for Windows support or candidate containment.
The directory-publication helper checks an exact registered file/directory
inventory and renames it to an absent destination. Its caller must hold the
session lock and durably stage the tree; it does not implement initialization
recovery or compare-and-swap against hostile concurrent host writers.
Git identity tests use an installed Git executable and disposable repositories.
The initialization helper accepts only exact registered controller prestate; it
does not recover a partially initialized Git repository. The layout validator
checks the private repository's registered paths and fixed symbolic HEAD before
Git interprets them. It is a callable foundation, not yet a Session entry-point
guard. It does not authenticate unreachable object contents; active reachable
objects remain the Git identity verifier's responsibility.

## Related documents

- [SPEC.md](SPEC.md) — authority, exact identity, persistent lifecycle and checks
- [SESSION.md](SESSION.md) — phase-two materialization, Git identity and recovery contract
- [walkthroughs.md](walkthroughs.md) — accepted scope decisions
- [DEFERRED.md](DEFERRED.md) — later required slices and revisit triggers
- [Portable advisory examples](../examples/agent-ready/README.md)

Implementation branch: `codex/caller-neutral-roadmap`. This is not a release or
permission to merge, publish, spend or replace the installed judge.
