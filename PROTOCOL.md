> **Lifecycle:** locked — caller contract for the current exact-byte implementation; roadmap extensions are not capabilities

# Vise executable protocol

This is the authoritative customer-facing CLI/JSON reference. It applies to
the pin-capable source on the caller-neutral roadmap branch, not every binary
printing `0.3.0`. [SPEC.md](SPEC.md) describes the evaluator and file formats;
[ROADMAP.md](ROADMAP.md) describes approved future delivery. Where older CLI
prose differs, this reference governs. OpenCode, a shell script, an IDE and a
CI worker are peers: none defines Vise's semantics.

Vise judges declared observations, not general correctness. Operators own
expectations and acceptance, customers orchestrate work, and hosts enforce
permissions. JSON is not authenticated evidence. No account or model call is
required by this local protocol.

## 1. Invocation and effects

Invoke an explicitly selected executable with an argument vector and a known
working directory. Except help/version, commands resolve the enclosing Git
work-tree root and use its `vise.toml`; there is no `--cwd`, alternate manifest,
stdin request, daemon, streaming event protocol or capability endpoint.
Probe commands execute from that root, including when Vise starts in a subdirectory.

| Invocation (append `--json`) | Meaning and effects |
| --- | --- |
| `help`, `--help`, `-h`, or no arguments | Global help; no repository required. `COMMAND --help`/`-h` gives command help. |
| `version` or `--version` | Build identity; no repository required; accepts no other arguments. |
| `init` | Operator setup: creates missing starter `vise.toml` and `AGENTS.md`, adds local-state ignores to `.gitignore`; does not overwrite an existing manifest/agent contract or record a baseline. |
| `status` | Readiness report: manifest, baseline, live fingerprint, proposals and recent journal. No behavior probes or metrics; may execute declared fingerprint commands and create/clean scratch. |
| `doctor` | Advisory static readiness report. Reads files and invokes Git inspection, but executes no manifest commands and writes no Vise state. |
| `run ID` | Execute one probe without baseline comparison. Deletes/regenerates its declared artifacts, uses scratch and enforces runner checks; does not record or journal a verdict. |
| `verify [--probe ID]` | Judge all probes and eligible metrics, or exactly one probe without metrics. May replay a mismatching observation; writes eligible judgment events to the local journal. |
| `gate [--probe ID] [--quiet]` | Same judgment and journal semantics as verify. Human rendering is shorter; `--quiet` affects human output only. |
| `record [--allow-dirty] [--i-reviewed-the-diff] [--preview \| --accept DIGEST]` | Operator-only two-pass baseline transaction. Runs probes, metrics and fingerprint commands. Successful write stores blobs, atomically replaces the lock, prunes obsolete blobs and appends the journal. |

`record --preview` runs the candidate but writes no baseline/blobs/journal;
artifacts and scratch still change. Its candidate describes a *possible*
acceptance, not an acceptance already performed. `--accept DIGEST` reruns both
passes and writes only the reviewed candidate digest. Replacing a baseline
requires that digest or the human-shaped `--i-reviewed-the-diff` gesture.
Neither flag authenticates an operator. `--allow-dirty` records dirty provenance
but cannot newly accept a pin. A failure after baseline replacement can leave
the new baseline installed even if journal append failed; inspect state before
retrying any write. Never interpret a nonzero transaction exit as rollback.

`init`, `record`, `verify`, `gate` and `run` acquire `.vise/run.lock`, creating
local state as needed, and wait for other writers. Waiting notices can appear
on stderr in JSON mode. `status`/`doctor` take no state lock. Status can observe
concurrent activity; its sections are not an atomic receipt of a judgment.
Invocation validation is not uniformly before root resolution/locking: unknown
commands and status/doctor arity are checked early; other flags are parsed
later. A malformed call can therefore create lock/scratch infrastructure or
report a repository error before a flag error. It does not authorize code edits.

### Argument parsing: supported spelling and observed edges

Use the spellings above, one occurrence per option, flags before positional
arguments, and `--name=value` when a value could look like an option.

- Every exact `--json` token is removed before other parsing, even after `--`
  or in a value position. `--json=false` is not a global JSON switch.
- Global help ignores trailing words. For known commands other than version,
  any `--help`/`-h` in the remaining arguments answers help before execution.
  Unknown commands with help still produce an invocation error.
