# Deterministic Scan
## Findings (critical:2 high:1 medium:10)
- [medium] ./ember/devbox/src/__tests__/terminal.test.ts:116 — TypeScript any:     expect(rows.every((r: any) => r.status === "revoked" && r.revoke_reason === "logout")).toBe(true);
- [medium] ./ember/devbox/src/__tests__/terminal.test.ts:159 — TypeScript any:     const reasons = rows.map((r: any) => r.revoke_reason);
- [high] ./ember/devbox/vite.config.ts:10 — Hardcoded localhost:       "/api": "http://localhost:3000",
- [critical] ./ember/devbox/web/src/components/NewMachineModal.tsx:115 — Placeholder:             <input className="input" value={name} onChange={e => setName(e.target.value)} placeholder="api-server" autoF
- [critical] ./ember/devbox/web/src/components/NewMachineModal.tsx:145 — Placeholder:               <input className="input" value={gitRepo} onChange={e => setGitRepo(e.target.value)} placeholder="https://g
- [medium] ./ember/devbox/web/src/components/NewMachineModal.tsx:57 — TypeScript any:     } catch (e: any) {
- [medium] ./ember/devbox/web/src/components/NewMachineModal.tsx:88 — TypeScript any:     } catch (e: any) {
- [medium] ./ember/devbox/web/src/lib/api.ts:6 — TypeScript any:   data: any;
- [medium] ./ember/devbox/web/src/lib/api.ts:7 — TypeScript any:   constructor(status: number, data: any) {
- [medium] ./ember/devbox/web/src/lib/api.ts:54 — TypeScript any: export const post = <T = any>(path: string, body?: any) =>
- [medium] ./ember/devbox/web/src/lib/api.ts:56 — TypeScript any: export const put = <T = any>(path: string, body?: any) =>
- [medium] ./ember/devbox/web/src/lib/auth.tsx:47 — TypeScript any:     } catch (e: any) { return e.message || "Login failed"; }
- [medium] ./ember/devbox/web/src/lib/auth.tsx:55 — TypeScript any:     } catch (e: any) { return e.message || "Registration failed"; }

