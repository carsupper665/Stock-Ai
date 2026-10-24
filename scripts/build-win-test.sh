#!/usr/bin/env bash
# Builds the Windows test package (no login) from WSL: four Windows executables, the frontend,
# start/stop scripts and settings. Rerun to update; data/, logs/ and secrets/ are kept.
# Usage: scripts/build-win-test.sh [output folder]   (default /mnt/c/stock-ai-win-test)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-/mnt/c/stock-ai-win-test}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

python3 "$root/scripts/check_versions.py" >/dev/null

build() { # module package output [go build flags...]
  local module=$1 package=$2 output=$3
  shift 3
  (cd "$root/$module" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath "$@" -o "$stage/$output" "$package")
  echo "built $output"
}
build backend . backend.exe
build llm_provider_server ./cmd/llm-provider-server llm-provider-server.exe
build agent_server ./cmd/agent-server agent-server.exe
build middleware ./cmd/middleware middleware.exe -tags wintest

npm --prefix "$root/frontend" run build --silent >/dev/null
cp -r "$root/frontend/dist" "$stage/web"
echo "built web/"

mkdir -p "$out/secrets" "$out/data" "$out/logs"
# Secrets are created once and then reused, so the databases keep working across rebuilds.
for name in backend_middleware_token backend_user_token agent_admin_token llm_admin_token llm_runtime_token; do
  file="$out/secrets/stock-ai-test_$name"
  [ -e "$file" ] || { openssl rand -hex 32 | tr -d '\n' > "$file"; echo "created secrets/stock-ai-test_$name"; }
done
key="$out/secrets/stock-ai-test_llm_master_key"
[ -e "$key" ] || { openssl rand -base64 32 | tr -d '\n' > "$key"; echo "created secrets/stock-ai-test_llm_master_key"; }

secret() { echo "secrets/stock-ai-test_$1"; }
cat > "$stage/.env" <<ENV
# Windows test package settings, rewritten by scripts/build-win-test.sh. Secrets live in secrets/.
USER_TOKEN_FILE=$(secret backend_middleware_token)
AGENT_SERVICE_TOKEN_FILE=$(secret backend_user_token)
USER_NAME=USER
PORT=27794
BACKEND_ADDR=127.0.0.1:27794
DB_DRIVER=sqlite
DB_DSN=data/backend.db
LOG_DIR=logs
BACKEND_URL=http://127.0.0.1:27794
LLM_SERVER_URL=http://127.0.0.1:28090
LLM_SERVER_ADDR=127.0.0.1:28090
LLM_SERVER_DB_PATH=data/llm-provider.db
LLM_SERVER_ADMIN_TOKEN_FILE=$(secret llm_admin_token)
LLM_SERVER_RUNTIME_TOKEN_FILE=$(secret llm_runtime_token)
LLM_SERVER_MASTER_KEY_FILE=$(secret llm_master_key)
LLM_SERVER_MODEL_CATALOG=[]
LLM_SERVER_PROVIDER_TIMEOUT=180s
AGENT_SERVER_URL=http://127.0.0.1:28080
AGENT_SERVER_ADDR=127.0.0.1:28080
AGENT_SERVER_DB_PATH=data/agent-server.db
AGENT_SERVER_HISTORY_DIR=data/agent-history
AGENT_SERVER_ADMIN_TOKEN_FILE=$(secret agent_admin_token)
AGENT_SERVER_BACKEND_URL=http://127.0.0.1:27794
AGENT_SERVER_BACKEND_USER_TOKEN_FILE=$(secret backend_user_token)
AGENT_SERVER_GATEWAY_URL=http://127.0.0.1:28090
AGENT_SERVER_GATEWAY_RUNTIME_TOKEN_FILE=$(secret llm_runtime_token)
AGENT_SERVER_REQUEST_TIMEOUT=200s
AGENT_SERVER_TOOL_TIMEOUT=10s
# The win-test middleware: no login, serves web/ on this address.
MIDDLEWARE_ADDR=127.0.0.1:24180
WEB_DIR=web
BACKEND_USER_TOKEN_FILE=$(secret backend_middleware_token)
ENV
cp "$root"/scripts/win-test/{start.ps1,stop.ps1,start.cmd,stop.cmd,README.md} "$stage/"

# A running executable cannot be replaced; stop the package in Windows first.
for exe in backend.exe llm-provider-server.exe agent-server.exe middleware.exe; do
  if [ -e "$out/$exe" ] && ! { : >> "$out/$exe"; } 2>/dev/null; then
    echo "$out/$exe is in use; run stop.cmd in Windows, then rebuild" >&2
    exit 1
  fi
done
rm -rf "$out/web"
cp -r "$stage"/. "$out/"
echo "win-test package ready: $out"
