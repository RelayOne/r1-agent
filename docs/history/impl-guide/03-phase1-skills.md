# 03 — Phase 1: Skills (the foundation)

This phase wires Stoke's existing skill registry into the prompt pipeline (it currently is not), adds a new repo-detection package, and makes skills selectable based on what tech the repo actually uses.

## Current state (read this first)

The Stoke codebase already has `internal/skill/registry.go` with:
- `Skill` struct (Name, Description, Keywords, Content, Source, Path, Priority)
- `Registry` with `Load`, `Get`, `List`, `Match`, `MatchOne`, `InjectPrompt`, `Add`, `Remove`, `SuggestSimilar`
- HTML comment-based keyword parsing (`<!-- keywords: foo, bar -->`)
- Levenshtein-based fuzzy matching

**The critical gap:** The registry is fully implemented but **never instantiated**. `InjectPrompt` is defined but nobody calls it. Verify this yourself:

```bash
grep -rn "InjectPrompt\|skill\.NewRegistry\|skill\.DefaultRegistry" internal/ | grep -v "_test.go" | grep -v "internal/skill/"
```

You should see zero results outside `internal/skill/`. The registry is an island.

## What you're building in this phase

1. **Rewrite `parseSkill`** to support YAML frontmatter + the existing HTML comment format
2. **Add `InjectPromptBudgeted`** that respects a token budget and uses tiered inclusion
3. **Add `UpdateFromResearch`** so research findings can update skills
4. **Add a `References` map** to `Skill` so progressive disclosure works
5. **Create `internal/skillselect` package** for repo tech stack detection
6. **Wire `skill.Registry` into `OrchestratorConfig` and `workflow.Engine`**
7. **Inject skills at the four prompt construction points** in `workflow.go` and `prompts/mission.go`
8. **Add `skills:` config section** to `internal/config`

---

## Step 1: Rewrite `parseSkill` for YAML frontmatter

**File:** `internal/skill/registry.go`

Add a new `Skill` field for references:

```go
type Skill struct {
    Name          string            `json:"name"`
    Description   string            `json:"description"`
    Keywords      []string          `json:"keywords"`
    Triggers      []string          `json:"triggers"`        // NEW — explicit trigger phrases from frontmatter
    AllowedTools  []string          `json:"allowed_tools"`   // NEW — tool whitelist from frontmatter
    Content       string            `json:"content"`
    Gotchas       string            `json:"gotchas"`         // NEW — extracted "Gotchas" section
    References    map[string]string `json:"references"`      // NEW — filename → content for progressive disclosure
    Source        string            `json:"source"`
    Path          string            `json:"path"`
    Priority      int               `json:"priority"`
    EstTokens     int               `json:"est_tokens"`      // NEW — estimated token count for budgeting
    EstGotchaTokens int             `json:"est_gotcha_tokens"` // NEW — token count of just the Gotchas section
}
```

Replace `parseSkill` with this implementation:

```go
// parseSkill extracts metadata from skill content. Supports both YAML frontmatter
// (Trail of Bits / Claude Code SKILL.md format) and the legacy HTML comment format.
func parseSkill(name, content, source, path string, priority int) *Skill {
    s := &Skill{
        Name:       name,
        Content:    content,
        Source:     source,
        Path:       path,
        Priority:   priority,
        References: make(map[string]string),
    }

    body := content

    // Parse YAML frontmatter if present (--- delimited block at start)
    if strings.HasPrefix(content, "---\n") {
        end := strings.Index(content[4:], "\n---\n")
        if end > 0 {
            frontmatter := content[4 : 4+end]
            body = content[4+end+5:]
            parseFrontmatter(frontmatter, s)
        }
    }

    // Extract description from first blockquote (if not set by frontmatter)
    if s.Description == "" {
        for _, line := range strings.Split(body, "\n") {
            if strings.HasPrefix(line, "> ") {
                s.Description = strings.TrimPrefix(line, "> ")
                break
            }
        }
    }

    // Extract keywords from HTML comment (legacy format, still supported)
    if len(s.Keywords) == 0 {
        for _, line := range strings.Split(body, "\n") {
            if strings.Contains(line, "<!-- keywords:") {
                kw := strings.TrimPrefix(line, "<!-- keywords:")
                kw = strings.TrimSuffix(kw, "-->")
                kw = strings.TrimSpace(kw)
                for _, k := range strings.Split(kw, ",") {
                    k = strings.TrimSpace(k)
                    if k != "" {
                        s.Keywords = append(s.Keywords, k)
                    }
                }
                break
            }
        }
    }

    // Fallback: derive keywords from name + description words
    if len(s.Keywords) == 0 {
        s.Keywords = append(s.Keywords, name)
        for _, w := range strings.Fields(s.Description) {
            if len(w) > 4 {
                s.Keywords = append(s.Keywords, strings.ToLower(w))
            }
        }
    }

    // Triggers (explicit) merge into keywords for matching
    for _, t := range s.Triggers {
        s.Keywords = append(s.Keywords, strings.ToLower(t))
    }

    // Extract Gotchas section for compressed injection
    s.Gotchas = extractSection(body, "Gotchas")

    // Estimate tokens (rough: 1 token ≈ 4 characters for English)
    s.EstTokens = len(body) / 4
    s.EstGotchaTokens = len(s.Gotchas) / 4

    return s
}

// parseFrontmatter is a minimal YAML frontmatter parser for SKILL.md files.
// It handles only the fields we care about: name, description, triggers, allowed-tools.
// Use a real YAML parser only if we need more.
func parseFrontmatter(fm string, s *Skill) {
    var inList string
    for _, line := range strings.Split(fm, "\n") {
        line = strings.TrimRight(line, "\r")
        if line == "" {
            inList = ""
            continue
        }

        if inList != "" {
            // Continuation of a list field
            trimmed := strings.TrimSpace(line)
            if strings.HasPrefix(trimmed, "- ") {
                v := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
                v = strings.Trim(v, `"'`)
                switch inList {
                case "triggers":
                    s.Triggers = append(s.Triggers, v)
                case "allowed-tools":
                    s.AllowedTools = append(s.AllowedTools, v)
                case "keywords":
                    s.Keywords = append(s.Keywords, v)
                }
                continue
            }
            inList = ""
        }

        idx := strings.Index(line, ":")
        if idx <= 0 {
            continue
        }
        key := strings.TrimSpace(line[:idx])
        val := strings.TrimSpace(line[idx+1:])
        val = strings.Trim(val, `"'`)

        switch key {
        case "name":
            if val != "" {
                s.Name = val
            }
        case "description":
            if val != "" {
                s.Description = val
            }
        case "triggers":
            if val != "" {
                // Inline list: triggers: [foo, bar]
                inline := strings.TrimSpace(val)
                if strings.HasPrefix(inline, "[") && strings.HasSuffix(inline, "]") {
                    inner := strings.TrimSuffix(strings.TrimPrefix(inline, "["), "]")
                    for _, v := range strings.Split(inner, ",") {
                        v = strings.TrimSpace(v)
                        v = strings.Trim(v, `"'`)
                        if v != "" {
                            s.Triggers = append(s.Triggers, v)
                        }
                    }
                }
            } else {
                inList = "triggers"
            }
        case "allowed-tools":
            if val == "" {
                inList = "allowed-tools"
            }
        case "keywords":
            if val == "" {
                inList = "keywords"
            }
        }
    }
}

