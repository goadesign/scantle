// build.mjs compiles the Scantle Checkpoint Studio React + OpenUI + shadcn/ui
// application into the single-file internal/controlplane/checkpoint.html bundle
// and exports the OpenUI catalog prompt to internal/goldenpath/decision_catalog.md.

import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import * as esbuild from "esbuild";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const checkOnly = process.argv.includes("--check");

const checkpointHtmlPath = path.resolve(
  __dirname,
  "../../internal/controlplane/checkpoint.html"
);
const decisionCatalogPath = path.resolve(
  __dirname,
  "../../internal/goldenpath/decision_catalog.md"
);

const stubCiInfoPlugin = {
  name: "stub-ci-info",
  setup(build) {
    build.onResolve({ filter: /^ci-info$/ }, () => ({
      path: "ci-info",
      namespace: "stub-ci-info",
    }));
    build.onLoad({ filter: /.*/, namespace: "stub-ci-info" }, () => ({
      contents: "export const id = null; export const isCI = false;",
      loader: "js",
    }));
  },
};

async function buildArtifacts() {
  const stylesCss = fs.readFileSync(path.join(__dirname, "src/styles.css"), "utf8").trim();

  const appBundle = await esbuild.build({
    entryPoints: [path.join(__dirname, "src/app.tsx")],
    bundle: true,
    write: false,
    platform: "browser",
    format: "iife",
    target: ["es2020"],
    minify: true,
    legalComments: "none",
    define: {
      "process.env.NODE_ENV": '"production"',
    },
    plugins: [stubCiInfoPlugin],
  });

  const rawJs = appBundle.outputFiles[0].text.trim();
  // Escape any "</script" sequence inside string literals so the inline <script>
  // tag in checkpoint.html is never prematurely closed by the HTML parser.
  const safeJs = rawJs.replace(/<\/script/gi, "<\\/script");

  const html = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline';" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Scantle Checkpoint Studio</title>
  <style>
${stylesCss}
  </style>
</head>
<body>
  <main id="scantle-checkpoint-root" data-profile="mcp-app" data-state="waiting">
    <header>
      <h1>Scantle Checkpoint Studio</h1>
      <p>Displays and refines CP1 Change Cards with OpenUI Decision Workspaces, CP2 C4 Architecture Previews and Structural Diffs, and CP3 Fast Draft Override results from structured MCP tool outputs.</p>
      <p><small>Submitting CP1 edits dispatches <code>confirm_intent</code> through the MCP Apps host; server authentication and <code>request_revision</code> checks remain authoritative.</small></p>
    </header>
    <p id="scantle-checkpoint-status" role="status" aria-live="polite">Awaiting MCP Apps host initialization and checkpoint tool result...</p>
    <section id="scantle-checkpoint-view" aria-label="Checkpoint Payload"></section>
  </main>
  <script>
  /* MCP Apps bridge origin guard: event.source !== window.parent */
  ${safeJs}
  </script>
</body>
</html>
`;

  const catalogBundle = await esbuild.build({
    entryPoints: [path.join(__dirname, "src/openui/library.tsx")],
    bundle: true,
    write: false,
    platform: "node",
    format: "cjs",
    target: ["node20"],
    define: {
      "process.env.NODE_ENV": '"production"',
    },
    plugins: [stubCiInfoPlugin],
  });

  const cjsCode = catalogBundle.outputFiles[0].text;
  const req = createRequire(import.meta.url);
  const mod = { exports: {} };
  const wrapper = vm.runInNewContext(
    "(function(exports, require, module, __filename, __dirname) { " + cjsCode + "\n})",
    {
      console,
      process: { env: { NODE_ENV: "production", OPENUI_TELEMETRY_DISABLED: "1" } },
      Buffer,
      TextEncoder,
      TextDecoder,
      URL,
      Set,
      Map,
      WeakMap,
      WeakSet,
      Symbol,
      Promise,
      Object,
      Array,
      String,
      Number,
      Boolean,
      RegExp,
      Error,
      TypeError,
      Math,
      Date,
      JSON,
      setTimeout,
      clearTimeout,
      setInterval,
      clearInterval,
    }
  );
  wrapper(mod.exports, req, mod, "library.cjs", __dirname);
  const catalogMarkdown = mod.exports.generateDecisionCatalogMarkdown().trim() + "\n";

  if (checkOnly) {
    const existingHtml = fs.existsSync(checkpointHtmlPath)
      ? fs.readFileSync(checkpointHtmlPath, "utf8")
      : "";
    const existingCatalog = fs.existsSync(decisionCatalogPath)
      ? fs.readFileSync(decisionCatalogPath, "utf8")
      : "";
    if (existingHtml !== html) {
      console.error("ERROR: internal/controlplane/checkpoint.html is out of date with ui/checkpoint/src.");
      process.exit(1);
    }
    if (existingCatalog !== catalogMarkdown) {
      console.error("ERROR: internal/goldenpath/decision_catalog.md is out of date with ui/checkpoint/src/openui/library.tsx.");
      process.exit(1);
    }
    console.log("OK: checkpoint.html and decision_catalog.md are up to date.");
    return;
  }

  fs.mkdirSync(path.dirname(checkpointHtmlPath), { recursive: true });
  fs.mkdirSync(path.dirname(decisionCatalogPath), { recursive: true });
  fs.writeFileSync(checkpointHtmlPath, html, "utf8");
  fs.writeFileSync(decisionCatalogPath, catalogMarkdown, "utf8");
  console.log(
    `Built checkpoint.html (${Buffer.byteLength(html)} bytes) and decision_catalog.md (${Buffer.byteLength(catalogMarkdown)} bytes).`
  );
}

buildArtifacts().catch((err) => {
  console.error(err);
  process.exit(1);
});
