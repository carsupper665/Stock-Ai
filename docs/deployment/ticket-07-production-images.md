# Ticket 07 production-image implementation

Date: 2026-10-12 (Asia/Taipei)  
Source revision at implementation start: `3f58d1b743667934a396d128effeb8bfb03d5ac5`

Four root-context, multi-stage Dockerfiles now produce scratch-based Linux
images for Backend, Agent Server, LLM Provider Server, and Console. Builder
images use immutable manifest-list digests. Runtime images use a fixed non-root
identity, static health probe, CA/timezone resources, explicit `/data` state,
and OCI revision/version/source/creation metadata. Console is the only image
containing browser assets. No runtime stage copies a source directory.

The executable evidence gate is `scripts/build-production-images.ps1`. It
requires a clean checkout and Linux Docker daemon, builds with maximal
provenance plus SBOM attestations, exports an SPDX SBOM, scans Critical/High
with a failing exit code, inventories the flattened runtime filesystem, and
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
