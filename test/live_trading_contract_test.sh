#!/usr/bin/env bash
set -euo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Program Files/Go/bin:$PATH"

ROOT="$PWD"
cd "$ROOT/server"

export GOCACHE="$PWD/.gocache"
mkdir -p "$GOCACHE"

if go test ./service -run 'TestLiveContract|TestLiveMarketProvider' -count=1; then
  echo "PASS"
else
  status=$?
  echo "FAIL: live trading contract tests failed"
  exit "$status"
fi
