# JetBrains Plugin Signing

The R1 MCP Bridge plugin (`ide/jetbrains/`, distributed as
`r1-mcp-bridge-<ver>.jar`) is signed by the R1 CI release pipeline.
This document captures the key-management flow and the Gradle
incantation. Spec reference: `specs/mcp-ide-bundles.md` §T11.1.

Marketplace publication is out of scope; today the signed jar is
distributed via the R1 GitHub release tarball.

## Key generation

JetBrains' plugin signing flow consumes a PEM-encoded RSA private
key plus a self-signed certificate chain.

```bash
# Generate a 4096-bit RSA private key (PEM, encrypted with AES-256).
openssl genrsa -aes256 -out r1-jetbrains-private.pem 4096

# Generate a self-signed X.509 cert (10-year validity) for the
# matching public key.
openssl req -new -x509 \
    -key r1-jetbrains-private.pem \
    -out r1-jetbrains-cert.pem \
    -days 3650 \
    -subj "/CN=RelayOne R1 Plugin/O=RelayOne/C=US"
```

Validate the pair:

```bash
openssl x509 -in r1-jetbrains-cert.pem -text -noout | head -10
openssl rsa -in r1-jetbrains-private.pem -check -noout
```

## Storage

- The encrypted private key (`r1-jetbrains-private.pem`) and key
  password live in the operator password manager. **Never commit
  either.**
- The certificate chain (`r1-jetbrains-cert.pem`) is non-sensitive
  and may be committed to a private CI configuration repo for
  reproducibility.
- CI secrets:

| Secret name                     | Value                                     |
| ------------------------------- | ----------------------------------------- |
| `R1_JETBRAINS_PRIVATE_KEY`      | Contents of `r1-jetbrains-private.pem`    |
| `R1_JETBRAINS_CERT_CHAIN`       | Contents of `r1-jetbrains-cert.pem`       |
| `R1_JETBRAINS_KEY_PASSWORD`     | The AES-256 passphrase used during keygen |
| `R1_JETBRAINS_PLUGIN_VERSION`   | Optional; overrides the `0.2.0` default   |

## Build + sign + verify

```bash
cd ide/jetbrains

# Run unit tests (FramingTest, DaemonClientTest).
./gradlew test

# Build the unsigned jar -> build/distributions/r1-mcp-bridge-<ver>.zip
./gradlew buildPlugin

# Sign the jar using the env-var-supplied key + cert.
# Produces r1-mcp-bridge-<ver>-signed.zip.
./gradlew signPlugin

# Verify the plugin loads against the declared since-build (261 = 2026.1).
./gradlew verifyPlugin
```

`signPlugin` reads the three env vars listed above; if any are
absent the IntelliJ Platform Gradle plugin no-ops the signing
step (useful for local builds where you don't have the private key).

## Verifying a signed jar

Operators who pull a release jar can confirm provenance:

```bash
unzip -p r1-mcp-bridge-<ver>-signed.zip META-INF/CERT.SF | head
unzip -p r1-mcp-bridge-<ver>-signed.zip META-INF/CERT.RSA | \
    openssl pkcs7 -inform DER -print_certs
```

The certificate's CN should read `RelayOne R1 Plugin` with
organization `RelayOne`.

## Key rotation

When rotating the private key:

1. Generate a new key + cert pair following the steps above. Reuse
   the same subject (`/CN=RelayOne R1 Plugin/O=RelayOne/C=US`) so
   verifiers don't reject the new chain.
2. Replace the three CI secrets in a single commit so signing
   doesn't break mid-rollout.
3. Bump `R1_JETBRAINS_PLUGIN_VERSION` to force a release artifact
   under the new key. Old releases remain signed by the old key
   (they don't rotate retroactively).
4. Archive the old key in the operator password manager — IDEs
   loaded with old releases continue to validate the old
   signature.

## Out of scope

- **JetBrains Marketplace publish.** R1 distributes via GitHub
  releases; Marketplace requires their own signing key (separate
  flow) and review process.
- **Time-stamped signatures.** RFC 3161 time-stamping is supported
  by the Gradle plugin but not used today — operators rebuild from
  HEAD if they need updated signing dates.

Spec: `specs/mcp-ide-bundles.md` §T6.1, §T11.1.