// extractSection finds a section by H2 header name and returns its body up to
// the next H2 or end of document. Returns empty string if not found.
func extractSection(body, name string) string {
    lines := strings.Split(body, "\n")
    var out []string
    inSection := false
    headerLower := strings.ToLower("## " + name)
    for _, line := range lines {
        if strings.HasPrefix(strings.ToLower(line), headerLower) {
            inSection = true
            continue
        }
        if inSection {
            if strings.HasPrefix(line, "## ") {
                break
            }
            out = append(out, line)
        }
    }
    return strings.TrimSpace(strings.Join(out, "\n"))
}
```

**Update `Load`** to also recognize `SKILL.md` (Trail of Bits / Claude Code convention) alongside `index.md` (existing Stoke convention):

```go
// In Load(), in the entry.IsDir() branch, replace the indexPath line with:
candidates := []string{
    filepath.Join(dir, name, "SKILL.md"),  // Trail of Bits / Claude Code
    filepath.Join(dir, name, "index.md"),  // legacy Stoke
}
var content []byte
var skillPath string
for _, c := range candidates {
    if data, err := os.ReadFile(c); err == nil {
        content = data
        skillPath = c
        break
    }
}
if skillPath == "" {
    continue
}
```

**Add reference loading** at the end of the directory branch (after parseSkill):

```go
// Load progressive disclosure references from references/ subdir
refsDir := filepath.Join(dir, name, "references")
if entries, err := os.ReadDir(refsDir); err == nil {
    for _, ref := range entries {
        if !ref.IsDir() && strings.HasSuffix(ref.Name(), ".md") {
            if data, err := os.ReadFile(filepath.Join(refsDir, ref.Name())); err == nil {
                key := strings.TrimSuffix(ref.Name(), ".md")
                r.skills[name].References[key] = string(data)
            }
        }
    }
}
```

---

## Step 2: Add `InjectPromptBudgeted`

```go
// InjectionTier classifies how much of a skill to include.
type InjectionTier int

const (
    TierFull       InjectionTier = iota // entire content
    TierGotchas                          // gotchas section only
    TierName                             // just name + description
)

// SkillSelection represents a chosen skill with how it should be rendered.
type SkillSelection struct {
    Skill *Skill
    Tier  InjectionTier
    Reason string  // for audit/debug: why was this selected
}

// InjectPromptBudgeted selects skills for the given prompt + repo profile, respecting
// the token budget. Returns the augmented prompt with skills injected.
//
// Selection priority (in order, until budget exhausted):
//   1. Always-on skills (tagged via stack=*always or matching "agent-discipline")
//   2. Repo-stack-matched skills (top 2, full content) — passed via stackMatches
//   3. Keyword-matched skills (top 3, gotchas only)
//
// The returned prompt has the following structure:
//   <skills>
//     ## Skill: agent-discipline
//     ...full content...
//     ---
//     ## Skill: postgres
//     ...full content...
//     ---
//     ## Skill: kafka (gotchas)
//     ...gotchas only...
//   </skills>
//   {original prompt}
func (r *Registry) InjectPromptBudgeted(prompt string, stackMatches []string, tokenBudget int) (string, []SkillSelection) {
    if tokenBudget <= 0 {
        tokenBudget = 3000
    }

    r.mu.RLock()
    defer r.mu.RUnlock()

    used := 0
    selected := []SkillSelection{}
    seen := make(map[string]bool)

    // Helper to add a skill if it fits in budget
    add := func(s *Skill, tier InjectionTier, reason string) {
        if seen[s.Name] {
            return
        }
        cost := s.EstTokens
        if tier == TierGotchas {
            cost = s.EstGotchaTokens
        } else if tier == TierName {
            cost = (len(s.Name) + len(s.Description)) / 4
        }
        if used+cost > tokenBudget {
            return
        }
        used += cost
        seen[s.Name] = true
        selected = append(selected, SkillSelection{Skill: s, Tier: tier, Reason: reason})
    }

    // Tier 1: always-on
    for _, s := range r.skills {
        if s.Name == "agent-discipline" || hasKeyword(s.Keywords, "always") {
            add(s, TierFull, "always-on")
        }
    }

    // Tier 2: repo stack matches (top 2, full content)
    stackCount := 0
    for _, name := range stackMatches {
        if stackCount >= 2 {
            break
        }
        if s := r.skills[name]; s != nil {
            add(s, TierFull, "repo-stack")
            stackCount++
        }
    }

    // Tier 3: keyword matches (top 3, gotchas only)
    matches := r.matchInternal(prompt)
    keywordCount := 0
    for _, s := range matches {
        if keywordCount >= 3 {
            break
        }
        if s.EstGotchaTokens == 0 {
            continue // skip skills without gotchas section in tier 3
        }
        add(s, TierGotchas, "keyword-match")
        keywordCount++
    }

    if len(selected) == 0 {
        return prompt, nil
    }

    var sb strings.Builder
    sb.WriteString("<skills>\n")
    for _, sel := range selected {
        sb.WriteString(fmt.Sprintf("## Skill: %s\n\n", sel.Skill.Name))
        switch sel.Tier {
        case TierFull:
            sb.WriteString(sel.Skill.Content)
        case TierGotchas:
            sb.WriteString("(gotchas only)\n\n")
            sb.WriteString(sel.Skill.Gotchas)
        case TierName:
            sb.WriteString(sel.Skill.Description)
        }
        sb.WriteString("\n\n---\n\n")
    }
    sb.WriteString("</skills>\n\n")
    sb.WriteString(prompt)

    return sb.String(), selected
}

