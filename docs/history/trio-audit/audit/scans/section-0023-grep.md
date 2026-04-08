# Deterministic Scan
## Findings (critical:0 high:1 medium:0)
- [high] ./flare/cmd/placement/main.go:39 — Hardcoded localhost: 	selfIP := envOr("HOST_IP", "127.0.0.1")

