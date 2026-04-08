# Deterministic Scan
## Findings (critical:2 high:0 medium:7)
- [critical] ./ember/devbox/web/src/pages/Admin.tsx:103 — Placeholder:                   <input className="input" style={{ width: 80 }} placeholder="$" value={creditAmount} onChange={e => set
- [critical] ./ember/devbox/web/src/pages/Admin.tsx:104 — Placeholder:                   <input className="input" style={{ flex: 1 }} placeholder="Description" value={creditDesc} onChange={e 
- [medium] ./ember/devbox/web/src/pages/Admin.tsx:113 — TypeScript any:               {detail.machines.map((m: any) => (
- [medium] ./ember/devbox/web/src/pages/Admin.tsx:124 — TypeScript any:               {detail.billingEvents.slice(0, 20).map((e: any) => (
- [medium] ./ember/devbox/web/src/pages/Credits.tsx:7 — TypeScript any: interface Transaction { type: string; amount_cents: number; balance_after_cents: number; created_at: string; metadata: a
- [medium] ./ember/devbox/web/src/pages/Dashboard.tsx:390 — TypeScript any: function StokeProgress({ data }: { data: any }) {
- [medium] ./ember/devbox/web/src/pages/Dashboard.tsx:416 — TypeScript any:   const done = tasks.filter((t: any) => t.status === "done" || t.status === "committed").length;
- [medium] ./ember/devbox/web/src/pages/Dashboard.tsx:426 — TypeScript any:       {tasks.map((t: any) => (
- [medium] ./ember/devbox/web/src/pages/ForgotPassword.tsx:17 — TypeScript any:     } catch (e: any) {

