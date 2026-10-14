# Local Test certification and immutable promotion

Ticket 10 uses an isolated Linux-container Docker Compose project and local
OIDC/JWKS plus fake OpenAI-compatible provider fixtures. The fixture has no
external IdP, DNS, TLS, host, credentials, or network dependency. Its fixed
provider response creates one limit order through the secured Agent flow. Test
secrets and the fixture signing key are generated into ignored `.local-test/`
files and mounted at runtime; they are never included in production images.

## Build and run local Test

Use a committed checkout and a Linux-mode Docker Desktop daemon. The local
registry listens at `127.0.0.1:5000`; BuildKit builder `stock-ai-t10` must be
available. Build and push all five production images from the current commit,
then pin their BuildKit digests before starting Compose:

```text
python3 scripts/local-test/build_images.py
python3 scripts/local-test/stage_images.py
python3 scripts/local-test/prepare.py
docker build -f scripts/local-test/Dockerfile -t stock-ai/local-test-fixture:local scripts/local-test
docker compose --project-name stock-ai-t10-cert --env-file .local-test/compose.env -f deploy/compose/compose.yaml -f deploy/compose/compose.test-edge.yaml -f deploy/compose/compose.local-test.yaml up -d
python3 scripts/local-test/capture_build_evidence.py
python3 scripts/local-test/certify.py
```

The certification runner exercises the complete secured flow and eight core
acceptance cases: `secured_product_flow`, `wrong_user`, `expired_session`,
`idp_denial`, `idp_restart`, `internal_service_restart`,
`docker_restart_failure`, and `test_material_absence`. It also checks issuer and
audience mismatch, fixture outage, anonymous `/login` and `/login/` redirects,
exact Release digest consumption, manifest substitution/tampering, and Compose
build-directive rejection. Each check writes a sanitized JSON result. Failed
attempts remain recorded in their own timestamped directory; promotion accepts
only an explicitly selected passed certification whose revision and five image
digests match the production build evidence.
Cookies, account tokens, IdP response bodies, credentials, and fixture keys
remain in memory or in ignored runtime files and are not written to evidence.

`capture_build_evidence.py` creates per-image SPDX SBOMs, Trivy SARIF scan
results, flattened filesystem inventories, BuildKit provenance, and build
acceptance results. It requires zero Critical/High findings and seals all
outputs with `SHA256SUMS`. `test_material_absence` scans each image layer,
flattened filesystem inventory, image config, registry manifest and BuildKit
metadata for local Test secrets, keys and certificates.

## Create and consume the promotion manifest

The promotion generator fails closed unless the build and certification
sidecars verify, all eight results pass, all five immutable registry digests
match the digests run in Test, and all SBOM, scan, inventory, provenance, and
acceptance records are present. It packages these artifacts with the sanitized
result files and emits `stock-ai.promotion-manifest.v1` plus a package-level
`SHA256SUMS` file:

```text
python3 scripts/create_promotion_manifest.py --build-evidence .local-test/build-evidence --test-certification deploy/evidence/local-test/certification/<run>/test-certification.json --registry-references .local-test/registry-references.json --output deploy/evidence/local-test/promotion/promotion.json
python3 scripts/local-test/verify_promotion.py --manifest deploy/evidence/local-test/promotion/promotion.json --output-dir deploy/evidence/local-test/release-gate/<run>
```

Release `Update` and Release preflight require this manifest. They verify the
manifest and sidecar, certification result hashes, package artifact hashes,
and exact digest equality against Test and build acceptance. They reject tags,
Compose `build` entries, changed digests, and any rebuild or substitution.
Release consumes the four application digests from the manifest; the OIDC
Gateway digest remains bound as part of the five-image Test certification.

The actual local certification and promotion evidence is retained under
`deploy/evidence/local-test/`. The older external-Test `not_run` record is
historical and is not used as a passing result.
