# Traceable production images

Ticket 07 packages Backend, Agent Server, LLM Provider Server, and Console as
Linux/amd64 images for Docker Desktop in Linux-container mode. The only final
base is `scratch`. Each final filesystem contains a statically linked service,
a statically linked health probe and CA roots; Go's pinned `timetzdata` build
tag embeds the timezone database. Console also has
the compiled frontend. Build stages use these immutable official-image indexes:

| Build base | Manifest-list digest |
|---|---|
| `golang:1.27.2-alpine3.23` | `sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079` |
| `node:24.21.0-alpine3.23` | `sha256:9ec4a2e289874ed0d722e1772ec2de45d2801541db8612f3638b26f128c69ac2` |
| `aquasec/trivy:0.75.0` (acceptance scanner) | `sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa` |

Go 1.27.2 is the exact toolchain accepted by the Ticket 06 gate. Node 24 is the
supported LTS line used only to compile Console. Alpine is confined to build
stages, so its shell and package manager cannot enter a production image. The
global `.dockerignore` admits only locked dependency manifests and required
source files; it rejects `.git`, `.scratch`, `.env`, tests, local databases,
logs, caches, and unrelated repository content before upload to BuildKit.

## Build and evidence

Install current Docker Desktop with Buildx, switch it to Linux
containers, and use PowerShell 7 from a clean checkout:

```powershell
pwsh -NoProfile -File ./scripts/build-production-images.ps1 `
  -Version 1.0.0 `
  -EvidenceDirectory C:\stock-ai-evidence\production-images-1.0.0
```

The command fails unless the checkout is clean and the Docker daemon reports
Linux mode. It creates an ephemeral `docker-container` BuildKit builder because
the default Docker driver cannot export attestations. For every image it records
an OCI archive carrying BuildKit's maximal SLSA provenance and SBOM attestations,
an SPDX JSON SBOM, the image configuration, the exported
filesystem inventory, constrained runtime acceptance, a same-input rebuild
comparison, and an anonymous scan from the digest-pinned Trivy 0.75.0 image.
Trivy emits both SPDX JSON and SARIF; `HIGH,CRITICAL --exit-code 1` makes either
severity, a database/download failure, or a missing report fatal. It also fails
if an exported filesystem contains source, `.env`, a
database, a log, an authentication file, compiler, shell, package manager,
download client, Node, npm, or other forbidden CLI named by the policy.

`provenance.json` binds the four image IDs to the Git revision/tree, commit
timestamp, semantic version, target platform, Docker/Buildx versions, the exact
Trivy image digest and reported version,
and SHA-256 of every Dockerfile and dependency lock input. `SHA256SUMS` seals
the evidence directory. Preserve that directory unchanged with the Test
certification record. A rebuild is comparable only when `revision`,
`source_tree`, `source_date_epoch`, `inputs_sha256`, platform, pinned base
digests, and Dockerfile version match. Byte identity is required from the
trimmed static binaries and fixed source epoch. The gate requires image IDs and
provenance materials to match. BuildKit attestation envelopes include a unique
build reference and invocation timestamps, so their index/archive digests are
expected to differ and both values are recorded rather than treated as image
drift. Retain both provenance files for independent review; only the first OCI
archive is kept in the sealed evidence directory.

The script deliberately does not publish. Ticket 10 assigns registry digests
only after Test certification and promotes those exact digests to Release.

## Runtime contract

All images run as numeric user/group `65532:65532`. Backend writes only
`/data/backend.db` and `/data/logs`; Agent Server writes only
`/data/agent-server.db` and `/data/history`; LLM Provider Server writes only
`/data/llm-provider.db`; Console has no persistent writable path. Secret files
are read-only mounts under the environment-specific namespace specified in
`production-secret-safety.md`. Optional Codex/Claude CLI harness executables
and their authentication files are not in the LLM image; enabling a harness
requires separately approved, read-only mounts and a new image/security review.

Run the images only with the following constraints (Ticket 08 encodes these in
Compose):

| Constraint | Backend | Agent | LLM | Console |
|---|---:|---:|---:|---:|
| read-only root | required | required | required | required |
| writable volume | `/data` | `/data` | `/data` | none |
| tmpfs `/tmp` (`noexec,nosuid`, 16 MiB) | required | required | required | required |
| memory | 512 MiB | 1 GiB | 512 MiB | 128 MiB |
| CPUs | 1.0 | 2.0 | 1.0 | 0.5 |
| PIDs | 100 | 200 | 100 | 50 |
| capabilities | drop all | drop all | drop all | drop all |
| privilege escalation | forbidden | forbidden | forbidden | forbidden |

Backend `/v1/health` verifies its database. Agent `/health` verifies the
runtime persistence latch. LLM `/healthz` verifies its database-backed server.
Console `/` verifies both its HTTP server and compiled `index.html`. None of
the probes needs a shell or credential.

## Review gate

Before accepting a build, verify `SHA256SUMS`, confirm every SARIF report has
zero Critical/High result, inspect all four filesystem lists, compare the
revision labels to `provenance.json`, and exercise every health check under the
read-only/resource-limited Compose stack. Missing evidence, a mutable tag, a
scan database error, or any image-ID mismatch blocks promotion.
