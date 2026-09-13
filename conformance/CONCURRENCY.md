# C15 Vise CLI concurrency harness

This standard-library-only harness exercises bounded black-box serialization
between two `record` processes and between a baseline-changing `record` and
`gate`. While the first writer is explicitly held inside a witnessed probe, it
also checks that bounded `status` and `doctor` readers complete without
releasing or overlapping that probe. It never invokes `record` outside its
generated scratch fixtures.

```sh
python3 concurrency.py --binary /absolute/path/to/vise --pair both
python3 concurrency.py --binary /absolute/path/to/vise --pair record-record --evidence-dir /tmp/evidence-rr
python3 concurrency.py --binary /absolute/path/to/vise --pair record-gate --evidence-dir /tmp/evidence-rg
```

Both paths are resolved to absolute paths before fixture commands change their
working directory. `--evidence-dir` may be absent or empty; the harness refuses
a nonempty directory. Without it, a fresh system temporary directory is created. Each run
writes `result.json`, including command stdout/stderr, execution traces,
cleanup status, binary hashes, and failure details. A failing conformance check
returns nonzero but still preserves that evidence.

The harness refuses Python optimization mode (`python -O`) because its
conformance assertions must remain enabled.

The evidence is deliberately narrow: it covers these writer pairs and held-
writer readers in the exercised environment. It does not claim that status is
an atomic cross-section judgment receipt, that the cleanup mechanism contains
arbitrary hostile descendants, or that the observations generalize to every
platform.

Cleanup can be tested deliberately:

```sh
python3 concurrency.py --binary /absolute/path/to/vise --pair record-record \
  --induce-cleanup-failure --evidence-dir /tmp/evidence-cleanup
```

That command intentionally returns nonzero after a probe starts. Its evidence
must show the assertion, process snapshots, trace, and cleanup actions.
