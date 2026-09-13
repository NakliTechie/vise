> **Lifecycle:** locked — phase-two implementation contract; runtime pending

# Trusted materialization and persistent session contract

This contract defines how the local reference host turns a validated candidate
bundle C and a trusted operator generation O into one persistent evaluator
checkout. It covers materialization, Git identity, restart, and the acceptance
reconciliation seam. It does not implement candidate execution, Docker, Vise
gate/record, delivery, or provider access.

## Authority boundary

The controller creates the session root and is its only writer. A candidate can
provide only a strictly decoded `SourceBundle`; it cannot provide host paths,
Git arguments, environment, policy, O, recovery identifiers, or destinations.
An operator provides O through a separate trusted interface. One session lock
serializes initialization, materialization, operator advance, evaluation,
acceptance, reconciliation, and later delivery.

C contains sorted relative paths, exact bytes, and one executable bit for each
regular file. O independently contains the manifest, lock, referenced blobs,
specs, dependencies, reviewed Git-interpreted files, and bridge policy. C and O
must be disjoint under exact, case-folded, NFC, and ancestor/descendant checks.
Candidate `.git`, `.vise`, and `.vise-host` components are forbidden at every
depth. This host additionally reserves `.gitignore` and `.gitattributes`
components at every depth in C. Legitimate versions of those files are reviewed
into O before a baseline is recorded. Ordinary `host/` paths remain valid C.

The controller owns `.git/` and `.vise-host/`. Vise owns its named mutable
runtime files. Candidate bytes are never interpreted or executed while C or O
is prepared or materialized.

## API

```python
@dataclass(frozen=True)
class FileRecord:
    path: str
    sha256: str
    size: int
    executable: bool

@dataclass(frozen=True)
class OperatorGeneration:
    generation: int
    files: tuple[SourceEntry, ...]
    encoded: bytes
    identity: str

@dataclass(frozen=True)
class FixedGitIdentity:
    author_name: str
    author_email: str
    committer_name: str
    committer_email: str
    timestamp: int
    timezone: str
    message_schema: int
    object_format: str

@dataclass(frozen=True)
class AssemblyIdentity:
    candidate: str       # canonical C envelope identity
    operator: str        # canonical O envelope identity
    tree: str            # T, Git tree of C union O file content
    commit: str          # K, provenance commit
    bootstrap: str       # immutable session bootstrap parent

@dataclass(frozen=True)
class MaterializationResult:
    identity: AssemblyIdentity
    observed_candidate: tuple[FileRecord, ...]
    observed_operator: tuple[FileRecord, ...]

@dataclass(frozen=True)
class AcceptanceObservation:
    generation: Literal["prior", "reviewed-next", "inconsistent"]
    outcome: Literal["known-success", "known-failure", "unknown"]
    lock_sha256: str
    receipt: bytes | None

class SessionError(RuntimeError): ...
class RecoveryRequired(SessionError): ...

def initialize_session(root: Path, operator: OperatorGeneration,
                       *, git: GitRunner,
                       identity: FixedGitIdentity) -> MaterializationResult
def open_session(root: Path, *, git: GitRunner) -> Session
def materialize_candidate(session: Session, candidate: SourceBundle) \
        -> MaterializationResult
def advance_operator(session: Session, *, expected_identity: str,
                     expected_generation: int,
                     replacement: OperatorGeneration) -> MaterializationResult
def observe_generation(session: Session) -> MaterializationResult
def reconcile_materialization(session: Session) -> MaterializationResult
def inspect_historical(session: Session, candidate_identity: str,
                       operator_identity: str) -> AssemblyIdentity
def reconcile_acceptance(session: Session) -> AcceptanceObservation
```

`GitRunner` accepts only fixed argument tuples, fixed stdin bytes, an explicit
working directory, and a complete explicit environment. It never accepts a
shell string. All public operations return only after releasing the session
lock. Malformed, stale, mismatching, or unsupported inputs raise `SessionError`
without changing the usable generation.

## Canonical operator generation

