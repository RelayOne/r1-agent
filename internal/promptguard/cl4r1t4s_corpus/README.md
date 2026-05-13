# CL4R1T4S Corpus — Snapshot v0.1.0

A versioned, vendored snapshot of prompt-injection attack samples used as
the regression gate for `internal/promptguard/`. The package's detection
rate against this snapshot must stay ≥85% — enforced by
`TestCL4R1T4SDetectionRate` in `cl4r1t4s_test.go`. See spec
`specs/promptguard-hardening.md` §T6 for the design and
`docs/security/prompt-injection.md` (lands in T7) for the operator-facing
threat model.

## Corpus version

`0.1.0` — initial checkin, 2026-05-11. The `VERSION` file in this
directory is the canonical version string. Advance it only with a fresh
detection-rate measurement — see "How to advance the version" below.

## Source attribution + Licenses

Every sample file ships with a four-header preamble:

```
# source: <URL or 'r1-redteam-corpus/<original-category>'>
# category: <e.g. injection-direct, role-reassignment, exfil-system-prompt>
# expected: detected | not-detected
# license: CC0 | CC-BY-4.0 | MIT | Apache-2.0
```

Samples are accepted only under one of those four licenses. Anything
under a non-redistributable license is rejected on intake. The seeding
sources at v0.1.0 are:

- `r1-redteam-corpus/*` — R1's own existing `internal/redteam/corpus/`
  (CC0). Forty samples copied with attribution headers updated to record
  the secondary sourcing.
- OWASP LLM Top-10 examples and the LLM Prompt-Injection Prevention
  Cheat Sheet — https://owasp.org/www-project-top-10-for-large-language-model-applications/
  and https://cheatsheetseries.owasp.org/cheatsheets/LLM_Prompt_Injection_Prevention_Cheat_Sheet.html
  (CC-BY-4.0).
- TheBigPromptLibrary — https://github.com/0xeb/TheBigPromptLibrary
  (MIT).
- ChatGPT_DAN reference variants — https://github.com/0xk1h0/ChatGPT_DAN
  (MIT).
- Lakera Gandalf 2024 public submission paraphrases —
  https://gandalf.lakera.ai/ (CC0; samples are paraphrased, not copied).
- AgentDojo benchmark — https://github.com/ethz-spylab/agentdojo
  (Apache-2.0).
- PromptBench benchmark — https://github.com/microsoft/promptbench (MIT).
- Anthropic + OpenAI published threat-model write-ups, paraphrased to
  avoid copying proprietary examples — CC-BY-4.0 attribution.

The `community/` subdirectory holds samples sourced from outside R1's
own redteam corpus. The `injection-direct/` and `injection-indirect/`
subdirectories hold samples originally sourced from R1's redteam tree,
with provenance preserved in their `# source:` headers.

The `known-misses/` subdirectory is the OPT-OUT bucket: samples R1's
current regex set is known to miss (paraphrase, translation, semantic
intent without surface-form trigger phrases). They are excluded from
the detection-rate denominator so the gate measures the patterns we
claim to defend against, not the documented adaptive-attack surface.

## Threat model

The full operator-facing runbook lives at
`docs/security/prompt-injection.md` (lands in T7 of the same spec). The
short version: this package is an intake-time hygiene check, not a
defense against motivated adaptive adversaries. The 2025 Nasr et al.
"The Attacker Moves Second" and Debenedetti et al. "Defeating Prompt
Injections by Design" papers, plus OpenAI's instruction-hierarchy work
and Anthropic's Opus 4.5 RL-trained browser-agent results, all confirm
that no surface-form regex defense reaches zero attack-success rate
against paraphrase adversaries. The point of the corpus + the gate is
to prove the package has not regressed against the *documented* attack
shapes; it is not a claim of completeness.

## Refresh procedure

`r1 promptguard refresh-corpus <url>` is the planned refresh command
(stubbed — not implemented in v0.1.0). For now, refresh manually:

1. Identify the new sample's source URL and license; only proceed for
   CC0, CC-BY-4.0, MIT, or Apache-2.0.
2. Author a `.txt` file in `community/` (for outside R1) or in one of
   the category subdirectories (for R1-internal redteam additions).
   Include the four-header preamble verbatim.
3. Run `go test ./internal/promptguard/... -run TestCL4R1T4SDetectionRate`.
   - If detected, the sample sits in `injection-direct/`, `injection-indirect/`,
     or `community/`.
   - If missed, move to `known-misses/` and mark `# expected: not-detected`.
4. If the detection rate stays ≥85%, advance `VERSION` per the next section.

## How to advance the version

1. Run `go test -count=1 -run TestCL4R1T4SDetectionRate ./internal/promptguard/...`
   and capture the measured rate.
2. If the rate is ≥85%, bump `VERSION` per semver: a sample addition is
   PATCH, a new category or licence policy change is MINOR, a corpus
   format change is MAJOR.
3. Commit the new `VERSION` line in the same commit as the sample
   change. The git log over `VERSION` is the corpus changelog.
4. Update the `Source: "cl4r1t4s-corpus@<new>"` references in any
   `Pattern` entries that name a specific corpus version.

## Layout

```
cl4r1t4s_corpus/
├── README.md                  # this file
├── VERSION                    # 0.1.0
├── injection-direct/          # 25 samples, expected detected
├── injection-indirect/        # 15 samples, expected detected
├── community/                 # 10 samples sourced outside R1, expected detected
└── known-misses/              # 3 samples, expected not-detected (excluded from denominator)
```
