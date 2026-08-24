#!/usr/bin/env bash
# Thin wrapper around `flowlog setup`, kept as the discoverable entry point
# next to setup.md and template.json.
#
# All the logic lives in the Go binary (doctor.go) so there is exactly one
# implementation of these API calls. This script only resolves how to invoke
# it, which is the one thing a shell is better at: on a fresh clone the binary
# may not be installed yet, so fall back to running from source.
#
# Usage:  ./bootstrap.sh [--os-url URL] [--osd-url URL] [--index PREFIX]

set -euo pipefail

if command -v flowlog >/dev/null 2>&1; then
  exec flowlog setup "$@"
fi

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if command -v go >/dev/null 2>&1; then
  echo "flowlog not on PATH; running from source in $REPO_DIR" >&2
  cd "$REPO_DIR" && exec go run . setup "$@"
fi

echo "error: neither 'flowlog' nor 'go' is on PATH." >&2
echo "Install flowlog first:  cd $REPO_DIR && go install" >&2
exit 1
