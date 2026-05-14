# TruthfulCompletion Benchmark — Reproduction Kit

This directory is the canonical way to reproduce a published
TruthfulCompletion leaderboard.

## What it does

Builds one Docker container per agent under test, then runs each
agent against the seed mission corpus. Outputs one JSON `RunResult`
per (agent, mission) pair into `./out/`.

## Requirements

- Docker 24+ with `docker compose` plugin.
- ~8 GB free disk for the container builds.
- API keys in your environment for the external agents you want to
  measure:
  - `ANTHROPIC_API_KEY` — for `r1`, `claude-code-default`, `claude-code-stop-hook`, `cursor`.
  - `OPENAI_API_KEY` — for `codex-cli`. `aider` will also use this if you don't set OpenRouter.
  - `OPENROUTER_API_KEY` — for the LLM judge when running a cross-vendor judge.

## Quick start

```bash
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...
./run.sh
```

The script:

1. Builds the three default service containers (`r1`, `claude-code`, `aider`).
2. Runs each against the 5 seed missions.
3. Writes per-run JSON to `./out/`.

Total runtime: ~30 minutes on a 4-core / 16 GB machine, dominated by
the LLM API calls.

## Outputs

Each successful run produces one file:

```
out/r1-bench-r1--seed-hello-easy.json
out/r1-bench-r1--seed-refactor-medium.json
out/r1-bench-claude-code--seed-hello-easy.json
...
```

Each file is a `bench.RunResult` (see `internal/bench/bench.go`)
serialized as indented JSON. Key fields:

```json
{
  "MissionID": "seed-hello-easy",
  "AgentID": "r1-antitrunc",
  "completion_attempted": true,
  "completion_truthful": true,
  "plan_items_completed": 2,
  "plan_items_total": 2,
  "delivery_ratio_percent": 95,
  "judge_verdict": "agrees_truthful",
  "WallTimeMs": 12450
}
```

## Aggregating into a leaderboard

The repo includes `internal/bench/leaderboard.go::BuildLeaderboard` +
`RenderMarkdown`. A small Go program reading the `./out/*.json`
files and calling those two functions produces the published Markdown
table. (The reproduction kit doesn't ship that aggregator yet — for
now, point your favorite scripting language at the JSON files.)

## Overriding the corpus

By default the kit runs the 5 seed missions. To run a different
subset (e.g. when you have the full 100-mission corpus mounted):

```bash
MISSIONS="my-mission-1 my-mission-2" ./run.sh
```

## Hermetic-network constraint

The `docker-compose.yml` sets `network_mode: bridge` rather than
`network_mode: none` because the external agents (`claude-code-default`, `codex-cli`, `cursor`) MUST reach their respective API endpoints to
function. R1-only runs can override this to `network_mode: none`:

```bash
SERVICES=r1-bench-r1 NETWORK_MODE=none ./run.sh
```

(That env var hook is not yet implemented; track it in
`plans/corpus-100.md`.)

## Versioning

Each Dockerfile pins its agent's CLI version via a build arg:

| Dockerfile | Build arg | Default |
|---|---|---|
| `Dockerfile.claude-code` | `CLAUDE_CODE_VERSION` | `latest` |
| `Dockerfile.aider` | `AIDER_VERSION` | (latest from pip) |

For a published-leaderboard reproduction, pin both to the versions
named in the methodology document for that run.

## Troubleshooting

**"neither ANTHROPIC_API_KEY nor OPENAI_API_KEY is set"** — set at
least one. The kit refuses to run a no-op matrix.

**A specific service fails with rate-limit errors** — the kit logs
"WARN: <svc>/<mission> failed" and continues. The aggregate
leaderboard will show that agent's rate over only the missions that
succeeded; the failures count as silent failures.

**docker compose build is slow** — the first build downloads the
Node/Python/Alpine layers. Subsequent builds reuse the cache and
take seconds.

## Spec reference

`specs/truthful-completion-benchmark.md` §T8.3 (item 57).
