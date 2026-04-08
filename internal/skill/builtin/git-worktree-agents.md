# git-worktree-agents

> Git worktree isolation patterns for parallel AI coding agents with safe merge coordination

<!-- keywords: worktree, parallel, agent, isolation, merge, conflict, mutex, concurrent, branch, git, multi-agent, stoke -->

## Why Worktrees

Git worktrees create linked working directories sharing the repository's object database while isolating HEAD, index, and working tree per agent. Creation is sub-second. The sweet spot is 3-5 parallel agents per repository -- beyond that, merge complexity (not compute) becomes the binding constraint.

## Critical Constraint: Concurrent Git Operations

Git was designed as a single-process tool. Running simultaneous git commands across worktrees sharing `.git` can corrupt the shared object store and refs. Stripe reportedly abandoned worktrees for agent fleets due to subtle shared-state corruption. **Serialize all git operations behind a mutex** -- this is the `mergeMu` pattern.

## Worktree Lifecycle

### Creation
```bash
# Branch-based (preferred for named work)
git worktree add ../agent-task-42 -b feature/task-42

# Detached HEAD (OpenAI Codex default -- no branch conflicts)
git worktree add --detach ../agent-task-42
```

No two worktrees can check out the same branch simultaneously -- Git enforces this to prevent irreconcilable staging states. Detached HEAD mode avoids this constraint entirely.

### Cleanup
```bash
git worktree remove ../agent-task-42    # deletes directory + metadata
git worktree prune                       # clean stale entries
```

`git gc` considers refs from all worktrees when determining object reachability -- nothing active gets collected.

## Task Partitioning Prevents Conflicts

The most effective conflict prevention is structural: assign different files or modules to different agents. The `parallel-dev` tool auto-discovers modular architectures (Django, NestJS) to identify self-contained modules with zero file overlap. "The thing that surprised me most wasn't the speed gain, it was how important the task boundary definition is."

### Locking Strategy
- **Optimistic** (Git default): Work in parallel, detect conflicts at merge. Works well for 3-6 agents on independent features.
- **Pessimistic:** Lock hotspot files before modifying (routing configs, DB schemas, package manifests).
- **Hybrid (recommended):** Lock only known shared files pessimistically, leave everything else optimistic.

For distributed coordination: etcd provides strong consistency with lease-based locking and native Go APIs. Redis with Redlock is faster but unsuitable for correctness-critical coordination.

## Merge Serialization

All merges to main must be serialized behind a mutex. The pattern:

1. Agent completes work in worktree, commits to branch
2. Acquire merge mutex
3. `git merge-tree --write-tree` for zero-side-effect conflict validation
4. If clean: fast-forward or merge commit
5. If conflicted: release mutex, attempt resolution, retry
6. Release merge mutex

Capture `BaseCommit` at worktree creation for `diff BaseCommit..HEAD` to understand exactly what changed.

## Filesystem Considerations at Scale

Each worktree duplicates working tree files but shares git objects. For a 1 GB repo, 10 worktrees consume ~10 GB. Build artifacts multiply faster -- `node_modules` and build caches are per-worktree.

| Filesystem | Concurrent Worktrees | Notes |
|------------|---------------------|-------|
| ext4 | 8-12 | Fixed inode allocation limits |
| XFS | 15-20 | Dynamic inodes |
| ZFS/Btrfs | 25+ | Copy-on-write, instant snapshots |

## Copy-on-Write Snapshots for Rollback

**Btrfs:** Snapshot subvolumes before agent work as O(1) metadata operation. On failure, delete and restore.

**OverlayFS:** Shared codebase as read-only lowerdir, agent changes in writable upperdir. Rollback = discard upperdir. Docker uses this as default storage driver.

**Git reflog:** Every HEAD movement recoverable for ~90 days. `git reset --hard <ref>` reverts to any previous state.

## Agent Coordination Pattern

The planner-worker pattern dominates successful implementations:

1. **Planner agent** explores codebase, creates tasks with file ownership boundaries
2. **Worker agents** focus on single tasks in isolated worktrees
3. **Completion gating:** A task is not done until tests pass. Period.
4. **Doom-loop detection:** Three consecutive failures trigger termination, not infinite retry

Cursor's evolution confirms: flat self-coordination failed catastrophically (20 agents slowed to throughput of 2-3). Planner-worker separation was the fix.

## Agent Identity in Commits

Set distinct author identities per worktree for traceability:
```bash
export GIT_AUTHOR_NAME="stoke-agent-3"
export GIT_AUTHOR_EMAIL="agent-3@stoke.local"
```

Let agents make many incremental commits during work, then squash before merge with aggregated trailers preserving provenance.

## Common Failure Modes

- **File stomping:** Multiple agents editing the same file. Fix: strict file ownership in task assignment.
- **Context loss between sessions:** Agent does not know what was already tried. Fix: persistent memory per task. Promova saw ~50% redundant work before fixing this.
- **Test blindness:** Agent declares completion with failing tests. Fix: test-gated completion, never trust agent self-assessment.
- **Silent fallback to main checkout:** Agent hits worktree error and silently operates in main. Fix: validate working directory matches expected worktree path before every operation.
