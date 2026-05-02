# 09 — The Snapshot Mechanism

The snapshot is the protected baseline of the user's pre-existing code. It is the boundary between "code Stoke is allowed to work on freely" and "code Stoke must defend by default." It exists because Stoke is operating on repositories that are usually not empty — they contain code the user wrote, code other teams wrote, code from prior Stoke runs, third-party dependencies, generated files, configuration. The snapshot captures the state of everything at the moment Stoke starts on a mission, and from that moment forward the CTO stance defends the snapshot against unmotivated modification.

The components above have referenced the snapshot repeatedly: the CTO's stance contract in component 1 says the CTO has veto authority on snapshot code, the supervisor's snapshot rules in component 4 enforce that authority structurally, the snapshot_annotation node type in component 6 is how the CTO records its understanding of the snapshot over time, the concern field templates in component 7 pull snapshot annotations into stance context, the shipped skill library in component 8 has a whole category on snapshot defense. This file fills in what has been referenced: what the snapshot actually is, how it is taken, how it is updated, and the mechanism by which Stoke knows whether a file or a modification is "in the snapshot" or not.

---

## What the snapshot is

The snapshot is a manifest — a list of files and their content hashes — taken at a specific moment in the repo's history. The moment is the point at which Stoke is first invoked on a mission, captured from the repo's git state at that instant. The manifest is stored under `.stoke/snapshot/manifest.json` and is git-tracked. Every file under version control at snapshot time is on the manifest. Every file *not* under version control at snapshot time is *not* on the manifest — untracked files are not part of the snapshot because they were not part of the repo's committed state.

The manifest has the following structure:

- **snapshot_commit_sha** — the git commit SHA at the moment the snapshot was taken. This is the canonical reference for "what the repo looked like when Stoke started."
- **snapshot_created_at** — timestamp
- **snapshot_created_by_mission** — the mission ID that took the snapshot (usually the first mission on this repo; subsequent missions use the existing snapshot if one exists, with the update rules below)
- **files** — a map from file path to content hash. The hash is the git blob SHA for that file at the snapshot commit. This is cheap to compute (it's what git already stores) and unambiguous (it's immutable for a given content).
- **directories** — a list of directory paths that existed at snapshot time. Tracked separately from files because empty directories don't have git blob SHAs but are still meaningful for structural annotations.
- **ignored_patterns** — patterns from `.gitignore` at snapshot time, so Stoke knows what was deliberately untracked.

The snapshot is *what the repo looked like*, not *what the repo should look like*. It is observational, not aspirational. The CTO's job is not to say "the snapshot represents the correct state of the code" — the CTO's job is to say "the snapshot represents what the user was already comfortable with, and any change to it must be motivated."

---

## When the snapshot is taken

The snapshot is taken at the first Stoke invocation on a repo that does not already have a snapshot. The wizard handles snapshot creation as part of initialization (component 10):

1. Wizard detects no `.stoke/snapshot/manifest.json` exists
2. Wizard walks the repo's current git-tracked state at the current HEAD
3. Wizard computes the manifest — file paths, git blob SHAs, directories, ignored patterns
4. Wizard writes the manifest to `.stoke/snapshot/manifest.json` and commits it as part of the initial `.stoke/` directory commit
5. The initial commit is recorded in the ledger as a `snapshot_annotation` node with type `snapshot_created`, referencing the commit SHA and noting "this is the baseline"

Subsequent Stoke invocations on a repo that already has a snapshot do not re-take it. The existing manifest is loaded and used. The snapshot is mission-invariant by default — different missions on the same repo share the same snapshot unless the user explicitly updates it.

