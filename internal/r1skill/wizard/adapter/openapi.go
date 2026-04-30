package adapter

import (
	"context"
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

type OpenAPIAdapter struct{}

func (OpenAPIAdapter) Format() string { return "openapi" }

func (OpenAPIAdapter) CanParse(raw []byte) error {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) == nil {
		if _, ok := doc["openapi"]; ok {
			return nil
		}
	}
	doc = map[string]any{}
	if yaml.Unmarshal(raw, &doc) == nil {
		if _, ok := doc["openapi"]; ok {
			return nil
		}
	}
	return context.Canceled
}

func (OpenAPIAdapter) Parse(_ context.Context, raw []byte, sourcePath string) (*SourceArtifact, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		if yamlErr := yaml.Unmarshal(raw, &doc); yamlErr != nil {
			return nil, yamlErr
		}
	}
	domains := []string{}
	if servers, ok := doc["servers"].([]any); ok {
		for _, server := range servers {
			if m, ok := server.(map[string]any); ok {
				if u, ok := m["url"].(string); ok {
					domain := u
					if idx := strings.Index(domain, "://"); idx >= 0 {
						domain = domain[idx+3:]
					}
					if idx := strings.Index(domain, "/"); idx >= 0 {
						domain = domain[:idx]
					}
					if domain != "" {
						domains = append(domains, domain)
					}
				}
			}
		}
	}
	artifact := &SourceArtifact{
		Format:   "openapi",
		Path:     sourcePath,
		RawBytes: raw,
		Parsed:   doc,
		Inferences: []Inference{
			{IRPath: "capabilities.network.allow_domains", Value: domains, Confidence: 0.95, Source: "servers"},
		},
	}
	return artifact, artifact.Validate()
}

func init() { Default.Register(OpenAPIAdapter{}) }
