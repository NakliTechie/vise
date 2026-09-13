> **Lifecycle:** locked — direction and delivery criteria accepted 2026-09-13; implementation incomplete

# Caller-neutral preservation and construction

Vise is a deterministic judge, usable by any caller. It freezes observations
for preservation and operator-authored expectations for construction. Vise owns
judgment; operators own specifications and acceptance; customers own
orchestration; hosts enforce authority. OpenCode is one customer, not an
architectural dependency. No model call is needed to judge a candidate.

This is the approved evolution of [SPEC.md](SPEC.md), not documentation claiming
that future features already exist. All fifteen batches below are in scope.
Deliver the exact-byte core first, then extensions as bounded, independently
tested slices. A research spike cannot substitute for an approved implementation.

## Locked architectural boundaries

- The local CLI remains independently useful without an account, daemon,
  network connection or external verifier. Optional trusted verification and
  hosted receipt services must not become mandatory core dependencies.
- Portable advisory integration examples and one named, independently tested
  hard-delivery integration are both required. A host's enforcement claim must
  identify its exact configuration and cover direct edits, subprocess writes,
  stale evidence and legitimate gate/journal/scratch operations. It must permit
  sanctioned proper-subset unmet-progress commits, not only fully green work.
- Authoring delivers exact-byte templates and reviewed capture before
  constrained shapes. Shape syntax is bounded, deterministic and operator-owned;
  acceptance still freezes concrete bytes. Missing constraints cannot silently
  turn strict empty streams or undeclared artifacts into wildcards.
- Independent operator/CI approval precedes optional signed acceptance. Trusted
  evaluator, policy and signer authority live outside the agent-writable
  checkout. Candidate execution receives no acceptance secrets or write tokens.
  Evidence binds source/artifact, judge, baseline, inputs, environment and the
  evaluated set; stale, substituted and replayed evidence must fail closed.
- Held-out expectation details stay operator-only. Agents receive bounded
  feedback; query accounting, retention and rotation are independently enforced.
  Evaluation must distinguish visible, held-out and not-run results, and must
  not claim that a candidate cannot detect it is being evaluated.
- Validate macOS/Linux first, Windows/non-POSIX afterward. Support claims require
  actual execution on the named platform, not merely cross-compilation.
- Stronger containment is an optional, explicitly capability-checked boundary;
  the current `declared-off` convention is not network enforcement. Structural
  analysis is distinct from behavioral judgment, not an equivalence proof.

## B02 diagnostic contract (first implementation slice)

These are intended diagnostic corrections, not permission to weaken a probe or
silence an OS error. They precede the remaining broad mutation proofs in B01.

1. Lock validation keeps its existing phase precedence: JSON structure/version,
   hash validation, then schema validation. Within each hash/schema phase,
   examine probe IDs in lexical order before metric IDs in lexical order.
   Within a probe, preserve existing field/category precedence and examine
   dependency, artifact and spec map keys in lexical order. Return the first
   error as before; repairing it exposes the next invalid entry. Valid locks
   remain valid, and invalid entries never become ignored entries.
2. Dependency filesystem diagnostics identify the declared repository-relative
   path, failed operation and useful OS cause without the checkout's absolute
   root. Equivalent failures in different checkout locations on the same
   platform must produce identical diagnostics. Preserve the underlying error
   chain for programmatic cause inspection. This is not a promise of identical
   OS wording across platforms and not a global path-redaction policy.
3. Repair authority is unchanged. A file also serving as a pin's spec is checked
   in its protected spec role before its dependency role. Edited, missing and
   symlinked specs route to the operator; ordinary dependency-only drift retains
   the existing probe-repair route. `status` remains a report (exit 0 for a valid
   call), while `gate`/`verify` judge these failures as harness (exit 2).
4. Required regression evidence includes multi-invalid probe/metric and nested
   map cases, next-error exposure after repair, two distinct checkout roots,
   missing leaf/parent and non-directory failures, preserved OS causes, and
   status/verify/gate repair routing with no candidate execution on preflight
   refusal. Good files, symlink refusal and existing role controls still hold.