- Record/verify/gate use Go-style flags: one or two leading hyphens, `=value`
  or a separate string value, booleans with optional `=true`/`=false`.
  Parsing stops at the first positional argument or `--`; leftover positional
  arguments are rejected. Duplicate options currently use the last value.
- `--probe=` currently means the full suite, not an empty selection.
  Unknown nonempty selectors are invocation errors after manifest loading and
  before checking for a baseline. `verify --quiet` is refused, while the
  parser currently accepts `verify --quiet=false`.

These edges describe compatibility with existing executables, not recommended
customer syntax or a promise that a future strict invocation profile keeps them.
Do not discover capabilities by sending ambiguous flags to a real repository.

### Child environment and containment

Manifest commands run through POSIX `sh -c`, with non-terminal streams and no
interactive stdin. Vise supplies caller `PATH` and `HOME`, stub `TZ`, `LANG` and
`LC_ALL`, `VISE_SEED`, `SOURCE_DATE_EPOCH=0`, `VISE=1`, `PYTHONHASHSEED=0`,
`NO_COLOR=1`, `TERM=dumb`, `COLUMNS=80`, `CI=1`, and per-command `VISE_TMP` and
`TMPDIR` under `.vise/tmp/`. Declared command environment entries add variables;
manifest validation forbids overriding those reserved defaults (and also
`LANGUAGE` and `TZDIR`). The shell may add its own variables. The Vise/Git process itself is
not subject to this child-environment allowlist.

Declared artifacts are deleted before each execution and hashed afterward.
Tracked artifacts are refused. Runner checks detect protected-state and
tracked/untracked-unignored checkout changes; ignored build caches are outside
ordinary checkout comparison. These are detection checks, not filesystem or
network denial. `network = "declared-off"` is a convention, not enforcement.
Processes escaping their session can outlive process-group cleanup. See SPEC's
execution limits; no Windows or hostile-code containment claim follows here.

## 2. Transport and compatibility

For a normally completed JSON invocation, stdout is one UTF-8 JSON object
followed by LF. There is no log preamble or second result. Object key order and
JSON escaping are insignificant. Stderr is a separate diagnostic channel, not
JSON; do not combine it with stdout. Human text, help prose, `detail`, `remedy`,
diffs and OS messages are for display, not machine branching.

All normal replies have integer `v: 1`, string `cmd`, integer `exit`, and
`next: {action: string, detail: string}`. Shape depends on command and route:
a usage error for `status` is a failure outcome, not a status report. Unknown
commands use `cmd: "vise"`; help uses `cmd: "help"`, optionally with `command`.
For completed replies, check that the child process exit matches `exit`.
Do not apply the gate exit table to an arbitrary command.

Treat duplicate keys, malformed/truncated JSON, extra JSON values, wrong types,
missing required fields, unsupported versions/enums, unexpected command,
exit mismatch, launch/collection failure, timeout or cancellation as a
**customer-side protocol/transport failure**. It is not a Vise verdict: do not
invent `exit: 2`, a `counts` object or a passing result on Vise's behalf.
Refuse delivery and retain the raw evidence for diagnosis. Unknown additive
object fields can be ignored, but unknown decision values must fail closed.

There are three separate identities: JSON envelope `v`, manifest/lock format
version, and executable build identity. `v: 1` and product version `0.3.0`
alone do not establish pin/exit-6 support. Customers must use a tested producer
revision/artifact compatibility record and conformance checks before enabling
construction workflows. There is currently no negotiated capability list.
An absent revision or `modified` means unknown, never a clean reproducible build.
Current manifest/lock readers reject unknown fields and unsupported format
versions; a consumer's additive-reply policy does not change file parsing.
An unsupported lock reader must be replaced through operator policy, not by
rewriting/downgrading the baseline. Reader acceptance of unknown optional
fields is not proof that it understands their semantics.

Adding optional descriptive fields is compatible with this consumer policy.
Adding actions/classes/states or changing required-field meaning requires an
explicit compatibility update and tests; never silently reinterpret an enum.
Future signed receipts, hosted evaluation and shapes are proposals under the
roadmap, not fields customers can assume this executable emits.

### Cancellation and known transport limitations

