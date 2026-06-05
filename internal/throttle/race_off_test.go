//go:build !race

package throttle_test

// underRace is false in normal (non-race) builds, so TestConcurrentSafety
// applies its strict steady-state rate bound and would catch a real
// over-admission regression. See race_on_test.go for the -race variant.
const underRace = false
