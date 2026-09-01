# Metrics

- What exists: numeric baselines, version capture, delta reporting, directional policy, and `no-regress` enforcement.
- User route: declare `[[metric]]`, record its baseline, then inspect `verify`, `gate`, or `status` output.
- Harness route: `scripts/verify verify metrics`.
- What usually lies: nonnumeric analyzer output, tool-version drift, unstable metric values, and quality regressions reported as behavior changes.
