#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
artifact_dir=${1:?usage: scripts/run-security-gate.sh ARTIFACT_DIR}
govulncheck=${GOVULNCHECK:?set GOVULNCHECK to the pinned govulncheck executable}
gitleaks=${GITLEAKS:?set GITLEAKS to the pinned gitleaks executable}

cd "$root"
git diff --quiet
git diff --cached --quiet
test -z "$(git ls-files --others --exclude-standard)"
revision=$(git rev-parse HEAD)
mkdir -p "$artifact_dir"

python3 - "$artifact_dir/metadata.json" "$revision" "$govulncheck" "$gitleaks" <<'PY'
import json
import platform
import subprocess
import sys

output, revision, govulncheck, gitleaks = sys.argv[1:]
def version(*command):
    return subprocess.run(command, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout.strip()

with open(output, "w", encoding="utf-8") as handle:
    json.dump({
        "revision": revision,
        "platform": platform.platform(),
        "git": version("git", "--version"),
        "go": version("go", "version"),
        "node": version("node", "--version"),
        "npm": version("npm", "--version"),
        "govulncheck": version(govulncheck, "-version"),
        "gitleaks_install": "github.com/zricethezav/gitleaks/v8@v8.24.2",
        "gitleaks_binary_sha256": version("sha256sum", gitleaks).split()[0],
    }, handle, indent=2)
    handle.write("\n")
PY

for module in backend agent_server llm_provider_server oidc_gateway; do
    (cd "$module" && go test -race -count=1 -json ./...) >"$artifact_dir/go-test-$module.json"
    (cd "$module" && go vet ./...) >"$artifact_dir/go-vet-$module.log" 2>&1
    (cd "$module" && "$govulncheck" -json ./...) >"$artifact_dir/govulncheck-$module.json"
done

python3 scripts/validate_security_contract.py >"$artifact_dir/security-contract.log"

(cd frontend && npm ci --ignore-scripts)
(cd frontend && npm test -- --reporter=json --outputFile="$artifact_dir/frontend-tests.json")
(cd frontend && npm run build) >"$artifact_dir/frontend-build.log" 2>&1
(cd frontend && npm audit --json) >"$artifact_dir/npm-audit.json"

"$gitleaks" dir --no-banner --redact=100 --report-format json --report-path "$artifact_dir/gitleaks.json" .

git diff --quiet
git diff --cached --quiet
test -z "$(git ls-files --others --exclude-standard)"
