# Deterministic Scan
## Findings (critical:0 high:1 medium:22)
- [high] ./ember/devbox/src/routes/account.ts:18 — Hardcoded localhost: const APP_URL = process.env.APP_URL || "http://localhost:3000";
- [medium] ./ember/devbox/src/routes/account.ts:15 — TypeScript any: const stripe = new Stripe(process.env.STRIPE_SECRET_KEY!, { apiVersion: "2024-04-10" as any });
- [medium] ./ember/devbox/src/routes/account.ts:17 — TypeScript any: const app = new Hono<{ Variables: { user: any; session: any; requestId: string } }>();
- [medium] ./ember/devbox/src/routes/account.ts:106 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/account.ts:134 — TypeScript any:   const { token } = await c.req.json().catch(() => ({} as any));
- [medium] ./ember/devbox/src/routes/account.ts:163 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/account.ts:182 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/account.ts:201 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/account.ts:225 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/account.ts:255 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/account.ts:280 — TypeScript any:     } catch (e: any) {
- [medium] ./ember/devbox/src/routes/account.ts:300 — TypeScript any:     } catch (e: any) {
- [medium] ./ember/devbox/src/routes/admin.ts:11 — TypeScript any: const app = new Hono<{ Variables: { user: any; session: any; requestId: string } }>();
- [medium] ./ember/devbox/src/routes/admin.ts:73 — TypeScript any:     await rawSql.begin(async (tx: any) => {
- [medium] ./ember/devbox/src/routes/admin.ts:80 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/routes/admin.ts:92 — TypeScript any:   const admin = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/ai.ts:68 — TypeScript any:     return c.json({ error: `AI provider error (${orRes.status})` }, orRes.status as any);
- [medium] ./ember/devbox/src/routes/ai.ts:84 — TypeScript any:     let lastUsage: any = null;
- [medium] ./ember/devbox/src/routes/ai.ts:134 — TypeScript any:   const data = await orRes.json() as any;
- [medium] ./ember/devbox/src/routes/api-keys.ts:14 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/api-keys.ts:25 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/api-keys.ts:27 — TypeScript any:   const label = (body as any).label || "default";
- [medium] ./ember/devbox/src/routes/api-keys.ts:47 — TypeScript any:   const user = c.get("user") as any;

