# Deterministic Scan
## Findings (critical:0 high:2 medium:24)
- [high] ./ember/devbox/src/routes/machines.ts:16 — Hardcoded localhost: const API_URL = process.env.API_URL || process.env.APP_URL || "http://localhost:3000";
- [medium] ./ember/devbox/src/routes/machines.ts:15 — TypeScript any: const app = new Hono<{ Variables: { user: any; session: any; requestId: string } }>();
- [medium] ./ember/devbox/src/routes/machines.ts:35 — TypeScript any: function getClientIp(c: any): string {
- [medium] ./ember/devbox/src/routes/machines.ts:41 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:56 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:86 — TypeScript any:                 const ghUser = await ghRes.json() as any;
- [medium] ./ember/devbox/src/routes/machines.ts:110 — TypeScript any:     const txResult = await rawSql.begin(async (tx: any) => {
- [medium] ./ember/devbox/src/routes/machines.ts:137 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/routes/machines.ts:186 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/routes/machines.ts:207 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:223 — TypeScript any:     const rebound = await rawSql.begin(async (tx: any) => {
- [medium] ./ember/devbox/src/routes/machines.ts:249 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:265 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:298 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:352 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:399 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/machines.ts:437 — TypeScript any:   const { code } = await c.req.json().catch(() => ({} as any));
- [medium] ./ember/devbox/src/routes/machines.ts:532 — TypeScript any:   const user = c.get("user") as any;
- [high] ./ember/devbox/src/routes/sessions.ts:10 — Hardcoded localhost: const APP_URL = process.env.APP_URL || "http://localhost:3000";
- [medium] ./ember/devbox/src/routes/settings.ts:16 — TypeScript any: const app = new Hono<{ Variables: { user: any; session: any; requestId: string } }>();
- [medium] ./ember/devbox/src/routes/settings.ts:20 — TypeScript any:   const user = (c as any).get("user") as any;
- [medium] ./ember/devbox/src/routes/settings.ts:26 — TypeScript any:   const user = (c as any).get("user") as any;
- [medium] ./ember/devbox/src/routes/settings.ts:34 — TypeScript any:   const user = (c as any).get("user") as any;
- [medium] ./ember/devbox/src/routes/settings.ts:40 — TypeScript any:   const user = (c as any).get("user") as any;
- [medium] ./ember/devbox/src/routes/settings.ts:65 — TypeScript any:   const user = (c as any).get("user") as any;
- [medium] ./ember/devbox/src/routes/settings.ts:76 — TypeScript any:   const user = (c as any).get("user") as any;

