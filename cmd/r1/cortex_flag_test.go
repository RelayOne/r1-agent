package main

import (
	"flag"
	"testing"
)

// resolveCortex mirrors the BuildConfig.CortexEnabled resolution used at the
// build + sow flag sites: `*cortex && !*noCortex` (default-on with kill-switch).
func resolveCortex(args []string) bool {
	fs := flag.NewFlagSet("cortex-test", flag.ContinueOnError)
	cortexEnabled := fs.Bool("cortex", true, "")
	noCortex := fs.Bool("no-cortex", false, "")
	_ = fs.Parse(args)
	return *cortexEnabled && !*noCortex
}

// TestCortexFlag_Default: with no flags, the cortex is ON (default-on).
func TestCortexFlag_Default(t *testing.T) {
	if !resolveCortex(nil) {
		t.Fatal("default (no flags) resolved cortex OFF; want ON (default-on)")
	}
}

// TestCortexFlag_NoCortex: --no-cortex is the kill-switch and wins.
func TestCortexFlag_NoCortex(t *testing.T) {
	if resolveCortex([]string{"--no-cortex"}) {
		t.Fatal("--no-cortex resolved cortex ON; want OFF (kill-switch)")
	}
}

// TestCortexFlag_Explicit: --cortex=false also disables.
func TestCortexFlag_Explicit(t *testing.T) {
	if resolveCortex([]string{"--cortex=false"}) {
		t.Fatal("--cortex=false resolved cortex ON; want OFF")
	}
}
