package adapter

import (
	"context"
	"strings"
)

type MarkdownAdapter struct{}

func (MarkdownAdapter) Format() string { return "r1-markdown-legacy" }

func (MarkdownAdapter) CanParse(raw []byte) error {
	text := string(raw)
	if strings.Contains(text, "---") && (strings.Contains(text, "name:") || strings.Contains(text, "description:")) {
		return nil
	}
	return context.Canceled
}

func (MarkdownAdapter) Parse(_ context.Context, raw []byte, sourcePath string) (*SourceArtifact, error) {
	text := string(raw)
	description := firstNonEmptyParagraph(text)
	artifact := &SourceArtifact{
		Format:   "r1-markdown-legacy",
		Path:     sourcePath,
		RawBytes: raw,
		Parsed:   map[string]string{"body": text},
		Inferences: []Inference{
			{IRPath: "description", Value: description, Confidence: 0.8, Source: "markdown-body"},
		},
	}
	return artifact, artifact.Validate()
}

func init() { Default.Register(MarkdownAdapter{}) }
