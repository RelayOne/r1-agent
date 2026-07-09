// Package seed is R1's file-backed seed/context injection resolver.
//
// It implements the canonical honestcrypto.SeedResolver seam (MECHANISM ONLY):
// given a profile name it assembles tiered seed text — T0 role -> T1 domain ->
// T2 task — and returns a prefix the caller PREPENDS to a system prompt (never
// replaces it). Only the assembly rule is committed here; the corpus content is
// per-tenant and gitignored (see seeds/.gitignore), except the checked-in
// seeds/EXAMPLE/ profile that documents the layout.
package seed

import (
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// tierFiles maps each tier to the candidate filenames looked up under
// <seedsPath>/<profile>/. The first existing file for a tier wins. Tiers are
// assembled in this fixed order: role (T0) -> domain (T1) -> task (T2).
var tierFiles = []struct {
	tier  string
	names []string
}{
	{"role", []string{"role.md", "role.txt"}},
	{"domain", []string{"domain.md", "domain.txt"}},
	{"task", []string{"task.md", "task.txt"}},
}

// FileSeedResolver resolves profiles from a seeds directory on disk. The layout
// is <seedsPath>/<profile>/{role,domain,task}.md — any tier file may be absent
// and is skipped.
type FileSeedResolver struct {
	seedsPath string
}

// NewFileSeedResolver builds a resolver rooted at seedsPath.
func NewFileSeedResolver(seedsPath string) *FileSeedResolver {
	return &FileSeedResolver{seedsPath: seedsPath}
}

// Backend returns the stable backend name.
func (r *FileSeedResolver) Backend() string { return "file" }

// Resolve assembles the profile's tiers (role -> domain -> task), skipping any
// tier whose file is absent or blank, joined by a blank line. An unknown profile
// or unreadable path yields an empty prefix and never errors — the caller's
// original prompt is preserved.
func (r *FileSeedResolver) Resolve(profile string, _ honestcrypto.SeedContext) honestcrypto.SeedResult {
	tiers := honestcrypto.SeedTiers{}
	if r.seedsPath != "" && profile != "" {
		dir := filepath.Join(r.seedsPath, profile)
		tiers.Role = readFirst(dir, tierFiles[0].names)
		tiers.Domain = readFirst(dir, tierFiles[1].names)
		tiers.Task = readFirst(dir, tierFiles[2].names)
	}
	// Reuse the canonical assembly rule so the file backend and the in-memory
	// default assemble identically.
	return honestcrypto.NewStaticSeedResolver(map[string]honestcrypto.SeedTiers{profile: tiers}).Resolve(profile, nil)
}

// readFirst returns the trimmed content of the first existing candidate file
// under dir, or "" if none exists / all are blank.
func readFirst(dir string, names []string) string {
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if s := trimTrailing(string(b)); s != "" {
			return s
		}
	}
	return ""
}

// trimTrailing trims trailing whitespace/newlines while preserving internal
// formatting of a tier's body.
func trimTrailing(s string) string {
	end := len(s)
	for end > 0 {
		c := s[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			end--
			continue
		}
		break
	}
	start := 0
	for start < end {
		c := s[start]
		if c == '\n' || c == '\r' {
			start++
			continue
		}
		break
	}
	return s[start:end]
}

// ResolvePrefix is the convenience the runtime calls: resolve profile from
// seedsPath and return the prefix to prepend. Empty profile or path yields "".
func ResolvePrefix(profile, seedsPath string) string {
	if profile == "" || seedsPath == "" {
		return ""
	}
	return NewFileSeedResolver(seedsPath).Resolve(profile, nil).Prefix
}
