# tests/agent/worktree/diff-and-merge.agent.feature.md

<!-- TAGS: smoke, worktree -->
<!-- DEPENDS: r1d-server -->

## Scenario: r1.worktree.diff returns a token-budgeted diff

- Given a worktree "${WORKTREE_ID}" with at least one committed file change since BaseCommit
- When the external agent calls r1.worktree.diff with worktree_id "${WORKTREE_ID}"
- Then the response is a unified diff with size at most 200 KiB
- And the diff begins with "diff --git" or contains zero hunks (empty diff)
- And re-calling r1.worktree.diff returns identical output (read-only, deterministic)

## Scenario: r1.worktree.diff with full=true returns the full diff

- Given a worktree "${WORKTREE_ID}" whose diff exceeds 200 KiB
- When the external agent calls r1.worktree.diff with worktree_id "${WORKTREE_ID}" and full=true
- Then the response size exceeds 200 KiB
- And no truncation marker is present in the body

## Scenario: r1.worktree.merge with strategy ff-only succeeds when fast-forward is possible

- Given a worktree "${WORKTREE_ID}" whose HEAD is a fast-forward of main
- When the external agent calls r1.worktree.merge with worktree_id "${WORKTREE_ID}" and strategy "ff-only"
- Then the response indicates merge success
- And r1.worktree.list no longer includes "${WORKTREE_ID}" (or marks it merged)
- And the bus contains a worktree_merged event for this worktree

## Scenario: r1.worktree.merge with strategy ff-only errors on a non-FF case

- Given a worktree "${WORKTREE_ID}" whose HEAD has diverged from main
- When the external agent calls r1.worktree.merge with strategy "ff-only"
- Then the response error_code is "conflict"
- And the worktree is unchanged (BaseCommit and HEAD identical)

## Scenario: r1.worktree.clean is idempotent

- Given a worktree "${WORKTREE_ID}"
- When the external agent calls r1.worktree.clean
- Then r1.worktree.list no longer includes "${WORKTREE_ID}"
- When the external agent calls r1.worktree.clean again
- Then no error is returned (already-cleaned is a no-op)

## Tool mapping (informative)
- "r1.worktree.diff" -> r1.worktree.diff
- "r1.worktree.merge" -> r1.worktree.merge
- "r1.worktree.list" -> r1.worktree.list
- "r1.worktree.clean" -> r1.worktree.clean
- "the bus contains" -> r1.bus.tail
