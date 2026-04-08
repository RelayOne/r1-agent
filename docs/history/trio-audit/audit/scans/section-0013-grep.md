# Deterministic Scan
## Findings (critical:0 high:0 medium:12)
- [medium] ./ember/devbox/src/routes/auth.ts:15 — TypeScript any: const app = new Hono<{ Variables: { user: any; session: any; requestId: string } }>();
- [medium] ./ember/devbox/src/routes/auth.ts:47 — TypeScript any:   setCookie(c, cookie.name, cookie.value, cookie.attributes as any);
- [medium] ./ember/devbox/src/routes/auth.ts:73 — TypeScript any:   setCookie(c, cookie.name, cookie.value, cookie.attributes as any);
- [medium] ./ember/devbox/src/routes/auth.ts:96 — TypeScript any:   setCookie(c, blank.name, blank.value, blank.attributes as any);
- [medium] ./ember/devbox/src/routes/auth.ts:109 — TypeScript any:     setCookie(c, blank.name, blank.value, blank.attributes as any);
- [medium] ./ember/devbox/src/routes/auth.ts:138 — TypeScript any:     const ghUser = await ghRes.json() as any;
- [medium] ./ember/devbox/src/routes/auth.ts:144 — TypeScript any:     const emails = await emailRes.json() as any[];
- [medium] ./ember/devbox/src/routes/auth.ts:145 — TypeScript any:     const verified = emails.find((e: any) => e.primary && e.verified);
- [medium] ./ember/devbox/src/routes/auth.ts:201 — TypeScript any:         const updates: any = { providerId: ghId };
- [medium] ./ember/devbox/src/routes/auth.ts:217 — TypeScript any:     setCookie(c, cookie.name, cookie.value, cookie.attributes as any);
- [medium] ./ember/devbox/src/routes/auth.ts:251 — TypeScript any:     const gUser = await res.json() as any;
- [medium] ./ember/devbox/src/routes/auth.ts:292 — TypeScript any:     setCookie(c, cookie.name, cookie.value, cookie.attributes as any);