**Why the snapshot is not re-taken per mission.** Re-taking would mean that any changes made by a prior mission become part of the new mission's snapshot, which would mean Stoke's own work becomes protected snapshot code for the next mission. This is wrong in two ways: it would make Stoke's earlier work undebatable by the next mission's CTO (which removes the mechanism that catches Stoke's own mistakes in retrospect), and it would gradually erase the distinction between "code the user owns" and "code Stoke wrote." The snapshot is the user's baseline; it stays the user's baseline across missions.

**The snapshot can, however, be explicitly updated.** The user may decide that code Stoke wrote in a prior mission has now been reviewed, accepted, and integrated into the user's own standard. At that point the user can promote specific files (or all changed files) into the snapshot via an explicit command through the wizard. Explicit promotion is the only mechanism for snapshot updates — implicit updates through Stoke's normal work are impossible by design.

---

## What "in the snapshot" means at modification time

When a stance proposes modifying a file, the supervisor's `snapshot.modification.requires_cto` rule (component 4, category 3) fires to check whether the file is in the snapshot. The check is:

1. Read the snapshot manifest from `.stoke/snapshot/manifest.json`
2. Look up the file path in the `files` map
3. If the file path is in the map, the file is in the snapshot and the rule fires (CTO consultation required)
4. If the file path is not in the map, the file is not in the snapshot (it was created by Stoke in this or a prior mission, or by another tool since the snapshot was taken) and the rule does not fire

The lookup is by path, not by content. A file whose content has changed since the snapshot (because a prior mission modified it with CTO approval, and the user has not yet promoted the modified version into the snapshot) is *still in the snapshot* and still triggers the CTO consultation for further modifications. This is intentional: just because one round of modifications was approved does not mean subsequent modifications are approved. Each modification is its own trust decision.

Files created by Stoke in the current mission are not in the snapshot and are freely modifiable by the stances that created them, subject to the normal consensus loop rules but without the CTO-veto step. This is the correct semantic: Stoke's own work should be iterable by Stoke without extraordinary friction, while the user's pre-existing code should be defended.

**Edge case: renamed files.** If a file existed at the snapshot and has since been renamed, the snapshot still references the old path. A modification to the renamed file is checked against the old path; the rename itself is a modification (the rule fires on the original path with the `rename` action type). The renamed file keeps its snapshot status until the user promotes the rename explicitly.

**Edge case: deleted files.** If a file existed at the snapshot and has since been deleted, the snapshot manifest still lists it, but the current filesystem does not. An attempt to recreate the file (e.g., a stance proposing a new file at the same path) is not treated as a snapshot modification — the file is gone from the current state, and recreating it is a new file creation, not a modification of the original. The snapshot manifest records what *was* there; if what was there is no longer there, the record is historical, not operative.

**Edge case: files added outside Stoke.** If the user or another tool adds files to the repo while Stoke is running (uncommon but possible in agentic development setups), those files are not in the snapshot. They are treated as Stoke-writable by default. If the user wants new files from outside tools to be protected, they must explicitly promote them into the snapshot.

---

## Snapshot annotations

The snapshot manifest is the *structural* record of what's in the baseline. Snapshot annotations are the *semantic* record of what the CTO has learned about that baseline. Annotations accumulate across missions, across sessions, across CTO consultations, and they are the CTO's institutional memory about the codebase.

Every annotation is a `snapshot_annotation` ledger node (schema in component 6). The CTO writes an annotation whenever it has learned something about the snapshot that future consultations should know:

- **During the first CTO consultation on a repo** (a cold-start case), the CTO walks the snapshot and writes initial annotations for high-signal areas: files that look intentionally structured, files that look like generated code, files that reference conventions (style guides, test patterns, architectural decisions), files that look like they might be load-bearing.
- **During any subsequent consultation**, the CTO may update its understanding by writing new annotations that supersede or refine prior ones. An area the CTO initially thought was accidental might turn out to be intentional; a pattern the CTO initially thought was load-bearing might turn out to be dead code.
- **When a proposed modification is evaluated**, the CTO reads all annotations for the affected files and uses them as input to its decision. If the annotations say "this file is an intentional pattern and the rest of the codebase depends on its shape," the CTO pushes back harder on modifications. If the annotations say "this file looks accidental and poorly structured," the CTO is more willing to approve smart changes.

Annotations are scoped to paths (not to content hashes), because the CTO's understanding of an area of the codebase should persist even if the content changes through approved modifications. The annotation for `internal/auth/` is about the authentication subsystem, not about the specific bytes that were there at snapshot time.

Annotations are append-only like everything else in the ledger. A CTO that wants to update an annotation writes a new annotation with a `supersedes` edge to the prior. The prior is preserved in the ledger for audit, but future reads find the superseding version.

**Annotations are inherited across missions.** A new mission on a repo with an existing snapshot inherits all annotations from prior missions. The CTO stance in the new mission reads those annotations as input, rather than walking the snapshot from scratch. This is how the CTO becomes more effective over time on a single repo: each mission's CTO learns something, the learning is recorded, the next mission's CTO starts from the accumulated knowledge.

---

## The snapshot annotation lifecycle

A stance proposes modifying a file in the snapshot. The supervisor's rule fires. A fresh-context CTO stance is spawned with:

1. The proposing stance's action and reasoning
2. The full content of the file being modified (read from the current filesystem)
3. The original snapshot content of the file (read from the snapshot commit via `git show snapshot_commit_sha:path`)
4. All existing annotations for the file and its containing directory (queried from the ledger)
5. The original user intent (from the mission's root)
6. Any related skills from the concern field (particularly snapshot-defense skills)
7. The relevant slice of the task DAG so the CTO knows what larger work the modification is part of

The CTO reads all of this and produces one of four decisions, each committed as its own ledger node:

- **`cto_approve_with_annotation`** — the CTO approves the modification and may write a new snapshot annotation that records what it learned about the file during the consultation. Approval is the common case for smart changes that have clear motivation and don't break anything load-bearing.
- **`cto_deny_with_reasoning`** — the CTO rejects the modification. The reasoning is recorded and sent back to the proposing stance, which can either revise the proposal to address the rejection or escalate if it disagrees. The CTO may also write an annotation explaining why the area is load-bearing.
- **`cto_approve_conditionally`** — the CTO approves the modification but requires specific changes to the proposed action (e.g., "you can modify this file but you also need to update the tests that exercise it," "you can refactor this function but the public signature must stay the same"). The conditional approval is a decision node the proposing stance must honor, and the modification is held until the conditions are met.
- **`cto_escalate_to_user`** — the CTO declines to approve or deny on its own because the modification is significant enough that the user should make the call directly. This is rare but important: a proposed rewrite of a major subsystem, a proposed change to user-facing API signatures, a proposed change to a file the CTO has marked as `load_bearing_area` with explicit "do not modify without user consent" notation. The escalation goes through the normal hierarchy.user_escalation rule path.

Each decision produces a decision_internal node attached to the loop, and for approvals it may also produce one or more snapshot_annotation nodes. The annotations are what make the next CTO consultation better — the accumulated record of what has been learned.

---

## Explicit snapshot updates

The user can update the snapshot at any time through the wizard's snapshot-update command. The update flow:

1. User invokes `stoke snapshot update [--all | --files file1 file2 ...]`
2. Wizard presents a preview of what will change in the snapshot manifest
3. Wizard asks the user to confirm
4. On confirmation, the wizard re-computes the manifest for the specified files (or all currently-tracked files if `--all`)
5. The new manifest is written to `.stoke/snapshot/manifest.json` and committed
6. A `snapshot_annotation` node is committed with type `snapshot_updated` and a reference to the prior snapshot commit, preserving the history of when the snapshot moved
7. Prior annotations are preserved — the update changes what's in the manifest but does not erase the CTO's accumulated understanding, because annotations are scoped to paths and the paths usually remain valid

**The update is always explicit.** There is no automatic snapshot update on mission completion, no snapshot update on successful PR merge, no snapshot update based on time or confidence. The substrate does not have any path to an implicit snapshot update. If the user wants to promote Stoke's work into the snapshot, the user types the command; otherwise the snapshot remains what it was.

**The update is per-file or all-or-nothing.** The user can promote specific files individually (useful when some of Stoke's work is ready for promotion and some is not) or the entire current state at once. There is no partial-file promotion (you cannot promote some of the changes in a file while keeping others out).

**Updates can be undone.** The snapshot manifest has its own git history (it's a git-tracked file), so `git revert` on the commit that updated the snapshot restores the prior manifest. The wizard surfaces this as a `stoke snapshot revert` command for user convenience. Reverting does not erase annotations — the CTO's learning persists across snapshot updates and reverts.

---

## The cold-start problem

When the CTO runs its first consultation on a repo with no prior annotations, it has nothing to go on except the raw files. This is the cold-start case and it is worth calling out because it shapes how the CTO behaves on first invocation.

Cold-start behavior:

- The CTO reads the file being modified and the file's immediate context (other files in the same directory, files imported by the file, the project's README and any top-level config files)
- The CTO applies the shipped snapshot-defense skills from the library to form initial hypotheses about the codebase's style, intent, and load-bearing areas
- The CTO errs on the side of caution for the first consultation on a repo: unfamiliar code is treated as "possibly load-bearing until proven otherwise," which means the first few consultations are more conservative than consultations later in a repo's Stoke history
- The CTO writes initial annotations liberally during cold-start consultations, because every annotation is future context — the investment pays off on the next consultation
- The user sees more CTO consultations during the early missions on a repo and fewer as the annotations accumulate and the CTO's understanding matures

This is not a failure mode; it is the correct behavior. A CTO that skipped cold-start caution would be approving modifications without understanding the code, which is exactly what the snapshot defense mechanism exists to prevent.

The wizard can optionally run a **cold-start annotation pass** at initialization, where the CTO walks the full snapshot and writes annotations for every significant file without waiting for a modification to trigger a consultation. This is opt-in because it has a token cost (each file walk is a CTO consultation), but for repos where the user wants the CTO to understand the codebase before the first mission starts, it's a one-time investment that makes every subsequent consultation faster and more informed. The wizard surfaces the option with an estimate of the token cost based on the repo size.

---

## Package structure

```
internal/snapshot/
├── doc.go
├── manifest.go              // manifest read/write, hash computation, git blob SHA lookup
├── manifest_test.go
├── check.go                 // the "is this file in the snapshot" lookup used by the supervisor rule
├── check_test.go
├── update.go                // explicit snapshot update flow (wizard command implementation)
├── update_test.go
├── cold_start.go            // cold-start annotation pass (wizard option at initialization)
└── cold_start_test.go
```

The snapshot component does not spawn stances — the CTO stance is spawned by the supervisor's rule, not by the snapshot component. The snapshot component is a passive data service: it provides the manifest, answers the "in snapshot" check, handles explicit updates, and optionally walks the snapshot for cold-start annotation. The supervisor and the CTO are the active components that use the snapshot data.

---

## What the snapshot mechanism does not do

- **It does not decide whether a modification is justified.** That is the CTO's job. The snapshot mechanism only answers "is this file in the snapshot" — the trust decision about whether to approve is made by the CTO with the annotations and the proposing stance's reasoning as input.
- **It does not track content changes for undo purposes.** Git already does that. The snapshot manifest is a baseline reference, not a version history. If you want to know what the file looked like three commits ago, you ask git; if you want to know what the file looked like at snapshot time, you ask the snapshot manifest.
- **It does not update automatically.** No mission completion, no merge, no time-based promotion. Every update is explicit via the wizard command.
- **It does not apply to non-code files indiscriminately.** The manifest covers all git-tracked files, but the CTO's consultation logic can be configured via the wizard to treat certain file types differently (e.g., documentation files might have looser protection, generated files might be excluded entirely). The per-file-type configuration is part of the wizard's surface; the snapshot mechanism itself is type-agnostic.
- **It does not handle cross-repo snapshots.** A repo with git submodules has one snapshot per repo, scoped to the top-level repo only. Submodule contents are not in the top-level snapshot. If Stoke is operating within a submodule, it takes its own snapshot there. This is intentional: submodule boundaries are module ownership boundaries, and the snapshot should not cross them.

---

## Validation gate

1. ✅ `go vet ./...` clean, `go test ./internal/snapshot/...` passes with >75% coverage
2. ✅ `go build ./cmd/r1` succeeds
3. ✅ The snapshot manifest is written to `.stoke/snapshot/manifest.json` on first initialization and is valid JSON parseable by the manifest reader
4. ✅ The manifest contains every git-tracked file from the snapshot commit, with correct git blob SHAs (verified against `git ls-tree -r snapshot_commit_sha`)
5. ✅ The manifest does not contain any untracked files (files in the working directory but not in git) — verified by snapshot-time state vs manifest comparison
6. ✅ The "is this file in the snapshot" check returns true for files in the manifest and false for files not in the manifest
7. ✅ The check returns true for a file whose content has been modified since the snapshot (the check is by path, not by content)
8. ✅ The check returns false for a file created after the snapshot
9. ✅ Renamed files are still considered in the snapshot under their original path (verified by a rename test)
10. ✅ Deleted-then-recreated files are not considered in the snapshot at the recreation (verified by delete-and-create test)
11. ✅ Explicit snapshot update via `stoke snapshot update --files path` updates only the specified file in the manifest and preserves the rest
12. ✅ Explicit snapshot update via `stoke snapshot update --all` updates all currently-tracked files in the manifest
13. ✅ Snapshot updates write a `snapshot_annotation` node of type `snapshot_updated` to the ledger
14. ✅ Snapshot updates preserve prior annotations (the annotations are not deleted or modified when the manifest changes)
15. ✅ Snapshot revert via `stoke snapshot revert` restores the prior manifest and writes a `snapshot_annotation` node of type `snapshot_reverted`
16. ✅ Cold-start annotation pass (when opt-in) spawns CTO stances for each significant file in the snapshot and writes initial annotations
17. ✅ The snapshot mechanism does not modify files on disk — it reads git state and writes ledger nodes, but does not touch the repo's working directory (verified by a test that runs all snapshot operations and confirms file mtimes are unchanged for repo files)
18. ✅ A subsequent Stoke invocation on a repo with an existing snapshot reuses the snapshot rather than re-taking it (verified by checking that `snapshot_commit_sha` in the manifest is unchanged after a second initialization attempt)
19. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

This file is component 9 of the new guide. It refers to several things specified in later components:

- **The wizard** is component 10. The wizard handles snapshot initialization, the update and revert commands, and the cold-start annotation pass as optional initialization flows.
- **The harness** spawns CTO stances when the supervisor's snapshot rules fire. The snapshot component provides the inputs (manifest, annotations, file contents); the harness handles stance creation.
- **The bench** (later) measures how often CTO consultations change behavior — a bench metric for the snapshot mechanism's effectiveness.

The next file to write is `10-the-wizard.md`. The wizard is where the user configures Stoke at initialization and where all the configuration-surface knobs from prior components (rule strength, concern field caps, skill confidence adjustments, snapshot creation, shipped library import) live in one coherent flow.
