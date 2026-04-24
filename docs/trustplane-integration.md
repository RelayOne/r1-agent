# TrustPlane Integration

How R1 talks to the TrustPlane gateway, how to switch between the
in-memory stub and the live HTTP client, and which env vars govern
the wiring.

## Architecture in one paragraph

R1 defines a narrow interface (`trustplane.Client`, 8 methods) for
everything it asks TrustPlane to do: identity registration, audit
anchoring, HITL approvals, reputation read/write, delegation
create/verify/revoke, and Cedar policy evaluation. Two
implementations ship: `StubClient` (in-process, always-pass, default)
and `RealClient` (hand-written HTTP against the vendored OpenAPI spec
at `internal/trustplane/openapi/gateway.yaml`). R1 deliberately
does **not** import any TrustPlane Go module; the only TrustPlane
artifact in-tree is the vendored spec, used as documentation for the
hand-written client.

## Selecting a client at startup

`stoke-mcp` (and any future binary using `trustplane.NewFromEnv`)
picks an implementation from environment variables:

| Variable                    | Purpose                                          | Required for mode |
| --------------------------- | ------------------------------------------------ | ----------------- |
| `STOKE_TRUSTPLANE_MODE`     | `stub` (default) or `real`                       | always            |
| `STOKE_TRUSTPLANE_URL`      | Gateway base URL, e.g. `https://gw.trustplane.dev` | `real`          |
| `STOKE_TRUSTPLANE_PRIVKEY`  | Ed25519 private key PEM (inline)                 | `real`*           |
| `STOKE_TRUSTPLANE_PRIVKEY_FILE` | Path to PEM file                             | `real`*           |

*Exactly one of `STOKE_TRUSTPLANE_PRIVKEY` or
`STOKE_TRUSTPLANE_PRIVKEY_FILE` must resolve to a non-empty value in
real mode. Inline wins over file (per `internal/secrets` resolution
order). Whitespace is trimmed from every source, so operators can
paste values with trailing newlines from `echo token > file` without
corruption.

Default (everything unset): `StubClient` — zero network, always-pass,
safe for local dev, CI unit tests, and the zero-configuration
`stoke-mcp` startup.

## DPoP (RFC 9449)

Every RealClient request carries a DPoP proof-of-possession header: a
short JWT signed with the caller's Ed25519 private key, embedding the
caller's public JWK, the HTTP method (`htm`), the full request URI
(`htu`), a fresh random `jti` nonce, and an `iat` timestamp. The
gateway verifies the signature against the JWK, checks that the JWK's
thumbprint matches the public key registered at identity creation,
and rejects `jti` replays inside a 5-minute window.

R1's DPoP signer is `internal/trustplane/dpop`, Go-stdlib-only
(`crypto/ed25519`, `encoding/base64`, `encoding/json`). No go-jose
dependency — EdDSA signing is 50 lines in-tree.

What the signer does **not** do yet:

- `ath` (access-token hash): R1 uses DPoP-only flows, no bound
  access tokens. Add `Signer.WithAccessToken` if/when needed.
- `DPoP-Nonce` echo: the gateway may demand a nonce on retry. No
  gateway we've tested requires it; plumb via `Signer.WithNonce`
  when operationally needed.

## Generating an identity key

```
openssl genpkey -algorithm ed25519 -out /etc/stoke/trustplane-priv.pem
chmod 600 /etc/stoke/trustplane-priv.pem

export STOKE_TRUSTPLANE_MODE=real
export STOKE_TRUSTPLANE_URL=https://gateway.trustplane.dev
export STOKE_TRUSTPLANE_PRIVKEY_FILE=/etc/stoke/trustplane-priv.pem
```

The public half of the key is submitted to the gateway at identity
registration (the first `RegisterIdentity` call). Key rotation =
re-register the identity with a new key; existing delegations issued
from the old key remain valid until expiry.

## Error handling

`RealClient` returns three kinds of error:

1. **Sentinels** for branch-worthy states:
   - `trustplane.ErrPolicyDenied` — policy evaluation returned deny.
   - `trustplane.ErrDelegationInvalid` — delegation is revoked,
     expired, or over-scoped.
2. **`*trustplane.httpError`** (internal type; reach via
   `errors.As(err, &he)`) for any other non-2xx response, carrying
   `Status`, `Method`, `URL`, and truncated `Body` for diagnostics.
3. **Plain `error`** for pre-flight problems: marshalling,
   network/transport failures, DPoP signing errors. These wrap the
   underlying cause so `errors.Is(err, io.EOF)` / similar continue
   to work.

`StubClient` returns the same sentinels (`ErrPolicyDenied` on empty
bundle name, `ErrDelegationInvalid` on revoked/expired/unknown ID) so
callers can write a single error-handling path that works against
both clients.

## Retry policy

None, by design. TrustPlane's write surface (delegation create /
revoke, reputation record, audit anchor) is not universally
idempotent; retry policy belongs in the caller, where it can reason
about side effects. GET paths (reputation lookup, delegation verify)
can be retried freely by the caller on transport errors.

## Updating the vendored spec

The OpenAPI YAML at `internal/trustplane/openapi/gateway.yaml` is
hand-maintained. When TrustPlane ships a gateway change R1
consumes:

1. Edit `gateway.yaml` to match the new contract.
2. Update the corresponding method on `RealClient`
   (`internal/trustplane/real.go`).
3. Add a test in `real_test.go` that uses `httptest.NewServer` to
   verify the new request shape and response decoding.
4. Update this doc with any new env var, error sentinel, or
   behavioral note.

The spec is intentionally a small slice of the TrustPlane API —
only the endpoints R1 calls. Don't add endpoints we don't
implement; don't remove endpoints RealClient uses.
