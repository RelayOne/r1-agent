package concern

import (
	"fmt"
	"strings"
)

// Render converts a ConcernField into XML-tagged prompt text suitable for
// injection into a stance's system prompt.
func Render(cf *ConcernField) string {
	var b strings.Builder

	fmt.Fprintf(&b, "<concern_field role=%q face=%q scope=%q>\n",
		cf.Role, cf.Face, renderScope(cf.Scope))

	hasSkills := false
	for _, s := range cf.Sections {
		if s.Name == "applicable_skills" && s.Content != "" {
			hasSkills = true
		}
		fmt.Fprintf(&b, "<section name=%q>\n", s.Name)
		b.WriteString(s.Content)
		if !strings.HasSuffix(s.Content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("</section>\n")
	}

	if hasSkills {
		b.WriteString("<skills_observability>\n")
		b.WriteString("The following skills have been loaded into your context. ")
		b.WriteString("If you apply any of them, emit a skill.applied event so the harness can track usage.\n")
		b.WriteString("</skills_observability>\n")
	}

	b.WriteString("</concern_field>")
	return b.String()
}

// renderScope formats a scope for the XML attribute.
func renderScope(s Scope) string {
	if s.TaskID != "" {
		return s.TaskID
	}
	if s.MissionID != "" {
		return s.MissionID
	}
	return "global"
}
