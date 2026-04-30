package adapter

import (
	"context"
	"testing"
)

func TestAdapter_AllEmitConfidence(t *testing.T) {
	cases := []struct {
		name    string
		adapter Adapter
		raw     []byte
	}{
		{name: "markdown", adapter: MarkdownAdapter{}, raw: []byte("---\nname: demo\n---\nBody")},
		{name: "openapi", adapter: OpenAPIAdapter{}, raw: []byte("{\"openapi\":\"3.0.0\",\"servers\":[{\"url\":\"https://api.example.com/v1\"}]}")},
		{name: "zapier", adapter: ZapierAdapter{}, raw: []byte("{\"zap\":\"demo\",\"steps\":[]}")},
		{name: "toml", adapter: CodexTOMLAdapter{}, raw: []byte("description = \"Demo skill\"\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := tc.adapter.Parse(context.Background(), tc.raw, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			if len(artifact.Inferences) == 0 {
				t.Fatalf("expected inferences")
			}
			for _, inf := range artifact.Inferences {
				if inf.Confidence < 0 || inf.Confidence > 1 {
					t.Fatalf("confidence out of range: %+v", inf)
				}
			}
		})
	}
}
