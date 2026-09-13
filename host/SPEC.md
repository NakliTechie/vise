> **Lifecycle:** locked — B07 implementation contract, not a host-support claim

# Local reference host — Spec

## Goal

Provide one caller-neutral, local trusted controller that can evaluate legitimate
candidate work and independently authorize delivery of exactly that work. Keep
the standalone Vise CLI useful without this host. Prompting is advisory; this
host's authority comes from separating candidate execution from judge, policy,
operator state and delivery. No model SDK or provider account is required.

The operator's accepted roadmap decisions govern scope. The local Docker executor
is a reversible technical selection under the named-host requirement, not a
user commitment to Docker or a hosted service. This specification folds the
independently reviewed B07 design and its persistent-lineage/lifecycle revision.

## Architectural decisions

### A1. Trust domains (walkthrough Q1–Q2)

- The trusted controller owns policy, snapshots, Git configuration, evaluator,
  operator inventories, operation records, receipts and delivery directories.
- The editing customer receives fixed file operations and container command
  execution in a disposable source workspace. Arguments never select Docker
  flags, host paths, credentials, policy or delivery destinations.
- Each session owns **one persistent private Git evaluation repository and Vise
  state**. The installed judge runs on the trusted host, outside candidate
  processes. Its journal, run lock and scratch remain writable to Vise.
- The trusted probe bridge exposes only fixed operator-declared probe IDs. Each
  command executes in a scoped container with read-only source, private writable
  output and temporary storage. It never receives the evaluator checkout,
  Docker socket, host credentials or delivery state.
- Delivery consumes controller-retained evidence, never caller-authored verdict
  JSON. The controller is the only writer of a new local delivery generation.

Operator/host-administrator access and kernel/daemon compromise are outside this
first profile. This does not contain an existing unrestricted Codex terminal.
Customers must actually use the restricted execution/file surface for the claim
to apply. Report prevention, detection and unsupported routes separately.

### A2. Canonical inventories and evaluation identity

Candidate inventory C is sorted relative paths, exact immutable bytes and one
executable-mode bit per regular file. Reject absolute/parent/root aliases,
duplicate case-folded paths, non-NFC paths, invalid Unicode, symlinks, special
files, prefix collisions and paths colliding with operator inventory O.
Never silently rename input. Candidate `.git`, `.vise` and `.vise-host`
components are reserved, case-insensitively. Ordinary `host/` source is allowed.

The version-1 bundle envelope is canonical JSON with `version: 1` and `files`, whose
entries contain `path`, canonical base64 `data` and boolean `executable`.
Sort object keys and files by path, use compact separators and ASCII escaping,
and append one LF. Its identity is `sha256:` of these exact encoded bytes.
Strict decoding rejects duplicate keys, noncanonical encoding, unknown fields,
invalid data and excessive nesting using the bundle error type.

All limits are exact positive integers (not bool/float). Defaults are 10,000
files, 16 MiB per file, 256 MiB total bytes, 4,096 UTF-8 bytes per path and
384 MiB encoded bytes. Enforce count while consuming input, not after exhausting
an iterable. Empty bundles/files and arbitrary binary bytes are legitimate.
Operators may set tighter limits. Operator path inventories can describe
reserved paths; this never authorizes a candidate to supply them.

O describes operator-owned manifest, specs, frozen dependencies, bridge policy,
lock and referenced blobs. Runtime journal/locks/scratch are separate mutable
state, not a frozen tree to restore on every call. Candidate/operator namespaces
must be disjoint, including case-folded aliases and ancestor/descendant paths.
Do not copy candidate Git metadata or run its code to prepare either inventory.

Initialize Git with isolated configuration and explicit identity. For each
session, C and O determine tree T. Fixed author, committer, timestamp, parent
(the session bootstrap commit) and message encoding C/O determine synthetic
commit K. Identical C/O select identical T/K within that session. External
revision labels are advisory; K is not the customer's authenticated commit or
a chronological history. Append-only controller operations record chronology.

Replace only the previous candidate inventory when materializing. Do not
recreate Git, truncate the journal, clear a rerun budget, discard acceptance or
re-record a baseline. A session lock serializes materialization, evaluation,
acceptance, reconciliation and delivery. Preserve Vise's documented consecutive
event/commit/lock rules; do not invent external query accounting here.

Independently inventory materialized paths, bytes and executable bits before
and after gate and immediately before delivery. Require exact C/O equality;
record C, O, T and K in the receipt. A transport hash alone is insufficient.

### A3. Evaluator binding and acceptance (walkthrough Q3–Q4)

Before candidate execution, bind judge digest/version, controller/bridge digest,
normalized policy, manifest, lock generation, referenced blobs, specs/dependencies,
image ID, environment fingerprint and evaluated set. Reject identity drift.
Operator definitions/fingerprints must include wrapper and image/policy identity;
never reinterpret an existing baseline with a different executor silently.

Use full Vise gate and retain its actual exit status and raw JSON. Apply the
public protocol's framing/semantic validation and independently check scope and
identity. Missing, truncated, extra-value, unknown or exit-mismatched evidence
is a host/protocol failure, not a manufactured Vise verdict.

Progress may follow green, or a proper-subset reduction in unmet IDs between full
results under the same manifest/lock/evaluator policy, with no regressed pin or
other failure class. Metrics skipped during unmet stay skipped. Final delivery
requires fresh full green. Passing-unaccepted pins retain that acceptance state;
gate, progress and delivery never invoke record.

