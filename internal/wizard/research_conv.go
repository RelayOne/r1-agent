package wizard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// runResearchConvergence asks the configured AI provider for additional config
// recommendations based on the detected profile. This is opt-in (--research flag)
// and best-effort.
func runResearchConvergence(ctx context.Context, p Provider, r *WizardResult) error {
	profileJSON, _ := json.Marshal(r.Profile)
	maturityJSON, _ := json.Marshal(r.Maturity)

	system := `You are a configuration advisor for R1, an AI coding orchestrator.
Given a detected technology profile and maturity classification, recommend
configuration adjustments. Be concise. Output JSON only with this schema:

{
  "stage_correction": "prototype|mvp|growth|mature|null",
  "additional_skills": ["skill_name"],
  "additional_compliance": ["pipeda", "casl"],
  "warnings": ["warning text"]
}

Do not include explanations outside the JSON.`

	user := fmt.Sprintf("Profile: %s\nMaturity: %s\n\nWhat config adjustments do you recommend?", profileJSON, maturityJSON)

	response, err := p.Chat(ctx, system, user)
	if err != nil {
		return err
	}

	// Parse response (be lenient with markdown fences)
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var rec struct {
		StageCorrection      string   `json:"stage_correction"`
		AdditionalSkills     []string `json:"additional_skills"`
		AdditionalCompliance []string `json:"additional_compliance"`
		Warnings             []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(response), &rec); err != nil {
		return fmt.Errorf("parse research response: %w", err)
	}

	if rec.StageCorrection != "" && rec.StageCorrection != "null" {
		if rec.StageCorrection != r.Config.Project.Stage {
			r.Rationale = append(r.Rationale, DetailedRationale{
				Field: "project.stage", Value: rec.StageCorrection, Source: "research",
				Evidence:   "AI advisor suggested correction from " + r.Config.Project.Stage,
				Confidence: 0.75,
			})
			r.Config.Project.Stage = rec.StageCorrection
		}
	}

	for _, c := range rec.AdditionalCompliance {
		r.Config.Security.Compliance = appendIfMissing(r.Config.Security.Compliance, c)
		r.Rationale = append(r.Rationale, DetailedRationale{
			Field: "security.compliance", Value: c, Source: "research", Confidence: 0.7,
		})
	}

	return nil
}
