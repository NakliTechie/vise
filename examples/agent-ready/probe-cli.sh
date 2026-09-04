#!/bin/sh
# Build this checkout quietly and run it with the given arguments.
#
# Quiet on success, loud on failure: the build's stderr is discarded when it
# works and printed when it does not. A probe should judge the output it means
# to freeze, not incidental noise from the toolchain — inside an agent sandbox
# the toolchain says things it never says in a terminal.
#
# Everything stays inside the workspace:
#   -mod=vendor    no module fetch, so no network
#   GOCACHE=$PWD   no writes outside the checkout, which a sandbox denies
#   GOTOOLCHAIN    named, so nothing is downloaded to satisfy go.mod
#
# GOCACHE must be an absolute path, so it is built from $PWD here rather than
# declared in the manifest's env: vise runs every probe from the repository root.
set -eu

BIN="$VISE_TMP/app"
if ! GOCACHE="$PWD/.gocache" GOFLAGS=-mod=vendor GOTOOLCHAIN=go1.25.13 \
     go build -o "$BIN" ./cmd/app 2>"$VISE_TMP/build.err"; then
  # Print only the compiler's own diagnostics. A build failure is deterministic
  # — the code either compiles or it does not — but the toolchain's other
  # chatter is not: sandbox permission warnings, cache notices, and download
  # lines vary between callers and between runs. Letting those into the
  # observation makes two passes of the same broken code disagree, and vise
  # reports that as a flake. The agent contract says stop and report on a
  # flake, so a plain compile error would end the session instead of telling
  # the agent to fix the code it just wrote.
  grep -E '(^#|\.go:)' "$VISE_TMP/build.err" >&2 || cat "$VISE_TMP/build.err" >&2
  exit 1
fi
exec "$BIN" "$@"
