package daemondisco

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// TokenBytes is the size of the token entropy buffer before hex
// encoding. The spec requires 32 random bytes; the on-the-wire form
// is therefore 64 hex characters.
const TokenBytes = 32

// MintToken returns a fresh, hex-encoded bearer token suitable for
// use as the daemon's `Authorization: Bearer ...` value (and the
// second value in the WS subprotocol list).
//
// The implementation reads 32 random bytes from `crypto/rand` and
// hex-encodes them. The token is **not persisted by this package**:
// callers (the `r1 serve` startup path) must invoke MintToken once
// per daemon process and store the result in the discovery file
// (mode 0600) plus their in-process auth state. A new daemon
// invocation always means a new token — old clients fail with 401
// and re-read `~/.r1/daemon.json` to refresh.
//
// `crypto/rand.Read` returns an error on entropy-source failure
// (very rare on modern OSes, but possible in chroot/sandbox setups
// without `/dev/urandom`). The error is wrapped so callers can
// distinguish it from JSON or filesystem failures.
func MintToken() (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("daemondisco: mint token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
