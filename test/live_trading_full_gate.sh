#!/usr/bin/env bash
set -euo pipefail
export PATH="/usr/bin:/bin:/mingw64/bin:/c/Program Files/Go/bin:$PATH"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
scripts=(
  test/live_contract_freeze_test.sh
  test/live_trading_contract_test.sh
  test/live_market_provider_test.sh
  test/live_kline_contract_test.sh
  test/paper_trading_flow_test.sh
  test/order_normalization_test.sh
  test/exchange_adapter_contract_test.sh
  test/live_execution_safety_gate_test.sh
  test/live_order_reconciliation_test.sh
  test/live_trading_audit_test.sh
)
for script in "${scripts[@]}"; do
  bash "$script"
done
cd "$ROOT/server"
export GOCACHE="$PWD/.gocache"
mkdir -p "$GOCACHE"
if go test ./...; then
  echo "PASS"
else
  code=$?
  echo "FAIL"
  exit "$code"
fi
