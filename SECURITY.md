# Security Policy

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| 0.1.x   | Yes                |
| < 0.1   | No                 |

## Reporting a Vulnerability

If you discover a security vulnerability in Stoke, please report it responsibly.

**Email:** security@goodventures.dev

Please include:

- A description of the vulnerability
- Steps to reproduce
- Affected versions
- Any potential impact assessment

We will acknowledge receipt within 48 hours and aim to provide a fix or mitigation
within 7 days for critical issues.

**Please do not open a public GitHub issue for security vulnerabilities.**

### Preferred Disclosure Channel — GitHub Security Advisories

The preferred channel is GitHub's private Security Advisories. Visit
<https://github.com/ericmacdougall/stoke/security/advisories/new> (signed-in users
only) and file a new draft advisory. This routes the report to maintainers without
public exposure and lets us collaborate on a fix through a private fork. Email to
`security@goodventures.dev` remains a valid alternative; use it if GitHub access
is unavailable or if the report concerns a third-party dependency we re-ship.

### What Stoke Does Not Defend Against

Stoke's prompt-injection defense layer (`internal/promptguard/`) is deliberately
modest; it catches copy-pasted jailbreak strings and template-token smuggling, not
sophisticated adaptive attacks. The authoritative threat model and the full list
of in-scope and out-of-scope adversary capabilities is in
[docs/security/prompt-injection.md](docs/security/prompt-injection.md). In brief,
Stoke does **not** defend against: adversaries with direct access to the operator's
shell, adversaries who can modify repository source files, state-sponsored or
hardware-level supply-chain compromise, or adaptive prompt-injection authored by
motivated attackers (see the 2025 OpenAI/Anthropic/DeepMind adaptive-attack study
for the >90% bypass rate across 12 published defenses). Stoke's layer is a
hygiene check, not a security boundary.

### Reported Bypasses — Honor List

We maintain a public honor list of researchers whose reports improved Stoke's
security posture. Entries are added by the maintainers after a responsible-
disclosure cycle completes. If you would like anonymous credit, say so in your
report and we will omit the name.

| Date       | Reporter           | Summary                                        | Fix commit / PR |
|------------|--------------------|------------------------------------------------|-----------------|
| (none yet) |                    |                                                |                 |

<!--
  To add an entry: append a row above with the disclosure date (YYYY-MM-DD),
  reporter name (or "anonymous, by request"), one-line summary of the bypass,
  and the short SHA or PR number that fixed it. Keep entries chronological.
-->

## Security Model

Stoke's security architecture includes:

- **11-layer policy engine** enforcing tool restrictions, sandbox isolation, and scope constraints
- **Enforcer hooks** (PreToolUse/PostToolUse) installed in every worktree
- **18-rule deterministic scanner** for secrets, eval, injection, and exec patterns
- **Sandbox fail-closed** (`sandbox.failIfUnavailable: true`)
- **MCP triple isolation** in plan and verify phases
- **Process group isolation** with SIGTERM/SIGKILL cleanup
- **Auth isolation** stripping API keys from subprocess environments

See [docs/operator-guide.md](docs/operator-guide.md) for the full security configuration reference.
