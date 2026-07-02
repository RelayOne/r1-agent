//go:build !race

package throttle_test

// underRace is false in normal (non-race) builds. See race_on_test.go
// for the -race variant. It is currently unreferenced scaffolding: the
// strict steady-state rate bound it once gated was dropped from
// TestConcurrentSafety, which asserts only data-race freedom and
// liveness — no numeric admission bound, race detector or not (see the
// comment in bucket_test.go). Precise burst/refill bounds are covered
// deterministically by the serial TestBurstThenDeny and TestRefill.
const underRace = false