SIGINT/SIGTERM make the executable interrupt active probe process groups and
exit 130/143 respectively. No final JSON, verdict journal entry or scratch
cleanup is guaranteed on interruption; SIGKILL has no cleanup guarantee.
The caller must await process termination and reject partial or stale output.
The OS may report a signal separately from an ordinary numeric exit.

Current implementation limitations, not success contracts:

- Failure to determine cwd happens before JSON dispatch: stderr and exit 2,
  without a JSON object, even when `--json` was requested.
- Destination write errors are currently ignored by JSON rendering. A process
  exit alone does not prove that the receiver obtained a complete reply.
- An outcome-encoding failure emits an indeterminate harness reply with an
  `encoding` failure but without normal `counts`. Treat it as an unusable
  judgment, not fabricated counts. The internal serializer fallback may use
  `cmd: "internal"`. Neither fallback is gate success.
- There is no fixed maximum total response size or total wall-clock duration.
  Bound collection and waiting in the customer; exceeding its limits fails
  closed. Time spent waiting for the state lock is not a probe timeout.

## 3. Gate and verify outcomes

| Exit | Verdict | Meaning | `next.action` |
| --- | --- | --- | --- |
| 0 | `green` | Every selected declared check matched | `proceed` |
| 1 | `red` | Stable changed accepted/recorded behavior | `revert` |
| 2 | `indeterminate` | Harness failure or refused invocation | `fix_probe`, `human`, or `fix_invocation` |
| 3 | `indeterminate` | Unstable observation | `quarantine_ack` |
| 4 | `indeterminate` | No baseline to judge against | `record_first` |
| 5 | `red` | Behavior held, enforced metric regressed | `revert` |
| 6 | `red` | An unaccepted pin is not met | `build` |

All eight actions are listed above; `rerun` is not an emitted action.
Missing-baseline preflight has priority; otherwise harness > flake > behavior
> unmet > metric. All failure classes are reported, not just the winning one.
`operator: true` on a harness failure routes repair to a human; `usage: true`
routes to `fix_invocation`. Ordinary probe repair is `fix_probe`, never license
to edit protected evaluator state. Stop on an unexplained pre-existing failure
except exit 6; that is an instruction to build to the operator's spec.

Normal outcome fields in addition to the common envelope:

| Field | Shape and absence semantics |
| --- | --- |
| `verdict` | `green`, `red`, or `indeterminate`. |
| `counts` | Object of integers: `declared`, `pass`, `behavior`, `flaky`, `harness`, `metric`, `unmet`, `skipped`. All eight are present, including zeros. |
| `classes` | Optional array of `harness`, `flake`, `behavior`, `unmet`, `metric`; absent when empty. |
| `failures` | Optional map keyed by check ID or infrastructure name; absent when empty. Keys need not be probe IDs. |
| `metrics` | Optional map ID → `{base, now, delta, direction, enforce}`. Numbers are finite; direction `up`/`down`, enforce `none`/`no-regress`. Report-only worsening is not failure. |
| `lock` | Optional `sha256:` plus 64 lowercase hex digits. Evaluator tamper hash, not a candidate source/artifact hash or signature. Absent if the route has not established it. |
| `pins` | Optional evaluated-pin summary, described below; absent when no pin was evaluated. |

Each failure requires `class`; optional fields are display `detail`, `diff`,
`operator` and `usage` (only emitted when true), and `expect`/`got` objects.
An expectation/observation object may have integer `exit`, hash-valued `stdout`
and `stderr`, and a `files` map from path to hash. They are not captured text;
absent fields mean not supplied, not zero exit or empty bytes.

On completed replay, skipped metrics remain in `declared`, not in `pass`.
Full-suite metrics run only if every behavior probe/pin passes; a selected
probe invocation excludes all metrics. There is no per-passing-probe map.
Preflight outcomes do not attest execution: in particular, the current
missing-baseline result can report `pass == declared` despite running nothing.
Synthetic infrastructure failures can also distort the denominator. Never
infer that checks ran from counts alone, especially on exits 2 or 4. These are
known reporting gaps, not exemptions allowing delivery.

`pins` requires `evaluated`, `unmet`, `unmet_count`, `passing_unaccepted`, and
`passing_unaccepted_count`. Counts are integers; the ID lists are arrays with
at most three IDs each (empty lists are `[]`). Counts can
exceed list lengths. These are summaries, not a complete unmet set. Use all
`failures` entries of class `unmet` for the complete set on a completed replay;
do not silently truncate progress comparisons to the summary. Pin IDs that
pass but remain unaccepted are the deliberate exception to unnamed passes.