O is canonical ASCII JSON with sorted keys, compact separators, one trailing
LF, and this version-one envelope:

```json
{"files":[{"data":"...","executable":false,"path":"vise.toml"}],"generation":0,"kind":"operator","version":1}
```

Paths and files obey the bundle bounds and regular-file rules. `generation` is
an exact non-negative integer. O may contain `.vise/blobs/...` and other
evaluator-owned paths. It cannot contain `.git`, `.vise-host`, the mutable Vise
paths `.vise/journal.jsonl`, `.vise/run.lock`, or `.vise/tmp`, or collide with C.
Its identity is SHA-256 of the exact encoded envelope.

## Persistent layout and schemas

```text
SESSION/
  .git/                             controller-owned private repository
  .vise/                            evaluator state
  .vise-host/
    session.lock
    session.json
    operations.jsonl
    generations/<sha256>            exact canonical C/O envelopes
    intents/materialize.json
    intents/acceptance.json
    recovery/<Q>/prior/
    recovery/<Q>/requested/
    recovery/<Q>/manifest.json
```

`session.json` is canonical JSON and contains:

```json
{
  "version":1,
  "session_id":"random controller identifier",
  "bundle_contract":1,
  "operator_contract":1,
  "host_contract":1,
  "git_policy":"sha256:...",
  "git_identity":{
    "author_name":"fixed","author_email":"fixed",
    "committer_name":"fixed","committer_email":"fixed",
    "timestamp":0,"timezone":"+0000","message_schema":1,
    "object_format":"sha1"
  },
  "bootstrap":"git object id",
  "current":{
    "candidate_envelope":"sha256:...",
    "operator_envelope":"sha256:...",
    "operator_generation":0,
    "tree":"T","commit":"K"
  },
  "candidate_inventory":[],
  "operator_inventory":[]
}
```

The exact canonical C/O envelope bytes are stored content-addressed before a
state pointer or intent refers to them. File hashes are comparison evidence,
not reconstruction material. Reopen bounds and strictly decodes each envelope,
recomputes its identity, validates the O counter and contract versions, and
reconstructs inventories, T, and K. It also validates fixed Git identity,
object format, bootstrap object, and canonical Git policy. Callers do not
resupply or override these values on reopen.

`operations.jsonl` is append-only chronology. Each canonical record contains
operation Q, phase, prior and requested C/O/T/K, observed outcome, and refusal
detail. Q is an idempotency key. A duplicate complete terminal record must be
byte-identical and is not appended. A torn final line is retained and may be
completed only from a separately durable terminal outcome. An earlier malformed
line or conflicting Q refuses.

## Content identity and evaluator activation

T is the Git tree for the exact paths, bytes, and `100644`/`100755` modes in C
union O. Envelope-only metadata is absent from T. K has T, the immutable
bootstrap as its sole parent, the fixed identity/timestamp, and a canonical
message containing C identity, O identity, and message schema. Thus:

- identical C/O in one session produces identical T/K;
- a C/O file path, byte, or mode change changes T and K;
- an O generation-only change preserves T but changes O identity and K; and
- restoring an earlier C under current O restores the corresponding T/K.

Computing objects is insufficient. A usable generation requires simultaneous
agreement among the independently observed C/O work tree, index tree T and
tracked modes, `HEAD=K`, K's parent/message/tree, and `session.json.current`.
It also requires no nonterminal intent and a clean Git status.

Git objects are constructed from retained bytes without asking Git to discover
work-tree content. A temporary controller-owned index is built and verified as
T. Publication installs index T through Git's lock/rename protocol, then updates
HEAD with compare-and-swap from the intent's exact prior K to requested K, then
atomically publishes current state. Unexpected refs or index content refuse;
the controller never force-updates them.

## Git read authority

The canonical root `.gitignore` is O and contains the required entries:

```text
.vise-host/
.vise/journal.jsonl
.vise/run.lock
.vise/tmp/
```

