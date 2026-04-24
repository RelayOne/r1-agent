---
name: Bug report
about: Report a reproducible bug in R1
title: "bug: <short description>"
labels: ["bug", "triage"]
assignees: []
---

<!--
  Before filing:
  - Search open and closed issues for duplicates.
  - If this is a security vulnerability, DO NOT open a public issue.
    See SECURITY.md for private disclosure channels.
  - If this is a question or design discussion, use GitHub Discussions
    instead.
-->

## Summary

<!-- One or two sentences. What happened, what did you expect? -->

## Environment

- R1 version / commit:        <!-- `stoke version` or `git rev-parse HEAD` -->
- Go version:                    <!-- `go version` -->
- OS / arch:                     <!-- e.g. macOS 14.5 arm64, Ubuntu 24.04 amd64 -->
- Claude Code CLI version:       <!-- `claude --version`, if relevant -->
- Codex CLI version:             <!-- `codex --version`, if relevant -->
- Mode (1 or 2):                 <!-- auth isolation mode; see docs/operator-guide.md -->
- Pool configuration:            <!-- Claude / Codex / OpenRouter / API / mixed -->

## Command invoked

```bash
# Exact command line, with flags. Redact secrets.
stoke ...
```

## Relevant config

<!--
  Paste the relevant portion of stoke.policy.yaml (redact API keys / tokens).
  Include only the fields that affect this bug. If the bug is in default
  behavior, say "defaults" and link to docs/operator-guide.md.
-->

```yaml

```

## Steps to reproduce

1.
2.
3.

## Expected behavior

<!-- What should have happened? -->

## Actual behavior

<!-- What actually happened? Include error messages verbatim. -->

## Logs / output

<!--
  Paste the relevant log slice. For large logs attach a file or a gist.
  Redact API keys, tokens, and private file paths.
  Helpful surfaces:
    - .stoke/reports/latest.json
    - .stoke/session.json
    - stderr from the failing command
-->

<details>
<summary>Logs</summary>

```

```

</details>

## Reproducibility

- [ ] Reproduces every run
- [ ] Reproduces intermittently (estimated rate: ____ / 10 runs)
- [ ] Reproduced once; cannot trigger again

## Additional context

<!-- Anything else that might help: recent upgrades, related issues, workarounds found, etc. -->

## Checklist

- [ ] I searched existing issues and did not find a duplicate.
- [ ] I included the exact command line I ran.
- [ ] I included the R1 version / commit hash.
- [ ] I redacted any API keys, tokens, or private paths from logs.
- [ ] This is not a security vulnerability (those go to SECURITY.md).
