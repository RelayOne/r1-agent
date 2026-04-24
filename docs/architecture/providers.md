# Provider Fallback Chain

R1 routes tasks to execution engines via a 5-provider fallback chain. The chain
is task-type aware: different task types have different preferred providers based on
observed benchmark performance.

## Providers

| Priority | Provider | Binary/API | Auth | Package |
|----------|----------|-----------|------|---------|
| 1 | Claude Code | `claude` CLI | OAuth (Mode 1) or API key (Mode 2) | `internal/engine/claude.go` |
| 2 | Codex CLI | `codex` CLI | OAuth (Mode 1) or API key (Mode 2) | `internal/engine/codex.go` |
| 3 | OpenRouter | REST API | `OPENROUTER_API_KEY` | `internal/apiclient/client.go` |
| 4 | Direct API | Anthropic Messages API | `ANTHROPIC_API_KEY` | `internal/apiclient/client.go` + `internal/agentloop/` |
| 5 | Lint-only | Local build/test/lint | None | `internal/verify/` |

### Additional providers (not in fallback chain)

| Provider | Binary/API | Package | Notes |
|----------|-----------|---------|-------|
| Gemini | `gemini` CLI | `internal/engine/gemini.go` | Standalone, not in routing table |
| Native | Anthropic Messages API | `internal/engine/native_runner.go` + `internal/agentloop/` | Direct API with tool-use loop |
| Ember | Ember /v1/ai/chat | `internal/provider/ember.go` | Managed AI routing via Ember SaaS |

## Task-type routing

The routing table in `internal/model/router.go` maps task types to provider preferences:

| Task Type | Primary | Fallback Chain |
|-----------|---------|---------------|
| Plan | Claude | Codex -> OpenRouter -> DirectAPI |
| Refactor | Claude | Codex -> OpenRouter -> DirectAPI |
| TypeSafety | Claude | Codex -> OpenRouter -> DirectAPI |
| Docs | Claude | Codex -> OpenRouter -> DirectAPI -> LintOnly |
| Security | Claude | Codex -> OpenRouter -> DirectAPI |
| Architecture | Codex | Claude -> OpenRouter -> DirectAPI |
| DevOps | Codex | Claude -> OpenRouter -> DirectAPI |
| Concurrency | Codex | Claude -> OpenRouter -> DirectAPI |
| Review | Codex | Claude -> OpenRouter -> DirectAPI -> LintOnly |

## Cross-model review

`model.CrossModelReviewer()` selects a different provider for the review phase
than was used for execution. This ensures independent verification:

- If Claude executed -> Codex reviews
- If Codex executed -> Claude reviews
- If neither available -> fallback to DirectAPI

## Provider resolution

`model.Resolve(taskType, availableProviders)` walks the routing table:

1. Check if the primary provider is available
2. Walk the fallback chain in order (skipping LintOnly unless it's the last option)
3. Return LintOnly if all others fail

## Diagnostics

```bash
stoke doctor --providers
```

Reports the availability and version of each provider in the chain.
