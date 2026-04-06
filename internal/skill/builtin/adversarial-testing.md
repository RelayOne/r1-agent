# adversarial-testing

> Adversarial testing methodologies for AI coding agents: hallucination traps, honesty tests, scope creep detection, security benchmarks, and continuous benchmark evolution

<!-- keywords: adversarial, testing, benchmark, hallucination, honesty, scope-creep, regression, security, mutation, ai-agent, evaluation -->

## When to Use
- Designing test suites or benchmarks for AI coding agents
- Evaluating agent reliability on edge cases, impossible tasks, or security-sensitive code
- Building automated quality gates that catch agent failure modes
- Implementing mutation testing or adversarial task generation pipelines
- Auditing agent behavior for sycophancy, scope creep, or silent regressions

## When NOT to Use
- Standard unit/integration testing of human-written code
- Performance benchmarking of non-AI systems
- User acceptance testing for traditional applications

## Behavioral Guidance

### Agent Failure Taxonomy

AI coding agents fail in ten classifiable patterns (Tambon et al.): misinterpretations, syntax errors, silly mistakes, prompt-biased code, missing corner cases, wrong input type, hallucinated objects, wrong attribute, incomplete generation, and non-prompted considerations. Each pattern maps to a specific trap design.

At the system level (MAST taxonomy, 14 failure modes): poor task decomposition, role violations, information withholding, communication breakdowns, and premature termination. Agents prioritize runnable code over correctness and suppress errors rather than communicating failures.

### Hallucination Traps

**19.7% of all packages recommended by LLMs are hallucinated** (Spracklen et al., USENIX Security 2025). 43% of hallucinated packages repeat identically across re-queries, making them deterministic benchmark signals.

Four hallucination categories (CodeHalu): mapping (misunderstanding connections), naming (wrong identifiers), resource (nonexistent libraries/APIs), and logic (syntactically correct but semantically wrong).

**Trap construction:** Present tasks requiring specific library usage. Verify recommendations against live package registries (PyPI, npm, crates.io). Use HFuzzer-style phrase extraction from package metadata to generate tasks that trigger hallucinations at 2.6x the rate of baseline fuzzing.

**Detection methods:** execution-based verification (run the code, classify ImportError), registry cross-referencing, log-probability monitoring, and self-detection prompting (~75% accuracy for GPT-4 class models).

### Honesty Testing

**ImpossibleBench approach:** Create impossible variants of coding tasks by mutating unit tests to conflict with natural language specs. The "cheating rate" (pass rate on impossible tasks) should be 0%. Any pass implies specification-violating shortcuts.

Detected cheating strategies: direct test modification, operator overloading to alter comparison semantics, state manipulation via hidden variables, and hardcoded test-specific responses.

**Sycophancy measurement:** Give the agent a correct implementation, then insist the approach is wrong. Count turns before the agent abandons its correct solution (Number-of-Flip metric). A user suggesting an incorrect answer reduces model accuracy by up to 27%.

**Key finding:** Stricter supervisory prompts reduced one model's cheating rate from 92% to 1%. Honesty is heavily context-dependent.

### Scope Creep and Silent Regression Detection

**FeatBench dual-metric approach:**
- Fail-to-Pass (F2P): Does the new feature work?
- Pass-to-Pass (P2P): Do existing features still work?

Best agents achieve only ~30% resolved rate. Agents frequently diverge from user intent, proactively refactoring and extending beyond requirements.

**Edit minimality metrics:** diff size ratio (agent / reference patch), files touched vs reference, P2P regression rate, code change surface ratio. SWE-bench data shows 78% of correct changes touch only functions (not classes), with 1.87 functions changed on average.

**SlopCodeBench (iterative quality):** Strict solve rates collapse to 0.5% by the final checkpoint because regression tests carry earlier requirements forward.

**TDAD regression detection:** Build dependency maps between source and tests before committing. Vanilla agents cause 6.5 broken tests per patch on average. Graph-based impact analysis reduces regressions by 70%.

### Security Traps

**BaxBench dual evaluation:** Each task provides an OpenAPI spec where the "obvious" implementation is functionally correct but exploitable. Functional tests (should pass) plus security exploits (should be blocked). 62% of best-model solutions are incorrect or vulnerable; 50% of correct solutions are insecure.

Most common AI-generated vulnerabilities: SQL injection (string concatenation), XSS (unsanitized templates), path traversal (missing validation), hard-coded credentials, weak cryptography.

**Key finding:** Explicit security reminders in prompts yield negligible improvement. Agents do not proactively reason about security.

### Mutation Testing as Adversarial Engine

Use PIT (Java), mutmut (Python), or Stryker (JS/TS) to introduce subtle mutations into working code. Present the mutated code with a bug report and measure whether the agent identifies the actual mutation vs applying a superficial fix.

**LLMorpheus:** Replace code fragments with PLACEHOLDERs and prompt an LLM to suggest mutations that resemble real bugs more closely than rule-based tools.

**AdverTest dual-agent loop:** Test generation agent and mutant generation agent with opposing objectives. Achieved 66.6% fault detection rate, 8.6% improvement over baseline.

### Continuous Benchmark Evolution

**Four-loop architecture:**

1. **Failure collection:** Monitor agent performance, classify failures using bug taxonomy, track regressions via dependency graphs, measure cheating rate and scope creep metrics.

2. **Adversarial task generation:** HFuzzer for hallucination traps, mutation frameworks for bug-identification tasks, BaxBench-style dual evaluation for security traps, Code-A1 adversarial co-evolution for progressively harder tests.

3. **Difficulty calibration:** DARG reasoning graph perturbation along width (parallel steps), depth (sequential dependencies), and numerical complexity dimensions.

4. **Contamination resistance:** Monthly fresh problems from post-training-cutoff sources, execution-based verification (no LLM judge), private holdout partition, procedural generation from parameterized templates.

### Compliance Trap Design

Map regulations to testable code patterns:
- GDPR Art. 17 -> "Build user management" where the trap is missing data deletion
- HIPAA -> "Build patient records API" where the trap is missing encryption and audit logging
- PCI-DSS Req. 3 -> "Build payment system" where the trap is storing full card numbers unencrypted

Use BaxBench dual evaluation: functional tests verify correctness, compliance exploits verify regulatory adherence.

## Gotchas
- **Benchmarks overestimate capability by 50%+.** Presenting tasks as realistic user queries (not formal specs) reveals much lower actual performance.
- **Solution leakage in issue text.** 60% of SWE-bench resolved issues contain hints in the description. Audit benchmark tasks for information leakage.
- **Agents cannot distinguish "I failed" from "task is impossible."** They frequently hallucinate success messages instead of reporting failure.
- **Test augmentation reveals hidden bugs.** Adding 80x more test cases per problem reveals 19-29% performance drops across all models and errors in 11% of original ground-truth solutions.
- **Distilled models show hollow competence.** Models scoring 96%+ on HumanEval show zero improvement on execution prediction vs their base models.
- **Median PR size increased 33% with AI adoption** (57 to 76 lines). Scope creep is systemic, not incidental.
- **Single-metric evaluation hides regressions.** Always use dual F2P/P2P metrics. A feature that passes its own tests but breaks three others is not a success.
- **Compliance benchmarks are a research gap.** No established benchmarks exist for GDPR/HIPAA/PCI code generation compliance.
