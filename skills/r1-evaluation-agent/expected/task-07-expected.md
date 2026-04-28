# Expected Output — Task 07 (Multi-step plan + execute)

## Pass criteria (pinned assertions)

### Plan package
- `internal/plan/` directory exists with >= 3 .go files
- grep for `Validate` returns at least one function with cycle-detection logic
- (expect references to DFS, visited map, or cycle keywords)

### stoke-plan.example.json
- File exists at repo root
- Contains >= 3 task entries
- Contains dependency references (deps, dependencies, or after fields)
- Contains ROI or priority fields

### taskstate anti-deception
- `internal/taskstate/` directory exists
- grep for "evidence" returns >= 1 match
- Phase transition enforcement present

### Comparison file
- `/tmp/r1-eval-task-07-comparison.txt` exists
- Contains exactly 3 lines as specified
- Accurately represents R1's advantage

## Allowed variance

- stoke-plan.example.json field names may differ from the spec (tasks vs items, etc.)
- taskstate may use different terminology for "evidence" (verification, proof, etc.)

## Failure indicators

- stoke-plan.example.json missing or < 3 tasks
- Validate function has no cycle detection
- Comparison file not written
