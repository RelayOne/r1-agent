// Toast notifications — replaces blocking alert() calls in panels.
//
// Spec: audit/scan-ts-stubs.md item #9. Tauri 2 WebView surfaces alert()
// as a system-modal prompt that blocks the entire renderer thread; user
// can't keep working until they dismiss. Toasts overlay the existing
// panel and auto-dismiss, so failures surface without disrupting flow.
//
// API:
//   import { toast } from "@/lib/toast";
//   toast.error("Save failed for foo");
//   toast.warn("Pack manifest has 2 unresolved skills");
//   toast.info("Skill installed");
//
// Implementation: a single fixed-position container appended to <body>;
// each toast is a div that fades in, persists for ~4 s, fades out. No
// dependency. No state library. The container is created lazily on the
// first toast() call so this module has zero cost in tests that never
// notify.

type ToastKind = "info" | "warn" | "error";

const TOAST_TTL_MS = 4_000;
const TOAST_FADE_MS = 200;

let container: HTMLElement | null = null;

function ensureContainer(): HTMLElement {
  if (container) return container;
  if (typeof document === "undefined") {
    // Test contexts that bypass the DOM get a no-op proxy. Keeps the
    // toast helper safe to import from headless unit tests.
    return { appendChild: () => undefined } as unknown as HTMLElement;
  }
  const el = document.createElement("div");
  el.id = "r1-toast-container";
  el.setAttribute("role", "status");
  el.setAttribute("aria-live", "polite");
  el.style.cssText = [
    "position:fixed",
    "bottom:1rem",
    "right:1rem",
    "z-index:9999",
    "display:flex",
    "flex-direction:column",
    "gap:0.5rem",
    "max-width:24rem",
    "pointer-events:none",
  ].join(";");
  document.body.appendChild(el);
  container = el;
  return el;
}

function show(kind: ToastKind, message: string): void {
  const root = ensureContainer();
  if (typeof document === "undefined") return;
  const node = document.createElement("div");
  node.setAttribute("data-toast-kind", kind);
  const palette: Record<ToastKind, string> = {
    info: "#1f6feb",
    warn: "#d29922",
    error: "#f85149",
  };
  node.style.cssText = [
    "background:#1c2026",
    `border-left:4px solid ${palette[kind]}`,
    "color:#e6e8ee",
    "padding:0.6rem 0.9rem",
    "border-radius:6px",
    "box-shadow:0 4px 16px rgba(0,0,0,0.4)",
    "font-size:0.875rem",
    "line-height:1.4",
    "opacity:0",
    `transition:opacity ${TOAST_FADE_MS}ms ease-out`,
    "pointer-events:auto",
  ].join(";");
  node.textContent = message;
  root.appendChild(node);
  // Trigger fade-in on next frame so the transition runs.
  requestAnimationFrame(() => {
    node.style.opacity = "1";
  });
  setTimeout(() => {
    node.style.opacity = "0";
    setTimeout(() => node.remove(), TOAST_FADE_MS);
  }, TOAST_TTL_MS);
}

export const toast = {
  info: (message: string) => show("info", message),
  warn: (message: string) => show("warn", message),
  error: (message: string) => show("error", message),
};
