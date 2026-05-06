// SPDX-License-Identifier: MIT
// Build-output verifier. Spec item 48/55.
//
// CI runs `cd web && npm run build && node scripts/verify-build-output.mjs`.
// Confirms that `internal/server/static/dist/` contains:
//   - index.html
//   - an "assets" directory with at least one .js file
// and that index.html references the bundle via a <script type="module">.
//
// Exits 0 on success; nonzero with a clear stderr message on failure.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const DIST = join(HERE, "..", "..", "internal", "server", "static", "dist");

function fail(msg) {
  console.error(`verify-build-output: ${msg}`);
  process.exit(1);
}

if (!existsSync(DIST)) {
  fail(`expected dist directory at ${DIST}; did vite build run?`);
}
if (!statSync(DIST).isDirectory()) {
  fail(`${DIST} exists but is not a directory`);
}

const indexHtml = join(DIST, "index.html");
if (!existsSync(indexHtml)) {
  fail(`missing index.html in ${DIST}`);
}
const html = readFileSync(indexHtml, "utf8");
if (!/<script[^>]+type=["']module["'][^>]*src=/i.test(html)) {
  fail(`index.html does not contain a <script type="module"> bundle ref`);
}

const assets = join(DIST, "assets");
if (!existsSync(assets) || !statSync(assets).isDirectory()) {
  fail(`missing assets directory in ${DIST}`);
}
const jsBundles = readdirSync(assets).filter((f) => f.endsWith(".js"));
if (jsBundles.length === 0) {
  fail(`no .js bundles found in ${assets}`);
}

console.log(`verify-build-output: ok (${jsBundles.length} JS bundle(s) in ${assets})`);
