# Deterministic Scan
## Findings (critical:0 high:1 medium:5)
- [medium] ./ember/devbox/src/routes/workers.ts:82 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/routes/workers.ts:124 — TypeScript any:   } catch (e: any) {
- [high] ./ember/devbox/src/seed.ts:25 — Console debug: console.log(`Admin user created: ${email} (id: ${userId})`);
- [medium] ./ember/devbox/src/__tests__/billing.test.ts:112 — TypeScript any:     expect(rows.every((r: any) => r.status === "cancelled")).toBe(true);
- [medium] ./ember/devbox/src/__tests__/identity.test.ts:42 — TypeScript any:     expect(rows.map((r: any) => r.provider)).toEqual(["github", "google"]);
- [medium] ./ember/devbox/src/__tests__/setup.ts:32 — TypeScript any:   } catch (e: any) {

