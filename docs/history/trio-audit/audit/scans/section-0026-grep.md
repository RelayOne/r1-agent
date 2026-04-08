# Deterministic Scan
## Findings (critical:4 high:0 medium:2)
- [critical] ./flare/internal/store/store.go:505 — Placeholder: 	// Build placeholders for IN clause
- [critical] ./flare/internal/store/store.go:506 — Placeholder: 	placeholders := make([]string, len(deadHostIDs))
- [critical] ./flare/internal/store/store.go:509 — Placeholder: 		placeholders[i] = fmt.Sprintf("$%d", i+1)
- [critical] ./flare/internal/store/store.go:519 — Placeholder: 		RETURNING id`, strings.Join(placeholders, ","))
- [medium] ./flare/sdk/typescript/src/index.ts:69 — TypeScript any:   private async request<T>(method: string, path: string, body?: any): Promise<T> {
- [medium] ./flare/sdk/typescript/src/index.ts:88 — TypeScript any:       throw new Error((err as any).error || `Flare API ${res.status}`);

