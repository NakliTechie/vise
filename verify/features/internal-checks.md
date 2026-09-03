# Internal checks

- What exists: POSIX shell syntax checks, optional ShellCheck lint, and package tests for parsing, path safety, process control, persistence, state, JSON contracts, and end-to-end command flows.
- User route: maintainers run the full harness or `go test ./...` and `go vet ./...`.
- Harness route: `scripts/verify verify internal-checks`.
- What usually lies: an in-process test that never exercises the built binary, a passing suite without race coverage, cached results mistaken for a fresh run, and a coverage report read as a measure of what is tested — the signal-handling tests drive a separately built binary, so the functions they cover report 0%, and three mutation audits found the reverse problem everywhere: tests that were counted as coverage and could not fail.
