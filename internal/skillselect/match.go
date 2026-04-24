package skillselect

import "sort"

// MatchSkills returns skill names that match a repo profile, ranked by relevance score.
// Higher-scored names appear first. These names can be passed to
// skill.Registry.InjectPromptBudgeted as stackMatches.
func MatchSkills(p *RepoProfile) []string {
	if p == nil {
		return nil
	}

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
	sorted := make([]kv, 0, len(scored))
	for k, v := range scored {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].Name < sorted[j].Name // stable tiebreak
	})

	out := make([]string, len(sorted))
	for i, s := range sorted {
		out[i] = s.Name
	}
	return out
}
