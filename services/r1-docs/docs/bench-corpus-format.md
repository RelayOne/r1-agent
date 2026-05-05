# Bench Corpus Format

## Directory Structure

```
corpus/
  <task-id>/
    task.yaml          # Task specification
    prompt.md          # Task prompt (what the AI agent sees)
    initial/           # Initial repository state (seed code)
      go.mod
      main.go
      ...
    visible_tests/     # Tests the agent can see
      main_test.go
    hidden_tests/      # Tests used for evaluation only
      hidden_test.go
    reference.patch    # Optional: expected diff for comparison
```

## task.yaml Schema

```yaml
id: "security-sql-injection-001"
title: "Fix SQL injection in user handler"
category: "security"           # security | correctness | refactoring | features | testing
language: "go"                 # go | python | typescript | rust | java
difficulty: 3                  # 1-5 scale
time_limit_seconds: 300        # max execution time
cost_limit_usd: 0.50           # max API cost per attempt
prompt_file: "prompt.md"
initial_repo: "initial/"
visible_tests: "visible_tests/"
hidden_tests: "hidden_tests/"
reference_patch: "reference.patch"  # optional
hidden_requirements:           # requirements not stated in prompt
  - "Must use parameterized queries, not string escaping"
  - "Must not break existing test coverage"
expected_failure_modes: []     # empty unless task is designed to be impossible
```

## Categories

| Category | Description | Example Tasks |
|----------|-------------|---------------|
| security | Fix vulnerabilities, add security controls | SQL injection, XSS, secrets in code |
| correctness | Fix bugs, handle edge cases | Off-by-one, nil pointer, race condition |
| refactoring | Improve structure without changing behavior | Extract interface, reduce complexity |
| features | Add new functionality | New endpoint, new CLI flag, new report |
| testing | Add or improve test coverage | Missing tests, table-driven tests |

## Judge Pipeline

Each task result goes through the judge stack:

1. **Build check** — Does the code compile?
2. **Visible tests** — Do the agent-visible tests pass?
3. **Hidden tests** — Do the evaluation-only tests pass?
4. **Test integrity** — Were any tests deleted or skipped?
5. **No placeholders** — No TODO/FIXME/panic("not implemented")?
6. **No suppressions** — No lint suppressions (@ts-ignore, etc)?
7. **Hallucination check** — No non-existent imports?
8. **Diff size** — Reasonable change size?
9. **Impossible task** — If designed to fail, did agent claim success?

## Running the Corpus

```bash
# Run all tasks against all harnesses, 3 repetitions
make bench

# Or directly:
go run ./bench/cmd/bench run --corpus corpus/ --harnesses stoke,claude_code,codex --reps 3

# Generate report
go run ./bench/cmd/bench report --input results.json --format html --output reports/bench.html
```

## Adding Tasks

1. Create a new directory under `corpus/` with a descriptive task ID
2. Write `task.yaml` following the schema above
3. Create `prompt.md` with the task description
4. Populate `initial/` with the seed repository
5. Add visible tests the agent can see
6. Add hidden tests for evaluation
7. Optionally add `reference.patch`
8. Run `make bench` to validate
