# Wizard Architecture

## Overview

The wizard (`internal/wizard/`) runs `r1 init` to generate project configuration scaled to the project's maturity stage: `r1.policy.yaml` at the project root (the artifact `config.LoadPolicy` and `--policy` discovery read) plus a wizard-native `config.yaml` and rationale under the r1 dir (`.r1/` or legacy `.stoke/`).

## CLI Wiring (`r1 init`)

| Invocation | Route |
|------------|-------|
| `r1 init [dir]` | `wizard.RunWizard` in `ModeAuto` — detect, show proposal, confirm |
| `r1 init --auto` / `-a` | `wizard.RunWizard` in `ModeYes` — accept all defaults, CI-safe |
| `r1 init --interactive` / `-i` | Legacy question wizard (`wizard.New(...).Run()`) — full prompt flow |
| `r1 init --research` | Adds the AI research-convergence pass; requires a resolvable model provider (`ANTHROPIC_API_KEY`, LiteLLM env/discovery) and errors out otherwise |

The planned `r1 wizard` command name is taken by the skill-authoring wizard, so all routing lives under `r1 init`.

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
| `auto` | Detect profile, apply defaults, show proposal, confirm (EOF/empty stdin accepts) |
| `interactive` | Reached via `--interactive`, which routes to the legacy question wizard instead of `RunWizard`; `ModeInteractive` inside `RunWizard` falls back to the proposal flow |
| `hybrid` | Auto-detect, show proposal, user confirms/modifies |
| `yes` | Accept all defaults, CI-safe (no prompts) |

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

1. `r1.policy.yaml` (project root) — mapped from the wizard result via `policyPreferences` + the legacy `GenerateYAML` generator, guaranteed loadable by `config.LoadPolicy`; this is what downstream config loading reads
2. `<r1dir>/config.yaml` — wizard-native representation, generated via `yaml.v3` with struct tags
3. `<r1dir>/wizard-rationale.md` — includes maturity assessment breakdown, field-level decisions with confidence scores
4. `<r1dir>/skills/` — copies selected skills from the home skills library

`<r1dir>` is `.r1/` (or legacy `.stoke/` when it already exists), resolved by `internal/r1dir`.

## Key Decisions

- Honesty enforcement is always "strict" regardless of maturity stage
- Risk tolerance scales: prototype=yolo, mvp=permissive, growth=standard, mature=conservative
- No external TUI dependency (huh) — uses stdin-based proposal display
- Provider interface for research convergence is optional (nil = skip)
