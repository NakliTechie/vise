# Vise producer conformance kit

The separate [CLI concurrency harness](CONCURRENCY.md) checks actual
record/record and record/gate pairs, plus readers during a held writer.
Run it explicitly; it is not included in the producer/consumer matrix below.

`producer.py` is a bounded, provider-free executable check for a specifically
selected Vise producer. It creates disposable Git repositories outside this
checkout, invokes the selected executable as a real subprocess, and retains
the complete observations and assertions as JSON evidence.

The subject is always explicit:

```sh
python3 conformance/producer.py \
  --binary /absolute/path/to/vise \
  --evidence-dir /absolute/path/to/evidence
```

`--binary` is required. A relative path is resolved from the caller's current
directory, not from a fixture, and must name an executable regular file. The
kit never defaults to the `vise` on `PATH`, never invokes `record` in its source
checkout, and never modifies an installed judge. `--evidence-dir` must not
already exist; this prevents a run from overwriting prior evidence.

## Acceptance contract

A conforming run exits 0 only when every required assertion passes. It writes
one `evidence.json` containing:

- schema and kit versions, UTC start/end times, platform and Python identity;
- the requested and resolved subject path, SHA-256, and the subject's parsed
  `version --json` reply;
- for every actual invocation: fixture and case names, exact argv, cwd,
  timeout, explicit child environment, process exit, raw stdout/stderr, parsed JSON object, duration, and
  assertion results;
- helper-authored fixture setup writes (path, size and digest) and Git commits
  with fixed author/committer identity and dates; these are not a complete
  filesystem audit of Vise or Git runtime writes; and
- separately labelled synthetic assertion-layer positive and negative controls.

Every normally completed subject invocation is checked to have exactly one
UTF-8 JSON object on stdout followed by LF and no second JSON value. Its process
exit must equal the reply's integer `exit`. Required envelope fields and the
closed decision vocabulary are checked before case-specific assertions.
Stderr is retained separately and is never concatenated with stdout.

The fixtures cover:

| Fixture | Required production observation |
| --- | --- |
| `public-routes` | global and command help, version/init/status/doctor reply shapes, repeated init, status/run error-outcome routes, raw-run streams and an empty files map |
| `raw-captures` | short binary stderr and stdout whose retained prefix splits a UTF-8 character at 262144 bytes; exact retained bytes, full-stream hash and truncation metadata |
| `missing-baseline` | gate exit 4, `record_first`, zero passes and all requested checks skipped; command/baseline/journal noncreation witnesses |
| `missing-baseline-multi` | two probes plus one metric; full gate declares/skips three, subset gate and verify declare/skip one; no commands execute |
| `unknown-selector` | gate exit 2, `fix_invocation`, `usage: true`, with a known selector control |
| `green-behavior` | green/proceed, then stable behavior divergence exit 1/revert |
| `hard-harness` | ordinary candidate-created untracked file produces exit 2/fix_probe |
| `operator-spec-drift` | edited pin spec produces exit 2/human and does not execute the candidate |
| `flake-budget` | ignored deterministic toggle produces exit 3/quarantine_ack twice; the third full rerun is refused as exit 2/human |
| `metric-regression` | unchanged behavior plus enforced metric regression produces exit 5/revert |
| `pin-lifecycle` | unaccepted unmet exit 6/build, dirty preview reports passing-unaccepted without acceptance, met-but-unaccepted exit 0/proceed, explicit preview/digest acceptance, then accepted stable regression exit 1/revert |
| `four-pins` | four complete unmet failure IDs while the bounded pin summary contains three; after all pass, four passing-unaccepted IDs are summarized as three; metrics are declared but skipped while pins are unmet |
| `strict-streams` | omitted expected streams are strict empty bytes and an exit-only pin is valid |
| `hard-over-tolerated` | a hard harness failure outranks a launch/exit-127 condition that is otherwise tolerated as unmet for an unaccepted pin |

The lifecycle fixture deliberately observes the current transaction anomaly:
initial `record` can exit 0 while the newly recorded pin remains unmet. This is
not normalized into a gate success. Missing-baseline replies must satisfy the
corrected C11 nonexecution counts; the historical false-pass anomaly is rejected.

