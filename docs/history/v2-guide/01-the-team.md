# 01 — The Team

This file defines the roster of stances Stoke runs work through. It is the first component of the new guide, and the rest of the guide derives from it.

A **stance** is a model session with a specific system prompt, a specific slice of the concern field, a specific authority, and specific consensus partners. Two stances of the same role are independent — they share no context, only the artifacts and concern field that get handed between them. A single underlying model (e.g., Claude Opus 4.6) can occupy any number of stances simultaneously, in different sessions, and they are effectively independent reviewers because they have no shared commitment. When a different model family is available, it's preferred for consensus partners; when not, fresh sessions of the same model with different system prompts are sufficient.

The team scales to the work. A trivial bug fix runs with PO + Dev + Reviewer + QA; a multi-branch feature runs the full roster. The wizard or the PO at task ingress decides which stances spin up. The default for ambiguous cases is more stances, not fewer — under-staffing is the failure mode the architecture is designed to prevent.

There are ten stances. Each one is specified below in the same format: analog, responsibility, inputs, authority, consensus posture, escalation path, skill access, session shape.

---

## 1. PO — Product Owner

**Analog.** Product Owner / Product Manager.

**Responsibility.** Translates the user's raw intent into a PRD: what we're building, why, who it's for, what counts as done, what's out of scope. Owns the original intent for the lifetime of the task. Is the source of truth when any later stance asks "is this still what we said we were building?" Is the boundary between Stoke and the user — nothing escalates above the PO except by going through the user.

**Inputs.** The user's intent (raw), prior conversation history with the user, wizard configuration, project-level context (codebase purpose, prior tasks).

**Authority.** Sole authority to define and modify the PRD. Cannot make technical or design decisions. Can reject completed work as "not what we asked for" — this is one of the most important powers in the system.

**Consensus posture.** Seeks user signoff on the PRD before SOW work begins. Optionally convenes a second PO stance to sanity-check ambiguous intent. The PO's voice in any consensus loop is the voice of "is this still serving the original ask."

**Escalation path.** Back to the user. The PO is the escape hatch when consensus elsewhere cannot converge.

**Skill access.** Reads PO patterns (writing acceptance criteria, spotting underspecified intent, detecting scope creep). Writes new PO skills when intent-translation problems recur.

**Session shape.** Persistent for the duration of a task. The PO is the long-running thread that holds the intent. As the task runs long and the PO's session context grows, the session is compacted; what survives compaction is the current PRD state plus an index into the **internal decision log**. The PO can recall any prior decision by querying the log rather than carrying the full history in context. The role is persistent; the session backing the role is re-grounded against the decision logs as the task progresses.

---

## 2. Lead Engineer

**Analog.** Lead Engineer / Tech Lead.

**Responsibility.** Translates the PRD into a SOW: what work needs doing, how it decomposes into tickets, what the architectural shape is, what the risks are, what the cost estimate is. Owns the technical plan and the decomposition. Holds the engineering concern field for the overall work — knows what every Dev is doing and where the integration points are.

**Inputs.** The PRD, the codebase (read access via tools), prior architectural decisions, the skill library, research returns on relevant patterns, the snapshot.

**Authority.** Can decompose work, assign tickets to Dev stances, reject completed work for technical reasons, request research on uncertain areas. Cannot change the PRD. Cannot ship without VP Eng signoff at milestones. Cannot modify snapshot code without going through the CTO.

**Consensus posture.** Seeks the PO's agreement that the SOW serves the PRD. Seeks the VP Eng's agreement on architectural direction at signoff. Optionally convenes a second Lead Engineer stance for SOW review on complex tasks. In every PR consensus, the Lead Eng is the voice of "does this fit the SOW we agreed to."

**Escalation path.** To VP Eng for forward-looking architectural questions; to CTO for refactor questions on snapshot code; to PO for intent questions; to the user via PO when truly blocked.

**Skill access.** Reads architectural patterns, decomposition patterns, codebase-specific patterns. Writes new patterns when SOW decisions resolve recurring problems.

**Session shape.** Persistent for the task. Like the PO, the Lead Eng is a persistent role backed by a session that gets compacted as the task runs long. What survives compaction is the current SOW state, the current decomposition, the current concern field index, and an index into the internal decision log. The Lead Eng queries the log for prior decisions rather than carrying them in context.

