# B06 finite release-critical invariant contract

This is the rule-level refinement of the fourteen parent groups P01–P14 in
the 2026-09-12 hardening inventory, not a claim that every possible path is
covered. It is written before the B06 test campaign. The rules derive from
SPEC.md, PROTOCOL.md, ROADMAP.md and the pin contract. No change to the root
judge, installed binary, acceptance authority or comparison strictness follows
from this document. Existing tests listed below are starting points to inspect,
not proof of coverage merely because they exist.

## Completion and evidence

Every numbered rule requires a persistent executable positive/negative check,
a fresh passing run against the selected candidate, and at least one targeted
control demonstrating that its check detects the defect it guards. A control
can be a compilable source mutation or a separately labelled malformed/faulted
fixture. Controls must isolate the rule: a compile error, an unrelated failed
gate, or an unchanged mutation earns no credit. Preserve source identity,
mutation diff, actual command/exit/output, exact restoration and final full
gate. Do not weaken a verifier while correcting production code.

States are uncovered → exercised → hardened. Mark hardened only with a
resolving check/control pair; finite subcases do not harden an entire parent
group. The campaign evidence records rule IDs, cases, remaining gaps, platforms
and reviewer scope. Historical runs retain their original source identities.
All rules start uncovered for this B06 campaign, although earlier source-bound
evidence can be replayed and credited explicitly. A release cannot ignore an
unresolved false green, wrong repair owner or unintended acceptance found here.

## Rule matrix

Test pointers are relative to `internal/vise/` unless prefixed `../cli/`.

| ID | Parent | Required rule and finite boundaries | Starting checks / gap |
| --- | --- | --- | --- |
| C01 | P01 | Valid stdout/stderr/artifact/exit-only pins reload; omitted streams stay strictly empty; malformed/empty declarations refuse. | `pin_test.go`, `exit_pin_test.go`, `schema_test.go` |
| C02 | P02 | Missing, symlinked, oversized, overlapping and nonregular spec paths refuse before execution; valid boundary-size specs remain usable. | `pin_test.go`, `paths_test.go`; include nonexecution witnesses |
| C03 | P03 | Unaccepted stable mismatch/127/live timeout/signal/missing artifact mean build; a met gate never accepts; later regression is still unmet until explicit acceptance. | `pin_test.go`, producer pin-lifecycle; add lock/acceptance snapshots around verify and gate |
| C04 | P04 | Clean met pins accept only through recording; dirty met source or dependency bytes never acquire acceptance, even with allow-dirty; unchanged accepted identity retains provenance. | `pin_test.go`; dirty declared dependency control missing |
| C05 | P05 | Accepted mismatches and execution/artifact failures retain acceptance and refuse baseline replacement; compare lock, blobs and journal before/after refusal. | `pin_test.go`; strengthen generation snapshots |
| C06 | P06 | Spec/dependency overlap uses protected-spec repair authority; ordinary dependency drift retains probe repair; no candidate runs after preflight refusal. | `spec_dep_test.go`, diagnostic tests |
| C07 | P03/P07 | Flakes compare execution conditions and complete artifact sets; stable timed-out streams are not treated as ordinary comparable output. | `pin_test.go`; frozen condition/artifact controls |
| C08 | P07/P13 | Two flakes exhaust the shared full-suite budget; unmet/transparent events do not reset it; full/subset histories preserve their documented scopes and identity boundaries. | `state_test.go`, `pin_test.go`, `status_precedence_test.go` |
| C09 | P08 | Evaluator/Git/tracked/stray writes and invalid artifact ownership remain hard even beside tolerated unaccepted conditions. | `guard_test.go`, `gitattack_test.go`, `tamper_test.go`, producer hard-over-tolerated |
| C10 | P09 | Harness/flake/behavior/unmet/metric precedence agrees with exit, verdict and next.action; behavior outranks unmet. | `state_test.go`, `pin_test.go`; mixed-class controls |
| C11 | P09/P10 | Skipped metrics are not passes; preflight reports cannot imply probes executed; counts, failure sets and scope are truthful for full/subset/empty-metric routes. | `pin_test.go`, CLI tests; missing-baseline pass-count anomaly requires disposition |
| C12 | P10 | Status reports historical acceptance, not current implementation success; bounded summaries and doctor spec provenance retain their documented meaning. | `status_test.go`, `pin_test.go`, `doctor_test.go` |
| C13 | P11 | Preview writes no generation/journal; accepting an obsolete candidate digest refuses without changing accepted state; current digest succeeds; repeat preserves unchanged state. | `record_test.go`, `../cli/app_test.go`; stale-digest control missing |
| C14 | P04/P06/P11 | Acceptance identity includes command, timeout, env, artifact declarations, expected exit/stream/file mapping, dependency and spec bytes; changed identity cannot silently carry old acceptance. | `pinIdentityEqual`, `RunHash`, `pin_test.go`; enumerate fields separately |
| C15 | P12 | Actual CLI record/record and record/gate writers serialize; a waiting process sees a complete generation; readers remain coherent. | `concurrency_test.go`, `state_test.go`; real CLI writer-pair evidence missing |
| C16 | P12 | Interrupted/failed persistence exposes an old or new complete lock generation, never references unavailable blobs; recovery retains usable state and honest journal status. | `fs_test.go`, `../cli/signal_test.go`; process-level persistence interruption remains distinct from injected I/O errors |
| C17 | P13 | Unknown commands/selectors/arguments produce invocation repair before baseline handling; known selectors remain usable and report only selected pins. | `../cli/unknown_probe_test.go`, `pin_test.go` |
| C18 | P14 | Normal CLI operations in independent fixtures cannot replace each other's evaluator state; declared paths cannot escape into protected locations. | `paths_test.go`, `config_test.go`; not arbitrary shell containment |
| C19 | P01/P08 | Capture compares full bytes/hashes at 256KiB−1, exactly 256KiB and +1; binary/split-UTF8 prefixes retain encoding/size/truncation identity; post-prefix differences are detected. | `capture_test.go`, `boundary_test.go`, producer raw-captures |
| C20 | P08 | Exited-parent pipe holders on either stream and output-copy failures remain hard beside exit127; live-parent timeout remains distinct; children terminate. | `pipe_holder_test.go`, `runner_test.go` |
| C21 | P09 | A failed or short CLI result write cannot return successful delivery; JSON encoding failure yields an honest non-success result, not an unparseable substitute green. | `../cli/app.go` output writer; ignored write errors and fallback counts require correction/control |
| C22 | P02/P03/P08 | Each declared artifact is compared; absence is tolerated only for unaccepted pins; directories/symlinks/tracked files refuse without deleting user-owned data. | `artifact_test.go`, `runner_test.go`, `verify_test.go` |
| C23 | P03/P08 | Execution diagnostics preserve observed exit and useful causes without asserting unsupported installation facts from ordinary application text. | exit127 tests; reproduced application “record not found” attribution needs separate correction |
| C24 | P02/P06 | Invalid lock selection is deterministic across multi-item maps and phase precedence; dependency diagnostics preserve OS causes and declared paths without checkout-root dependence. | `diagnostic_order_test.go`, dependency diagnostic tests; B02 source-bound controls |

