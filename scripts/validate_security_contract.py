#!/usr/bin/env python3
"""Validate SEC-DEPLOY-01 and its executable authorization cases."""

from __future__ import annotations

import csv
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CONTRACT = ROOT / "docs" / "deployment" / "security-contract.md"
MATRIX = ROOT / "docs" / "deployment" / "authorization-matrix.csv"

FIELDS = {
    "case_id", "method", "path", "principal", "presented_credential",
    "edge_action", "middleware_action", "upstream", "upstream_credential",
    "expected_status",
}
PRINCIPALS = {"anonymous", "oidc_user", "account"}
ACTIONS = {"allow", "deny", "not_found"}
UPSTREAMS = {"none", "middleware", "backend", "agent", "llm", "edge"}
PRIVATE_PREFIXES = ("/agent", "/llm")
REQUIRED_PATH_PREFIXES = ("/v1", "/agent", "/llm", "/assets", "/healthz", "/oauth2")
REQUIRED_SECTIONS = (
    "## 1. Network listeners and trust boundaries",
    "## 2. Identities and permissions",
    "## 3. Credentials, persistent assets, and dependencies",
    "## 4. Test and Release OIDC values",
    "## 5. Trusted-header contract",
    "## 6. Executable authorization matrix",
    "## 7. Risk register",
)
REQUIRED_COMPONENTS = (
    "Nginx edge", "Middleware", "Backend", "Agent Server",
    "LLM Provider Server", "IdP", "External model/market providers",
    "Container registry", "DNS, time and trust services", "Persistent data",
)
REQUIRED_IDENTITIES = (
    "Anonymous browser", "OIDC USER", "Account token", "Middleware",
    "Agent Server", "LLM Provider Server", "Operator",
)
REQUIRED_SECURITY_ITEMS = (
    "TLS private key/certificate", "OIDC client secret",
    "OIDC cookie encryption/signing key", "Account tokens",
    "IdP signing private key", "Allowed USER subjects",
    "Agent Admin credential", "Backend middleware credential",
    "Agent→Backend USER credential",
    "LLM Admin credential", "LLM Runtime credential", "LLM master key",
    "Provider API tokens", "Backend DB", "Agent DB and History",
    "LLM Provider DB", "IdP database/key store", "Logs and backup sets",
    "Container images/SBOM/scan evidence", "IdP/JWKS",
    "Registry pull credential", "Market/model APIs", "DNS/NTP/CA services",
)
REQUIRED_HEADERS = (
    "Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Real-IP",
    "X-Original-URI", "X-Auth-Request-Subject", "X-Auth-Request-User",
    "X-Auth-Request-Email", "X-Auth-Request-Access-Token",
    "Proxy-Authorization", "`Authorization` is handled by route", "`Cookie`",
)
RISK_PATTERN = re.compile(
    r"^\| (R-\d{3}) \| (Critical|High|Medium|Low) \| (Open|Closed|Accepted) "
    r"\| ([^|]+) \| ([^|]+) \| ([^|]+) \| ([^|]+) \|$",
    re.MULTILINE,
)


def fail(errors: list[str], message: str) -> None:
    errors.append(message)


def validate_contract(text: str) -> list[str]:
    errors: list[str] = []
    for value in (
        REQUIRED_SECTIONS + REQUIRED_COMPONENTS + REQUIRED_IDENTITIES
        + REQUIRED_SECURITY_ITEMS + REQUIRED_HEADERS
    ):
        if value not in text:
            fail(errors, f"contract missing required value: {value}")

    for marker in (
        "stock-ai-test", "stock-ai-release", "stock-ai-api-test",
        "stock-ai-api-release", "__Host-stock-ai-test-session",
        "__Host-stock-ai-release-session",
        "https://${TEST_APP_HOST}/oauth2/callback",
        "https://${RELEASE_APP_HOST}/oauth2/callback",
    ):
        if marker not in text:
            fail(errors, f"OIDC isolation value missing: {marker}")

    if "MUST NOT\nreceive Release signing/private keys" not in text:
        fail(errors, "Test/Release signing-key isolation is not explicit")

    risks = RISK_PATTERN.findall(text)
    if not risks:
        fail(errors, "risk register has no complete risk rows")
    raw_risk_rows = [line for line in text.splitlines() if line.startswith("| R-")]
    if len(risks) != len(raw_risk_rows):
        fail(errors, "risk register contains a malformed/incomplete risk row")
    risk_ids = [risk[0] for risk in risks]
    if len(risk_ids) != len(set(risk_ids)):
        fail(errors, "risk register contains duplicate IDs")
    for _, _, _, reproduction, control, owner, ticket in risks:
        if not all(value.strip() for value in (reproduction, control, owner, ticket)):
            fail(errors, "risk row has an empty reproduction/control/owner/ticket")
        if not re.search(r"\b(?:0[2-9]|1[0-6])\b", ticket):
            fail(errors, f"risk row has no owning ticket: {ticket.strip()}")
    return errors


