# Governance

This document describes how decisions are made in the R1 project
(legacy product name: Stoke) — who can merge what, how to propose
changes, and the path to becoming a maintainer. It complements
`STEWARDSHIP.md` (what the project promises its users) with an
account of how the project runs internally.

## Roles

### Contributor

Anyone who opens a pull request, issue, or GitHub Discussion. No
formal onboarding — read `CONTRIBUTING.md`, sign the CLA the first
time you submit code (`CLA.md`), and open a PR. Contributions are
evaluated on technical merit, not affiliation.

### Maintainer

A contributor with write access to the repository. Maintainers:

- Review and merge pull requests.
- Triage issues and close stale ones.
- Cut release tags.
- Vote on maintainer nominations and RFCs.
- Enforce the Code of Conduct.

Maintainers are listed in `.github/CODEOWNERS` (authoritative) and in
the repository's GitHub team membership.

### BDFL (Benevolent Dictator For Life)

The repository owner (`ericmacdougall`) breaks ties when maintainers
cannot reach lazy consensus on an RFC and acts as the final arbiter on
license, CLA, and org-level decisions. The BDFL role is deliberately
narrow — day-to-day work is driven by maintainer consensus, not by
BDFL decree.

## Decision process

### Small changes (bug fixes, doc updates, tests, non-breaking features)

- One maintainer approval merges.
- CI must be green.
- CLA must be signed (enforced by the CLA Assistant workflow).
- No formal discussion required.

### Architecture changes (new packages, new subsystems, cross-cutting refactors)

- Two maintainer approvals merge.
- A GitHub Discussion in the "Design" category explaining the change
  is expected before the PR opens (a short one is fine — the point is
  a written trail, not ceremony).
- CI must be green.

### Breaking changes (public API changes, CLI flag removals, config-format changes)

- Requires an RFC (GitHub Discussion in the "RFC" category) open for
  at least seven calendar days.
- Lazy consensus: if no maintainer objects within seven days, the RFC
  is accepted.
- If a maintainer objects, the BDFL breaks the tie.
- Implementation PR references the RFC in the commit message.
- Changelog entry is mandatory.

## Becoming a maintainer

Maintainer access is granted to contributors who have demonstrated
sustained, high-quality contributions and alignment with the
project's stewardship commitments.

Path:

1. **Sustained contribution.** Open and land pull requests over a
   period of at least three months. No hard count on PR volume — the
   bar is "I'd trust this person to review my code."
2. **Nomination.** An existing maintainer opens a GitHub Discussion
   in the "Governance" category nominating the contributor, with a
   short rationale.
3. **Lazy consensus, seven days.** If no existing maintainer objects
   within seven days, the nomination carries.
4. **Access granted.** The BDFL (or any maintainer with org admin) adds
   the new maintainer to the GitHub team and updates
   `.github/CODEOWNERS` if applicable.

Stepping down is always fine, no notice required. A maintainer who
has been inactive for more than twelve months is moved to "emeritus"
status — still credited, no write access.

## Code ownership

`.github/CODEOWNERS` is the authoritative source for required
reviewers on specific paths. A CODEOWNERS entry can require review
from a named maintainer or team for changes to sensitive files
(release workflows, security configuration, the ledger schema).

If a path has no CODEOWNERS entry, any maintainer may review and
merge per the decision rules above.

## Release cadence

Releases are tag-driven. There is no fixed release cadence — a tag is
cut when there is something useful to ship. The expectation is that
`main` is always releasable (CI green, no known regressions against
`STEWARDSHIP.md` commitments).

The `release.yml` GitHub Actions workflow signs artifacts with cosign
and publishes the Homebrew formula to `ericmacdougall/homebrew-stoke`.

## Code of Conduct

All participation is governed by `CODE_OF_CONDUCT.md`. Violations are
reported to the BDFL; enforcement actions (warning, temporary ban,
permanent ban) are decided by the BDFL in consultation with
maintainers.

## Amendments

This document is itself governed by the "breaking change" process —
amendments require an RFC open for seven days, lazy consensus of
maintainers, and a changelog entry.
