#!/usr/bin/env bash
set -euo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Program Files/Go/bin:$PATH"

ROOT="$PWD"
cd "$ROOT/server"

export GOCACHE="$PWD/.gocache"
mkdir -p "$GOCACHE"

if go test ./service -run 'TestLiveContract' -count=1 && \
   go test ./router -run 'Test(AccountWebSocketReceivesTradeEvents|AccountWebSocketUsesFrozenEnvelopeAndRedactsSecrets|LiveAccountHTTPFlow|LiveOrderClientOrderIDIsIdempotentOverHTTP)$' -count=1; then
  echo "PASS"
else
  status=$?
  echo "FAIL: live contract freeze tests failed"
  exit "$status"
fi
