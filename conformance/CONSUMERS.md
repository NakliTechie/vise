# B05 reference consumer contract

These are two independent, provider-free interpretation examples: POSIX shell
with jq, and Python standard library. Neither imports Vise's Go types or the
producer kit's validator. They consume the same captured-process envelope and
emit the same normalized decision. A shared test launcher is not shared
interpretation code. This contract defines the B05 slice before implementation.

## Boundary and commands

Each driver supports:

```
DRIVER consume CAPTURE.json EXPECTED.json
DRIVER progress PREVIOUS.json CURRENT.json EXPECTED.json
```

`consume` interprets a completed gate/verify attempt. `progress` additionally
checks whether two full gates satisfy the sanctioned unmet-progress rule.
The examples never record a baseline, edit candidate files, commit, deliver an
artifact, or call a model/provider. The test launcher must exercise both actual
Vise subprocess captures and separately labelled synthetic fault captures.

The orchestrator owns capture and fresh identity collection. `EXPECTED.json`
is a separate current-request/policy input, not read from the response itself.
The driver compares the captured binding with this expected binding. A host
must independently compute and protect it; an agent-writable file is not an
authentication mechanism. These interpreters do not establish B07/B12's hard
delivery boundary or eliminate the race between a check and later delivery.
No timestamp-only freshness inference is permitted.
The capture's `binding` is collected before launch; required `binding_after`
is independently collected after process termination. Both must match the
current EXPECTED binding. This detects changes during a cooperative capture,
not forged metadata. At least one live matrix fixture must hash its actual
inputs on both sides of execution and reject an edit between those snapshots.

## Capture envelope v1

All required keys below are present. Unknown additive object fields are ignored;
unknown enum/version values fail closed. Integers exclude booleans. JSON must
reject duplicate object keys, non-finite numbers, malformed/truncated input,
and multiple values. The embedded stdout is exactly one UTF-8 JSON object plus
LF; stderr stays separate. Outer envelope whitespace is insignificant.

```
{
  "v": 1,
  "binding": {
    "request_id": "unique attempt identifier",
    "cmd": "gate",
    "argv": ["gate", "--json"],
    "root": "/absolute/repository",
    "scope": {"kind": "full", "probes": ["a", "b"], "metrics": ["size"]},
    "candidate": "sha256:<64 lowercase hex>",
    "manifest": "sha256:<64 lowercase hex>",
    "lock": "sha256:<64 lowercase hex>",
    "evaluator": "sha256:<64 lowercase hex>",
    "environment": "sha256:<64 lowercase hex>",
    "artifact": "sha256:<64 lowercase hex>",
    "producer": {"version": "0.3.0", "revision": "<40 lowercase hex>", "modified": false}
  },
  "binding_after": "same binding object, independently collected after termination",
  "termination": {"kind": "exit", "code": 0},
  "elapsed_ms": 100,
  "stdout": "{...Vise reply...}\n",
  "stderr": ""
}
```

The example hashes and binding_after shorthand are placeholders, not acceptable
input values; binding_after must be an object. Producer `built` is an optional
string retained when supplied by version reporting. Identities
are opaque hashes supplied by the orchestrator: candidate includes dirty source
when applicable; manifest covers the declared scope/dependencies; environment
covers the declared execution identity; artifact names the intended delivery
bytes. Vise's reply does not independently emit or authenticate those fields.
`lock` is the evaluator tamper identity reported by Vise (covering the manifest,
lock and referenced observations), not a raw hash of the vise.lock file.
The live collector obtains it through separate status calls around execution
and also hashes the actual tracked files for candidate identity.
`lock` is required in the binding even for missing-baseline attempts (the
orchestrator hashes an explicit absence marker). A present reply lock must
match it; completed exits 0/1/3/5/6 require a reply lock.

`cmd` is gate or verify. `argv` starts with that command, includes `--json`, and
contains only the command, `--json`, and optionally `--probe ID` (one nonempty
ID). Full scope has no selector; probe scope has exactly one matching probe and
no metrics. Scope ID arrays are sorted, unique nonempty strings; probes is nonempty.
Probe and metric sets must be disjoint. The live fixture derives this scope
from its frozen manifest, not from reply counts.
These deliberately unambiguous invocation forms are not a new Vise parser.

`termination.kind` can be exit, signal, timeout, launch_error, collection_error.
Only exit with integer code can supply usable judgment evidence. Other kinds
are transport failures even if stdout contains an earlier green object.

EXPECTED has `v: 1`, `binding` (current expected binding),
`trusted_producers` (nonempty array of `{evaluator, revision, modified}`),
`max_elapsed_ms` (positive integer), and `max_stream_bytes` (positive integer).
The exact producer artifact/revision/modified triple must be in that policy;
product version alone grants no compatibility. Absent build stamps are unknown
and refused by this profile. This is a compatibility allowlist, not a signature.
Each input file is capped at 4 MiB; each decoded stream must also fit the policy
bound, capped at 1 MiB. Each JSON document has a 128-level nesting limit (root
value is level 1, each contained value adds one); exceeding it is a bounded
consumer refusal, not a Vise outcome. Elapsed duration is nonnegative and must fit policy.