Mismatching probes are replayed once: consistent new observations produce
behavior/unmet, inconsistent observations produce flake. A launch failure,
timeout, signal termination or missing artifact may be a tolerated *unmet*
condition for an unaccepted pin. An evaluator write, pipe-holder or other hard
harness error still wins; pin status cannot excuse it. Accepted pins do not
receive the unaccepted-pin tolerance.

Two journaled flakes touching an evaluated set at the same commit and lock
exhaust its budget; a subsequent gate/verify is refused with exit 2 / `human`.
The two flakes may involve different checks. Narrowing to one check carries
its relevant history, not a new budget. Refusal is not a new judgment event.
Do not poll gates or retry until success. Status is suitable for inspection;
per-probe replay is diagnosis, not full-suite delivery evidence. A commit or
baseline change can create a new budget but is not permission to launder flakes.

## 4. Pin lifecycle and sanctioned progress

An operator-authored `expect` makes a probe a pin. Omitted streams expect empty
bytes; omitted exit expects 0; every declared artifact must have a spec. An
exit-only pin is valid; an empty expectation and expected exit 127 are not.
Named spec files must be valid, readable regular files within the repository,
not evaluator state or artifacts, and at most 262144 bytes each.

Acceptance and current observation are independent axes:

| Recorded acceptance | Current observation | Gate result (absent higher failures) |
| --- | --- | --- |
| Unaccepted | Spec unmet | Exit 6; build. |
| Unaccepted | Spec met | Exit 0; name passing-unaccepted; operator still has to accept. |
| Accepted | Spec met | Exit 0; historical acceptance remains. |
| Accepted | Spec diverges | Exit 1 for stable divergence, or harness/flake as applicable; not exit 6. |

Only a clean-tree operator record can newly accept a met pin. Gate never
changes acceptance. Acceptance belongs to a specific definition, dependencies
and spec hashes; changing those creates a new identity subject to review.
For unchanged identity, acceptance is historical and cannot be revoked by
re-recording a regression. An accepted failing pin must not become unaccepted
merely to permit build mode.

A green full gate sanctions a commit. While building, an unmet-only full gate
can sanction a progress commit only when, under the *same manifest and lock*,
the current full unmet ID set is a **proper subset** of the previous full set
and no previously passing pin regressed. Fewer counts alone are insufficient;
exchanging failed IDs is not progress. A per-probe gate is not a comparison
point. Metrics skipped during construction were not checked. The CLI does not
issue a commit-permission token: the customer/host must enforce this rule.

## 5. Other reply schemas

All entries below retain the common envelope and can instead return a normal
failure-outcome shape on invocation/operational errors.

- **Version:** required string `version`; optional `revision`, boolean
  `modified`, and string `built` from VCS build metadata. Missing is unknown.
- **Help:** either `commands` (map name → `{summary, usage}`) plus
  `global_options` (map flag → description), or strings `command` and `usage`.
- **Init:** `created`, an array of written path names, possibly `null` or empty
  if nothing needed writing; exit 0 / `human` asks the operator to configure.
- **Raw run:** `probe`, `files` (map artifact path → hash, `{}` when empty),
  plus both stream captures. Each capture always supplies `NAME_size` (full
  byte count), `NAME_hash` (full-stream SHA256), `NAME_truncated` (boolean),
  and exactly one of `NAME` (UTF-8 string) or `NAME_base64` (standard base64
  bytes). Only the first 262144 bytes are retained; encoding is chosen from
  the prefix's UTF-8 validity, including a split multibyte sequence. Empty
  streams are empty text with the empty-byte hash. Exit mirrors the probe,
  including nonzero and a lone launch failure 127; next is `proceed` meaning
  execution finished, **not** behavior passed. Hard runner errors use the
  harness-outcome shape instead.
- **Record:** outcome envelope plus optional `candidate` (hash of canonical
  candidate lock bytes), `review_diff` (display text), and `pins`. Record pins
  have complete sorted `accepted` and `unmet` arrays (including empty arrays),
  and optional `passing_unaccepted` array. These differ from bounded gate
  summaries. A successful freeze or preview exits 0 even with unmet pins and
  `classes: ["unmet"]`; its `verdict: "green"` is transaction success, not
  a passing gate. Preview next is `human`; freeze next is `proceed`. A record
  self-test flake can use `fix_probe`, not the gate's `quarantine_ack` mapping.
  Never use record as the customer refactor-loop judge.
