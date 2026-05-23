#!/usr/bin/env bash
# coverage.sh runs the Go test suite with coverage and fails if total statement
# coverage falls below a threshold. Threshold is COVERAGE_MIN (default 50).
#
# Only packages that have tests are measured (running `-cover` over a package
# with no tests trips a toolchain `covdata` lookup), so this gate tracks the
# coverage of the tested core. Add tests for a new package to bring it in.
set -euo pipefail

THRESHOLD="${COVERAGE_MIN:-50}"
profile="$(mktemp -t agentle-cov.XXXXXX)"
trap 'rm -f "$profile"' EXIT

pkgs="$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | tr '\n' ' ')"
if [ -z "${pkgs// }" ]; then
  echo "no packages with tests found" >&2
  exit 1
fi

# shellcheck disable=SC2086
go test -covermode=set -coverprofile="$profile" $pkgs

total="$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')"
echo "----------------------------------------"
printf 'total coverage: %s%% (minimum %s%%)\n' "$total" "$THRESHOLD"

if awk -v t="$total" -v min="$THRESHOLD" 'BEGIN { exit (t+0 < min+0) ? 0 : 1 }'; then
  echo "FAIL: coverage ${total}% is below the ${THRESHOLD}% threshold" >&2
  exit 1
fi
echo "coverage OK"