// matchInternal is the unlocked version of Match. Caller must hold r.mu.RLock().
func (r *Registry) matchInternal(text string) []*Skill {
    lower := strings.ToLower(text)
    var matches []*Skill
    for _, s := range r.skills {
        for _, kw := range s.Keywords {
            if strings.Contains(lower, strings.ToLower(kw)) {
                matches = append(matches, s)
                break
            }
        }
    }
    sort.Slice(matches, func(i, j int) bool {
        return matches[i].Priority > matches[j].Priority
    })
    return matches
}

func hasKeyword(keywords []string, target string) bool {
    for _, k := range keywords {
        if k == target {
            return true
        }
    }
    return false
}
```

---

## Step 3: Add `UpdateFromResearch`

Skills should improve when research returns new findings.

```go
// UpdateFromResearch scans research entries and merges findings into matching skills.
// Research with category=gotcha → appends to skill Gotchas section.
// Research with category=pattern → appends to skill body.
// Returns the list of updates made for logging/audit.
func (r *Registry) UpdateFromResearch(entries []research.Entry) ([]SkillUpdate, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    var updates []SkillUpdate
    for _, entry := range entries {
        // Match research to skill by topic or tag
        targetSkill := r.findSkillForResearch(entry)
        if targetSkill == nil {
            continue
        }

        // Skip if already merged (idempotent on entry ID)
        if r.researchAlreadyMerged(targetSkill, entry.ID) {
            continue
        }

        // Determine what section to update
        section := "Patterns"
        for _, tag := range entry.Tags {
            if strings.EqualFold(tag, "gotcha") || strings.EqualFold(tag, "failure") {
                section = "Gotchas"
                break
            }
        }

        if err := r.appendToSkillSection(targetSkill, section, entry); err != nil {
            return updates, fmt.Errorf("update skill %s: %w", targetSkill.Name, err)
        }

        updates = append(updates, SkillUpdate{
            SkillName: targetSkill.Name,
            EntryID:   entry.ID,
            Section:   section,
            Added:     entry.Content[:min(200, len(entry.Content))],
        })
    }

    return updates, nil
}

type SkillUpdate struct {
    SkillName string
    EntryID   string
    Section   string
    Added     string
}

func (r *Registry) findSkillForResearch(entry research.Entry) *Skill {
    // Match topic against skill name first
    if s := r.skills[entry.Topic]; s != nil {
        return s
    }
    // Match tags against skill names
    for _, tag := range entry.Tags {
        if s := r.skills[tag]; s != nil {
            return s
        }
    }
    // Match query keywords against skill keywords
    matches := r.matchInternal(entry.Query)
    if len(matches) > 0 {
        return matches[0]
    }
    return nil
}

func (r *Registry) researchAlreadyMerged(s *Skill, entryID string) bool {
    return strings.Contains(s.Content, fmt.Sprintf("[research:%s]", entryID))
}

func (r *Registry) appendToSkillSection(s *Skill, section string, entry research.Entry) error {
    // Read current content
    content, err := os.ReadFile(s.Path)
    if err != nil {
        return err
    }

    addition := fmt.Sprintf("\n- %s [research:%s]\n", strings.TrimSpace(entry.Content), entry.ID)

    // Find the section header in the content
    sectionHeader := "## " + section
    idx := strings.Index(string(content), sectionHeader)
    var newContent string
    if idx < 0 {
        // Append section at end
        newContent = string(content) + "\n\n" + sectionHeader + "\n" + addition
    } else {
        // Find end of section (next ## or EOF)
        rest := string(content[idx+len(sectionHeader):])
        nextSection := strings.Index(rest, "\n## ")
        if nextSection < 0 {
            // Append to end of file
            newContent = string(content) + addition
        } else {
            // Insert before next section
            insertAt := idx + len(sectionHeader) + nextSection
            newContent = string(content[:insertAt]) + addition + string(content[insertAt:])
        }
    }

    if err := os.WriteFile(s.Path, []byte(newContent), 0644); err != nil {
        return err
    }

    // Reload the skill
    s.Content = newContent
    s.Gotchas = extractSection(newContent, "Gotchas")
    s.EstTokens = len(newContent) / 4
    s.EstGotchaTokens = len(s.Gotchas) / 4

    return nil
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}
```

---

## Step 4: Create `internal/skillselect` package

**File:** `internal/skillselect/profile.go`

```go
// Package skillselect detects a repository's technology profile and selects
// relevant skills from a registry. Detection is layered: file existence first
// (cheapest), then manifest parsing, then content sampling.
//
// The detection rules are derived from GitHub Linguist, Vercel's framework
// detectors, Nixpacks, and Heroku buildpacks.
package skillselect

import (
    "encoding/json"
    "io/fs"
    "os"
    "path/filepath"
    "regexp"
    "strings"
)

// RepoProfile is the detected technology stack of a repository.
type RepoProfile struct {
    Languages       []string `json:"languages"`        // go, rust, typescript, python
    Frameworks      []string `json:"frameworks"`        // react, react-native, nestjs, fastify, vite, next
    Databases       []string `json:"databases"`         // postgres, mongo, redis, valkey, mysql, sqlite
    MessageQueues   []string `json:"message_queues"`    // kafka, rabbitmq, nats
    CloudProviders  []string `json:"cloud_providers"`   // gcp, aws, cloudflare, fly, azure
    InfraTools      []string `json:"infra_tools"`       // docker, terraform, kubernetes, pulumi
    Protocols       []string `json:"protocols"`         // graphql, grpc, rest, mcp, websocket
    BuildTools      []string `json:"build_tools"`       // turbo, nx, vite, cargo, gradle
    PackageManagers []string `json:"package_managers"`  // npm, pnpm, yarn, bun, uv, poetry, cargo
    TestFrameworks  []string `json:"test_frameworks"`   // jest, vitest, pytest, gotest, cargo-test
    CIPlatforms     []string `json:"ci_platforms"`      // github-actions, gitlab-ci, circleci
    HasMonorepo     bool     `json:"has_monorepo"`
    HasDocker       bool     `json:"has_docker"`
    HasCI           bool     `json:"has_ci"`
    Confidence      map[string]float64 `json:"confidence"` // per-detection confidence 0-1
}

// DetectProfile scans a repository root and builds a RepoProfile.
// Returns a partially filled profile even on errors — detection is best-effort.
func DetectProfile(root string) (*RepoProfile, error) {
    p := &RepoProfile{
        Confidence: make(map[string]float64),
    }

    // Layer 1: file existence checks (highest confidence, lowest cost)
    detectByFiles(root, p)

    // Layer 2: manifest parsing
    detectByManifests(root, p)

    // Layer 3: monorepo polyglot scan (2-3 levels deep)
    if p.HasMonorepo {
        detectPolyglot(root, p)
    }

    // Deduplicate
    dedupAll(p)

    return p, nil
}