Explicit operator acceptance is a separate reviewed preview/exact-digest
transaction, using the same probe bridge. Inspect the resulting lock/blob
generation before advancing O. A record at K can retain K as historical
`accepted_commit`; the resulting next O selects a new T/K on the next evaluation.
Never rewrite that historical provenance to fit the next synthetic commit.
After a lost record receipt, inspect actual state rather than automatically
retrying record. Preserve Vise's diagnostics for interrupted/inconsistent state.

### A4. Container lifecycle and artifact publication

The first profile uses an operator-pinned local Linux image and deterministic
commands available in it. Use fixed non-root execution, no network, read-only
root/source, dropped capabilities, no-new-privileges and finite process, memory,
CPU, output and time limits. No pulls or paid resources occur implicitly.
Read-only root alone does not make writable mounts immutable.

Before create, persist and sync intent containing a random session invocation
ID, exact container name, controller/session labels, expected image, mount/options
digest and phase. On startup/resume, bridge failure/timeout and before delivery,
reconcile every unfinished intent against Docker. Verify ownership/configuration,
stop the exact live container, observe terminal state and remove only that
registered disposable container. Never globally prune or broadly kill processes.
Unknown Docker state, ownership mismatch or unconfirmed termination refuses
evaluation/delivery. A terminated CLI/bridge does not prove container shutdown.

Each probe has one distinct artifact root, possibly containing multiple declared
files. Roots cannot overlap each other or source/operator paths. Author manifest
and mappings for this profile before recording, not as a silent lock migration.
Each invocation writes to a private output directory. After observed container
shutdown, inspect without following links, reject special/undeclared files, and
copy declared regular files into a second trusted bounded staging directory.
Retain the complete path/hash/size set. Genuine missing files remain missing for
Vise's accepted/unaccepted classification; do not substitute unconditional host
failure or host success for this classification.

Reset only the registered prior evaluator artifact root, then publish the whole
new root by one same-filesystem rename before the bridge returns. An unpublished
or interrupted transfer cannot create a usable success receipt. Recovery handles
only registered staging paths. Preserve raw candidate stdout/stderr/exit and
honest launch/timeout/copy failures; a Docker failure is not successful output.

### A5. Delivery generations

Require a fresh full green result for the frozen bundle and unchanged evaluator
and policy. Check source and retained artifact identities again. Copy into new
controller-owned staging, validate the complete generation, then rename once to
a new delivery ID; never overwrite a prior delivery. Stale/selected-scope results,
changed editing content, altered image/baseline/policy, substituted artifacts,
forged caller replies, failed cleanup or interrupted receipt publication refuse
delivery. Pair each refusal with a matching legitimate delivery witness.

## Public surfaces and private data

Foundation: pure Python bundle values, canonical encoding/decoding and protected
path validation. It performs no filesystem import or process execution.
Later phases add restricted edit/execute, freeze, evaluate, progress and deliver
operations. Operator initialization/acceptance uses a separate trusted surface.
Version their concrete request/response schemas before exposing them.

Candidate-visible data is its source, declared execution observations and bounded
status. Controller policy/state, operator approval authority, retained receipts
and delivery destinations remain private. This profile does not promise held-out
expectation secrecy; that is the separately required B13 contract.

## Acceptance matrix

| ID | Required finite witnesses |
| --- | --- |
| H01 | Actual host/Docker/image/bridge/judge/input inventory; changed identity rejected before delivery. |
| H02 | Ordinary file edits and shell edits reach frozen evaluation; empty/binary/executable inputs remain exact. |
| H03 | Editor/shell/interpreter/alias/substitution routes cannot alter protected controller state; distinguish prevention and detection. |
| H04 | Unmet → proper-subset progress → green-unaccepted → explicit acceptance → regression; journal, locks, scratch and skipped metrics remain truthful. |
| H05 | Wrong source/artifact/evaluator/image/policy/scope, forged reply and post-check edits refuse stale delivery; fresh counterparts deliver. |
| H06 | Interrupt edit, create-before-start, bridge SIGTERM/SIGKILL, evaluation, output transfer and receipt publication; recover exact scoped state without partial delivery. |
| H07 | Execute filesystem/network/process profile on named actual platform; daemon-unavailable recovery refuses until reconciled. |
| H08 | Plain CLI and separate programmatic customer drive full lifecycle; independent authorized review supports host claim. |

Also repeat identical C/O to prove stable T/K and retained journal/rerun semantics;
change C/O separately; re-materialize old C; change a materialized byte/mode;
interrupt both sides of artifact rename; accept, restart and regress a met pin.
Do not infer these witnesses from bundle unit tests or configuration alone.

## Build sequence

1. Canonical inventories and strict bounded encoding, with independent unit review.
2. Trusted materialization, persistent session identity, policy and receipt schemas.
3. Fixed Docker bridge, scoped reconciliation and POSIX-shell construction fixture.
4. Evaluation/progress/explicit operator acceptance and immutable delivery lifecycle.
5. Two-client acceptance replay and precise supported/experimental/unverified matrix.

No release, merge, deployment, installed-judge replacement or new spending is
authorized by this design. The outstanding B06 independent review is not replaced
by B07 unit tests or host implementation.

## Deliberate first-profile limits

No shared mutable output roots, arbitrary network-service workflows, universal
toolchain compatibility or universal host containment claim. Additional approved
roadmap work remains required as detailed in [DEFERRED.md](DEFERRED.md).

## References

- [Walkthrough decisions](walkthroughs.md)
- [Build status](README.md)
- [Public Vise contract](../conformance/README.md)
- [Docker run options](https://docs.docker.com/reference/cli/docker/container/run/)
- [Bind mounts](https://docs.docker.com/engine/storage/bind-mounts/)
- [Docker authority model](https://docs.docker.com/engine/security/)
