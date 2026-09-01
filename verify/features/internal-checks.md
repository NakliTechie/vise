# Internal checks

- What exists: package tests for parsing, path safety, process control, persistence, state, JSON contracts, and end-to-end command flows.
- User route: maintainers run the full harness or `go test ./...` and `go vet ./...`.
- Harness route: `scripts/verify verify internal-checks`.
- What usually lies: an in-process test that never exercises the built binary, a passing suite without race coverage, and cached results mistaken for a fresh run.
