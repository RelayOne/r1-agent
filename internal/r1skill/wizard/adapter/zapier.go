package adapter

import (
	"context"
	"encoding/json"
	"strings"
)

type ZapierAdapter struct{}

func (ZapierAdapter) Format() string { return "zapier" }

func (ZapierAdapter) CanParse(raw []byte) error {
	if strings.Contains(string(raw), "\"zap\"") || strings.Contains(string(raw), "\"steps\"") {
		return nil
	}
	return context.Canceled
}

func (ZapierAdapter) Parse(_ context.Context, raw []byte, sourcePath string) (*SourceArtifact, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	artifact := &SourceArtifact{
		Format:   "zapier",
		Path:     sourcePath,
		RawBytes: raw,
		Parsed:   doc,
		Inferences: []Inference{
			{IRPath: "description", Value: "Converted from Zapier workflow", Confidence: 0.6, Source: "export"},
		},
	}
	return artifact, artifact.Validate()
}

func init() { Default.Register(ZapierAdapter{}) }
