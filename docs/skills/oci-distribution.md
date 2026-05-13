# OCI distribution path (C7 T3)

Audience: operators with existing OCI registry infrastructure (GHCR,
Docker Hub, Harbor, Artifact Registry) who want to host R1 skill
packs without running `r1 skills pack serve` themselves.

R1 does not push or pull from OCI registries in v2 — the
`/v2/packs/...` HTTP surface is canonical. This document specifies
the interoperability surface so third-party tooling (ORAS,
`docker buildx`, custom CI) can host packs on OCI hosts.

## Media types

| Layer       | Media type                                  | Content                |
|-------------|----------------------------------------------|------------------------|
| Config blob | `application/vnd.r1.pack.v2+json`            | The full v2 manifest JSON shipped at `manifest.v2.json` in the pack root. |
| Layer blob  | `application/vnd.r1.pack.v2.archive+tar+gzip`| The pack tarball (same shape as `/v2/packs/{id}/blob.tar.gz`). |
| Signature   | `application/vnd.r1.pack.v2.signature+json`  | The detached `pack.sig.json` envelope from `internal/skillmfr`. |

The chosen media types follow [ORAS conventions](https://oras.land/)
for "agent skills as OCI artifacts" — see the Salaboy 2026-04 and
Thomas Vitale 2026 writeups for the established pattern.

## Layout

```
└─ config: application/vnd.r1.pack.v2+json        (manifest.v2.json bytes)
└─ layers:
   ├─ application/vnd.r1.pack.v2.archive+tar+gzip (pack tarball)
   └─ application/vnd.r1.pack.v2.signature+json   (pack.sig.json)
```

## Example: push with ORAS

```bash
oras push \
  ghcr.io/your-org/r1-packs/alpha:1.0.0 \
  --config manifest.v2.json:application/vnd.r1.pack.v2+json \
  alpha.tar.gz:application/vnd.r1.pack.v2.archive+tar+gzip \
  pack.sig.json:application/vnd.r1.pack.v2.signature+json
```

## Example: pull with ORAS

```bash
oras pull ghcr.io/your-org/r1-packs/alpha:1.0.0
# Drops manifest.v2.json, alpha.tar.gz, pack.sig.json into cwd.
```

The consuming side then:

1. Extracts `alpha.tar.gz` into a pack root directory.
2. Drops `pack.sig.json` and `manifest.v2.json` next to it.
3. Runs the normal `r1 skills pack install / verify / adopt` flow.

## Trust-root interaction

OCI distribution does NOT change the trust-root contract. The kid in
`pack.sig.json` must still appear in the consumer's trust-root
document. Operators distributing packs through OCI typically run a
separate `/v2/trust-root` endpoint on their own infrastructure (or
ship the document via configuration management).

## Not in scope

R1 itself does not run an OCI client in v2 (no
`r1 skills pack push-oci` / `pull-oci`). Adding those wrappers is a
follow-up that depends on `manifest_schema_version: 2.0.0` but is
not required to ship the federated runtime story.