It does not ignore `.vise/blobs`, `vise.toml`, `vise.lock`, specs, dependencies,
or other O files. Additional ignore rules and all `.gitattributes` files are
reviewed O content. Controller-owned `.git/info/exclude` and
`.git/info/attributes` are canonical empty regular files. `.git/config` has an
exact known schema for repository format, hooks, excludes, and attributes; all
other entries refuse reopen.

Each Git invocation receives a complete environment, not inherited variables:

```text
PATH=<controller-pinned Git directory>
HOME=<empty controller-owned directory>
LC_ALL=C
TZ=UTC
GIT_CONFIG_NOSYSTEM=1
GIT_CONFIG_SYSTEM=/dev/null
GIT_CONFIG_GLOBAL=/dev/null
GIT_CONFIG_COUNT=0
GIT_TERMINAL_PROMPT=0
GIT_ATTR_NOSYSTEM=1
```

Read calls add `GIT_OPTIONAL_LOCKS=0`; writes omit it. Fixed arguments identify
the Git directory and work tree, disable hooks/external diffs/filters, and bind
the canonical excludes and attributes policy. No inherited `GIT_*`, credential,
pager, editor, alternates, or shell variable survives. Candidate content named
like configuration, a hook, shell, or Vise JSON is treated only as bytes.

## Observed-tree policy

All walks are descriptor-anchored beneath the controller-created session root.
They use `lstat`, no-follow opens, and exact relative names. A regular file with
link count greater than one, a symlink, FIFO, socket, device, invalid mode, or
unknown entry refuses. An allowed directory is exactly:

1. an ancestor implied by retained/requested C or O file paths;
2. the registered controller-owned `.git` structure;
3. registered `.vise-host` state, intent, generation, and recovery paths;
4. `.vise` itself and O's exact `.vise/blobs` inventory/ancestors;
5. the named mutable `.vise/journal.jsonl` and `.vise/run.lock` parents; or
6. registered mutable `.vise/tmp` and its Vise-created contents.

The walker never excludes `.vise` wholesale. Unknown directories, unexpected
empty directories, unknown blobs, and other `.vise` children refuse. Candidate
replacement never removes O, Git, controller, journal, run-lock, or scratch
state.

## Replacement transaction

Only paths in the retained previous C inventory may be replaced by a candidate
operation. Operator advance separately replaces only retained O paths. Before
mutation, observed paths, bytes, modes, and types must equal the retained prior
inventory and no unknown path may exist.

The materialization intent uses these durable phases:

### `PREPARING`

The controller first atomically publishes and syncs an ownership intent naming
Q and its exact future `.vise-host/recovery/Q` path before creating that path.
It then exclusively creates the recovery root. Absence or partial content is an
expected interrupted preparation state. Live work tree, index, HEAD, and current
pointer remain exactly prior; deletion is forbidden.

`prior/` receives exact retained C/O bytes and modes copied with no-follow reads.
`requested/` receives exact requested bytes and modes from retained canonical
envelopes. The controller fsyncs files and directories and independently checks
both inventories. The canonical manifest binds Q, session, prior/requested
envelope identities and O counters, file records, T/K, prior/requested index
trees and HEADs, and both staged inventory hashes.

Restart may resume from durable canonical envelopes or delete only Q's
registered incomplete recovery root and record aborted preparation, provided
the entire live prior generation remains exact. It never adopts unregistered
recovery content.

### `REPLACING`

Only after both recovery generations and manifest are complete, synced, and
revalidated may the controller sync an intent transition to `REPLACING`. This
first authorizes deletion. It rechecks the complete prior live state, removes
only exact retained paths, installs staged regular files through trusted parent
descriptors, and verifies requested C/O. It then publishes index T, CAS-updates
HEAD, publishes current state, and writes a durable terminal outcome.

At restart, the controller validates intent, manifest, complete backups,
canonical envelopes, operation ownership, and staged bytes/modes before touching
the live tree. Missing or tampered required material after deletion refuses and
retains evidence. A registered mixture may be restored completely to prior or
finished completely to requested. Unknown live content, Git state, objects, or
pointer refuses. No mixed state is usable.

