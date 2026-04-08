# Deterministic Scan
## Findings (critical:9 high:15 medium:0)
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:98 — Empty body:                 pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:112 — Empty body:                     pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:136 — Empty body:                 pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:232 — Empty body:             pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:300 — Empty body:                     pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:304 — Empty body:         pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:341 — Empty body:                     pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:384 — Empty body:         pass
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:429 — Empty body:             pass
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:306 — Print debug:         print("Note: lsof not found, cannot check if port is in use", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:408 — Print debug:         print(f"Error: {workspace} is not a directory", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:413 — Print debug:         print(f"No runs found in {workspace}", file=sys.stderr)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:435 — Print debug:         print(f"
  Static viewer written to: {args.static}
")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:450 — Print debug:     print(f"
  Eval Viewer")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:451 — Print debug:     print(f"  ─────────────────────────────────")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:452 — Print debug:     print(f"  URL:       {url}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:453 — Print debug:     print(f"  Workspace: {workspace}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:454 — Print debug:     print(f"  Feedback:  {feedback_path}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:456 — Print debug:         print(f"  Previous:  {args.previous_workspace} ({len(previous)} runs)")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:458 — Print debug:         print(f"  Benchmark: {benchmark_path}")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:459 — Print debug:     print(f"
  Press Ctrl+C to stop.
")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:466 — Print debug:         print("
Stopped.")
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:443 — Hardcoded localhost:         server = HTTPServer(("127.0.0.1", port), handler)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/skill-creator/skills/skill-creator/eval-viewer/generate_review.py:446 — Hardcoded localhost:         server = HTTPServer(("127.0.0.1", 0), handler)

