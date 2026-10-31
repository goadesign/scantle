// build.mjs derives ui/checkpoint/src/types.ts from the Goa-generated tool
// result schemas in gen/scantle/toolsets/scantle, compiles the Scantle
// Checkpoint Studio React + OpenUI + shadcn/ui application into the single-file
// internal/controlplane/checkpoint.html bundle, and exports the OpenUI catalog
// prompt to internal/goldenpath/decision_catalog.md.

import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import * as esbuild from "esbuild";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const checkOnly = process.argv.includes("--check");

const typesTsPath = path.join(__dirname, "src/types.ts");
const checkpointHtmlPath = path.resolve(
  __dirname,
  "../../internal/controlplane/checkpoint.html"
);
const decisionCatalogPath = path.resolve(
  __dirname,
  "../../internal/goldenpath/decision_catalog.md"
);
const registryLookupPath = path.resolve(
  __dirname,
  "../../gen/scantle/toolsets/scantle/registry_schema_lookup.go"
);
const registrySchemasPath = path.resolve(
  __dirname,
  "../../gen/scantle/toolsets/scantle/registry_schemas.go"
);
const mcpAdapterPath = path.resolve(
  __dirname,
  "../../gen/mcp_scantle/adapter_server.go"
);
const openApiPath = path.resolve(
  __dirname,
  "../../gen/http/openapi3.json"
);

// compactSchema strips prose descriptions and root $schema URLs while keeping
// every structural JSON Schema constraint (type, properties, required,
// additionalProperties, items, enum, $ref, $defs).
function compactSchema(node) {
  if (!node || typeof node !== "object") {
    return node;
  }
  const out = {};
  if (typeof node.type === "string") {
    out.type = node.type;
  }
  if (typeof node.$ref === "string") {
    out.$ref = node.$ref;
  }
  if (Array.isArray(node.enum)) {
    out.enum = node.enum;
  }
  if (Array.isArray(node.required)) {
    out.required = node.required;
  }
  if (node.additionalProperties !== undefined) {
    out.additionalProperties =
      typeof node.additionalProperties === "object" && node.additionalProperties !== null
        ? compactSchema(node.additionalProperties)
        : node.additionalProperties;
  }
  if (node.items && typeof node.items === "object") {
    out.items = compactSchema(node.items);
  }
  if (node.properties && typeof node.properties === "object") {
    out.properties = {};
    for (const [k, v] of Object.entries(node.properties)) {
      out.properties[k] = compactSchema(v);
    }
  }
  if (node.$defs && typeof node.$defs === "object") {
    out.$defs = {};
    for (const [k, v] of Object.entries(node.$defs)) {
      out.$defs[k] = compactSchema(v);
    }
  }
  return out;
}