### `CLEANUP`

After requested work tree/index/HEAD/current agree, a durable outcome keyed by Q
is synced before the intent enters `CLEANUP`. Cleanup deletes only registered
recovery-manifest paths, records progress, and tolerates an already absent
registered path. Restart trusts the durable terminal identity and neither
reopens replacement nor requires already deleted backups. Unknown cleanup
content refuses. The intent is cleared only after Q's registered root is gone
and its parent is synced.

Initialization uses canonical empty C, an empty bootstrap tree/index, and no
prior O. Its `PREPARING` rules are separate until bootstrap, fixed Git policy,
identity schema, exact C/O envelopes, and initial O are durable.

## Operator advance and live-O rule

`advance_operator` compares both expected identity and expected generation with
current O. Replacement generation must be current plus one and the operation
must carry separate operator authority. Stale, repeated, skipped, or lower
generations refuse. The operation preserves exact C and all mutable Vise state.

Older retained C may be rematerialized under current O. Older retained C/O may
also be inspected and T/K recomputed as explicitly non-deliverable historical
evidence. Historical O is never activated: it cannot change live O, HEAD/index,
journal, acceptance, or current state. Phase two defines no rollback operation.

## Acceptance reconciliation seam

Ordinary materialization treats any live O mismatch as drift. A later explicit
operator acceptance transaction is the sole exception because Vise record may
create initially unknown exact `vise.lock` and blob bytes. Before record, the
controller durably registers Q, evaluated K, prior O, reviewed preview digest,
fixed command/scope, and judge/bridge/policy identities. Only `vise.lock` and
regular files directly beneath `.vise/blobs/` may differ; C and every other O
path remain exact.

The preview candidate digest is exactly SHA-256 of the exact canonical lock
bytes Vise proposes and later writes. It is not a host-defined lock/blob digest
and is not produced by reserializing the lock in Python. Reconciliation first
compares the complete observed O with retained prior O. Exact prior O is
classified `prior` without comparing its lock digest to a different reviewed
preview. Only a non-prior generation can be classified or adopted as
`reviewed-next`; for that branch, the SHA-256 of its exact observed lock bytes
must equal the registered preview digest. Separately, every referenced blob
must be a regular file whose bytes hash to its name, and blob additions/removals
must be explained by prior versus new lock, except documented content-addressed
orphans. A third generation is `inconsistent` and refuses.

Reconciliation never retries record or invents a journal entry. It reports two
independent facts:

- generation: `prior`, `reviewed-next`, or `inconsistent`;
- outcome: `known-success`, `known-failure`, or `unknown`.

Unchanged prior lock bytes alone imply `prior` and `unknown`: no invocation,
identical-byte publication, and lock-before-journal interruption are
indistinguishable without a receipt. A complete validated receipt may establish
known outcome even with identical lock bytes. A reviewed new generation with a
lost receipt may be adopted as O-next but its operation outcome remains unknown.
Any unrelated drift, torn lock, wrong exact lock digest, invalid blob, or
unauthorized pin-acceptance change refuses and retains intent.

Acceptance produces O-next and then T-next/K-next. The lock retains historical
provenance per pin identity: a previously accepted unchanged pin retains its
exact prior `accepted_commit`, while a previously unaccepted pin newly accepted
by this transaction carries evaluated K. A new or identity-changed pin accepted
by this transaction likewise carries evaluated K. A pin not accepted remains
null. Reconciliation rejects loss or alteration of retained earlier provenance
and rejects any wrong commit on a newly accepted pin. Neither earlier commits
nor K are rewritten to K-next. Live O advances monotonically and is never
rolled back by recovery or historical inspection.

## Finite acceptance matrix

### Identity and positive lifecycle

1. Assemble ordinary multi-file, empty, binary, executable C with
   manifest/spec/blob/ignore O; independently observed bytes/modes equal C/O,
   actual index tree equals T, actual HEAD equals K, and status is clean.
2. Repeat identical C/O and restart; T/K are stable while chronology appends.
3. Change C byte, path, and mode separately; T/K change. Restore old C under
   current O; its T/K returns.