def load_matrix(path: Path) -> tuple[list[dict[str, str]], list[str]]:
    errors: list[str] = []
    with path.open(newline="", encoding="utf-8") as handle:
        reader = csv.DictReader(handle)
        if set(reader.fieldnames or ()) != FIELDS:
            fail(errors, f"matrix fields differ: {reader.fieldnames}")
        rows = list(reader)
    return rows, errors


def validate_matrix(rows: list[dict[str, str]]) -> list[str]:
    errors: list[str] = []
    ids: set[str] = set()
    for line, row in enumerate(rows, start=2):
        case_id = row.get("case_id", "")
        if not case_id or case_id in ids:
            fail(errors, f"line {line}: missing or duplicate case_id {case_id!r}")
        ids.add(case_id)
        if row.get("principal") not in PRINCIPALS:
            fail(errors, f"{case_id}: unknown principal")
        if row.get("edge_action") not in ACTIONS:
            fail(errors, f"{case_id}: unknown edge_action")
        if row.get("upstream") not in UPSTREAMS:
            fail(errors, f"{case_id}: unknown upstream")
        status = row.get("expected_status", "")
        if status != "success" and not re.fullmatch(r"[1-5]\d\d", status):
            fail(errors, f"{case_id}: invalid expected_status")
        denied = row.get("edge_action") in {"deny", "not_found"} or row.get("middleware_action") in {"deny", "not_found"}
        if denied and (row.get("upstream") != "none" or row.get("upstream_credential") != "none"):
            fail(errors, f"{case_id}: denied request reaches an upstream")
        if row.get("principal") == "anonymous" and row.get("path", "").startswith(PRIVATE_PREFIXES):
            if row.get("middleware_action") != "deny" or row.get("upstream") != "none":
                fail(errors, f"{case_id}: anonymous private-service request is not blocked by Middleware")
        if row.get("principal") == "account" and row.get("path", "").startswith(PRIVATE_PREFIXES):
            if row.get("middleware_action") != "deny":
                fail(errors, f"{case_id}: Account token unlocks a private service")
        if row.get("upstream") == "agent" and row.get("upstream_credential") != "agent_admin":
            fail(errors, f"{case_id}: Agent upstream lacks exact Admin substitution")
        if row.get("upstream") == "llm":
            expected = "llm_runtime" if row.get("path") == "/llm/v1/models" else "llm_admin"
            if row.get("upstream_credential") != expected:
                fail(errors, f"{case_id}: LLM route has wrong credential role")
        if row.get("path") == "/llm/v1/generate" and row.get("upstream") != "none":
            fail(errors, f"{case_id}: /llm/v1/generate is open to browsers")
        if row.get("upstream") == "backend" and row.get("principal") == "oidc_user":
            if row.get("upstream_credential") != "backend_middleware_token":
                fail(errors, f"{case_id}: Backend session request lacks the backend_middleware_token credential")
        if row.get("upstream") == "backend" and row.get("principal") == "account":
            if row.get("upstream_credential") != "account_bearer":
                fail(errors, f"{case_id}: Account bearer not preserved to Backend")

    for prefix in REQUIRED_PATH_PREFIXES:
        if not any(row.get("path", "").startswith(prefix) for row in rows):
            fail(errors, f"matrix has no case for {prefix}")
    for principal in PRINCIPALS:
        if not any(row.get("principal") == principal for row in rows):
            fail(errors, f"matrix has no {principal} case")
    for case_id in ("API-008", "API-009", "HEADER-001", "HEADER-002"):
        if case_id not in ids:
            fail(errors, f"matrix missing isolation/spoofing case {case_id}")
    return errors


def main() -> int:
    missing = [str(path.relative_to(ROOT)) for path in (CONTRACT, MATRIX) if not path.is_file()]
    if missing:
        print("security contract validation failed:")
        for path in missing:
            print(f"- missing {path}")
        return 1
    text = CONTRACT.read_text(encoding="utf-8")
    rows, errors = load_matrix(MATRIX)
    errors.extend(validate_contract(text))
    errors.extend(validate_matrix(rows))
    if errors:
        print("security contract validation failed:")
        for error in errors:
            print(f"- {error}")
        return 1
    print(f"security contract valid: {len(rows)} authorization cases, {len(RISK_PATTERN.findall(text))} risks")
    return 0


if __name__ == "__main__":
    sys.exit(main())
