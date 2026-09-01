# Self-hosted dogfood

- What exists: a real campaign where a committed vise probe records `go run ./cmd/vise --help`, gates the unchanged tree, detects a source-level help change, then returns to green after revert.
- User route: `scripts/verify verify dogfood` or the full sweep.
- Harness route: the harness clones the current committed checkout and isolates Go caches under the checkout-derived verification root.
- What usually lies: tests that call command functions without executing the built judge, probes that accidentally protect application source as evaluator input, and reverts that restore source without restoring behavior.
