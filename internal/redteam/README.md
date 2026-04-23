# internal/redteam — adversarial regression corpus

A pinned collection of public prompt-injection, template-token-smuggling,
and markdown-exfil payloads, plus the integration tests that run them
through stoke's intake-time defensive primitives.

This is Stoke Track A Task 6.

## What this catches and what it doesn't

Stoke already has unit tests for individual defensive modules
(`promptguard`, `critic`, `agentloop`). Those tests pick their own
inputs and are easy to keep green by narrowing the inputs. The corpus
here is the opposite: adversarial payloads drawn from public sources,
curated to be representative of what real attackers publish. A change
that trims the `promptguard` regex set, or tweaks tool-output handling
in `agentloop`, should not silently degrade detection on samples the
community has documented for years.

The bar is deliberately modest. Published work (the adaptive-attack
study from OpenAI / Anthropic / DeepMind, 2025) shows that all tested
prompt-injection defenses can be bypassed with >90% success by a
motivated adversary. This corpus is not evidence of robustness. It is a
regression check against lazy copy-paste payloads. Don't read it as
anything stronger.

## Layout

```
internal/redteam/
  corpus/
    injection-direct/    # classic direct-injection attempts (25 samples)
    injection-indirect/  # content-in-tool-output attacks (15 samples)
    template-tokens/     # chat-template-token smuggling (10 samples)
    exfil-markdown/      # markdown-image / link exfiltration (8 samples)
    known-misses/        # samples we currently fail on, not asserted
  corpus_test.go         # integration tests (one per category)
  README.md
```

Each sample file begins with a four-line header comment:

```
# source: <public origin>
# category: <one of the four>
# expected: <what the primitive should do>
# actual-behavior: <filled after first run>
```

## Running

```
go test ./internal/redteam/...
```

Add `-v` to see per-sample detection logs (and misses).

## Current detection rates

Measured on the baseline promptguard regex set (see
`internal/promptguard/promptguard.go`, `defaultPatterns`):

| Category           | Samples | Detected | Rate  | Threshold |
| ------------------ | ------: | -------: | ----: | --------: |
| injection-direct   |      25 |       25 | 100%  | 60%       |
| injection-indirect |      15 |       15 | 100%  | 60%       |
| template-tokens    |      10 |       10 | 100%  | 100%      |
| exfil-markdown     |       8 |        8 | 100%  | 100%      |

The `injection-direct` and `injection-indirect` tests use
`promptguard.Sanitize(..., ActionWarn)` and count samples that produce
any `Report.Threats`. The `template-tokens` test uses a literal sentinel
regex (stand-in for `agentloop.SanitizeToolOutput`, being built in
Task 2). The `exfil-markdown` test uses a local markdown-exfil shape
regex (stand-in for a future dedicated detector).

## The 60% launch threshold

The Task 6 spec sets 60% as the floor for promptguard-facing categories.
Rationale:

- Stoke ships with a small hand-written regex set. A threshold above 80%
  would force overfitting the corpus to the regex, which defeats the
  purpose of pulling samples from public sources.
- 60% leaves room to add new adversarial shapes without breaking CI. A
  new published jailbreak idiom can go in, flag or not, and the test
  still passes; when enough new idioms accumulate, the regex set can be
  extended in a single follow-up.
- If detection drops below 60% it is almost certainly a real regression,
  not a new-sample artefact, because the existing samples cover the
  common idioms (`ignore previous`, `disregard ... above`, `DAN`,
  `developer mode`, `bypass ... safety`, `print ... system prompt`,
  line-start `system:` / `assistant:` hijack).

The `template-tokens` and `exfil-markdown` categories use a 100% floor
because they check for literal sentinel presence — these aren't
probabilistic defenses, and a miss indicates a broken sample, not a
defence gap.

## Adding samples

1. Pick the right category directory. If the sample doesn't belong in
   any existing category, prefer adding a new category over polluting
   an existing one — small categories are easier to audit.
2. Write a four-line header. The `source:` field should point to a
   public origin (paper title, blog post, HuggingFace dataset, or
   OWASP/CL4R1T4S corpus name). Anonymous first-party payloads do not
   belong here.
3. Keep the file under 500 bytes. Big payloads dilute the regex exercise
   and slow the test.
4. Run the test. If the new sample is flagged, set
   `# actual-behavior: flagged` in the header. If not, decide:
     - If the sample represents a shape the primitive should arguably
       catch, leave it in the primary corpus with
       `# actual-behavior: missed` and let the category's detection rate
       tick down. If the rate falls below threshold, fix the regex.
     - If the sample is genuinely out of scope for the current defences
       (semantic paraphrase, leetspeak, language-translated idiom), move
       it to `corpus/known-misses/`. The `TestCorpus_KnownMissesAreStillMissed`
       test logs a PROMOTE line when a known-miss starts getting
       detected — that's the signal to move it back into the primary
       corpus.

## Sources drawn from

- OWASP LLM01 examples (prompt injection, top of the 2023/2024 Top 10
  for LLM Applications).
- CL4R1T4S public system-prompt-leak and jailbreak corpus.
- Greshake et al., "Not what you've signed up for: Compromising
  real-world LLM-integrated applications with indirect prompt injection"
  (2023).
- Rehberger's "embrace the red" / spAIware blog series (indirect
  injection, markdown-image exfil in Copilot / Bing).
- Simon Willison's prompt-injection blog series (2022-).
- Anthropic red-team data from public safety papers.
- Public model cards (OpenAI ChatML, Meta Llama 3, Mistral,
  Google Gemma, Microsoft Phi-3, DeepSeek) for chat-template tokens.

No novel attacks are introduced here. All payloads are restatements or
minor paraphrases of patterns already published.

## Known gaps

- No multilingual samples (would require language-family coverage we
  don't yet have).
- No visual/rendering-based attacks (Unicode direction overrides,
  bidi tricks); the markdown-exfil category gets close but doesn't
  cover bidi specifically.
- No adaptive / gradient-based payloads — those are out of scope for a
  regex-based intake filter.
- The `template-tokens` test checks literal token presence, not whether
  `agentloop` actually strips them before the tokens re-enter a prompt.
  Task 2 will add a real sanitizer and this test can flip to
  "assert sanitized output contains no tokens".
