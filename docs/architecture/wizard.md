# Wizard Architecture

## Overview

The wizard (`internal/wizard/`) runs `stoke init` to generate a project-specific `.stoke/config.yaml` with quality gates, model selection, security rules, and skill configuration scaled to the project's maturity stage.

## Maturity Classification

8-signal weighted scoring system:

| Signal | Weight | Detection Method |
|--------|--------|-----------------|
| Git activity | 15% | Commit count, contributor count, age |
| Review process | 15% | PR templates, CODEOWNERS, branch protection |
| Tests | 15% | Test file count, coverage config, test frameworks |
| CI/CD | 15% | GitHub Actions, CircleCI, Jenkins, etc. |
| Documentation | 10% | README size, docs/ directory, API docs |
| Security | 10% | SECURITY.md, Dependabot, security scanning |
| Dependencies | 10% | Lock files, dependency count, pinning |
| Observability | 10% | Logging, metrics, tracing imports |

Composite score maps to stages:
- **0–20**: prototype
- **21–40**: mvp
- **41–70**: growth
- **71–100**: mature

## Modes

| Mode | Behavior |
|------|----------|
| `auto` | Detect profile, apply defaults, skip confirmation |
| `interactive` | Full prompts for each section |
| `hybrid` | Auto-detect, show proposal, user confirms/modifies |
| `yes` | Like auto, CI-safe (no stdin) |

## Config Types

Structured Go types with yaml/json tags:

- `WizardConfig` — top-level container
- `ProjectConfig` — name, description, type, domains
- `ModelsConfig` — primary, review, research models
- `QualityConfig` — honesty_enforcement (always strict), coverage, lint
- `SecurityConfig` — secret scanning, dependency audit, sandbox
- `InfrastructureConfig` — CI, deployment, IaC
- `ScaleConfig` — parallel tasks, max cost
- `WizardSkillsConfig` — always_on, auto_detect, token_budget
- `TeamConfig` — size, review required, CODEOWNERS
- `RiskConfig` — risk tolerance (yolo → conservative, scales with stage)

## Research Convergence

Optional AI-powered config refinement:

```
wizard.RunWizard(ctx, Opts{Provider: anthropicProvider})
  → buildDefaultConfig(profile, maturity)
  → runResearchConvergence(provider, profile, config)
    → provider.Chat() with JSON schema
    → parse stage_correction, additional_skills, additional_compliance
  → merge recommendations into config
```

## Output

`writeOutput()` produces:

1. `.stoke/config.yaml` — generated via `yaml.v3` with struct tags
2. `.stoke/wizard-rationale.md` — includes maturity assessment breakdown, field-level decisions with confidence scores
3. `.stoke/skills/` — copies selected skills from `~/.stoke/skills/` library

## Key Decisions

- Honesty enforcement is always "strict" regardless of maturity stage
- Risk tolerance scales: prototype=yolo, mvp=permissive, growth=standard, mature=conservative
- No external TUI dependency (huh) — uses stdin-based proposal display
- Provider interface for research convergence is optional (nil = skip)
