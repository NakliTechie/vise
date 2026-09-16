> **Lifecycle:** living — foundations and persistent replacement implemented; initialization recovery and host runtime pending

# Local reference host

An optional, caller-neutral trusted controller for Vise, with a fixed local
Docker executor and independent delivery authority. The standalone CLI remains
usable without it. This directory currently records the contract, not a usable
host or a certified containment boundary. Source bundles, operator generations,
owned-directory I/O, Git identity, private Git layout and persistent replacement
have tests. Session operations retain C/O, observe work-tree/index/HEAD/current
agreement, and reconcile registered replacement transactions. Durable initial
session recovery, actual evaluation/acceptance and delivery remain unfinished.
These modules do not launch candidate code.

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
python3 -B -m unittest -v host.test_bundle host.test_operator host.test_storage host.test_storage_tree_publication host.test_git_identity host.test_git_initialization_prestate host.test_git_inventory host.test_git_quoted_paths host.test_pin_provenance host.test_session host.test_session_recovery host.test_initial_seed host.test_acceptance_generation
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
Git interprets them, including at Session reopen and replacement preflight.
It does not authenticate unreachable object contents; active reachable objects
remain the Git identity verifier's responsibility. Git index construction uses
NUL-delimited names so literal quote characters remain data.

Session tests cover retained identities, monotonic operator advance, historical
inspection, exact path inventories, registered preparation/replacement/cleanup,
selected abrupt-process partial writes, and idempotent reopen. They do not prove
arbitrary Git-child interruption or physical power-loss durability. Incomplete
initialization and an unverifiable first ownership write still refuse while
retaining evidence. The isolated seed builder seals exact Git bootstrap bytes,
directories, requested index and fixed identity; it is not yet wired into
durable Session initialization. No phase-two completion is claimed.

The read-only pin-provenance helper checks that unchanged accepted pins retain
their historical commit and newly accepted pins name the evaluated commit. It
does not authorize acceptance, invoke `record`, or validate the complete lock,
reviewed digest, blobs, unrelated paths, or execution receipt. A separate pure
snapshot classifier checks exact prior O before a different reviewed preview,
raw lock-byte digest equality for reviewed-next, unchanged unrelated O, blob
closure and pin history. Hash-only large captures do not require blobs; existing
content-addressed orphans may remain or be pruned, but novel unreferenced blobs
refuse. This classifier does not observe filesystem types/links or C, validate
the complete native lock schema or receipts, infer record outcome, allocate
O-next, or perform durable acceptance. Those authority boundaries remain required.

## Related documents

- [SPEC.md](SPEC.md) — authority, exact identity, persistent lifecycle and checks
- [SESSION.md](SESSION.md) — phase-two materialization, Git identity and recovery contract
- [walkthroughs.md](walkthroughs.md) — accepted scope decisions
- [DEFERRED.md](DEFERRED.md) — later required slices and revisit triggers
- [Portable advisory examples](../examples/agent-ready/README.md)

Implementation branch: `codex/caller-neutral-roadmap`. This is not a release or
permission to merge, publish, spend or replace the installed judge.
