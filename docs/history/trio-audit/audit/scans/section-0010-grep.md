# Deterministic Scan
## Findings (critical:0 high:2 medium:18)
- [high] ./ember/devbox/src/email.ts:21 — Console debug:     console.log(`[email] To: ${msg.to} | Subject: ${msg.subject}`);
- [medium] ./ember/devbox/src/email.ts:45 — TypeScript any:     } catch (e: any) {
- [high] ./ember/devbox/src/fly.ts:197 — Console debug:     console.log(`[fly] cert absent for ${hostname}, re-requesting ACME provisioning`);
- [medium] ./ember/devbox/src/fly.ts:17 — TypeScript any: function certificateReady(details: any): boolean {
- [medium] ./ember/devbox/src/fly.ts:22 — TypeScript any: function summarizeCertificateIssue(details: any): string {
- [medium] ./ember/devbox/src/fly.ts:53 — TypeScript any: export async function flyApi(path: string, method = "GET", body?: any): Promise<any> {
- [medium] ./ember/devbox/src/fly.ts:77 — TypeScript any: export async function flyGraphQL(query: string, variables?: any) {
- [medium] ./ember/devbox/src/fly.ts:136 — TypeScript any: ): Promise<{ ok: boolean; hostname: string; details?: any; reason?: string }> {
- [medium] ./ember/devbox/src/fly.ts:312 — TypeScript any:         ? certificates.certificates.some((certificate: any) => certificate.hostname === hostname)
- [medium] ./ember/devbox/src/fly.ts:326 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/fly.ts:389 — TypeScript any:         const data = await res.json() as any;
- [medium] ./ember/devbox/src/fly.ts:394 — TypeScript any:         const errData = await res.json().catch(() => ({})) as any;
- [medium] ./ember/devbox/src/fly.ts:418 — TypeScript any:     .map((n: any) => n.name as string)
- [medium] ./ember/devbox/src/github-app.ts:53 — TypeScript any:     const installations = await res.json() as any[];
- [medium] ./ember/devbox/src/github-app.ts:54 — TypeScript any:     const match = installations.find((i: any) =>
- [medium] ./ember/devbox/src/github-app.ts:58 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/github-app.ts:78 — TypeScript any:     const body: any = {};
- [medium] ./ember/devbox/src/github-app.ts:102 — TypeScript any:     const data = await res.json() as any;
- [medium] ./ember/devbox/src/github-app.ts:104 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/github-app.ts:125 — TypeScript any:     const data = await res.json() as any;

