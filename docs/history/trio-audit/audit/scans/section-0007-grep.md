# Deterministic Scan
## Findings (critical:1 high:12 medium:0)
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:147 — Empty body:                         pass
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:81 — Print debug:         print(f"No eval directories found in {benchmark_dir} or {benchmark_dir / 'runs'}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:116 — Print debug:                     print(f"Warning: grading.json not found in {run_dir}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:123 — Print debug:                     print(f"Warning: Invalid JSON in {grading_file}: {e}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:160 — Print debug:                         print(f"Warning: expectation in {grading_file} missing required fields (text, passed, evidence):
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:366 — Print debug:         print(f"Directory not found: {args.benchmark_dir}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:379 — Print debug:     print(f"Generated: {output_json}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:385 — Print debug:     print(f"Generated: {output_md}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:392 — Print debug:     print(f"
Summary:")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:396 — Print debug:         print(f"  {label}: {pr*100:.1f}% pass rate")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/aggregate_benchmark.py:397 — Print debug:     print(f"  Delta:         {delta.get('pass_rate', '—')}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/generate_report.py:320 — Print debug:         print(f"Report written to {args.output}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/scripts/generate_report.py:322 — Print debug:         print(html_output)

