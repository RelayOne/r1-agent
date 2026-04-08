# Deterministic Scan
## Findings (critical:0 high:9 medium:8)
- [high] ./ember/devbox/src/index.ts:86 — Console debug:       console.log(msg.replace(/\?[^ ]*/, "?[REDACTED]"));
- [high] ./ember/devbox/src/index.ts:90 — Console debug:   console.log(msg);
- [high] ./ember/devbox/src/index.ts:147 — Console debug:   console.log("[flags] v1/workers: enabled");
- [high] ./ember/devbox/src/index.ts:154 — Console debug:   console.log("[flags] v1/ai: enabled");
- [high] ./ember/devbox/src/index.ts:233 — Console debug: console.log(`Ember Cloud API starting on :${port}`);
- [medium] ./ember/devbox/src/index.ts:142 — TypeScript any: const notEnabledWorkers = (c: any) => c.json({ error: "Workers API not enabled. Set ENABLE_V1_WORKERS=true." }, 501);
- [medium] ./ember/devbox/src/index.ts:143 — TypeScript any: const notEnabledAI = (c: any) => c.json({ error: "Managed AI not enabled. Set ENABLE_MANAGED_AI=true." }, 501);
- [medium] ./ember/devbox/src/index.ts:222 — TypeScript any:   } catch (e: any) {
- [high] ./ember/devbox/src/middleware.ts:86 — Hardcoded localhost:     const appUrl = process.env.APP_URL || "http://localhost:3000";
- [medium] ./ember/devbox/src/middleware.ts:33 — TypeScript any:       setCookie(c, blank.name, blank.value, blank.attributes as any);
- [medium] ./ember/devbox/src/middleware.ts:39 — TypeScript any:       setCookie(c, fresh.name, fresh.value, fresh.attributes as any);
- [high] ./ember/devbox/src/migrate.ts:6 — Console debug: console.log("Running Drizzle migrations...");
- [high] ./ember/devbox/src/migrate.ts:15 — Console debug:     console.log(`Running SQL migration: ${file}`);
- [high] ./ember/devbox/src/migrate.ts:24 — Console debug: console.log("All migrations complete.");
- [medium] ./ember/devbox/src/migrate.ts:19 — TypeScript any: } catch (e: any) {
- [medium] ./ember/devbox/src/rate-limit.ts:58 — TypeScript any: let pgSql: any = null;
- [medium] ./ember/devbox/src/rate-limit.ts:108 — TypeScript any:   pgSql`DELETE FROM rate_limit_hits WHERE ts < ${new Date(Date.now() - maxWindowMs).toISOString()}`.catch((e: any) => co

