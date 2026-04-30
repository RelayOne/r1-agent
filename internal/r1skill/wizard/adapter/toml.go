package adapter

import (
	"context"
	"strings"
)

type CodexTOMLAdapter struct{}

func (CodexTOMLAdapter) Format() string { return "codex-toml" }

func (CodexTOMLAdapter) CanParse(raw []byte) error {
	text := string(raw)
	if strings.Contains(text, "[") && strings.Contains(text, "=") {
		return nil
	}
	return context.Canceled
}

func (CodexTOMLAdapter) Parse(_ context.Context, raw []byte, sourcePath string) (*SourceArtifact, error) {
	text := string(raw)
	description := valueAfterEquals(text, "description")
	if description == "" {
		description = valueAfterEquals(text, "developer_instructions")
	}
	artifact := &SourceArtifact{
		Format:   "codex-toml",
		Path:     sourcePath,
		RawBytes: raw,
		Parsed:   map[string]string{"body": text},
		Inferences: []Inference{
			{IRPath: "description", Value: description, Confidence: 0.7, Source: "toml"},
		},
	}
	return artifact, artifact.Validate()
}

func init() { Default.Register(CodexTOMLAdapter{}) }

func firstNonEmptyParagraph(text string) string {
	for _, part := range strings.Split(text, "\n\n") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" && !strings.HasPrefix(trimmed, "---") {
			return strings.ReplaceAll(trimmed, "\n", " ")
		}
	}
	return ""
}

func valueAfterEquals(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key) {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		return strings.Trim(strings.TrimSpace(parts[1]), "\"'")
	}
	return ""
}
