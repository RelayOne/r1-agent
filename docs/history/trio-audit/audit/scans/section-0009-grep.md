# Deterministic Scan
## Findings (critical:0 high:18 medium:0)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:73 — Print debug:             print(f"Split: {len(train_set)} train, {len(test_set)} test (holdout={holdout})", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:84 — Print debug:             print(f"
{'='*60}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:85 — Print debug:             print(f"Iteration {iteration}/{max_iterations}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:86 — Print debug:             print(f"Description: {current_description}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:87 — Print debug:             print(f"{'='*60}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:170 — Print debug:                 print(f"{label}: {tp+tn}/{total} correct, precision={precision:.0%} recall={recall:.0%} accuracy={accura
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:174 — Print debug:                     print(f"  [{status}] rate={rate_str} expected={r['should_trigger']}: {r['query'][:60]}", file=sys.st
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:183 — Print debug:                 print(f"
All train queries passed on iteration {iteration}!", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:189 — Print debug:                 print(f"
Max iterations reached ({max_iterations}).", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:194 — Print debug:             print(f"
Improving description...", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:216 — Print debug:             print(f"Proposed ({improve_elapsed:.1f}s): {new_description}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:229 — Print debug:         print(f"
Exit reason: {exit_reason}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:230 — Print debug:         print(f"Best score: {best_score} (iteration {best['iteration']})", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:269 — Print debug:         print(f"Error: No SKILL.md found at {skill_path}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:315 — Print debug:     print(json_output)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:322 — Print debug:         print(f"
Report: {live_report_path}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/run_loop.py:328 — Print debug:         print(f"Results saved to: {results_dir}", file=sys.stderr)
- [high] ./ember/devbox/src/auth.ts:40 — Hardcoded localhost: const APP_URL = process.env.APP_URL || "http://localhost:3000";