func detectByFiles(root string, p *RepoProfile) {
    fileRules := []struct {
        Path       string
        Set        func(*RepoProfile)
        Confidence float64
    }{
        // Languages by manifest presence
        {"go.mod", func(p *RepoProfile) { p.Languages = append(p.Languages, "go") }, 0.99},
        {"Cargo.toml", func(p *RepoProfile) { p.Languages = append(p.Languages, "rust") }, 0.99},
        {"package.json", func(p *RepoProfile) { p.Languages = append(p.Languages, "typescript", "javascript") }, 0.95},
        {"pyproject.toml", func(p *RepoProfile) { p.Languages = append(p.Languages, "python") }, 0.95},
        {"requirements.txt", func(p *RepoProfile) { p.Languages = append(p.Languages, "python") }, 0.90},
        {"Gemfile", func(p *RepoProfile) { p.Languages = append(p.Languages, "ruby") }, 0.99},
        {"composer.json", func(p *RepoProfile) { p.Languages = append(p.Languages, "php") }, 0.99},
        {"pom.xml", func(p *RepoProfile) { p.Languages = append(p.Languages, "java") }, 0.99},
        {"build.gradle", func(p *RepoProfile) { p.Languages = append(p.Languages, "java", "kotlin") }, 0.95},
        {"build.gradle.kts", func(p *RepoProfile) { p.Languages = append(p.Languages, "kotlin") }, 0.99},
        {"mix.exs", func(p *RepoProfile) { p.Languages = append(p.Languages, "elixir") }, 0.99},
        {"deno.json", func(p *RepoProfile) { p.Languages = append(p.Languages, "typescript"); p.Frameworks = append(p.Frameworks, "deno") }, 0.99},
        {"deno.jsonc", func(p *RepoProfile) { p.Languages = append(p.Languages, "typescript"); p.Frameworks = append(p.Frameworks, "deno") }, 0.99},

        // Build tools / monorepo
        {"turbo.json", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "turborepo"); p.HasMonorepo = true }, 0.99},
        {"nx.json", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "nx"); p.HasMonorepo = true }, 0.99},
        {"pnpm-workspace.yaml", func(p *RepoProfile) { p.PackageManagers = append(p.PackageManagers, "pnpm"); p.HasMonorepo = true }, 0.99},
        {"lerna.json", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "lerna"); p.HasMonorepo = true }, 0.99},
        {"rush.json", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "rush"); p.HasMonorepo = true }, 0.99},
        {"WORKSPACE", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "bazel"); p.HasMonorepo = true }, 0.99},
        {"WORKSPACE.bazel", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "bazel"); p.HasMonorepo = true }, 0.99},
        {"MODULE.bazel", func(p *RepoProfile) { p.BuildTools = append(p.BuildTools, "bazel"); p.HasMonorepo = true }, 0.99},

        // Cloud providers (sentinel files, highest confidence)
        {"wrangler.toml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "cloudflare") }, 0.99},
        {"wrangler.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "cloudflare") }, 0.99},
        {"fly.toml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "fly") }, 0.99},
        {"app.yaml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }, 0.85},
        {"cloudbuild.yaml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }, 0.99},
        {".gcloudignore", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }, 0.95},
        {"firebase.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp", "firebase") }, 0.99},
        {".firebaserc", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp", "firebase") }, 0.99},
        {"vercel.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "vercel") }, 0.99},
        {"netlify.toml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "netlify") }, 0.99},
        {"render.yaml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "render") }, 0.99},
        {"serverless.yml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }, 0.85},
        {"template.yaml", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }, 0.75},
        {"cdk.json", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws"); p.InfraTools = append(p.InfraTools, "cdk") }, 0.99},
        {".ebextensions", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }, 0.99},

        // Container/orchestration
        {"Dockerfile", func(p *RepoProfile) { p.HasDocker = true; p.InfraTools = append(p.InfraTools, "docker") }, 0.99},
        {"docker-compose.yml", func(p *RepoProfile) { p.HasDocker = true; p.InfraTools = append(p.InfraTools, "docker", "docker-compose") }, 0.99},
        {"docker-compose.yaml", func(p *RepoProfile) { p.HasDocker = true; p.InfraTools = append(p.InfraTools, "docker", "docker-compose") }, 0.99},
        {"compose.yml", func(p *RepoProfile) { p.HasDocker = true; p.InfraTools = append(p.InfraTools, "docker", "docker-compose") }, 0.99},

        // CI/CD platforms
        {".github/workflows", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "github-actions"); p.HasCI = true }, 0.99},
        {".gitlab-ci.yml", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "gitlab-ci"); p.HasCI = true }, 0.99},
        {"Jenkinsfile", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "jenkins"); p.HasCI = true }, 0.99},
        {".circleci/config.yml", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "circleci"); p.HasCI = true }, 0.99},
        {".travis.yml", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "travis"); p.HasCI = true }, 0.99},
        {"bitbucket-pipelines.yml", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "bitbucket"); p.HasCI = true }, 0.99},
        {"azure-pipelines.yml", func(p *RepoProfile) { p.CIPlatforms = append(p.CIPlatforms, "azure-devops"); p.HasCI = true }, 0.99},
    }

    for _, rule := range fileRules {
        if exists(filepath.Join(root, rule.Path)) {
            rule.Set(p)
            p.Confidence[rule.Path] = rule.Confidence
        }
    }
}

func detectByManifests(root string, p *RepoProfile) {
    // Parse package.json for frameworks, databases, ORMs
    if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
        parsePackageJSON(data, p)
    }
    // Parse go.mod for databases, frameworks, gRPC, GraphQL
    if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
        parseGoMod(data, p)
    }
    // Parse Cargo.toml for Rust deps
    if data, err := os.ReadFile(filepath.Join(root, "Cargo.toml")); err == nil {
        parseCargoToml(data, p)
    }
    // Parse pyproject.toml / requirements.txt
    if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
        parsePyprojectToml(data, p)
    }
    if data, err := os.ReadFile(filepath.Join(root, "requirements.txt")); err == nil {
        parseRequirements(data, p)
    }
    // Parse docker-compose.yml for services (databases, queues, caches)
    for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
        if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
            parseCompose(data, p)
            break
        }
    }
    // Parse .env / .env.example for connection strings and provider env vars
    for _, name := range []string{".env.example", ".env.template", ".env.local", ".env"} {
        if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
            parseEnvFile(data, p)
            break
        }
    }
    // Scan for *.tf files
    detectTerraform(root, p)
}

