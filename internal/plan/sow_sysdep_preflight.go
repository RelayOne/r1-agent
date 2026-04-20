package plan

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// PreflightSystemDeps is H-69: detect OS-level binaries referenced by
// SOW acceptance criteria and install them when possible.
//
// Background: H-65 handles workspace-level npm/pnpm devDependencies
// (tsc, vitest, next, …), but planners also write ACs that invoke
// system binaries — docker, docker-compose, psql, redis-cli, curl,
// jq, etc. When those aren't on PATH the AC fails with exit 127 or
// "command not found" and the harness enters a repair loop that can't
// fix anything (the worker also can't install system packages from
// inside a sandboxed run). R08-sentinel-full's AC2 today failed
// because docker-compose wasn't available; the worker iterated three
// times writing alternate commands before escalating.
//
// H-69 runs ONCE before session dispatch:
//   1. Scan every AC command + the SOW prose for references to
//      known system binaries.
//   2. Check each against $PATH via exec.LookPath.
//   3. For any missing binary, emit a diagnostic. If
//      STOKE_SYSDEP_INSTALL=1 and apt-get is available, attempt
//      to install the matching Debian package. ("Need sudo?" is
//      decided by whether the current process can write to
//      /var/lib/apt/lists — if yes, run apt without sudo; else
//      use sudo -n which fails fast if no passwordless sudo.)
//   4. If installation fails OR the env flag is off, the diagnostic
//      tells the operator what's missing so they can install it
//      manually.
//
// Returns a diagnostic slice. Non-fatal on every intermediate error.
func PreflightSystemDeps(sow *SOW) []string {
	if sow == nil {
		return nil
	}
	needed := collectSystemBinaries(sow)
	if len(needed) == 0 {
		return nil
	}

	var missing []string
	for _, bin := range needed {
		if _, err := exec.LookPath(bin); err == nil {
			continue
		}
		missing = append(missing, bin)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)

	var diag []string
	diag = append(diag, fmt.Sprintf("sysdep preflight: ACs reference %d system binary/binaries not on $PATH: %s", len(missing), strings.Join(missing, ", ")))

	shouldInstall := os.Getenv("STOKE_SYSDEP_INSTALL") == "1"
	if !shouldInstall {
		diag = append(diag, "sysdep preflight: STOKE_SYSDEP_INSTALL=1 is not set; not auto-installing. Set it to have H-69 attempt `apt-get install` of missing packages.")
		return diag
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		diag = append(diag, "sysdep preflight: apt-get not available; can't auto-install. Operator must install the missing binaries manually.")
		return diag
	}

	var toInstall []string
	for _, bin := range missing {
		if pkg, ok := systemBinaryToDebianPackage[bin]; ok {
			toInstall = append(toInstall, pkg)
		} else {
			diag = append(diag, fmt.Sprintf("sysdep preflight: no known Debian package maps to binary %q — skipping", bin))
		}
	}
	if len(toInstall) == 0 {
		return diag
	}

	// Deduplicate package names (multiple binaries can share one package).
	seen := map[string]bool{}
	uniq := toInstall[:0]
	for _, p := range toInstall {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}

	args := append([]string{"install", "-y", "--no-install-recommends"}, uniq...)
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command("apt-get", args...)
	} else {
		cmd = exec.Command("sudo", append([]string{"-n", "apt-get"}, args...)...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		diag = append(diag, fmt.Sprintf("sysdep preflight: apt-get install failed (%v); missing binaries remain: %s. Output tail: %s",
			err, strings.Join(uniq, ", "), tailLines(out, 6)))
		return diag
	}
	diag = append(diag, fmt.Sprintf("sysdep preflight: installed %s via apt-get", strings.Join(uniq, ", ")))
	return diag
}

// Known system binary → Debian package map. Covers the binaries that
// scope-suite rungs have actually invoked (docker, postgres, redis)
// plus a handful of commonly-scripted tools (jq, curl). Additions
// should be evidence-based — see the failing-AC log tail to justify
// a new entry before adding it.
var systemBinaryToDebianPackage = map[string]string{
	"docker":         "docker.io",
	"docker-compose": "docker-compose",
	"psql":           "postgresql-client",
	"pg_isready":     "postgresql-client",
	"redis-cli":      "redis-tools",
	"mysql":          "default-mysql-client",
	"sqlite3":        "sqlite3",
	"jq":             "jq",
	"curl":           "curl",
	"wget":           "wget",
	"git":            "git",
	"make":           "make",
	"gcc":            "gcc",
	"g++":            "g++",
	"python3":        "python3",
	"pip":            "python3-pip",
	"pip3":           "python3-pip",
	"node":           "nodejs",
	"npm":            "npm",
}

// sysBinaryRE captures the first word of the AC command plus any
// occurrence of a known system binary elsewhere in the command string
// (for pipelines like `docker ps | grep ...`). We don't enumerate
// every binary up front — we scan against the keyset of
// systemBinaryToDebianPackage since that's the authoritative list we
// know how to install.
var sysBinaryFirstWordRE = regexp.MustCompile(`(?:^|&&\s*|;\s*|\|\s*|\|\|\s*)\s*([a-zA-Z][a-zA-Z0-9_.-]*)`)

func collectSystemBinaries(sow *SOW) []string {
	seen := map[string]bool{}
	consider := func(tok string) {
		if _, ok := systemBinaryToDebianPackage[tok]; ok {
			seen[tok] = true
		}
	}
	scan := func(s string) {
		if s == "" {
			return
		}
		for _, m := range sysBinaryFirstWordRE.FindAllStringSubmatch(s, -1) {
			consider(m[1])
		}
	}
	for _, sess := range sow.Sessions {
		for _, ac := range sess.AcceptanceCriteria {
			scan(ac.Command)
			scan(ac.Description)
		}
	}
	// Also scan the SOW prose / description — some planners embed
	// instructions like "run `docker-compose up -d`" in task text.
	scan(sow.Description)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