4. Change an O file byte/path/mode; T/K change. Change O generation only; T is
   stable and K changes.
5. Preserve sentinel journal, run-lock, and scratch content through C changes,
   O changes, restart, and historical inspection; none appears in O or T.

### Path, inventory, and Git authority

6. Reject root/nested mixed-case `.git`, `.vise`, `.vise-host`, `.gitignore`,
   and `.gitattributes` in C; pair reviewed ignore/attribute files in O and
   ordinary `host/src` C.
7. Reject symlink parent/leaf, hard link, FIFO, unknown file/directory, unknown
   `.vise` child/blob, unexpected empty directory, and changed prior byte/mode
   before mutation. Pair every allowed directory/runtime class.
8. Change `.git/config`, `.git/info/exclude`, `.git/info/attributes`, HEAD,
   index tree/mode, an object, or current pointer separately; reopen refuses.
9. Inject hostile inherited Git config, hook, attributes, filter, alternates,
   credential, pager, editor, and shell variables. The fixed runner receives
   none, produces the same T/K, executes none, and retains operator observations.
10. Store config/hook/shell/Vise-looking bytes in allowed C; materialization
    preserves them without interpreting or executing them.

### Transaction and restart

11. Interrupt before/after ownership-intent sync, recovery mkdir, each prior
    and requested copy, manifest sync, and `REPLACING`; restart twice.
    `PREPARING` preserves exact prior live state.
12. Interrupt after first removal/install, object write, temporary-index sync,
    index publication, HEAD CAS, current publication, and terminal outcome.
    Restart twice and obtain one complete prior or requested generation.
13. Interrupt before/after terminal JSONL append, `CLEANUP`, every cleanup
    deletion, intent removal, and parent sync. Restart twice; one terminal Q
    remains and completed replacement never reopens.
14. Corrupt/miss/link/add a prior or requested recovery item before replacement;
    refuse without changing live bytes. Repeat after deletion; refuse while
    retaining all remaining evidence.
15. Exercise torn final chronology line, byte-identical duplicate Q,
    conflicting Q, unregistered recovery root, unknown cleanup path, and initial
    session interruption. Only exact registered/idempotent cases recover.

### Persistence, O authority, and acceptance

16. Reopen without caller identity values under changed ambient defaults;
    reconstruct exact C/O/T/K. Refuse missing/noncanonical envelope, bad contract
    version/counter/object format/identity/bootstrap, stale expected O, repeated,
    skipped, or lower generation.
17. Inspect old C/O and recompute historical T/K without mutating live current
    O, HEAD/index, journal, or acceptance. Attempted live old-O activation
    refuses. Authorized current+1 O advances.
18. Acceptance rows: a different reviewed preview followed by no invocation
    leaves exact prior O classified `prior`/`unknown`; identical lock with no
    receipt is also `prior`/`unknown`; identical lock with receipt has its known
    outcome; reviewed changed lock with lost receipt is
    `reviewed-next`/`unknown`; complete changed-lock receipt has its known
    outcome. Assert generation/outcome independently and zero retries.
19. Reject correct blobs with a combined host digest instead of exact lock-byte
    digest, whitespace-altered or other valid-but-different lock, missing,
    changed, linked or extra unexplained blob, unrelated spec/C change, and
    unknown `.vise` child. In a mixed-pin positive, retain an unchanged pin's
    earlier accepted K-old and stamp only a newly accepted pin with evaluated K.
    Reject an alteration of K-old and a wrong commit for the newly accepted pin
    in separate rows.
20. Accept the new pin at K beside the retained K-old pin, freeze O-next,
    compute K-next, restart, and regress C. The lock retains both historical
    values at K-old and K respectively; neither becomes K-next, live O never
    rolls back, and no journal entry is synthesized.

These tests establish only phase-two trusted materialization and session
identity. Actual Docker identity, candidate execution containment, Vise
gate/record lifecycle, delivery, and the separate B06 review require later
evidence.