func parsePackageJSON(data []byte, p *RepoProfile) {
    var pkg struct {
        Dependencies    map[string]string `json:"dependencies"`
        DevDependencies map[string]string `json:"devDependencies"`
        Workspaces      json.RawMessage   `json:"workspaces"`
    }
    if err := json.Unmarshal(data, &pkg); err != nil {
        return
    }
    if len(pkg.Workspaces) > 0 {
        p.HasMonorepo = true
    }
    all := map[string]bool{}
    for k := range pkg.Dependencies {
        all[k] = true
    }
    for k := range pkg.DevDependencies {
        all[k] = true
    }
    for dep := range all {
        switch {
        // Frameworks
        case dep == "next":
            p.Frameworks = append(p.Frameworks, "nextjs")
        case dep == "react":
            p.Frameworks = append(p.Frameworks, "react")
        case dep == "react-native":
            p.Frameworks = append(p.Frameworks, "react-native")
        case dep == "@nestjs/core":
            p.Frameworks = append(p.Frameworks, "nestjs")
        case dep == "fastify":
            p.Frameworks = append(p.Frameworks, "fastify")
        case dep == "express":
            p.Frameworks = append(p.Frameworks, "express")
        case dep == "vite":
            p.Frameworks = append(p.Frameworks, "vite"); p.BuildTools = append(p.BuildTools, "vite")
        case dep == "vue":
            p.Frameworks = append(p.Frameworks, "vue")
        case dep == "@sveltejs/kit":
            p.Frameworks = append(p.Frameworks, "sveltekit")
        case dep == "astro":
            p.Frameworks = append(p.Frameworks, "astro")
        case dep == "nuxt":
            p.Frameworks = append(p.Frameworks, "nuxt")
        case dep == "@remix-run/dev":
            p.Frameworks = append(p.Frameworks, "remix")
        case dep == "@angular/cli":
            p.Frameworks = append(p.Frameworks, "angular")
        case dep == "hono":
            p.Frameworks = append(p.Frameworks, "hono")
        case dep == "tauri" || strings.HasPrefix(dep, "@tauri-apps/"):
            p.Frameworks = append(p.Frameworks, "tauri")
        case dep == "electron":
            p.Frameworks = append(p.Frameworks, "electron")
        case dep == "expo":
            p.Frameworks = append(p.Frameworks, "expo", "react-native")
        // Databases
        case dep == "pg" || dep == "pg-pool":
            p.Databases = append(p.Databases, "postgres")
        case dep == "mysql2":
            p.Databases = append(p.Databases, "mysql")
        case dep == "mongoose" || dep == "mongodb":
            p.Databases = append(p.Databases, "mongo")
        case dep == "redis" || dep == "ioredis":
            p.Databases = append(p.Databases, "redis")
        case dep == "better-sqlite3" || dep == "sqlite3":
            p.Databases = append(p.Databases, "sqlite")
        case dep == "@elastic/elasticsearch":
            p.Databases = append(p.Databases, "elasticsearch")
        case dep == "@aws-sdk/client-dynamodb":
            p.Databases = append(p.Databases, "dynamodb")
        case dep == "prisma" || dep == "@prisma/client":
            p.Frameworks = append(p.Frameworks, "prisma")
        case dep == "drizzle-orm":
            p.Frameworks = append(p.Frameworks, "drizzle")
        case dep == "typeorm":
            p.Frameworks = append(p.Frameworks, "typeorm")
        case dep == "@supabase/supabase-js":
            p.CloudProviders = append(p.CloudProviders, "supabase"); p.Databases = append(p.Databases, "postgres")
        // Message queues / streaming
        case dep == "kafkajs" || strings.HasPrefix(dep, "@confluentinc/"):
            p.MessageQueues = append(p.MessageQueues, "kafka")
        case dep == "amqplib":
            p.MessageQueues = append(p.MessageQueues, "rabbitmq")
        case dep == "bullmq" || dep == "bull":
            p.MessageQueues = append(p.MessageQueues, "bullmq")
        // Cloud SDKs
        case strings.HasPrefix(dep, "@aws-sdk/") || dep == "aws-sdk":
            p.CloudProviders = append(p.CloudProviders, "aws")
        case strings.HasPrefix(dep, "@google-cloud/") || dep == "firebase" || dep == "firebase-admin":
            p.CloudProviders = append(p.CloudProviders, "gcp")
        case strings.HasPrefix(dep, "@azure/"):
            p.CloudProviders = append(p.CloudProviders, "azure")
        case strings.HasPrefix(dep, "@cloudflare/"):
            p.CloudProviders = append(p.CloudProviders, "cloudflare")
        // Protocols
        case dep == "graphql" || strings.HasPrefix(dep, "@apollo/"):
            p.Protocols = append(p.Protocols, "graphql")
        case strings.HasPrefix(dep, "@grpc/"):
            p.Protocols = append(p.Protocols, "grpc")
        case dep == "ws" || dep == "socket.io" || dep == "socket.io-client":
            p.Protocols = append(p.Protocols, "websocket")
        case strings.HasPrefix(dep, "@modelcontextprotocol/"):
            p.Protocols = append(p.Protocols, "mcp")
        // Test frameworks
        case dep == "jest" || dep == "@jest/core":
            p.TestFrameworks = append(p.TestFrameworks, "jest")
        case dep == "vitest":
            p.TestFrameworks = append(p.TestFrameworks, "vitest")
        case dep == "@playwright/test":
            p.TestFrameworks = append(p.TestFrameworks, "playwright")
        case dep == "cypress":
            p.TestFrameworks = append(p.TestFrameworks, "cypress")
        case dep == "mocha":
            p.TestFrameworks = append(p.TestFrameworks, "mocha")
        // Payments
        case dep == "stripe":
            p.Frameworks = append(p.Frameworks, "stripe")
        }
    }
}

