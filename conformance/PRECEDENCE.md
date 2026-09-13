# Finite CLI ownership and precedence matrix

Run this explicit, provider-free harness with Python 3.11+ on a POSIX host:

```sh
python3 -B conformance/precedence.py \
  --binary /absolute/path/to/tested/vise \
  --case all \
  --evidence-dir /absolute/path/to/new-precedence-evidence
```

The evidence directory must be absent or empty. Fixtures, command results,
process statuses, assertions, and failures remain there for inspection.
`--case` selects one of thirteen cases; `--help` lists their names.
The harness requires Git and a POSIX shell, uses only Python's standard
library, and never defaults to the installed Vise on PATH. It does not run
automatically in the producer or consumer matrix.

| Cases | Finite observation |
| --- | --- |
| `pure-exit127`, `met-pin` | Unmet construction and passing-but-unaccepted controls |
| `same-pin-hard`, `same-pin-git`, `same-pin-stray`, `same-pin-invalid` | One unaccepted pin exits 127 and also damages evaluator/Git state, writes a stray file, or creates a symlink artifact; the harness requires `harness` and `fix_probe` |
| `same-pin-tracked` | A tracked declared artifact causes operator-owned preflight refusal without deleting its bytes or writing a baseline; this row does not execute the pin |
| `harness-flake`, `flake-behavior`, `flake-metric` | Both named classes coexist; the higher-precedence exit and action win |
| `behavior-unmet-metric-held`, `unmet-metric-held` | Required failure classes appear and the metric command remains unexecuted |
| `metric-only` | The metric executes and reports a regression with exit 5 |

The pure, met, and executed same-pin cases check the lock's null acceptance
and unchanged hashes of the lock, specification, and declared dependency.
Mixed-class cases check exact class sets and selected counts. Execution
witness files distinguish evaluated metrics from skipped ones. These checks
do not certify atomic snapshots of every file in a generation.

Git setup removes inherited `GIT_*` variables, fixes identity and dates,
disables system/global configuration, prompts and hooks, and chooses `main`.
Results retain version output, JSON/process-exit agreement, and the binary's
starting and ending hash. Each subprocess has a 15-second bound; this is not
containment against hostile detached descendants. Run only a trusted candidate
binary in disposable fixtures, never production or customer repositories.

This black-box harness was authored from the public contract without Go source
access. Historical compact versions and their six source-specific defect
controls are retained in the B06 evidence records. An early predecessor used
separate probes where it claimed same-pin damage, and a single retry where it
claimed mixed classes; those rows were not credited as precedence evidence.

The finite matrix does not cover environment/fingerprint drift, corrupt
operator-owned baselines, all artifact shapes, all metric policies/directions,
every metric-held combination, or other platforms. Consult the invariant
matrix and evidence records for complementary checks; this harness alone does
not close the entire C09/C10 contract or establish release readiness.
