#!/usr/bin/env bash
set -euo pipefail
export PATH="/usr/bin:/bin:/mingw64/bin:/c/Program Files/Go/bin:$PATH"
cd "$(dirname "$0")/../server"
export GOCACHE="$PWD/.gocache"
mkdir -p "$GOCACHE"
if go test ./service -run 'TestOrderNormalizationUsesSymbolRules' -count=1; then
  echo "PASS"
else
  code=$?
  echo "FAIL"
  exit "$code"
fi