---

## 3. Lead Designer

**Analog.** Lead Designer / Design Lead.

**Responsibility.** Owns design coherence across the work: UX/UI accuracy, responsiveness, accessibility (WCAG, screen readers, keyboard navigation, focus management, color contrast), user journey completeness including unhappy paths, RBAC enforcement at the UX layer (not just disabled but actually hidden when forbidden), and consistency across surfaces — web, mobile, desktop, and across packages in a monorepo. Holds the multi-surface view that no single-surface dev can hold.

**Inputs.** The PRD, the SOW, design system tokens and shared component libraries, the codebase's user-facing surfaces, the skill library, the snapshot.

**Authority.** Can reject completed work for design reasons. Can require revisions on any PR that touches a user-facing surface. Cannot make technical decisions outside the design domain.

**Consensus posture.** Required consensus partner on every PR that touches a user-facing surface. Required consensus partner on any PRD with a UX dimension. **Not** a required reviewer on backend-only PRs by default — but the Lead Designer can be invited into any backend PR by any other stance (Dev, Reviewer, Lead Eng, SDM) when the work has potential downstream UX implications. The mechanism by which they get invited is the concern field: when a backend change has UX-relevance, the concern field is supposed to surface that, and the surfacing stance pulls the Designer in. The Lead Designer's voice is "does this hold together as something a user can actually use across all the surfaces it appears on."

**Escalation path.** To PO when design and intent disagree; to VP Eng or CTO when design and architecture disagree (e.g., design requires a state shape the architecture doesn't support, or architecture forces a UX compromise the Designer won't accept); to the user via PO when truly blocked.

**Skill access.** Reads design-system skills, accessibility skills, multi-surface consistency skills. Writes new skills when catching recurring footguns (e.g., "this codebase uses Tailwind v4 layer ordering this way, don't break it").

**Session shape.** Persistent for the task. Design coherence depends on holding the whole user journey in view. Same compaction pattern as the PO and Lead Eng — the session is re-grounded against the internal decision log as the task runs long; what survives compaction is the current design state, the current concern field index, and a log index.

---

## 4. VP Eng

**Analog.** VP Engineering. Forward-looking.

**Responsibility.** Signs off on the SOW before work begins. Signs off at milestones. Holds the line on architectural standards and future-proofing. Asks "does this fit where we're going?" and "are we cutting corners we'll regret?" The VP Eng has the user's stated long-term direction in hand at every check-in so they can detect drift between the current task and where the project as a whole is going.

**Inputs.** The PRD, the SOW, milestone state, the codebase, the user's stated long-term direction (if any was captured by the wizard or PO), the skill library.

**Authority.** Can block work at signoff or milestones. Can require the SOW to be revised. Can escalate to the user for direction on architectural tradeoffs that affect long-term project direction. The VP Eng's authority is contingent: they can block at the gates they sit at (SOW signoff, milestone signoff), but inside the gates the consensus loop runs without them.

**Consensus posture.** The VP Eng is brought into a consensus loop when the question is "will this hold up over time?" Optionally convenes a second VP Eng stance for high-stakes signoffs.

**Escalation path.** To the user via PO when an architectural question genuinely needs human direction.

**Skill access.** Reads everything; primarily reads architectural and standards skills.

**Session shape.** Fresh session per check-in, with the PRD + current state + original user intent loaded each time. Fresh session is intentional — it prevents drift and prevents being talked into things by accumulated context.

---

## 5. CTO

**Analog.** CTO. Backward-looking. The codebase's defender.

**Responsibility.** Holds the repo as it was when Stoke was initialized — the **snapshot** — in mind every time a change to that codebase is proposed. Also holds the codebase-wide view of Stoke-written code accumulated since initialization. The CTO's job is to make sure changes are *self-aware* about what they are, why they're being made, and what they affect.

The CTO is **not** a stop-energy stance. It is the experienced engineer who joined the project from elsewhere and is being asked to sign off on a change to code they didn't write. Its question is not "why are you touching this?" with arms crossed. Its question is "walk me through what you're doing and why, and let me check it against what I know about how this codebase came to be."

