# Deterministic Scan
## Findings (critical:1 high:0 medium:5)
- [medium] ./ember/devbox/web/src/pages/ResetPassword.tsx:33 — TypeScript any:     } catch (e: any) { setError(e.data?.error || e.message || "Reset failed"); }
- [medium] ./ember/devbox/web/src/pages/SessionView.tsx:27 — TypeScript any:   const done = tasks.filter((t: any) => t.status === "done" || t.status === "committed").length;
- [medium] ./ember/devbox/web/src/pages/SessionView.tsx:52 — TypeScript any:         {tasks.map((t: any, i: number) => (
- [critical] ./ember/devbox/web/src/pages/Settings.tsx:140 — Placeholder:           placeholder="#!/bin/bash&#10;# cargo install cargo-watch&#10;# npm install -g wrangler" />
- [medium] ./ember/devbox/web/src/pages/Settings.tsx:69 — TypeScript any:     } catch (e: any) {
- [medium] ./ember/devbox/web/src/pages/VerifyEmail.tsx:15 — TypeScript any:       .catch((e: any) => { setStatus("error"); setError(e.data?.error || e.message || "Verification failed"); });

