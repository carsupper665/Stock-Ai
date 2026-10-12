# Ticket 07 production-image implementation

Date: 2026-10-12 (Asia/Taipei)  
Accepted application revision: `411f1cd3025984c4cacda3c40174a44af3282b65`  
Accepted source tree: `c0388bea6a3668aeba5b6668bdf629c5cf42ceeb`

Four root-context, multi-stage Dockerfiles now produce scratch-based Linux
images for Backend, Agent Server, LLM Provider Server, and Console. Builder
images use immutable manifest-list digests. Runtime images use a fixed non-root
identity, static health probe, CA/timezone resources, explicit `/data` state,
and OCI revision/version/source/creation metadata. Console is the only image
containing browser assets. No runtime stage copies a source directory.

The executable evidence gate is `scripts/build-production-images.ps1`. It
requires a clean checkout and Linux Docker daemon, builds with maximal
provenance plus SBOM attestations, exports an SPDX SBOM, scans Critical/High
anonymously with the digest-pinned official Trivy 0.75.0 image and a failing
exit code, inventories the flattened runtime filesystem, and
rejects forbidden files and tools. Its sealed `provenance.json` identifies all
dependency inputs needed to compare a same-revision rebuild.

Repository policy tests run with:

```text
python3 -m unittest scripts.test.test_production_images
```

The build/scan evidence itself is intentionally generated outside the source
checkout. A successful run adds `acceptance-summary.json`, two BuildKit metadata
records per image, runtime acceptance JSON/logs, inventories, inspections,
SPDX/SARIF, provenance, and `SHA256SUMS`. Archive that directory with the Test
certification record; do not claim image approval from this implementation note alone. See
`production-images.md` for the exact operator command, evidence interpretation,
runtime limits, writable resources, and promotion blocker rules.

## Windows Docker acceptance run

The complete gate ran from a clean checkout on Windows Docker Desktop 4.76.0
(Engine 29.5.2, Linux/amd64 containers) with Buildx 0.34.0. The operator command
was:

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass `
  -File ./scripts/build-production-images.ps1 `
  -Version 0.7.0 `
  -EvidenceDirectory \\wsl.localhost\Ubuntu-24.04\tmp\stock-ai-production-images-411f1cd
```

The command exited 0 and the source checkout remained clean. The evidence bundle
contains 46 files including `SHA256SUMS`; its seal file has SHA-256
`c550b57cd81e1a0a484c6f21923b6b61ba50d51749820a92685a0c6b9fb2c52c`.
Running `sha256sum -c SHA256SUMS` independently verified all 45 covered
artifacts. `acceptance-summary.json` has SHA-256
`0734fecfdf8d0adb6c3e43f22517d676fee50974710d71b2d19abb35086a2cda`;
`provenance.json` has SHA-256
`bbb974c71b840755722cde26ea66cb9ed9cf9c541f9e671e14228cc24282f3b2`.
The complete bundle, including OCI archives and attestations, must accompany the
Ticket 10 Test certification; this report is its reviewable summary, not a
replacement for the sealed files.

### Artifact and reproducibility result

Each image was built twice from the same revision, tree, dependency inputs,
source epoch, platform, Dockerfile and pinned bases. Runtime image IDs matched.
The two attestation-envelope digests differ because BuildKit records unique
invocation IDs and timestamps; both are retained and their provenance materials
matched exactly.

| Image | Image ID (first = rebuild) | First / rebuild attested result | OCI archive SHA-256 |
|---|---|---|---|
| Backend | `sha256:8130106d86ed0f673ea7c9e05dcc093a4071757d829ac136e3be994f99c724c6` | `sha256:492fad03264fe3409c884873147cfbc5dfc029e1236bfa9a413ce4c188a720e3` / `sha256:30287eafed8565fe8e0b1193e21995696e2e11306b6fa0169b6e8146e99f747c` | `9efb45c17193747f9b8840af54548fa4adfa4afaf38a31c6d43db87b0ffe28c4` |
| Agent Server | `sha256:2cec65ffd5358f5fc989d50b2e7816cf67b3690012628567510f0706b048e1cb` | `sha256:0f6416b736c8899771aa45018ab834eebbc57adf43caa7e59a221485799197aa` / `sha256:3757742c63402a931088bd5343a58cc596afb6899f7c40b41ba2d6187c6cb5ae` | `293fcf47e1ebed91a565b5f070a36fa64bdf520671367ad071e9844a14cbd8d5` |
| LLM Provider Server | `sha256:04fce2dbf1fd34c301256c10fbc06a8db5e77bad97b343f037081c916b53eddc` | `sha256:c6a06dcef4aa99e63ccce681ae7bce709057c46dc76195313d0b8d8a0dbab59b` / `sha256:d767413eec2468e028d7062f398a1b841f118898b2e82944ad190cf93266c3f2` | `4096221b2963f6b9099b6951cad0d7b42ffbc91f783b6deb5d50eb2e5a1e7705` |
| Console | `sha256:7ff72c9577b5f52248dbddb1d43fb99f6a58be718966b7e6131eb87a6e013e6f` | `sha256:0153ecc88c310898a8b4ce30a03e97c0711be67f169b25f8227e57567d2a4317` / `sha256:d51f6397fd8d670d8104516a6b480c4803341b63cd01cd4fcbc06f55527572bb` | `d1605e5443c49733b82efec4dd52783a517488c5351939461cc0828864ea75ea` |

### Scan and filesystem policy result

The run used the official anonymous scanner
`aquasec/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa`.
Every image produced a non-empty SPDX JSON SBOM and SARIF report under
`--severity HIGH,CRITICAL --exit-code 1`. All four SARIF `results` arrays were
empty:

| Image | Critical | High | SARIF SHA-256 |
|---|---:|---:|---|
| Backend | 0 | 0 | `f758f4c89c8b767fefd0cf13df8713203d84342676e935007cf84da0d13c7883` |
| Agent Server | 0 | 0 | `ea76d2b73e9cc231a6e8d5c34c06351d4a44169321bfe62fb4624e2bc605c981` |
| LLM Provider Server | 0 | 0 | `0268d65c815b2d942fb27b333154c315fa7e25c863295a73e8da5adec7fee02c` |
| Console | 0 | 0 | `863e902e10109c975f301882a97a8d7c2615b8512bc30dec2be12ff4a4eda4a3` |

The flattened inventories contain only the service binary, static healthcheck,
CA bundle, declared empty data directories, and (for Console) compiled public
assets plus Docker's injected pseudo-filesystem entries. The automated denylist
found no source, compiler, shell, package manager/cache, downloader, `.env`,
database, log, authentication material, Node/npm, or unrelated CLI.

### Constrained runtime result

All four containers reached Docker health status `healthy` as numeric
`65532:65532` with a read-only root, `/tmp` tmpfs (`noexec,nosuid`, 16 MiB), all
capabilities dropped, `no-new-privileges`, and declared memory/CPU/PID limits.
The common CA bundle was present and non-empty (181,724 bytes, SHA-256
`028fca5150ee1915c4474489985d55c54fceaa680c754cc8d1301734f01b2232`).
Backend, Agent Server, and LLM Provider Server each created a non-empty database
at its declared `/data` path on a writable volume; Console ran with no writable
volume. This proves compatibility with the Ticket 08 read-only-root contract.
