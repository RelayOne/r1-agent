# R1 Evaluation Agent

A first-class R1 skill that proves (or disproves) parity-or-better between
R1+Stoke and Claude Code, Manus, and Hermes. It dogfoods R1's own tool surface.

## Invocation

```bash
# Weekly cadence (auto — fires Monday 09:00 UTC via scheduler)
r1 skill run r1-evaluation-agent

# On-demand
r1 skill run r1-evaluation-agent

# Force-refresh reference docs even if checked today
r1 skill run r1-evaluation-agent force-refresh

# Re-run a single battery task
r1 skill run r1-evaluation-agent task-only:task-03
```

## What it does

1. Reads `evaluation/r1-vs-reference-runtimes-matrix.md`
2. Optionally re-fetches Claude Code / Manus / Hermes public docs
3. Runs the 10-task comparison battery (see `tasks/`)
4. Updates the matrix (parity %, new rows, status changes)
5. Files new gap tasks in `plans/work-orders/work-r1.md`
6. Writes a run report to `evaluation/runs/YYYY-MM-DD-HHMM/report.md`

## Output

- **Matrix:** `evaluation/r1-vs-reference-runtimes-matrix.md` (updated in-place)
- **Run report:** `evaluation/runs/YYYY-MM-DD-HHMM/report.md`
- **Gap tasks:** appended to `plans/work-orders/work-r1.md` section `## Phase R1-PARITY`

## Directory layout

```
skills/r1-evaluation-agent/
├── manifest.yaml          # Stoke skill manifest (skillmfr format)
├── prompt.md              # Agent prompt: full evaluation loop
├── README.md              # This file
├── report-template.md     # Template for per-run reports
├── tasks/
│   ├── task-01-bash-shell.md
│   ├── task-02-file-edit.md
│   ├── task-03-multi-file-search.md
│   ├── task-04-web-fetch-summarize.md
│   ├── task-05-code-refactor-rename.md
│   ├── task-06-mcp-tool-call.md
│   ├── task-07-multi-step-plan-execute.md
│   ├── task-08-skill-discovery-and-load.md
│   ├── task-09-image-understanding.md
│   └── task-10-pdf-parse-summarize.md
└── expected/
    ├── task-01-expected.md
    ├── task-02-expected.md
    ├── task-03-expected.md
    ├── task-04-expected.md
    ├── task-05-expected.md
    ├── task-06-expected.md
    ├── task-07-expected.md
    ├── task-08-expected.md
    ├── task-09-expected.md
    └── task-10-expected.md
```

## How to update the matrix

1. Add a new row in the correct category section of
   `evaluation/r1-vs-reference-runtimes-matrix.md`.
2. Assign the next available row number (#).
3. If GAP: assign R1P-NNN and append to `work-r1.md`.
4. Update the row counts table at the bottom of the matrix.
5. Re-run the skill: `r1 skill run r1-evaluation-agent`

## How to add a new battery task

1. Create `tasks/task-NN-<ability-name>.md` following the template in
   any existing task file.
2. Create `expected/task-NN-expected.md` with pinned assertions.
3. Update the task count in `prompt.md` and `report-template.md`.
4. Commit as `feat(r1-eval): add battery task NN — <ability-name>`.

## Cost estimate

A full run (10 tasks + docs refresh) costs approximately $0.50–$2.00
depending on model choice and network latency. Task-only runs cost ~$0.05.

## Constraints

- No fabricated abilities. UNVERIFIED if docs unavailable.
- No fabricated R1-ENHANCED. Must cite a real `internal/` package path.
- Honest gaps. Every real loss is listed.
- No `interface{}` hacks in any code.
