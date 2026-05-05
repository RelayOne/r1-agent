// cmd/r1-server/e2e/graph-fps.mjs
//
// Spec 2 §6 + checklist T11. Headless-Chromium FPS probe driven by
// Playwright. Invoked by graph_e2e_test.go (build tag e2e) which
// passes the server URL + duration via env vars and parses the
// final JSON line of stdout.
//
// Inputs (env):
//   R1_SERVER_BASE_URL          e.g. http://127.0.0.1:38291
//   R1_SERVER_E2E_DURATION_SEC  duration of FPS sample window
//   R1_SERVER_E2E_FIXTURE_NODES expected node count (test-only seed)
//
// Output:
//   Multiple diagnostic lines on stdout (printed for human debug).
//   The LAST stdout line is a JSON object the Go test parses:
//     { meanFps: number, p99Frame: number (ms), errors: string[], frames: number }

import { chromium } from 'playwright';

const baseURL  = process.env.R1_SERVER_BASE_URL || 'http://127.0.0.1:8080';
const durSec   = Number(process.env.R1_SERVER_E2E_DURATION_SEC) || 5;
const wantN    = Number(process.env.R1_SERVER_E2E_FIXTURE_NODES) || 3000;
const sessId   = process.env.R1_SERVER_E2E_SESSION_ID || 'e2e-fixture';

const errors = [];

const browser = await chromium.launch({
  headless: true,
  args: ['--no-sandbox', '--enable-features=SharedArrayBuffer'],
});
const ctx = await browser.newContext();
const page = await ctx.newPage();
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', e => errors.push(String(e)));

const url = `${baseURL}/session/${sessId}/graph`;
console.log(`navigating: ${url}`);
await page.goto(url, { waitUntil: 'load' });
await page.waitForFunction(() => !!window.__GRAPH_RENDERER__, { timeout: 15000 });

// Sample frame timings inside the page over the requested window.
const result = await page.evaluate(async (d) => {
  return await new Promise(res => {
    const frames = [];
    const t0 = performance.now();
    let prev = t0;
    function loop(t) {
      frames.push(t - prev);
      prev = t;
      if (t - t0 < d * 1000) requestAnimationFrame(loop);
      else {
        // Drop the first 5 frames (warmup).
        const samples = frames.slice(5);
        samples.sort((a, b) => a - b);
        const sum = samples.reduce((s, x) => s + x, 0);
        const mean = sum / samples.length;
        const p99 = samples[Math.floor(samples.length * 0.99)];
        res({
          meanFps: 1000 / mean,
          p99Frame: p99,
          frames: samples.length,
        });
      }
    }
    requestAnimationFrame(loop);
  });
}, durSec);

console.log(`actual nodes loaded:`, await page.evaluate(() => window.__GRAPH_RENDERER__?.nodes?.size || 0));
console.log(`expected nodes:`, wantN);

await browser.close();

const out = { ...result, errors };
console.log(JSON.stringify(out));