## Campaign order and boundaries

### C21 delivery contract

Detected result-stream errors (including short writes without an error) take
precedence over the intended command exit: return harness exit 2, whether the
intended result was success, a judgment refusal or a raw probe exit. This applies
to JSON and human output, including raw stdout/stderr mirrors. Do not retry a
partially delivered reply or append a replacement JSON object. A best-effort
stderr diagnostic may identify the transport failure; no body is guaranteed on
a broken destination. A real broken pipe may instead terminate the executable
with SIGPIPE, which is also unusable delivery, never success.

Human `record --i-reviewed-the-diff` must abort before persistence if its
pre-overwrite diff cannot be written. Failure delivering the final receipt
does not roll back an already completed operation; callers must inspect state
before considering another write. A successful writer return is not proof of
receiver consumption or an external buffer flush.

Encoding failures return a valid single JSON harness diagnostic when the
destination works. Its counts describe one failed `encoding` diagnostic, not
executed probes, and it carries no candidate, lock or successful judgment from
the unencodable result. Use JSON escaping even for arbitrary error text. Keep
the existing fix_probe action; callers must not treat this diagnostic as a
usable gate result. Test successful delivery alongside error, short, partial
and full-length-with-error writes, nonzero raw exits, valid fallback framing,
pre-overwrite refusal and real process broken-pipe behavior.

1. Acceptance/provenance C03–C05/C13–C14: add missing persistent cases first,
   then targeted source controls and independent black-box replay.
2. Judgment/ownership C01–C02/C06–C12/C17–C18/C22/C24: replay existing precise
   guards, add missing case boundaries, and credit each rule separately.
3. Execution/state C15–C16/C19–C21/C23: deterministic synchronization and
   minimized error injection precede any process/timing claim.
4. Run constructive and adversarial independent customer reviews on a copied,
   identity-checked subject in separate scratch repositories. Give reviewers
   the public contract, not maker reasoning or implementation changes.
5. Close the matrix only after replaying the final source and reviewing every
   gap; retain any unsupported platform/host claims explicitly.

The initial review roster uses independent Codex contexts under existing
allowance. A second model family through OpenCode is subject to current
zero-cost metadata and a successful bounded liveness check; executable presence
alone is not availability. If unavailable, record homogeneous-family review as
a downgrade, not multi-family evidence. No automatic paid fallback is allowed.
All fixture writes and record commands occur in disposable repositories.
No release, merge, purchase, provider provisioning or installed-judge replacement
is authorized by this campaign. macOS results cannot establish Linux/Windows
support, and local tamper detection cannot establish B07/B12 host authority.