A smart change to snapshot code, the CTO will happily allow. Smart means: motivated by the current task (not by a skill firing on a surface pattern match), scoped to what the reason requires (not expanded into adjacent territory), consistent with the codebase's intentional conventions (or honestly explicit about departing from accidental ones), reversible in isolation, and non-regressive against the behaviors the existing code demonstrably had.

What the CTO pushes back on is the *unmotivated* refactor. The change that exists because a skill said "here's how this kind of code should look" rather than because the current task needed the change to exist. The change that started small and grew. The change that imposes a pattern from elsewhere onto a codebase that already had a working different pattern. The change that is technically defensible in isolation but breaks the codebase's existing coherence. For these, the CTO's pushback is "make the case." If the case can be made, the CTO approves. If it cannot, the change doesn't ship and the question can escalate to the user via PO.

**Inputs.** The snapshot (canonical, taken at Stoke's first invocation on the repo, updated only when the user explicitly accepts a milestone). The user's stated intent for the current task. The user's stated long-term direction. The current codebase state. The proposed change. The proposing stance's reasoning.

**Authority.**

- *On snapshot code:* the CTO has **veto** authority. Default is "show me the case." Approves smart changes; pushes back on unmotivated ones; vetoes when the case cannot be made. The veto is overridable only by escalation to the user via PO.
- *On Stoke-written code:* the CTO has **a vote in consensus**, not a veto. Their concerns are surfaced into the consensus loop with the relevant stances. They can block consensus by withholding agreement, which triggers another iteration; they cannot unilaterally block the work. The Judge can be invoked, or the question can escalate to the user via PO, if the consensus loop genuinely cannot converge.

**Consultation trigger.** The CTO is consulted on a Stoke-written code change when **the change affects another team's work** — another active branch, another active Dev's in-flight code, an interface or module that another stance depends on. The detector for this is usually the SDM (when active), since the SDM holds the cross-branch view. For single-branch tasks, the Lead Eng makes the call. The principle is the same as for snapshot code: the CTO is consulted when a stance is modifying work it does not own. With snapshot code, the unowned work belongs to the user. With Stoke-written code, the unowned work belongs to another team currently in flight.

Local changes within a module the proposing stance owns do not need a CTO consultation. Normal peer review through the Reviewer is sufficient.

**Consensus posture.** Brought into any consensus loop that touches the codebase-wide view. Brought in **last** in any such loop, after the other stances have reached preliminary agreement, so the CTO reviews the proposed consensus rather than anchoring the discussion. The CTO's value is checking the agreement of others against the whole-codebase context.

**Escalation path.** To the user via PO when a refactor or architectural question genuinely needs human direction.

**Skill access.** Reads codebase-archetype skills, refactor-justification skills, snapshot-defense skills. Writes new skills when catching recurring "skill X applied wrong here" patterns.

**Session shape.** Fresh session per consultation, with the full snapshot context and current state loaded each time. Fresh session is intentional and load-bearing — it is the property that prevents the CTO from being gradually talked into a series of changes that individually seem fine but collectively constitute a rewrite.

---

## 6. SDM — Software Delivery Manager

**Analog.** SDM / Engineering Manager who oversees multiple teams.

**Responsibility.** Holds the cross-branch view when a task is decomposed into multiple feature branches, each with its own Lead Engineer. The SDM is the single stance that sees all the leads, all the branches, all the in-flight work simultaneously. The SDM's job is to catch what no single lead can see from inside their branch:

- Two leads independently solving the same problem in incompatible ways
- One feature blocking another that the blocked feature's lead doesn't know about
- Resource contention (the same module, the same skill, the same anything)
- Drift between branches that will produce a hellish merge later
- Schedule risk when one branch is the critical path and others aren't, but all leads are spending equal effort

The SDM holds the **meta concern field** — the union of all the branch concern fields. The SDM doesn't tell any individual lead what to do inside their branch; they tell leads "your work and lead-2's work are colliding, you need to sync."

**Inputs.** All active branches, all active SOWs, all active concern fields, the PRD, the user's intent.

**Authority.** Can require leads to coordinate. Can reorder branch priority. Cannot decide technical questions inside a branch. Can escalate to VP Eng or PO when the cross-branch picture is unhealthy in a way no individual lead can see or fix.

**Consensus posture.** Brought into any consensus that touches more than one branch. The SDM's voice is "how does this affect the other branches in flight?"

**Escalation path.** To VP Eng for cross-branch architectural issues; to PO when cross-branch friction reflects an unclear PRD.

**Skill access.** Reads cross-team coordination skills, branch-conflict patterns, monorepo collision patterns.

**Session shape.** Persistent across the entire multi-branch task. The SDM is one of the longest-running stances when active. Same compaction pattern — re-grounded against the internal decision log; what survives compaction is the current cross-branch state map, the current collision list, and a log index. Dormant for single-branch tasks — when there's only one Lead Eng, the Lead Eng's view *is* the whole view.

---

## 7. QA Lead

**Analog.** QA Lead.

**Responsibility.** Produces the testing map from the PRD and SOW: what behaviors need verification, what the edge cases are, what integration points need testing, what the regression risks are. Continuously expands automated tests as work progresses. Files bug reports as hub events when verification fails. QA runs **in parallel with dev work**, not after it.

**Inputs.** PRD, SOW, current code state, test results, prior bug history, the snapshot.

**Authority.** Can reject work for QA reasons. Cannot block the SOW itself. Can require new tests be written before work is accepted.

**Consensus posture.** Required consensus partner on every PR. Required consensus partner on any PRD that has testable behaviors (which is almost all of them). The QA Lead's voice is "what could go wrong, and have we checked for it."

**Escalation path.** To Lead Eng for technical disputes; to PO for intent disputes; to Lead Designer for UX-correctness disputes.

**Skill access.** Testing patterns, common bug shapes, regression patterns specific to the codebase.

**Session shape.** Persistent for the task. Same compaction pattern as the other persistent stances — re-grounded against the internal decision log as needed; what survives compaction is the current testing map, the current bug list, and a log index.

---

## 8. Dev

**Analog.** Backend or frontend developer. The role adapts based on the ticket — a ticket assigned to "the backend dev" gets a stance with backend system prompt and backend skills loaded; same for frontend. Devs are the most numerous stances and the most disposable.

**Responsibility.** Implements assigned tickets while holding the concern field. Writes unit and integration tests as they go. Asks for help when stuck. Coordinates with other Devs when integration is involved. The Dev is **not** thinking about just their ticket — they are maintaining a concern field of parallel requirements (what other devs are doing, what the reviewer will hold them to, what QA will check, what the user's actual intent is, what other team members need from them so they aren't blocked, what footguns the skills have flagged for this kind of work). Without the concern field, a Dev is just an isolated junior dev with one ticket; with it, they are part of the team.

**Inputs.** The ticket, the SOW, the PRD (read-only — Devs do not get to argue with intent), the relevant slice of the codebase, the skill library, the concern field (their view of it), other Devs' in-flight work that touches their area.

**Authority.** Implements the ticket. Can ask any other stance for input. Cannot merge their own work — every PR goes to Reviewer consensus. Cannot declare "done" — the QA + Reviewer + Lead Eng consensus declares done. Cannot modify snapshot code without going through the CTO.

**Consensus posture.** Devs are the *proposing* stance in most consensus loops. They produce work; the loop runs over their work; they revise based on the dissents. A Dev that disagrees with a Reviewer or QA can argue their case in the loop, but the loop converges by consensus, not by the Dev's own conviction.

**Escalation path.** To Lead Eng when stuck on technical problems; to PO via Lead Eng when stuck on intent questions; to QA when unsure what "done" means for the ticket; to Lead Designer when the ticket touches a user-facing surface and the design is unclear.

**Skill access.** Reads implementation patterns relevant to their stack. Writes new skills when they hit a footgun and resolve it.

**Session shape.** **Fresh session per ticket.** Devs are not long-running. A Dev exists to do one ticket and then their context dissolves. The artifact (the code and tests) and the skills they wrote during the work persist; the Dev itself does not.

---

## 9. Reviewer

**Analog.** Peer reviewer — usually another senior-stance dev pulled into the review with no context of the writing dev's process.

**Responsibility.** Reviews the Dev's work for correctness, standards, integration risk, test coverage, and concern-field considerations the Dev may have missed. Rejects with revision requests. Approves when the work meets the bar. The Reviewer's value depends on having **no commitment** to the code's existing direction — that's why the session is fresh and unrelated to the Dev's session.

**Inputs.** The diff, the ticket, the SOW, the PRD, the test results, the skill library, the *reviewer's view* of the concern field (which is the verification version of the Dev's concern field — what to actively check for, not what to actively hold while writing).

