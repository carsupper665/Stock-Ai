# Local Test certification evidence

The accepted final run is bound to source revision `906326524f70acf77dcffeff86e9433896830509`:

- Certification: `certification/20261014T101728Z/test-certification.json`
- Promotion package and SHA-256 inventory: `promotion/promotion.json`, `promotion/SHA256SUMS`
- Release digest-gate results: `release-gate/20261014T101728Z/`
- Build SBOMs, HIGH/CRITICAL scans, filesystem inventories, provenance and checksums are packaged in `promotion/`.

The certification result has 16 passed checks and binds all five Test images by
immutable digest. The package-level Release checks separately record exact
digest acceptance, substitution rejection, manifest tamper rejection and build
directive rejection.

Earlier timestamped certifications and `promotion-da6fc3e/` are preserved as
historical attempts from earlier revisions. They are not inputs to or evidence
for the final promotion above.