Synthetic negative controls never alter producer output or fixture state. They
copy a known-good reply in memory, then independently change the reply exit,
change the process exit, append a second JSON frame, truncate JSON, or substitute
an unknown action; omit/type-corrupt required fields; introduce duplicate or
non-finite data; fabricate green-with-failures; or mismatch build/revert actions.
The kit's assertion layer must reject each copy.
They are stored under `synthetic_negative_controls`, not among actual runs.
Positive controls under `synthetic_positive_controls` require acceptance of
valid route variants and unknown additive fields, including nested descriptive
fields in counts, pin summaries and status sections. Closed decision enums
remain strict. Malformed capture encodings, sizes, hashes, nested failure
objects and required field types must be rejected. These controls test the
fixture assertion layer; they are not independent B05 reference consumers.

The validator checks required outcome/report fields and the known optional
fields used by this finite matrix. It does not exhaustively validate historical
journal entry contents or every semantic relationship of arbitrary replies.
For truncated streams, generic validation can check the prefix and hash format,
not recompute a full-stream hash from missing bytes; the raw-capture fixture
additionally checks its full hash against independently known generated bytes.
The executable hash is checked again at completion to detect replacement
during the run; this is local evidence, not a signed provenance guarantee.

## Bounds and interpretation

Each child has a 10-second wall-clock limit. Responses larger than 1 MiB per
stream are rejected after collection; the temporary capture files themselves
can grow until the child ends or is terminated. The fixture count is finite;
there is no retry-until-green loop. A normal run is intended to finish within two minutes. A timeout,
collection error, invalid transport, unexpected result, or failed negative
control makes the kit exit 1 while preserving evidence when possible.

Fixtures use only Python's standard library, POSIX shell commands declared in
their manifests, Git, and the selected Vise executable. Seeds, commit identities,
commit timestamps, locale, and timezone are fixed. The evidence says what ran
on the recorded platform; this kit makes no portability claim for platforms on
which it was not executed. It is not a containment boundary and its fixtures
must not run untrusted binaries.

## Independent reference consumers (B05)

The [consumer contract](CONSUMERS.md) defines two separate implementations:
`consumer.py` uses Python's standard library; `consumer-shell.sh` uses POSIX
shell, jq and iconv. Neither imports the producer kit or the other's validator.
Both interpret a captured gate/verify attempt against a separate expected
request and compatibility policy:

```sh
python3 conformance/consumer.py consume CAPTURE.json EXPECTED.json
sh conformance/consumer-shell.sh consume CAPTURE.json EXPECTED.json
python3 conformance/consumer.py progress PREVIOUS.json CURRENT.json EXPECTED.json
sh conformance/consumer-shell.sh progress PREVIOUS.json CURRENT.json EXPECTED.json
```

Driver exit 0 means the reply was interpreted, including a red Vise result.
Use the normalized `disposition` field to branch. Invalid, interrupted,
incompatible or stale evidence exits 2 without a stdout decision. Passing but
unaccepted pins remain visible; metric regression still reports that metrics
were evaluated. `progress` checks full unmet ID sets and common evaluator
identity before reporting a proper-subset construction step.

Run the independent matrix with Python 3.11+ (the launcher parses its live
fixture manifest with standard-library tomllib):

```sh
python3 -B conformance/consumer_matrix.py \
  --binary /absolute/path/to/tested/vise \
  --evidence-dir /absolute/path/to/new-consumer-evidence
```

The matrix first runs the producer kit. It feeds real producer replies into
explicitly **synthetic consumer envelopes**, retaining their original argv
separately; these are interpretation checks, not freshness attestations.
Separate live fixtures collect actual source/artifact/environment/manifest
identities before and after a gate, obtain the evaluator tamper identity from
status, parse declared scope from the fixture manifest, and test an actual
post-gate artifact edit and SIGINT interruption. Current expected metadata is
a separate input, never copied from a result by the drivers themselves.

The launcher independently authors expected decisions, compares both driver
outputs against them, and retains each command/status/output plus driver
hashes. Synthetic malformed, additive, interrupted, identity-mismatch and
proper-subset cases are labelled. Driver invocations have a ten-second bound;
on timeout the launcher terminates their process group before continuing.
This process-group cleanup is not containment against detached hostile
descendants. Input and response bounds are part of the consumer contract.

These small reference interpreters do not enforce delivery or protect the
orchestrator's policy files. A trusted host must compute and protect current
identities, serialize delivery with verification and refuse substitutions.
Local metadata matching is not a signature or proof against an agent that can
rewrite both inputs. Those requirements remain in B07/B12; local-only Vise
does not acquire a mandatory hosted service from these examples.
