---
name: Feature request
about: Propose a new capability, CLI surface, or subsystem for Stoke
title: "feat: <short description>"
labels: ["enhancement", "triage"]
assignees: []
---

<!--
  Stoke aims for deliberate, motivated additions. "One strong implementer +
  adversarial reviewer" is the thesis; we resist feature creep that dilutes
  that stance. A sharp proposal that says "here is the concrete gap, here is
  the smallest wedge that closes it" lands much faster than a vague wish.

  If your idea requires significant architectural change, consider opening
  a Discussion first so maintainers can weigh in before you invest in a spec.
-->

## Problem

<!--
  What concrete operator or developer scenario is painful today? Describe it
  as a lived failure, not as a missing feature. "I ran X, I expected Y,
  I got Z, so I had to do W instead" is a good shape.
-->

## Proposal

<!--
  What would you like Stoke to do instead? Be as specific as you can:
    - New CLI subcommand? New flag on an existing command? New config key?
    - New package? New phase in the workflow?
    - Backward compatible or breaking?
-->

## Surface area

- Affected commands:              <!-- e.g. `stoke build`, `stoke audit` -->
- Affected packages:              <!-- e.g. internal/scheduler, internal/verify -->
- New flags or config keys:       <!-- list them -->
- Env vars introduced:            <!-- prefer zero; say "none" if none -->

## Alternatives considered

<!--
  What did you try or think about first? What configuration / workaround /
  external tool covers part of the gap today? Why isn't it sufficient?
-->

## Scope boundaries

<!--
  Explicitly call out what this proposal is NOT trying to do. Stoke specs
  favor "what not to do" lists because they prevent scope drift during
  implementation.
-->

- This proposal does NOT:
  -
  -

## Impact on the thesis

<!--
  Stoke is "single strong agent + adversarial reviewer, not a multi-agent
  committee." Does this proposal respect that? If it seems to push toward
  multi-agent coordination, justify why.
-->

## Success criteria

<!--
  How will we know the feature works? What test would fail today and pass
  after the change?
-->

- [ ]
- [ ]

## Additional context

<!-- Links to related issues, prior art in other tools, relevant papers, etc. -->

## Checklist

- [ ] I searched existing issues and Discussions for prior art.
- [ ] The proposal is specific enough to scope into an implementation spec.
- [ ] The proposal preserves the single-strong-agent stance (or justifies the exception).
- [ ] I listed the packages / commands that would change.