func parseGoMod(data []byte, p *RepoProfile) {
    content := string(data)
    rules := []struct {
        Pattern string
        Apply   func(*RepoProfile)
    }{
        // Postgres
        {"github.com/jackc/pgx", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
        {"github.com/lib/pq", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
        {"gorm.io/driver/postgres", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres"); p.Frameworks = append(p.Frameworks, "gorm") }},
        // MySQL
        {"github.com/go-sql-driver/mysql", func(p *RepoProfile) { p.Databases = append(p.Databases, "mysql") }},
        // MongoDB
        {"go.mongodb.org/mongo-driver", func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
        // Redis
        {"github.com/redis/go-redis", func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
        {"github.com/go-redis/redis", func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
        // SQLite
        {"github.com/mattn/go-sqlite3", func(p *RepoProfile) { p.Databases = append(p.Databases, "sqlite") }},
        {"modernc.org/sqlite", func(p *RepoProfile) { p.Databases = append(p.Databases, "sqlite") }},
        // Kafka
        {"github.com/twmb/franz-go", func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
        {"github.com/segmentio/kafka-go", func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
        {"github.com/confluentinc/confluent-kafka-go", func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
        // gRPC
        {"google.golang.org/grpc", func(p *RepoProfile) { p.Protocols = append(p.Protocols, "grpc") }},
        // GraphQL
        {"github.com/99designs/gqlgen", func(p *RepoProfile) { p.Protocols = append(p.Protocols, "graphql") }},
        {"github.com/graphql-go/graphql", func(p *RepoProfile) { p.Protocols = append(p.Protocols, "graphql") }},
        // Cloud SDKs
        {"github.com/aws/aws-sdk-go-v2", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }},
        {"github.com/aws/aws-sdk-go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }},
        {"cloud.google.com/go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }},
        {"github.com/Azure/azure-sdk-for-go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "azure") }},
        {"github.com/cloudflare/cloudflare-go", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "cloudflare") }},
        // Web frameworks
        {"github.com/gin-gonic/gin", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "gin") }},
        {"github.com/labstack/echo", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "echo") }},
        {"github.com/gofiber/fiber", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "fiber") }},
        {"github.com/go-chi/chi", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "chi") }},
        // Stripe
        {"github.com/stripe/stripe-go", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "stripe") }},
        // Hedera
        {"github.com/hashgraph/hedera-sdk-go", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "hedera") }},
    }
    for _, rule := range rules {
        if strings.Contains(content, rule.Pattern) {
            rule.Apply(p)
        }
    }
}

func parseCargoToml(data []byte, p *RepoProfile) {
    content := string(data)
    rules := []struct {
        Pattern string
        Apply   func(*RepoProfile)
    }{
        {"sqlx", func(p *RepoProfile) { /* check features later */ }},
        {`tokio-postgres`, func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
        {`mongodb`, func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
        {`redis = `, func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
        {"axum", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "axum") }},
        {"actix-web", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "actix") }},
        {"rocket", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "rocket") }},
        {"tokio", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "tokio") }},
        {`tauri = `, func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "tauri") }},
    }
    for _, rule := range rules {
        if strings.Contains(content, rule.Pattern) {
            rule.Apply(p)
        }
    }
    if strings.Contains(content, "[workspace]") {
        p.HasMonorepo = true
    }
}

func parsePyprojectToml(data []byte, p *RepoProfile) {
    parsePyDeps(string(data), p)
    if strings.Contains(string(data), "[tool.poetry]") {
        p.PackageManagers = append(p.PackageManagers, "poetry")
    }
    if strings.Contains(string(data), "[tool.uv]") {
        p.PackageManagers = append(p.PackageManagers, "uv")
    }
}

func parseRequirements(data []byte, p *RepoProfile) {
    parsePyDeps(string(data), p)
    p.PackageManagers = append(p.PackageManagers, "pip")
}

func parsePyDeps(content string, p *RepoProfile) {
    rules := []struct {
        Pattern string
        Apply   func(*RepoProfile)
    }{
        {"psycopg2", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
        {"asyncpg", func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
        {"pymongo", func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
        {"motor", func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
        {"redis", func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
        {"django", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "django") }},
        {"flask", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "flask") }},
        {"fastapi", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "fastapi") }},
        {"pytest", func(p *RepoProfile) { p.TestFrameworks = append(p.TestFrameworks, "pytest") }},
        {"sqlalchemy", func(p *RepoProfile) { p.Frameworks = append(p.Frameworks, "sqlalchemy") }},
        {"boto3", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "aws") }},
        {"google-cloud-", func(p *RepoProfile) { p.CloudProviders = append(p.CloudProviders, "gcp") }},
    }
    for _, rule := range rules {
        if strings.Contains(strings.ToLower(content), rule.Pattern) {
            rule.Apply(p)
        }
    }
}

