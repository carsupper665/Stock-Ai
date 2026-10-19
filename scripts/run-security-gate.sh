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
test "$(go env GOVERSION)" = "go1.27.2"
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

for module in backend agent_server llm_provider_server middleware; do
    (cd "$module" && go test -race -count=1 -json ./...) >"$artifact_dir/go-test-$module.json"
    (cd "$module" && go vet ./...) >"$artifact_dir/go-vet-$module.log" 2>&1
    (cd "$module" && "$govulncheck" -json ./...) >"$artifact_dir/govulncheck-$module.json"
done

python3 - "$artifact_dir" <<'PY'
import json
import pathlib
import sys

artifact_dir = pathlib.Path(sys.argv[1])
projects = {
    "backend": "backend",
    "agent_server": "stock-ai/agent-server",
    "llm_provider_server": "stock-ai/llm-provider-server",
    "middleware": "middleware",
}
allowed = {
    "GO-2026-5932": {
        "severity": "informational",
        "owner": "Stock AI release owner",
        "control": "The vulnerable openpgp package is not imported or called; this gate blocks if a project call trace appears.",
        "expiry": "2027-01-31",
    }
}
summary = {"status": "pass", "called_findings": [], "exceptions": []}
for name, project in projects.items():
    text = (artifact_dir / f"govulncheck-{name}.json").read_text(encoding="utf-8")
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        item, offset = decoder.raw_decode(text, offset)
        finding = item.get("finding")
        if finding is None:
            continue
        advisory = finding["osv"]
        called = any(frame.get("module") == project and frame.get("function") for frame in finding.get("trace", []))
        if called:
            summary["called_findings"].append({"module": name, "advisory": advisory})
        elif advisory in allowed and not any(exception["advisory"] == advisory for exception in summary["exceptions"]):
            summary["exceptions"].append({"advisory": advisory, **allowed[advisory]})
        elif advisory not in allowed:
            summary["status"] = "fail"
            summary.setdefault("unapproved_findings", []).append({"module": name, "advisory": advisory})

if summary["called_findings"]:
    summary["status"] = "fail"
with (artifact_dir / "govulncheck-summary.json").open("w", encoding="utf-8") as handle:
    json.dump(summary, handle, indent=2)
    handle.write("\n")
if summary["status"] != "pass":
    raise SystemExit("Go vulnerability policy failed")
PY

python3 scripts/validate_security_contract.py >"$artifact_dir/security-contract.log"

(cd frontend && npm ci --ignore-scripts)
(cd frontend && npm test -- --reporter=json --outputFile="$artifact_dir/frontend-tests.json")
(cd frontend && npm run build) >"$artifact_dir/frontend-build.log" 2>&1
(cd frontend && npm audit --json) >"$artifact_dir/npm-audit.json"

"$gitleaks" dir --no-banner --redact=100 --report-format json --report-path "$artifact_dir/gitleaks.json" .

git diff --quiet
git diff --cached --quiet
test -z "$(git ls-files --others --exclude-standard)"