## Interpretation

Validate the common Vise envelope, required counts, closed verdict/action/class
and metric enums, integer process/JSON exit equality, requested cmd, optional
failure/metric/pin shapes, and every standardized field present on these
gate/verify outcome routes. Only unknown additive field names may be ignored.
Use PROTOCOL.md's exact gate exit/verdict/action mapping, not record semantics.
On exit 2, harness usage markers require fix_invocation; otherwise harness
operator markers require human; otherwise the action is fix_probe. No other
failure class can carry a true authority marker. Require reported failure
classes and counts to agree; do not let contradictory routing reach callers.
Reject green with failures/classes, nonzero failure/skipped counts, or
pass != declared. Exits 0/1/3/4/5/6 must account for the bound full or selected
declared scope. Exit 4 requires zero passes/failure counts, skipped == declared,
and absence of lock, failures, classes, metrics and pins. Legacy exit-4 replies
that claimed passes without execution are refused by this corrected profile.
Do not infer executed checks from non-green preflight counts. Exit-2 diagnostic
counts may describe infrastructure rather than the bound suite.

Extract the complete sorted unmet set from all failure entries of class unmet,
never from the capped pin summary. Summary IDs must be unique/sorted, at most
three, and agree with the corresponding complete count where knowable. An
unmet-only reply must account for its complete unmet set in counts and pins.
Pin summary IDs must belong to the bound probe scope; evaluated cannot exceed
that scope's probe count. This does not identify passing pins omitted by the
three-ID bound.
Failure keys may be infrastructure names on preflight exits, not only probes.

Successful interpretation exits 0 and writes one JSON object plus LF with:

- `v: 1`, `disposition`: proceed (0), revert (1/5), build (6), escalate (2/3/4);
- `vise_exit`, `verdict`, `next_action`, sorted `classes`, full `unmet_ids`;
- `checks_skipped` (total reported skipped count, including probes in preflight);
- `metrics_skipped` (count, or null when a full exit 2 cannot attribute skips to
  metrics; zero for a subset, all bound metrics on exit 4, and the reported
  metric-only skip count on completed replay), `metrics_checked` (true for a full exit 0 or 5 with
  zero skipped metrics; exit 5 evaluated metrics and found a regression);
- `passing_unaccepted_ids` (bounded summary), `passing_unaccepted_count`,
  `operator_acceptance_required` (whether that count is nonzero);
- `binding` containing only the defined binding fields, not arbitrary extras.

Exit 0 here means **interpreted**, not **Vise passed**. Even an interpreted red
returns a decision object; callers branch on `disposition`. A malformed,
interrupted, oversized, incompatible or stale attempt exits 2, writes no stdout
decision, and gives a bounded diagnostic on stderr. Never manufacture a Vise
exit/count/verdict or reuse an earlier result after a failed attempt. No retries.

## Proper-subset progress

EXPECTED for `progress` additionally requires `previous_binding` and
`lineage: {parent_candidate, child_candidate}` matching the old/new candidate
hashes. Validate each capture against its own expected binding and the same
producer allowlist/bounds. This explicit lineage is an orchestrator assertion,
not something the Vise response proves.

Both commands must be full `gate`, both exits must be 6, both classes must be
exactly `["unmet"]`. Require the same root, argv, scope, manifest, lock,
evaluator, producer and environment; different request IDs and candidate hashes
are required. Artifact identities may differ with the candidate. Both captures
must have complete accounting: failures exactly equal the unmet set; unmet IDs
are within scope; counts.declared equals probes+metrics; counts.skipped equals
metrics; counts.pass equals probes minus unmet; pin summary/counts agree.

Progress is allowed only if the current complete unmet set is a strict subset
of the previous one. A swapped ID, equal count/set, newly regressed passing pin,
subset invocation or identity change cannot grant progress. Malformed/stale
captures use exit 2 with no decision. Valid captures that do not qualify return
exit 0 and exactly `{v:1, progress_allowed:false, metrics_checked:false}`.
A qualifying pair returns the same shape with progress_allowed true.

## Required executable matrix

Run both implementations independently and compare normalized JSON to an
independently authored expected result, not only to one another. Cover all
exits/actions, green passing-unaccepted, more than three unmet IDs and skipped
metrics; valid additive fields; invalid framing/duplicates/types/versions/enums;
exit mismatch; signal/timeout/collection failure; oversize; and stale request,
root/candidate/manifest/lock/evaluator/environment/scope/artifact bindings.
Include positive real-process captures with before/after identity collection
and an actual between-snapshot mutation refusal; also the strict-subset/equal/swapped/
regressed/subset-scope/changed-lock progress pairs. Label injected replies and
synthetic identities distinctly from real Vise outputs and measured identities.
Retain actual driver process statuses, stdout/stderr, bounds, platform, driver
hashes, fixture identities and failures. macOS evidence is not Linux/Windows
validation. Core producer and root evaluator inputs remain unchanged.
