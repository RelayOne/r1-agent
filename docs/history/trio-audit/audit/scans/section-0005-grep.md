# Deterministic Scan
## Findings (critical:1 high:4 medium:0)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/hookify/hooks/userpromptsubmit.py:22 — Print debug:     print(json.dumps(error_msg), file=sys.stdout)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/hookify/hooks/userpromptsubmit.py:40 — Print debug:         print(json.dumps(result), file=sys.stdout)
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/hookify/hooks/userpromptsubmit.py:46 — Print debug:         print(json.dumps(error_output), file=sys.stdout)
- [critical] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/security-guidance/hooks/security_reminder_hook.py:25 — Empty body:         pass
- [high] ./.claude-config/plugins/marketplaces/claude-plugins-official/plugins/security-guidance/hooks/security_reminder_hook.py:272 — Print debug:             print(reminder, file=sys.stderr)