- **Doctor:** required `ready` boolean and `findings` array of
  `{check, detail, remedy}`; `[]` when none. `check` identifies the advisory
  check, not a verdict class. Exit 0 for a valid report, including no Git;
  `ready` means only that no implemented advisory check found a problem.
- **Status:** described next. Valid reports exit 0, even when not ready.

Status requires `state`, `manifest`, `lock`, `pending_proposals` and `next`.
CLI reports also carry `tool: {version, revision?, modified?}`. Both `manifest`
and `lock` contain `present`, `valid`, `probes`, `metrics`; optional `error`
describes read/validation failure. Zero counts in unavailable sections are not
evidence of an empty valid baseline. Lock may additionally have:

- `fingerprint_match` boolean, absent when not compared;
- `recorded_commits` array, `hash` tamper hash, `drift` array of display lines;
- `pins: {declared, accepted, unaccepted, unaccepted_count}`, reporting stored
  acceptance only. `unaccepted` is at most three IDs, `[]` when empty. It says
  nothing about whether those pins pass in the current working tree.

Status states currently are `no-git`, `not-initialized`, `unrecorded`, `ready`,
`harness-error`, `environment-drift`, `baseline-drift`, `rerun-refused`.
Use `next.action` for the route, not exit 0. Optional `proposal_error` describes
agent-writable proposal parsing and does not change the judgment/readiness
action. `journal_unreadable: true` distinguishes an unreadable journal from an
empty one. Optional `journal` contains at most five recent events from a
256-KiB tail scan; absent means none supplied, not necessarily no historical
events. Entries have `e` and optional `at`, `commit`, `dirty`, `verdict`,
`counts`, `metrics` (ID → number), `flaky`, `probe_set`, `lock`, and `pins`
(record-pin shape). Journal data is historical and locally writable state,
not an authenticated current result or an exhaustive history API.

## 6. Bounds, freshness and delivery

Captures have a per-stream/artifact 262144-byte prefix bound; full hashes still
cover the full observations. Gate pin lists cap at three IDs. Diff windows
show the first divergence, bounded context and clipped lines. Status journal
count/tail is bounded. **The whole JSON object is not constant-sized:** failures,
metric maps, record pin lists, status drift and path/message lengths can grow
with input. Customers choose a collection limit sufficient for their declared
workload, and report an oversized response as incomplete, never as green.

Capture the requested command, exact argv/evaluated scope, root, executable
identity/artifact hash, candidate source/artifact identity, evaluator identity,
environment and process outcome alongside a reply. The gate JSON itself does
not contain a request ID, candidate commit/dirty-content hash, evaluator build
identity, full evaluated-ID list or signed freshness proof. `lock` identifies
evaluator inputs, not the candidate. `status.tool` and a nearby journal event
cannot fill these gaps by inference.

Recheck whenever source, baseline, spec/dependencies, evaluator, environment,
scope or delivery artifact changes. A green subset result cannot certify the
whole suite; an old full result cannot certify a new edit. Serialize intended
delivery with verification or use an independent trusted host that binds the
exact artifact and policy. Advisory prompts and an editable JSON file cannot
enforce that boundary. Provider callbacks, retries, queues, deduplication,
notifications and UI labels remain customer responsibilities.

## 7. Executable evidence and remaining work

Run `go test ./internal/cli -run TestProtocol -count=1` for the focused contract
checks. Existing pin, CLI, signal and runner tests cover additional evaluator
semantics. These in-process checks are separate from the
[provider-free producer conformance kit](conformance/README.md), which invokes
an explicitly selected executable in disposable repositories and retains real
process evidence plus labelled assertion-layer controls. Its finite coverage
and platform limits are documented alongside the command. B05 still requires
two independent reference consumers and their fault matrix. Do not claim a
tested host boundary or cross-platform support from this document.

The preflight counts and output-write limitations above require separate
release-critical disposition; documenting them does not repair them. The
known exit-127 stderr-attribution issue likewise remains a diagnostic finding:
prose can mistake an application message for a shell message, so customers
must never extract authority or an install command from that text.
