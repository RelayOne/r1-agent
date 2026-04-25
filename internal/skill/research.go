package skill

import (
	"fmt"
	"os"
	"strings"

	"github.com/RelayOne/r1-agent/internal/research"
)

// SkillUpdate records a single research-to-skill merge.
type SkillUpdate struct {
	SkillName string
	EntryID   string
	Section   string
	Added     string
}

// UpdateFromResearch scans research entries and merges findings into matching skills.
// Research with category=gotcha → appends to skill Gotchas section.
// Research with category=pattern → appends to skill body.
// Returns the list of updates made for logging/audit.
func (r *Registry) UpdateFromResearch(entries []research.Entry) ([]SkillUpdate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	updates := make([]SkillUpdate, 0, len(entries))
	for _, entry := range entries {
		targetSkill := r.findSkillForResearch(entry)
		if targetSkill == nil {
			continue
		}

		// Skip if already merged (idempotent on entry ID)
		if strings.Contains(targetSkill.Content, fmt.Sprintf("[research:%s]", entry.ID)) {
			continue
		}

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

		preview := entry.Content
		if len(preview) > 200 {
			preview = preview[:200]
		}
		updates = append(updates, SkillUpdate{
			SkillName: targetSkill.Name,
			EntryID:   entry.ID,
			Section:   section,
			Added:     preview,
		})
	}

	return updates, nil
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

func (r *Registry) appendToSkillSection(s *Skill, section string, entry research.Entry) error {
	content, err := os.ReadFile(s.Path)
	if err != nil {
		return err
	}

	addition := fmt.Sprintf("\n- %s [research:%s]\n", strings.TrimSpace(entry.Content), entry.ID)

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
			newContent = string(content) + addition
		} else {
			insertAt := idx + len(sectionHeader) + nextSection
			newContent = string(content[:insertAt]) + addition + string(content[insertAt:])
		}
	}

	if err := os.WriteFile(s.Path, []byte(newContent), 0o644); err != nil { // #nosec G306 -- skill markdown is user-readable content, not sensitive.
		return err
	}

	// Reload the skill in-memory
	s.Content = newContent
	s.Gotchas = extractSection(newContent, "Gotchas")
	s.EstTokens = len(newContent) / 4
	s.EstGotchaTokens = len(s.Gotchas) / 4

	return nil
}
