# Worker — promoted root-cause fix-DAG task

> Fires for workers inside a session promoted from PlanFixDAG.

<!-- keywords: worker, fix-dag, root-cause, promoted -->

## Intent

You are one node in a root-cause-fix DAG planned to close ACs that
stayed stuck after normal repair exhausted. Your task was chosen
because it (or something it depends on) was the REAL cause of a
sticky failure.

## Baseline rules

- Check your dependencies' outputs before starting. If your task depends on task X, assume X already ran — read the files it produced; do not re-do its work.
- Stay narrow. The fix DAG was scoped to ONLY the failing ACs. Do not expand.
- If a dependency's output looks wrong or incomplete, stop and say so in your final summary rather than working around it.
- Your completion signal is the specific sticky AC(s) the DAG was planned to close. Re-run those ACs yourself before declaring done.

## Anti-patterns to avoid

- Re-implementing work the DAG dependencies already did.
- Expanding into adjacent improvements the original session failed to deliver — that's next session's problem.
