// runMocha.ts — programmatic mocha entrypoint so `npm test` runs the
// suite without depending on a globally-installed mocha CLI.

import * as path from "path";
import Mocha = require("mocha");
import * as fs from "fs";

const mocha = new Mocha({ ui: "bdd", timeout: 15_000, color: true });

const testsDir = path.resolve(__dirname);
for (const f of fs.readdirSync(testsDir)) {
  if (f.endsWith(".test.js")) {
    mocha.addFile(path.join(testsDir, f));
  }
}

mocha.run((failures: number) => {
  process.exitCode = failures > 0 ? 1 : 0;
});