func parseCompose(data []byte, p *RepoProfile) {
    content := string(data)
    rules := []struct {
        Pattern *regexp.Regexp
        Apply   func(*RepoProfile)
    }{
        {regexp.MustCompile(`(?m)image:\s*(?:postgres|postgresql)(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "postgres") }},
        {regexp.MustCompile(`(?m)image:\s*mysql(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "mysql") }},
        {regexp.MustCompile(`(?m)image:\s*mariadb(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "mariadb") }},
        {regexp.MustCompile(`(?m)image:\s*mongo(?:db)?(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "mongo") }},
        {regexp.MustCompile(`(?m)image:\s*redis(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "redis") }},
        {regexp.MustCompile(`(?m)image:\s*valkey(?::|\s|$)`), func(p *RepoProfile) { p.Databases = append(p.Databases, "valkey") }},
        {regexp.MustCompile(`elasticsearch`), func(p *RepoProfile) { p.Databases = append(p.Databases, "elasticsearch") }},
        {regexp.MustCompile(`clickhouse`), func(p *RepoProfile) { p.Databases = append(p.Databases, "clickhouse") }},
        {regexp.MustCompile(`(?m)image:\s*(?:apache/)?kafka`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
        {regexp.MustCompile(`(?m)image:\s*confluentinc/cp-kafka`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "kafka") }},
        {regexp.MustCompile(`(?m)image:\s*rabbitmq`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "rabbitmq") }},
        {regexp.MustCompile(`(?m)image:\s*nats`), func(p *RepoProfile) { p.MessageQueues = append(p.MessageQueues, "nats") }},
        {regexp.MustCompile(`(?m)image:\s*memcached`), func(p *RepoProfile) { p.Databases = append(p.Databases, "memcached") }},
    }
    for _, rule := range rules {
        if rule.Pattern.MatchString(content) {
            rule.Apply(p)
        }
    }
}

func parseEnvFile(data []byte, p *RepoProfile) {
    content := string(data)
    if strings.Contains(content, "DATABASE_URL=postgres") || strings.Contains(content, "DATABASE_URL=postgresql") {
        p.Databases = append(p.Databases, "postgres")
    }
    if strings.Contains(content, "DATABASE_URL=mysql") {
        p.Databases = append(p.Databases, "mysql")
    }
    if strings.Contains(content, "MONGODB_URI") || strings.Contains(content, "MONGO_URL") {
        p.Databases = append(p.Databases, "mongo")
    }
    if strings.Contains(content, "REDIS_URL") || strings.Contains(content, "REDIS_HOST") {
        p.Databases = append(p.Databases, "redis")
    }
    if strings.Contains(content, "KAFKA_BROKERS") || strings.Contains(content, "KAFKA_BOOTSTRAP_SERVERS") {
        p.MessageQueues = append(p.MessageQueues, "kafka")
    }
    if strings.Contains(content, "STRIPE_SECRET_KEY") || strings.Contains(content, "STRIPE_PUBLISHABLE_KEY") {
        p.Frameworks = append(p.Frameworks, "stripe")
    }
    if strings.Contains(content, "AWS_ACCESS_KEY_ID") || strings.Contains(content, "AWS_REGION") {
        p.CloudProviders = append(p.CloudProviders, "aws")
    }
    if strings.Contains(content, "GOOGLE_CLOUD_PROJECT") || strings.Contains(content, "GCP_PROJECT") {
        p.CloudProviders = append(p.CloudProviders, "gcp")
    }
    if strings.Contains(content, "CF_") || strings.Contains(content, "CLOUDFLARE_") {
        p.CloudProviders = append(p.CloudProviders, "cloudflare")
    }
}

func detectTerraform(root string, p *RepoProfile) {
    found := false
    filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
        if err != nil || d.IsDir() {
            if d != nil && d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
                return filepath.SkipDir
            }
            return nil
        }
        if strings.HasSuffix(d.Name(), ".tf") {
            found = true
            if data, err := os.ReadFile(path); err == nil {
                content := string(data)
                if strings.Contains(content, `"aws"`) || strings.Contains(content, "hashicorp/aws") {
                    p.CloudProviders = append(p.CloudProviders, "aws")
                }
                if strings.Contains(content, `"google"`) || strings.Contains(content, "hashicorp/google") {
                    p.CloudProviders = append(p.CloudProviders, "gcp")
                }
                if strings.Contains(content, `"azurerm"`) {
                    p.CloudProviders = append(p.CloudProviders, "azure")
                }
                if strings.Contains(content, `"cloudflare"`) {
                    p.CloudProviders = append(p.CloudProviders, "cloudflare")
                }
            }
        }
        return nil
    })
    if found {
        p.InfraTools = append(p.InfraTools, "terraform")
    }
}

func detectPolyglot(root string, p *RepoProfile) {
    // Scan apps/, packages/, services/, libs/, modules/, tools/ for nested manifests
    dirs := []string{"apps", "packages", "services", "libs", "modules", "tools"}
    for _, d := range dirs {
        sub := filepath.Join(root, d)
        if info, err := os.Stat(sub); err == nil && info.IsDir() {
            entries, _ := os.ReadDir(sub)
            for _, entry := range entries {
                if !entry.IsDir() {
                    continue
                }
                subRoot := filepath.Join(sub, entry.Name())
                detectByFiles(subRoot, p)
                detectByManifests(subRoot, p)
            }
        }
    }
}

func exists(path string) bool {
    _, err := os.Stat(path)
    return err == nil
}

func dedupAll(p *RepoProfile) {
    p.Languages = dedupStrings(p.Languages)
    p.Frameworks = dedupStrings(p.Frameworks)
    p.Databases = dedupStrings(p.Databases)
    p.MessageQueues = dedupStrings(p.MessageQueues)
    p.CloudProviders = dedupStrings(p.CloudProviders)
    p.InfraTools = dedupStrings(p.InfraTools)
    p.Protocols = dedupStrings(p.Protocols)
    p.BuildTools = dedupStrings(p.BuildTools)
    p.PackageManagers = dedupStrings(p.PackageManagers)
    p.TestFrameworks = dedupStrings(p.TestFrameworks)
    p.CIPlatforms = dedupStrings(p.CIPlatforms)
}

func dedupStrings(s []string) []string {
    seen := make(map[string]bool, len(s))
    out := make([]string, 0, len(s))
    for _, v := range s {
        if !seen[v] {
            seen[v] = true
            out = append(out, v)
        }
    }
    return out
}
```

**File:** `internal/skillselect/match.go`

```go
package skillselect

// MatchSkills returns the skill names that match a repo profile, ranked by relevance.
// The scoring prioritizes exact stack matches over generic matches.
func MatchSkills(p *RepoProfile) []string {
    scored := map[string]int{}

    // Languages → high score
    for _, lang := range p.Languages {
        scored[lang] += 100
    }
    // Frameworks → high score
    for _, fw := range p.Frameworks {
        scored[fw] += 80
    }
    // Databases → high score
    for _, db := range p.Databases {
        scored[db] += 90
    }
    // Cloud providers → medium-high
    for _, cloud := range p.CloudProviders {
        scored[cloud] += 70
        // Also tag generic cloud-* skill
        scored["cloud-"+cloud] += 70
    }
    // Message queues
    for _, mq := range p.MessageQueues {
        scored[mq] += 80
    }
    // Protocols
    for _, proto := range p.Protocols {
        scored[proto] += 60
    }
    // Infra
    for _, infra := range p.InfraTools {
        scored[infra] += 50
    }
    // Test frameworks
    for _, tf := range p.TestFrameworks {
        scored[tf] += 30
    }
    // Always-on engineering skills
    scored["agent-discipline"] = 1000
    scored["code-quality"] = 500
    scored["testing"] = 400
    scored["security"] = 400
    scored["error-handling"] = 350
    scored["distributed-systems"] = 200

    if p.HasMonorepo {
        scored["monorepo"] = 250
    }
    if p.HasDocker {
        scored["docker"] = 100
    }
    if p.HasCI {
        scored["ci-cd"] = 100
    }

    // Sort by score descending
    type kv struct {
        Name  string
        Score int
    }
    var sorted []kv
    for k, v := range scored {
        sorted = append(sorted, kv{k, v})
    }
    // Stable sort: highest score first
    for i := 0; i < len(sorted); i++ {
        for j := i + 1; j < len(sorted); j++ {
            if sorted[j].Score > sorted[i].Score {
                sorted[i], sorted[j] = sorted[j], sorted[i]
            }
        }
    }

    out := make([]string, len(sorted))
    for i, s := range sorted {
        out[i] = s.Name
    }
    return out
}
```

**File:** `internal/skillselect/profile_test.go` — write tests for the detection. At minimum:

1. `TestDetectGoPostgresProject` — fixture with `go.mod` containing `pgx`, expect `["go"]` and `["postgres"]`
2. `TestDetectNextJSWithPrisma` — fixture with `package.json` containing `next`, `@prisma/client`, `pg`, expect frameworks, prisma, postgres
3. `TestDetectMonorepo` — fixture with `turbo.json` and nested `apps/web/package.json`, `apps/api/go.mod`, expect monorepo + both languages
4. `TestDetectDockerCompose` — fixture with docker-compose containing postgres + redis services
5. `TestDetectCloudflareWorkers` — fixture with `wrangler.toml`
6. `TestDetectCIPlatforms` — fixture with `.github/workflows/test.yml`

Use `t.TempDir()` and write fixture files.

---

## Step 5: Wire skill registry into the workflow pipeline

**File:** `internal/app/app.go`

Add to `OrchestratorConfig` struct:

```go
type OrchestratorConfig struct {
    // ... existing fields ...
    SkillRegistry *skill.Registry           // skill library (nil = disabled)
    SkillSelector *skillselect.RepoProfile  // detected repo profile (nil = disabled)
}
```

In `NewOrchestrator()` (or wherever the config is constructed), add skill loading:

```go
import "github.com/ericmacdougall/stoke/internal/skill"
import "github.com/ericmacdougall/stoke/internal/skillselect"

// Load skills (best-effort)
skillRegistry := skill.DefaultRegistry(projectRoot)
if err := skillRegistry.Load(); err != nil {
    log.Printf("[skill] load failed: %v (continuing without skills)", err)
}
cfg.SkillRegistry = skillRegistry

// Detect repo profile (best-effort)
profile, err := skillselect.DetectProfile(projectRoot)
if err != nil {
    log.Printf("[skillselect] detect failed: %v", err)
}
cfg.SkillSelector = profile
```

**File:** `internal/workflow/workflow.go`

Add to `Engine` struct:

```go
type Engine struct {
    // ... existing fields ...
    SkillRegistry *skill.Registry
    StackMatches  []string  // pre-computed from RepoProfile
}
```

Pass through from app.go where Engine is constructed.

**Find these two lines** (you can use the exact line numbers from the existing code, but verify):

```go
// Around line 1474
return stokeprompts.BuildPlanPrompt(task, false, "")
```

**Replace with:**

```go
prompt := stokeprompts.BuildPlanPrompt(task, false, "")
if e.SkillRegistry != nil {
    prompt, _ = e.SkillRegistry.InjectPromptBudgeted(prompt, e.StackMatches, 3000)
}
return prompt
```

**Find this line:**

```go
// Around line 1552
return stokeprompts.BuildExecutePrompt(task, verificationStr, "")
```

**Replace with:**

```go
prompt := stokeprompts.BuildExecutePrompt(task, verificationStr, "")
if e.SkillRegistry != nil {
    prompt, _ = e.SkillRegistry.InjectPromptBudgeted(prompt, e.StackMatches, 3000)
}
return prompt
```

Do the same for any review prompt construction in `workflow.go` (search for `BuildReviewPrompt` or similar).

**File:** `internal/orchestrate/orchestrator.go`

Pass `SkillRegistry` from `OrchestratorConfig` into the workflow Engine when constructing it. Find where `workflow.Engine{...}` is constructed and add the field.

---

## Step 6: Add skills config section

**File:** `internal/config/config.go`

Add to the Policy struct:

```go
type Policy struct {
    // ... existing fields ...
    Skills SkillsConfig `yaml:"skills"`
}

type SkillsConfig struct {
    Enabled       bool     `yaml:"enabled"`
    AlwaysOn      []string `yaml:"always_on"`         // skill names always injected
    AutoDetect    bool     `yaml:"auto_detect"`       // run skillselect
    TokenBudget   int      `yaml:"token_budget"`      // max tokens for injection (default 3000)
    ResearchFeed  bool     `yaml:"research_feed"`     // auto-update skills from research store
    Excluded      []string `yaml:"excluded"`          // skill names to never load
}
```

Default values when the section is missing:

```go
func defaultSkillsConfig() SkillsConfig {
    return SkillsConfig{
        Enabled:      true,
        AutoDetect:   true,
        TokenBudget:  3000,
        ResearchFeed: true,
        AlwaysOn:     []string{"agent-discipline"},
    }
}
```

Apply defaults in the existing config-loading function (find where defaults are filled in for other sections).

---

## Step 7: Add `stoke skill` CLI commands

**File:** `cmd/stoke/main.go`

Add a new top-level command `skill` with subcommands `list`, `add`, `remove`, `show`, `reload`, `select`. The `select` subcommand runs `skillselect.DetectProfile` and prints the matched skills.

Pattern:

```go
case "skill":
    return runSkillCmd(args[1:])
```

```go
func runSkillCmd(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("usage: stoke skill <list|add|remove|show|reload|select>")
    }
    reg := skill.DefaultRegistry(".")
    if err := reg.Load(); err != nil {
        return err
    }
    switch args[0] {
    case "list":
        for _, s := range reg.List() {
            fmt.Printf("  %-30s %s\n", s.Name, s.Description)
        }
    case "show":
        if len(args) < 2 {
            return fmt.Errorf("usage: stoke skill show <name>")
        }
        s := reg.Get(args[1])
        if s == nil {
            return fmt.Errorf("skill %q not found", args[1])
        }
        fmt.Println(s.Content)
    case "select":
        profile, err := skillselect.DetectProfile(".")
        if err != nil {
            return err
        }
        matches := skillselect.MatchSkills(profile)
        fmt.Println("Detected:", profile.Languages, profile.Frameworks, profile.Databases)
        fmt.Println("Top matches:")
        for i, name := range matches {
            if i >= 10 {
                break
            }
            if s := reg.Get(name); s != nil {
                fmt.Printf("  %d. %s — %s\n", i+1, s.Name, s.Description)
            }
        }
    case "reload":
        if err := reg.Load(); err != nil {
            return err
        }
        fmt.Printf("Loaded %d skills\n", len(reg.List()))
    // add and remove existing methods on Registry are exposed similarly
    default:
        return fmt.Errorf("unknown subcommand: %s", args[0])
    }
    return nil
}
```

---

## Validation gate for Phase 1

Before moving to Phase 2/3:

1. `go vet ./...` passes with zero output
2. `go build ./cmd/stoke` succeeds
3. `go test ./internal/skill/... ./internal/skillselect/... ./internal/app/... ./internal/workflow/...` passes
4. `stoke skill list` shows the loaded skills
5. `stoke skill select` on the Stoke repo itself prints `["go"]` as a detected language and matches the `go`, `agent-discipline`, etc. skills
6. Run an existing test mission that produces a plan prompt — verify (via logs or by adding a temporary debug print) that the prompt contains `<skills>` content
7. Append a phase 1 section to `STOKE-IMPL-NOTES.md` with what you did and any blockers

## Now go to `04-phase2-wizard.md` (or `08-skill-library-extraction.md` if you're parallel with another agent).
