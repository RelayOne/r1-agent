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
