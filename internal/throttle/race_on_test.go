//go:build race

package throttle_test

// underRace is true when the test binary is built with -race. The race
// detector serializes memory access and injects large scheduling jitter, which
// makes the two-tier ReserveN/CancelAt rate-limiter admit approximately under
// high contention — so timing-tight numeric bounds are relaxed under -race
// (see TestConcurrentSafety). The data-race guarantee is still enforced by the
// detector itself.
const underRace = true
