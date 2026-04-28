# Task 07 — Multi-step plan + execute

**Ability under test:** SOW-tree decomposition (row #34) + planning tools (rows #32, #33)  
**Reference product:** Claude Code (plan mode, TaskCreate/Update/List)  
**R1 equivalent:** `internal/plan/`, `internal/taskstate/`, `internal/workflow/`, `internal/intent/`

## Task description

Demonstrate R1's multi-step planning capability by:

1. **Reading the plan package** — confirm `internal/plan/` exists and
   has load/save/validate with cycle detection:
   ```bash
   ls ./internal/plan/
   grep -n "func.*Validate" ./internal/plan/*.go | head -10
   ```

2. **Reading a real plan example** — read `stoke-plan.example.json` at
   the repo root and report:
   - Number of tasks in the plan
   - Whether dependencies are present
   - Whether ROI fields are present

3. **Confirming taskstate anti-deception gates** — read
   `internal/taskstate/` and confirm the phase-transition rules exist:
   ```bash
   grep -n "evidence" ./internal/taskstate/*.go | head -10
   ```

4. **SOW vs Claude Code TaskCreate comparison** — write a 3-line
   comparison to `/tmp/r1-eval-task-07-comparison.txt`:
   ```
   Claude Code: TaskCreate creates a flat task with status only
   R1: Plan creates a tree with dependency graph, ROI filter, GRPW priority
   R1 advantage: tasks cannot be "completed" without evidence gates
   ```

## Acceptance criteria

- [ ] `internal/plan/` contains validate function with cycle DFS
- [ ] `stoke-plan.example.json` has >= 3 tasks with dependency references
- [ ] `internal/taskstate/` has evidence-gate references
- [ ] Comparison file written correctly

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: plan package verified but taskstate not found
- FAIL: plan package missing