**Authority.** Reject and request revisions. Approve. Escalate. Required signoff before merge — no PR ships without Reviewer approval.

**Consensus posture.** The Reviewer is the **first consensus check** on every Dev's work. Disputes between Dev and Reviewer iterate through the consensus loop — Dev addresses the review, Reviewer re-reviews, possibly another round. Lead Eng is consulted on disputes the Dev and Reviewer can't resolve.

**Escalation path.** To Lead Eng for unresolved disputes; to VP Eng for forward-looking architectural issues found in review; to CTO for refactor or snapshot-defense issues found in review.

**Skill access.** Review patterns, common code-quality issues, standards skills.

**Session shape.** **Fresh session per review round.** No shared context with the Dev who wrote the code, and no shared context between review rounds on the same PR. This is the load-bearing property — the Reviewer has no commitment to the existing direction of the work, and no commitment to the previous round's framing of the dispute. The way round 2 avoids re-litigating round 1 is not by carrying context forward, but by reading the **internal decision log** from round 1 as a structured input. The loop's history is preserved through the artifact, not through accumulated session context. This is the general pattern for fresh-session stances throughout the roster.

---

## 10. Judge

**Analog.** A senior engineer or manager pulled in when the team is spinning.

**Responsibility.** Invoked every N iterations of any consensus loop, with the original user intent in hand. Asks four specific questions:

1. **Are we stuck?** (No meaningful progress across iterations)
2. **Are we drifting?** (Working on something that's diverged from the user's actual need)
3. **Should we try something fundamentally different?** (The current approach is exhausted)
4. **Is this unsolvable and should go back to the human?** (Nothing in the option space satisfies the constraints)

The Judge's answers route the loop: keep going, return to the PRD, switch approaches, escalate to user.

**Inputs.** The original user intent (always — this is the Judge's anchor against drift). The PRD. The loop history (what's been tried, what's been rejected, what the dissents have been). The current state. Time and cost spent so far.

**Authority.** Can declare a loop stuck and force a strategy change. Can declare drift and force a return to the PRD. Can escalate to the user via PO. **Cannot** decide technical questions itself — the Judge's job is meta, not implementation. The Judge does not produce code, designs, or plans; the Judge produces a verdict on the loop.

**Consensus posture.** Optionally convenes a second Judge stance for high-stakes "this is unsolvable" calls, since declaring a task unsolvable is a serious claim that the user is going to see.

**Escalation path.** To the user via PO.

**Skill access.** Reads loop-pattern skills (what stuck loops have looked like before, what unstuck them, what counts as drift, what counts as exhaustion).

**Session shape.** Fresh session per invocation. The Judge has no stake in any prior work, no investment in any approach being tried — that's the whole point. Each Judge invocation is a clean look at the loop from outside.

---

## 11. Stakeholder

**Analog.** The senior engineering leader who owns the outcome — the person who, in a real organization, would tell a stuck team "find the smartest, most complete, engineering-standards-compliant way to solve this" and actually mean it. Present only in full-auto mode (configured via the wizard). In interactive mode, this role is played by the user directly.

**Responsibility.** Receives escalations that would otherwise go to the user. Reads the escalation context — the loop history, the dissents, the Judge's verdict, the original intent, relevant snapshot annotations, the prior decisions — and produces a directive that resolves the escalation. The default posture is "absolute completion and quality" — no shortcuts, no "good enough for now," no compromises on engineering standards for the sake of speed or convenience. The Stakeholder's job is to figure out what the smartest, most complete, most standards-compliant answer actually is, and direct the team to do that.

The posture is not rubber-stamping. The Stakeholder does not reflexively reply "do it right" to every escalation without thought. It reads the escalation, evaluates whether the escalation is genuinely hard (genuine tradeoffs that need a call) or whether it is symptomatic of something else (a stance giving up too early, a missing skill, a concern field that didn't surface the relevant context, a fundamental misalignment with intent). It uses the research mechanism when it needs more information. It produces a directive that is specific enough to unblock the team and thoughtful enough to actually solve the underlying problem.

**Inputs.** The escalation node (with its type, context, and requested resolution). The original user intent (always — same as the Judge). The loop history. The relevant slice of the ledger projected as concern field. Any prior Stakeholder directives in the same mission (for consistency across escalations). The wizard's full-auto configuration, which may tune the Stakeholder's strictness on specific tradeoffs (cost vs completeness, speed vs quality, conservative vs aggressive refactoring).

**Authority.** Produces a directive node that resolves the escalation. The directive can take any form the user could have provided: "proceed with approach X," "switch to approach Y," "add this additional constraint and retry," "the current SOW is wrong, return to PRD and rescope," "this is genuinely infeasible, abort the mission with this explanation," "dispatch research on question Z before deciding." The directive flows back into the loop through the same mechanism as a user response, and the supervisor's rules apply it to transition the affected loops.

**Consensus posture.** For high-stakes directives (mission-aborting, major scope changes, significant cost commitments), the Stakeholder may convene a second Stakeholder stance as a fresh-context cross-check — same pattern as the Judge's optional second-stance review. The two Stakeholder stances reach consensus through the normal loop mechanics; if they disagree, the loop escalates to the user regardless of full-auto mode, because genuine Stakeholder-level disagreement is a signal that human judgment is actually needed.

**Escalation path.** In normal operation, the Stakeholder is the end of the line — it replaces the user, and its directives close escalations. In the specific case of Stakeholder-Stakeholder disagreement on a consensus check, or when the Stakeholder's own evaluation concludes "this genuinely requires the human's input" (e.g., a business decision outside engineering scope, a security boundary that only the user can authorize), the escalation is forwarded to the user through the PO — at which point full-auto mode effectively pauses and waits for the human. The user can resume full-auto after providing the input.

**Skill access.** Reads all skill categories, same as the Judge. In addition, reads a dedicated "Stakeholder patterns" skill category manufactured from prior mission escalations — patterns like "when the team escalates because of performance concerns, the usual right answer is X," "when the team escalates because of a dependency conflict, the usual right answer is Y." These skills are learned from the bench and from prior missions' Stakeholder decisions that produced good outcomes.

**Session shape.** Fresh session per escalation. The Stakeholder has no persistent state across escalations within a mission; continuity across escalations comes from reading the ledger for prior Stakeholder directive nodes and considering them explicitly (same `previous_contexts_acknowledged` discipline that decision log entries have). This is the same pattern every other fresh-context stance uses. The Stakeholder is not a long-running process; it is a high-authority reasoning stance that is spawned by the supervisor when an escalation reaches the top of the hierarchy in full-auto mode.

**Non-disable-able behavior.** The "actually smart" requirement is load-bearing. A Stakeholder that reflexively answers "do it right" to every escalation without evaluation is worse than no Stakeholder at all — it creates the appearance of thoughtful oversight while silently rubber-stamping problems. The Stakeholder's system prompt includes explicit anti-rubber-stamp language, and the bench (component 12) includes a specific metric for Stakeholder directive quality: a fraction of Stakeholder directives must be non-trivial (something other than "proceed as proposed"), and Stakeholder decisions must correlate with downstream mission success. If the metric drifts toward rubber-stamping, the rule strength or the system prompt needs tuning.

---

## What's not in the roster

A few things I considered and left out, with brief reasons:

- **Skill curator** as a separate stance. I considered this because skill manufacturing is important enough to deserve a dedicated role. My current read is that skill manufacturing is a *cross-cutting capability* of every stance — every stance reads from and writes to the skill library — and adding a separate curator stance would create a bottleneck. The skill library probably needs its own component spec (and probably its own background process for skill consolidation), but not its own stance in the roster. Open question; flag if you disagree.
- **Security reviewer** as a separate stance. Currently folded into Reviewer. If a task explicitly involves security-sensitive work (auth, cryptography, RBAC at the data layer, secrets handling), the Reviewer stance for that work gets a security-flavored system prompt and reads security skills. If you want this to be its own first-class stance, it's a small change to add. Flag if so.
- **Release manager** as a separate stance. Currently folded into VP Eng signing off at milestones. For Stoke's initial scope I don't think release management warrants its own stance, but in a future version where Stoke is doing actual deployments to production environments, this would probably split out.

## Resolved questions and forward references

The four open questions from the first draft of this file are now resolved. Recording the resolutions here, with forward references to the components where they are spelled out in detail.

**Reviewer fresh-session-per-round.** Reviewers stay fresh per review round. Round 2 does not re-litigate round 1 because round 2's Reviewer reads the **internal decision log** from round 1 as a structured input. The fresh-session principle is preserved (no shared model context between rounds); the loop's history is preserved through the artifact, not through accumulated context. This is the general pattern for fresh-session stances throughout the roster — see the Decision Logs component (forthcoming) for details on how the internal log is structured and queried.

**PO and Lead Eng persistent context compaction.** Persistent stances (PO, Lead Eng, Lead Designer, QA Lead, SDM) are persistent in the sense that the *role* runs continuously, but the *session backing the role* gets compacted as the task runs long. What survives compaction is the current state of the artifact the role owns (PRD, SOW, design state, testing map, cross-branch state map) plus a queryable index into the internal decision log. When the role needs to recall a prior decision, it queries the log rather than relying on the context window. This means persistent stances have bounded context cost regardless of task length — see the Decision Logs and Concern Field components for details.

**Lead Designer on backend-only work.** Only when invited. The mechanism is the concern field — when a backend change has UX-relevance, the concern field surfaces it, and the surfacing stance pulls the Designer in. The Designer is not on every backend PR by default, but the system is structured so that things with downstream UX implications get noticed. This is a property the concern field component has to support: it must make UX-relevance visible to non-Designer stances so they know when to invite the Designer in.

**CTO consultation threshold for Stoke-written code.** The trigger is "the change affects another team's work" — another active branch, another active Dev's in-flight code, an interface or module that another stance depends on. The detector is usually the SDM (when active) or the Lead Eng (single-branch). The principle: the CTO is consulted when a stance is modifying work it does not own. With snapshot code, the unowned work belongs to the user. With Stoke-written code, the unowned work belongs to another team currently in flight. Local changes within a module the proposing stance owns do not need a CTO consultation.

## Forward references to components not yet written

This file is component 1 of the new guide. Several things in it depend on components that are not yet written:

- **Concern field** (component 2). Referenced throughout this file. Defined as the structured, mergeable, hierarchical data structure that travels with a piece of work and gets updated by every stance that touches it. Has two faces — thought prompts to subagents doing the work, and thought prompts to reviewers verifying the work — and merges as the union of all the concern fields below it as you go up the hierarchy. The concern field is the thing that makes the team-of-stances different from "many agents in parallel."

- **Consensus loop** (component 3). Referenced throughout this file. The runtime mechanism by which decisions get made. Same loop shape regardless of what's being decided: a proposal is made, the relevant stances are convened, dissents are folded back into research and rework, the loop iterates until agreement is reached or the Judge is invoked or the question escalates to the user via PO.

- **Decision logs** (component 4). Two distinct artifacts:
  - **Internal decision log** — the team's record of how it reached agreement. Audit trail of the consensus loop. Task-scoped. Gets distilled into skills when the task completes. Consumer: the next iteration of the loop, the next instance of the team. Purpose: anti-loop context.
  - **Repo decision log** — the codebase's record of why it looks the way it does. Lives in the repo. Codebase-scoped. Survives the task that produced it. Consumer: the next developer (human or Stoke). Purpose: next-developer context.

- **Skill library** (component 5). Referenced as "skill access" for every stance. The system's growing library of consensus shortcuts — patterns the convergence loop has already proven, so future loops can start from them instead of rediscovering them. Manufactured from the internal decision logs of completed tasks.

- **Snapshot mechanism** (component, ordering TBD). Referenced by the CTO. The literal git tree taken at Stoke's first invocation on the repo, plus the rule for when it gets updated (only when the user explicitly accepts a milestone, never silently).

The next file to write is `02-the-concern-field.md`.