The broader stderr `not found` attribution concern is a reproduction task,
not a confirmed defect or an authorization to guess missing-command identity.

## Delivery batches and proof of completion

| Batch | Deliverable and required evidence |
| --- | --- |
| B02 | Stable diagnostic contract above, regressions that fail on the prior code, broad verification and unchanged unrelated observations. |
| B01 | Five stable selected mutation proofs: exit-only pin, unknown selector, spec/dependency authority, pipe holder and exit 127. Retain mutation identities, raw verdicts, exact restoration and a full green gate; fresh-clone and uncached checks must match the final candidate. |
| B03 | Executable CLI/JSON protocol covering commands, invocation errors, all exits/actions, optional fields, pin lifecycle, compatibility, freshness, cancellation and ownership; no vendor event model in the core. |
| B04 | Independent executable producer-conformance fixtures, including all verdict/action branches, pin acceptance transitions, multiple results, skipped metrics and negative controls. |
| B05 | Consumer fault fixtures for malformed/truncated/unsupported/mismatched/stale replies, plus shell/CI and non-Go reference drivers with equivalent decisions and bounded retries. |
| B06 | Finite critical-invariant matrix and independent review, including OpenCode and Codex/Sol when authorized routes are available; record actual coverage and reviewer limitations. |
| B07 | Portable advisory examples and the named hard-delivery integration with independent refusal, legitimate-operation and stale-delivery tests. |
| B08 | OpenCode customer integration: build/unmet rendering, parser/readiness, duplicate events, cancellation, freshness and notifications, tested against its actual current surface. |
| B09 | Three construction pilots spanning at least two languages, varied inputs and planted wrong implementations; plain CLI cycle, operator acceptance, exact evidence and measured outcomes. |
| B10 | Reconciled documentation and contracts, exact release-artifact install/replay/rollback and platform evidence, limitations and draft collateral. Publishing remains separately authorized. |
| B11 | Templates and reviewed capture, then constrained shapes with strict identity, resource bounds and deterministic concrete-byte acceptance; pilot and planted-defect comparisons. |
| B12 | Trusted external verification, then optional signing with independent policy, provenance, rotation/revocation/recovery and replay/substitution negative controls. |
| B13 | Operator-held challenges with private detailed results, bounded agent feedback and external query accounting; overfit controls and leakage limitations. |
| B14 | Separately contracted HTTP/browser/service lifecycle; property/metamorphic probes; curated mutation tooling; protected metric ratchets; non-chimeric partial recording; stronger process/filesystem/network containment; Windows/non-POSIX; structural map/AST evidence; optional hosted receipts/dashboard. Each slice requires a representative workload, implementation and acceptance tests. |
| B15 | Bounded CLI polish, deterministic ecosystem initialization, ntkit guidance and verified Zed/Amp examples; validate actual host syntax and capabilities before claiming support. |

Dependency order: specification fold → B02 → B01 → B03 → B04 → B05 →
B06/B07 → B09 → B10. B08 follows the consumer contract on its own customer
track. B11–B15 follow the core phase; they are full-goal requirements, not all
prerequisites for the first exact-byte release. Detailed designs must preserve
these decisions; genuinely new product choices require operator direction.

## Release and execution boundaries

Release evidence is finite: the critical invariant matrix, five mutation proofs,
three construction pilots and exact-artifact replay. Unresolved false greens,
wrong repair authority or unintended acceptance block release. A green gate is
evidence only for declared observations; uncovered behavior must be named.

Work happens in isolated goal branches, with sanctioned green-step commits and
pushes. Relevant specifications may be updated for these approved decisions.
Intended evaluator-input/baseline transitions require documented diff review
and preview-and-exact-digest acceptance; unrelated regressions are never folded
into a new baseline. Mutation-only flakes may be diagnosed autonomously, but
mutations must be restored and a clean full gate obtained before progress.
Unexplained failures on clean code stop implementation.

The installed judge remains fixed unless separately authorized. Merge, release
publication and spending require explicit approval. Reuse existing hardware and
included allowances; paid capacity is not implicitly authorized by this roadmap.
