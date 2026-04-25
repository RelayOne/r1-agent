// Package skill — manifest.go
//
// STOKE-003 backfill: every shipped builtin skill gets a
// validated Capability Manifest so the skillmfr registry is
// non-empty out of the box. Without this, a fresh Stoke
// install has 61 builtin skills callable via DefaultRegistry
// but zero manifests registered — meaning every skill looks
// "unregistered" to the dispatcher's drift-detection logic.
//
// Derivation rules (keep it strict — we'd rather generate a
// narrow manifest than a wrong one):
//
//   - Name         → Skill.Name
//   - Version      → "builtin-1.0.0" (all shipped builtins
//                    share a version; operator-authored user
//                    skills carry their own version when
//                    set via frontmatter, which we respect)
//   - Description  → Skill.Description (required non-empty;
//                    for skills missing a description,
//                    derive from the first non-empty line
//                    of Content)
//   - InputSchema  → generic `{type: object, properties: {
//                    context: {type: string}}}` — skills
//                    take free-form context rather than a
//                    structured payload
//   - OutputSchema → generic `{type: object, properties: {
//                    guidance: {type: string}}}`
//   - WhenToUse    → derived from Triggers + Keywords (at
//                    least one non-empty phrase required;
//                    fallback to the description when the
//                    skill had neither)
//   - WhenNotToUse → two stock entries per skill (SOW floor):
//                    "when the task is outside this skill's
//                    domain" and "when a more specific skill
//                    applies" — operators customize by
//                    editing the manifest post-registration
//   - BehaviorFlags.MutatesState = false (skills are guidance)
//   - BehaviorFlags.RequiresNetwork = false
//   - BehaviorFlags.CostCategory = "free"
//
// The backfill is idempotent — re-running against a registry
// that already has a manifest for the skill skips the
// re-register (Register would succeed either way since
// Validate is the same, but we avoid spurious
// index-rebuilds).
package skill

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1-agent/internal/skillmfr"
)

// builtinManifestVersion is the shared version stamped onto
// every shipped builtin skill's manifest. Upgrades bump this
// when the shipped library changes in a way that should
// invalidate pinned-version callers.
const builtinManifestVersion = "builtin-1.0.0"

// defaultInputSchema is the generic input shape used for
// every skill manifest. Skills are guidance patterns, not
// structured RPCs, so the "input" is a freeform context
// string the dispatcher passes through.
var defaultInputSchema = json.RawMessage(`{"type":"object","properties":{"context":{"type":"string"}},"required":["context"]}`)

// defaultOutputSchema mirrors defaultInputSchema on the
// emit side.
var defaultOutputSchema = json.RawMessage(`{"type":"object","properties":{"guidance":{"type":"string"}},"required":["guidance"]}`)

// defaultWhenNotToUse is the minimum two-entry floor from
// the SOW. Operators override per-skill by editing the
// manifest after the initial backfill.
var defaultWhenNotToUse = []string{
	"when the task is outside this skill's stated domain",
	"when a more specific skill in the registry is a better fit",
}

// ToManifest returns a validated skillmfr.Manifest derived
// from the skill. Returns an error only when the resulting
// manifest fails skillmfr.Manifest.Validate — which should
// not happen for a well-formed skill (Name + Description
// are always set by parseSkill), but the error path exists
// so callers can detect malformed input skills rather than
// panicking.
func (s *Skill) ToManifest() (skillmfr.Manifest, error) {
	if s == nil {
		return skillmfr.Manifest{}, fmt.Errorf("skill: ToManifest on nil skill")
	}
	desc := strings.TrimSpace(s.Description)
	if desc == "" {
		desc = firstNonEmptyLine(s.Content)
	}
	if desc == "" {
		desc = fmt.Sprintf("Built-in skill: %s", s.Name)
	}
	when := deriveWhenToUse(s)
	if len(when) == 0 {
		// Fallback: use the description as the single when-to-use
		// entry so the SOW's "≥1" floor is satisfied.
		when = []string{desc}
	}
	m := skillmfr.Manifest{
		Name:         s.Name,
		Version:      builtinManifestVersion,
		Description:  desc,
		InputSchema:  defaultInputSchema,
		OutputSchema: defaultOutputSchema,
		WhenToUse:    when,
		WhenNotToUse: defaultWhenNotToUse,
		BehaviorFlags: skillmfr.BehaviorFlags{
			MutatesState:    false,
			RequiresNetwork: false,
			CostCategory:    "free",
		},
	}
	if err := m.Validate(); err != nil {
		return skillmfr.Manifest{}, fmt.Errorf("derived manifest for skill %q invalid: %w", s.Name, err)
	}
	return m, nil
}

// BackfillManifests registers a manifest for every skill in
// the registry that the manifest registry doesn't already
// have. Returns (registered, skipped, aggregatedErr).
// aggregatedErr collects every per-skill failure (derivation
// or registration) so callers see the full diagnostic set
// rather than just the first one.
//
// Intended call sites:
//   - cmd/stoke-mcp main() at startup — seeds the manifest
//     registry with every builtin so stoke_invoke treats
//     any shipped skill name as registered
//   - config.LoadSkills caller — batched backfill after
//     Load so user-added skills also get manifests
func BackfillManifests(sr *Registry, mr *skillmfr.Registry) (registered, skipped int, aggErr error) {
	if sr == nil || mr == nil {
		return 0, 0, fmt.Errorf("skill: BackfillManifests: nil registry")
	}
	// Snapshot the skill map under the read lock so we don't
	// hold it across validates + registry writes.
	sr.mu.RLock()
	skills := make([]*Skill, 0, len(sr.skills))
	for _, sk := range sr.skills {
		skills = append(skills, sk)
	}
	sr.mu.RUnlock()

	var failures []string
	for _, sk := range skills {
		if _, have := mr.Get(sk.Name); have {
			skipped++
			continue
		}
		m, err := sk.ToManifest()
		if err != nil {
			failures = append(failures, fmt.Sprintf("derive %q: %v", sk.Name, err))
			continue
		}
		if err := mr.Register(m); err != nil {
			failures = append(failures, fmt.Sprintf("register %q: %v", sk.Name, err))
			continue
		}
		registered++
	}
	if len(failures) > 0 {
		aggErr = fmt.Errorf("skill: BackfillManifests: %d skill(s) failed:\n  - %s",
			len(failures), joinLines(failures))
	}
	return registered, skipped, aggErr
}

// joinLines is strings.Join with a per-line indent so
// multi-error output renders readably in terminal logs.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0] + func() string {
		if len(lines) == 1 {
			return ""
		}
		out := ""
		for _, l := range lines[1:] {
			out += "\n  - " + l
		}
		return out
	}()
}

// deriveWhenToUse concatenates a skill's Triggers + Keywords
// into distinct free-form phrases. Triggers first because
// they're higher-signal (explicit frontmatter entries);
// keywords second (auto-extracted).
func deriveWhenToUse(s *Skill) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	for _, t := range s.Triggers {
		add(t)
	}
	for _, k := range s.Keywords {
		add(k)
	}
	return out
}

// firstNonEmptyLine returns the first non-blank line of s,
// trimming leading "> " blockquote markers from Stoke's skill
// markdown convention.
func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		t = strings.TrimPrefix(t, "# ")
		t = strings.TrimPrefix(t, "> ")
		t = strings.TrimSpace(t)
		if t != "" {
			return t
		}
	}
	return ""
}
