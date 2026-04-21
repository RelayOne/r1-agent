package engine

import "crypto/rand"

// randReadImpl is the default implementation for newShortID; split
// into its own file so test code can replace cryptoRandRead with a
// deterministic stub.
func randReadImpl(p []byte) (int, error) { return rand.Read(p) }