// tsTypeForProperty converts a JSON Schema property node into its TypeScript
// type expression.
function tsTypeForProperty(prop) {
  if (typeof prop.$ref === "string") {
    return prop.$ref.replace(/^#\/\$defs\//, "");
  }
  if (prop.type === "string") {
    if (Array.isArray(prop.enum) && prop.enum.length > 0) {
      return prop.enum.map((v) => JSON.stringify(v)).join(" | ");
    }
    return "string";
  }
  if (prop.type === "boolean") {
    return "boolean";
  }
  if (prop.type === "integer" || prop.type === "number") {
    return "number";
  }
  if (prop.type === "array") {
    return prop.items ? `${tsTypeForProperty(prop.items)}[]` : "unknown[]";
  }
  if (prop.type === "object") {
    if (prop.additionalProperties && typeof prop.additionalProperties === "object") {
      return `Record<string, ${tsTypeForProperty(prop.additionalProperties)}>`;
    }
    return "Record<string, unknown>";
  }
  return "unknown";
}

// emitInterface renders a single TypeScript interface from a JSON Schema object.
function emitInterface(name, schema) {
  const req = new Set(Array.isArray(schema.required) ? schema.required : []);
  const props = schema.properties || {};
  const lines = [`export interface ${name} {`];
  for (const [propName, propSchema] of Object.entries(props)) {
    const optional = req.has(propName) ? "" : "?";
    lines.push(`  ${propName}${optional}: ${tsTypeForProperty(propSchema)};`);
  }
  lines.push("}");
  return lines.join("\n");
}

// generateTypesModule reads the Goa-generated tool schemas via
// gen/scantle/toolsets/scantle/registry_schema_lookup.go, verifies parity with
// gen/mcp_scantle/adapter_server.go and gen/http/openapi3.json, and generates
// ui/checkpoint/src/types.ts.
function generateTypesModule() {
  const lookupSrc = fs.readFileSync(registryLookupPath, "utf8");
  const regSrc = fs.readFileSync(registrySchemasPath, "utf8");
  const adapterSrc = fs.readFileSync(mcpAdapterPath, "utf8");
  const openApi = JSON.parse(fs.readFileSync(openApiPath, "utf8"));

  const toolToFunc = {};
  for (const m of lookupSrc.matchAll(
    /case\s+"([^"]+)":\s*return\s+declarations\.(schema\d+)\(\)/g
  )) {
    toolToFunc[m[1]] = m[2];
  }

  const rawByTool = {};
  for (const [tool, fn] of Object.entries(toolToFunc)) {
    const re = new RegExp(
      "func \\(declarations registryDeclarations\\) " +
        fn +
        "\\(\\) \\*genregistry\\.ToolSchema \\{[\\s\\S]*?ResultSchema:\\s*\\[\\]byte\\(\"((?:\\\\.|[^\"\\\\])*)\"\\)"
    );
    const m = regSrc.match(re);
    if (!m) {
      throw new Error(`Missing ResultSchema for ${tool} (${fn}) in registry_schemas.go`);
    }
    const regSchema = JSON.parse(JSON.parse(`"${m[1]}"`));

    const mcpName = tool.replace(/^scantle\./, "");
    const outRe = new RegExp(
      'Name:\\s*"' +
        mcpName +
        '"[\\s\\S]*?OutputSchema:\\s*json\\.RawMessage\\("((?:\\\\.|[^"\\\\])*)"\\)'
    );
    const outMatch = adapterSrc.match(outRe);
    if (!outMatch) {
      throw new Error(`Missing OutputSchema for ${mcpName} in gen/mcp_scantle/adapter_server.go`);
    }
    const mcpSchema = JSON.parse(JSON.parse(`"${outMatch[1]}"`));
    const oaType = mcpSchema.title;
    const oaSchema = openApi.components?.schemas?.[oaType];
    if (
      !oaSchema ||
      JSON.stringify(regSchema.properties) !== JSON.stringify(mcpSchema.properties) ||
      JSON.stringify(regSchema.required) !== JSON.stringify(mcpSchema.required) ||
      JSON.stringify(regSchema.required) !== JSON.stringify(oaSchema.required)
    ) {
      throw new Error(`Schema parity mismatch for ${tool} (${oaType})`);
    }
    rawByTool[tool] = regSchema;
  }

  if (
    JSON.stringify(rawByTool["scantle.propose_change"]) !==
      JSON.stringify(rawByTool["scantle.confirm_intent"]) ||
    JSON.stringify(rawByTool["scantle.propose_change"]) !==
      JSON.stringify(rawByTool["scantle.get_change"])
  ) {
    throw new Error("CP1 tool ResultSchemas (propose_change, confirm_intent, get_change) diverged");
  }

  const rootSchemas = {
    ChangeCardResult: compactSchema(rawByTool["scantle.propose_change"]),
    ArchitecturePreviewResult: compactSchema(rawByTool["scantle.preview_architecture"]),
    DiffResult: compactSchema(rawByTool["scantle.diff"]),
    DraftOverrideResult: compactSchema(rawByTool["scantle.draft_override"]),
  };

  const emittedDefs = new Set();
  const interfaceBlocks = [];
  for (const [rootName, rootSchema] of Object.entries(rootSchemas)) {
    for (const [defName, defSchema] of Object.entries(rootSchema.$defs || {})) {
      if (!emittedDefs.has(defName)) {
        emittedDefs.add(defName);
        interfaceBlocks.push(emitInterface(defName, defSchema));
      }
    }
    interfaceBlocks.push(emitInterface(rootName, rootSchema));
  }

  return `// Code generated by ui/checkpoint/build.mjs from gen/scantle/toolsets/scantle; DO NOT EDIT.
//
// Package types declares the generated MCP tool result interfaces and
// JSON-Schema-driven runtime validators for Scantle Checkpoint Studio.

${interfaceBlocks.join("\n\n")}

interface JSONSchemaNode {
  type?: string;
  $ref?: string;
  enum?: string[];
  required?: string[];
  additionalProperties?: boolean | JSONSchemaNode;
  items?: JSONSchemaNode;
  properties?: Record<string, JSONSchemaNode>;
  $defs?: Record<string, JSONSchemaNode>;
}

const GENERATED_RESULT_SCHEMAS: Record<string, JSONSchemaNode> = ${JSON.stringify(
    rootSchemas,
    null,
    2
  )};

// isPlainObject reports whether v is a non-null, non-array JavaScript object.
export function isPlainObject(v: unknown): v is Record<string, unknown> {
  return Boolean(v) && typeof v === "object" && !Array.isArray(v);
}

// matchesJSONSchema validates an unknown runtime value against a generated
// Draft 2020-12 JSON Schema node and its root $defs dictionary.
function matchesJSONSchema(
  value: unknown,
  schema: JSONSchemaNode,
  defs: Record<string, JSONSchemaNode>
): boolean {
  if (typeof schema.$ref === "string") {
    const defName = schema.$ref.replace(/^#\\/\\$defs\\//, "");
    const target = defs[defName];
    return Boolean(target) && matchesJSONSchema(value, target, defs);
  }
  if (schema.type === "string") {
    if (typeof value !== "string") {
      return false;
    }
    return !Array.isArray(schema.enum) || schema.enum.includes(value);
  }
  if (schema.type === "boolean") {
    return typeof value === "boolean";
  }
  if (schema.type === "integer" || schema.type === "number") {
    return (
      typeof value === "number" &&
      Number.isFinite(value) &&
      (schema.type !== "integer" || Number.isInteger(value))
    );
  }
  if (schema.type === "array") {
    if (!Array.isArray(value)) {
      return false;
    }
    return !schema.items || value.every((item) => matchesJSONSchema(item, schema.items!, defs));
  }
  if (schema.type === "object") {
    if (!isPlainObject(value)) {
      return false;
    }
    const props = schema.properties || {};
    const req = schema.required || [];
    for (let i = 0; i < req.length; i++) {
      if (value[req[i]] === undefined) {
        return false;
      }
    }
    for (const key of Object.keys(value)) {
      const propVal = value[key];
      if (propVal === undefined) {
        continue;
      }
      if (Object.prototype.hasOwnProperty.call(props, key)) {
        if (!matchesJSONSchema(propVal, props[key], defs)) {
          return false;
        }
      } else if (schema.additionalProperties === false) {
        return false;
      } else if (
        schema.additionalProperties &&
        typeof schema.additionalProperties === "object"
      ) {
        if (!matchesJSONSchema(propVal, schema.additionalProperties, defs)) {
          return false;
        }
      }
    }
    return true;
  }
  return false;
}

function validateGeneratedRoot(name: string, value: unknown): boolean {
  const schema = GENERATED_RESULT_SCHEMAS[name];
  return Boolean(schema) && matchesJSONSchema(value, schema, schema.$defs || {});
}

// isChangeCardResult validates that a payload conforms to the generated ChangeCardResult schema.
export function isChangeCardResult(p: unknown): p is ChangeCardResult {
  return validateGeneratedRoot("ChangeCardResult", p);
}

// isArchitecturePreviewResult validates that a payload conforms to the generated ArchitecturePreviewResult schema.
export function isArchitecturePreviewResult(p: unknown): p is ArchitecturePreviewResult {
  return validateGeneratedRoot("ArchitecturePreviewResult", p);
}

// isDiffResult validates that a payload conforms to the generated DiffResult schema.
export function isDiffResult(p: unknown): p is DiffResult {
  return validateGeneratedRoot("DiffResult", p);
}

// isDraftOverrideResult validates that a payload conforms to the generated DraftOverrideResult schema.
export function isDraftOverrideResult(p: unknown): p is DraftOverrideResult {
  return validateGeneratedRoot("DraftOverrideResult", p);
}
`;
}

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
  const generatedTypesTs = generateTypesModule();
  if (!checkOnly) {
    fs.writeFileSync(typesTsPath, generatedTypesTs, "utf8");
  }

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
      <p>Review and confirm domain decisions, concrete examples, architecture previews, and prompt updates for your change.</p>
      <details class="scantle-details">
        <summary>Details</summary>
        <p><small>Submitting Checkpoint 1 edits dispatches <code>confirm_intent</code> through the MCP Apps host; server authentication and <code>request_revision</code> checks remain authoritative.</small></p>
      </details>
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
    const existingTypes = fs.existsSync(typesTsPath)
      ? fs.readFileSync(typesTsPath, "utf8")
      : "";
    const existingHtml = fs.existsSync(checkpointHtmlPath)
      ? fs.readFileSync(checkpointHtmlPath, "utf8")
      : "";
    const existingCatalog = fs.existsSync(decisionCatalogPath)
      ? fs.readFileSync(decisionCatalogPath, "utf8")
      : "";
    if (existingTypes !== generatedTypesTs) {
      console.error("ERROR: ui/checkpoint/src/types.ts is out of date with gen/scantle/toolsets/scantle.");
      process.exit(1);
    }
    if (existingHtml !== html) {
      console.error("ERROR: internal/controlplane/checkpoint.html is out of date with ui/checkpoint/src.");
      process.exit(1);
    }
    if (existingCatalog !== catalogMarkdown) {
      console.error("ERROR: internal/goldenpath/decision_catalog.md is out of date with ui/checkpoint/src/openui/library.tsx.");
      process.exit(1);
    }
    console.log("OK: types.ts, checkpoint.html, and decision_catalog.md are up to date.");
    return;
  }

  fs.mkdirSync(path.dirname(checkpointHtmlPath), { recursive: true });
  fs.mkdirSync(path.dirname(decisionCatalogPath), { recursive: true });
  fs.writeFileSync(checkpointHtmlPath, html, "utf8");
  fs.writeFileSync(decisionCatalogPath, catalogMarkdown, "utf8");
  console.log(
    `Built types.ts (${Buffer.byteLength(generatedTypesTs)} bytes), checkpoint.html (${Buffer.byteLength(html)} bytes), and decision_catalog.md (${Buffer.byteLength(catalogMarkdown)} bytes).`
  );
}

buildArtifacts().catch((err) => {
  console.error(err);
  process.exit(1);
});
