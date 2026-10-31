// Package openui defines the Scantle Decision Studio component library using
// @openuidev/react-lang and Zod v4 schemas, validates completed CP1 OpenUI
// compositions against the active Change Card, and exports the catalog prompt.

import React from "react";
import { z } from "zod/v4";
import {
  createLibrary,
  createParser,
  defineComponent,
  Renderer,
} from "@openuidev/react-lang";
import {
  Alert,
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  ResponsiveTable,
} from "../components/shadcn";
import { useCP1Draft } from "../context/cp1-draft";
import { ChangeCardResult } from "../types";

interface ElementNodeLike {
  type?: string;
  typeName?: string;
  props?: Record<string, unknown>;
  partial?: boolean;
  hasDynamicProps?: boolean;
}

// unwrapProps extracts the evaluated props object from a nested OpenUI ElementNode.
function unwrapProps<T>(item: unknown): T | null {
  if (!item || typeof item !== "object") {
    return null;
  }
  const candidate = item as ElementNodeLike;
  if (candidate.type === "element" && candidate.props && typeof candidate.props === "object") {
    return candidate.props as unknown as T;
  }
  return item as T;
}

// PolicySimulatorRow represents a single scenario row inside a PolicySimulatorTable.
export const PolicySimulatorRow = defineComponent({
  name: "PolicySimulatorRow",
  props: z.object({
    scenario: z.string().min(1),
    inputSummary: z.string().min(1),
    outcomes: z.array(z.string().trim().min(1)).min(1),
    exampleIndex: z.number().int().min(1).optional(),
  }),
  description:
    "A scenario row in PolicySimulatorTable. outcomes must provide one non-empty outcome string per option of the parent ambiguity in exact option order; exampleIndex is an optional 1-based index into examples.",
  component: () => null,
});

// PolicySimulatorTable renders an interactive option toggle and responsive comparison matrix.
export const PolicySimulatorTable = defineComponent({
  name: "PolicySimulatorTable",
  props: z.object({
    title: z.string().min(1),
    ambiguityIndex: z.number().int().min(1),
    rows: z.array(PolicySimulatorRow.ref).min(1),
  }),
  description:
    "Interactive policy comparison table bound to a 1-based ambiguityIndex (1..N in ambiguities). Toggling an option updates the canonical CP1 answer draft and highlights the active outcome column.",
  component: ({ props }) => {
    const draft = useCP1Draft();
    const idx = typeof props.ambiguityIndex === "number" ? props.ambiguityIndex - 1 : -1;
    const amb = draft && Array.isArray(draft.card.ambiguities) ? draft.card.ambiguities[idx] : undefined;
    if (!amb || !draft) {
      return null;
    }

    const selectedOption = draft.draftAnswers[amb.id] || "";
    const selectedIdx = amb.options.indexOf(selectedOption);
    const controlsDisabled = Boolean(draft.rpcInFlight);
    const rawRows = Array.isArray(props.rows) ? props.rows : [];

    return (
      <Card
        className="scantle-openui-card"
        data-openui-component="PolicySimulatorTable"
        data-openui-ambiguity-index={String(props.ambiguityIndex)}
      >
        <CardHeader>
          <div className="scantle-field-row">
            <CardTitle>{props.title}</CardTitle>
            <Badge>{amb.id}</Badge>
          </div>
          <CardDescription>{amb.question}</CardDescription>
        </CardHeader>
        <CardContent>
          <div
            className="scantle-field-row"
            role="group"
            aria-label={"Simulator options for " + amb.question}
          >
            {amb.options.map((opt) => (
              <Button
                key={opt}
                variant="pill"
                disabled={controlsDisabled}
                data-openui-ambiguity-id={amb.id}
                data-openui-option={opt}
                aria-pressed={selectedOption === opt ? "true" : "false"}
                onClick={() => draft.setAnswer(amb.id, opt)}
              >
                {opt}
              </Button>
            ))}
          </div>
          <ResponsiveTable ariaLabel={props.title}>
            <thead>
              <tr>
                <th>Scenario</th>
                <th>Input</th>
                {amb.options.map((opt, optIdx) => (
                  <th
                    key={opt}
                    className={optIdx === selectedIdx ? "shadcn-cell-active" : ""}
                  >
                    {opt}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rawRows.map((rawRow, rIdx) => {
                const row = unwrapProps<{
                  scenario?: string;
                  inputSummary?: string;
                  outcomes?: string[];
                  exampleIndex?: number;
                }>(rawRow);
                if (!row) {
                  return null;
                }
                const outcomes = Array.isArray(row.outcomes) ? row.outcomes : [];
                const exItem =
                  typeof row.exampleIndex === "number"
                    ? draft.draftExamples[row.exampleIndex - 1]
                    : undefined;
                return (
                  <tr key={String(rIdx)}>
                    <td data-label="Scenario">
                      <strong>{row.scenario || ""}</strong>
                      {exItem ? (
                        <div>
                          <Badge>{exItem.id}</Badge>
                        </div>
                      ) : null}
                    </td>
                    <td data-label="Input">{row.inputSummary || ""}</td>
                    {amb.options.map((opt, optIdx) => (
                      <td
                        key={opt}
                        data-label={opt}
                        className={optIdx === selectedIdx ? "shadcn-cell-active" : ""}
                      >
                        {outcomes[optIdx] || "—"}
                      </td>
                    ))}
                  </tr>
                );
              })}
            </tbody>
          </ResponsiveTable>
        </CardContent>
      </Card>
    );
  },
});

// AmbiguityChoiceCard renders a focused decision card for a single 1-based ambiguity.
export const AmbiguityChoiceCard = defineComponent({
  name: "AmbiguityChoiceCard",
  props: z.object({
    ambiguityIndex: z.number().int().min(1),
    rationale: z.string().optional(),
  }),
  description:
    "Focused decision card bound to a 1-based ambiguityIndex (1..N in ambiguities) with optional domain trade-off rationale.",
  component: ({ props }) => {
    const draft = useCP1Draft();
    const idx = typeof props.ambiguityIndex === "number" ? props.ambiguityIndex - 1 : -1;
    const amb = draft && Array.isArray(draft.card.ambiguities) ? draft.card.ambiguities[idx] : undefined;
    if (!amb || !draft) {
      return null;
    }

    const selectedOption = draft.draftAnswers[amb.id] || "";
    const controlsDisabled = Boolean(draft.rpcInFlight);

    return (
      <Card
        className="scantle-openui-card"
        data-openui-component="AmbiguityChoiceCard"
        data-openui-ambiguity-index={String(props.ambiguityIndex)}
      >
        <CardHeader>
          <div className="scantle-field-row">
            <CardTitle>{amb.question}</CardTitle>
            <Badge>{amb.id}</Badge>
          </div>
          {props.rationale ? <CardDescription>{props.rationale}</CardDescription> : null}
        </CardHeader>
        <CardContent>
          <div
            className="scantle-field-row"
            role="group"
            aria-label={"Decision card options for " + amb.question}
          >
            {amb.options.map((opt) => (
              <Button
                key={opt}
                variant="pill"
                disabled={controlsDisabled}
                data-openui-ambiguity-id={amb.id}
                data-openui-option={opt}
                aria-pressed={selectedOption === opt ? "true" : "false"}
                onClick={() => draft?.setAnswer(amb.id, opt)}
              >
                {opt}
              </Button>
            ))}
          </div>
        </CardContent>
      </Card>
    );
  },
});

// WorkflowStep renders a single step in a WorkflowTimeline.
export const WorkflowStep = defineComponent({
  name: "WorkflowStep",
  props: z.object({
    step: z.string().min(1),
    actor: z.string().min(1),
    detail: z.string().min(1),
    gate: z.string().optional(),
  }),
  description: "A single workflow step showing the step name, actor, detail, and optional policy gate.",
  component: ({ props }) => (
    <li className="scantle-timeline-step" data-openui-component="WorkflowStep">
      <div className="scantle-field-row">
        <strong>{props.step}</strong>
        <Badge>{props.actor}</Badge>
        {props.gate ? <Badge variant="accent">Gate: {props.gate}</Badge> : null}
      </div>
      <p>{props.detail}</p>
    </li>
  ),
});

// WorkflowTimeline renders an ordered sequence of WorkflowStep items.
export const WorkflowTimeline = defineComponent({
  name: "WorkflowTimeline",
  props: z.object({
    title: z.string().min(1),
    steps: z.array(WorkflowStep.ref).min(1),
  }),
  description: "Ordered workflow sequence illustrating how actors and policy gates interact.",
  component: ({ props, renderNode }) => (
    <Card className="scantle-openui-card" data-openui-component="WorkflowTimeline">
      <CardHeader>
        <CardTitle>{props.title}</CardTitle>
      </CardHeader>
      <CardContent>
        <ol className="scantle-timeline">{renderNode(props.steps)}</ol>
      </CardContent>
    </Card>
  ),
});

// ImpactCallout renders a domain invariant or risk highlight banner.
export const ImpactCallout = defineComponent({
  name: "ImpactCallout",
  props: z.object({
    variant: z.enum(["info", "warning"]),
    title: z.string().min(1),
    summary: z.string().min(1),
  }),
  description: "Callout banner highlighting a preserved contract invariant or architectural trade-off.",
  component: ({ props }) => (
    <Alert
      variant={props.variant === "warning" ? "warning" : "default"}
      data-openui-component="ImpactCallout"
    >
      <strong>{props.title}: </strong>
      {props.summary}
    </Alert>
  ),
});

// DecisionWorkspace is the root OpenUI container for CP1 decision instruments.
export const DecisionWorkspace = defineComponent({
  name: "DecisionWorkspace",
  props: z.object({
    title: z.string().min(1),
    children: z
      .array(
        z.union([
          PolicySimulatorTable.ref,
          AmbiguityChoiceCard.ref,
          WorkflowTimeline.ref,
          ImpactCallout.ref,
        ])
      )
      .min(1),
  }),
  description:
    "Root container for a Scantle CP1 Decision Workspace combining policy simulators, decision cards, workflow timelines, and impact callouts.",
  component: ({ props, renderNode }) => {
    const draft = useCP1Draft();
    const composedRev =
      draft?.card.ui_composition?.composed_for_revision ||
      draft?.card.request_revision ||
      "preview";
    return (
      <div className="scantle-decision-workspace" data-openui-component="DecisionWorkspace">
        <div className="scantle-field-row">
          <h3>{props.title}</h3>
          <Badge variant="accent">OpenUI Decision Studio</Badge>
          <Badge variant="accent">Model-Proposed Illustration</Badge>
        </div>
        <p className="shadcn-card-desc">
          composed_for_revision: <code>{composedRev}</code>
        </p>
        {draft?.localExamplesEdited ? (
          <Alert id="scantle-openui-local-edit-notice" variant="warning">
            <strong>Local Examples Modified: </strong>
            Confirmation examples have been edited locally since{" "}
            <code>{draft.card.request_revision}</code>. Instruments in this workspace reflect the
            model-proposed illustration for the saved revision until re-analyzed.
          </Alert>
        ) : null}
        <div className="scantle-decision-grid">{renderNode(props.children)}</div>
      </div>
    );
  },
});

// scantleDecisionLibrary is the canonical OpenUI component library for Scantle CP1.
export const scantleDecisionLibrary = createLibrary({
  root: "DecisionWorkspace",
  components: [
    DecisionWorkspace,
    PolicySimulatorTable,
    PolicySimulatorRow,
    AmbiguityChoiceCard,
    WorkflowTimeline,
    WorkflowStep,
    ImpactCallout,
  ],
  componentGroups: [
    {
      name: "CP1 Decision Instruments",
      components: [
        "DecisionWorkspace",
        "PolicySimulatorTable",
        "PolicySimulatorRow",
        "AmbiguityChoiceCard",
        "WorkflowTimeline",
        "WorkflowStep",
        "ImpactCallout",
      ],
      notes: [
        "Always start with root = DecisionWorkspace(title, children).",
        "ambiguityIndex is a 1-based integer index (1..N) into the returned ambiguities array.",
        "exampleIndex is an optional 1-based integer index (1..M) into the returned examples array.",
        "In PolicySimulatorRow, outcomes must be a string array with the exact same length and order as the referenced ambiguity's options array.",
        "Never emit Query(), Mutation(), $variables, or @Action steps.",
      ],
    },
  ],
});

const scantleDecisionParser = createParser(
  scantleDecisionLibrary.toJSONSchema(),
  scantleDecisionLibrary.root
);

const schemaToComponentName = new Map<unknown, string>(
  Object.values(scantleDecisionLibrary.components).map((comp) => [comp.props, comp.name])
);

// allowedSlotComponentNames inspects a Zod v4 property schema and returns the
// allowed component names when the property is a component reference slot.
function allowedSlotComponentNames(propSchema: unknown): string[] | null {
  const def = (propSchema as { _zod?: { def?: Record<string, unknown> } })?._zod?.def;
  if (!def) {
    return null;
  }
  if (def.type === "array") {
    return allowedSlotComponentNames(def.element ?? def.innerType);
  }
  if (def.type === "union" && Array.isArray(def.options)) {
    const names = def.options
      .map((opt) => schemaToComponentName.get(opt))
      .filter((n): n is string => typeof n === "string");
    return names.length > 0 ? names : null;
  }
  const single = schemaToComponentName.get(propSchema);
  return single ? [single] : null;
}

export interface OpenUIValidationResult {
  ok: boolean;
  error?: string;
}

// projectAndValidateNode recursively verifies that an AST ElementNode was
// produced by an OpenUI component call (not a forged object literal), matches
// its parent slot's allowed component types, satisfies its canonical Zod v4
// props schema, and obeys the active Change Card's ordinal and option bounds.
function projectAndValidateNode(
  node: unknown,
  allowedTypes: string[],
  card: ChangeCardResult,
  parentAmbiguity?: { id: string; options: string[] }
): { ok: true; projectedProps: Record<string, unknown> } | { ok: false; error: string } {
  if (!node || typeof node !== "object") {
    return { ok: false, error: "OpenUI element node must be an object." };
  }
  const el = node as ElementNodeLike;
  if (
    el.type !== "element" ||
    el.partial !== false ||
    typeof el.hasDynamicProps !== "boolean" ||
    typeof el.typeName !== "string" ||
    !el.props ||
    typeof el.props !== "object" ||
    Array.isArray(el.props)
  ) {
    return {
      ok: false,
      error: "OpenUI slot contains a non-component or forged element literal.",
    };
  }
  if (!allowedTypes.includes(el.typeName)) {
    return {
      ok: false,
      error:
        "Unsupported component type " +
        el.typeName +
        " (allowed: " +
        allowedTypes.join(", ") +
        ").",
    };
  }
  const compDef = scantleDecisionLibrary.components[el.typeName];
  if (!compDef) {
    return { ok: false, error: "Unknown OpenUI component " + el.typeName + "." };
  }

  const ambiguities = Array.isArray(card.ambiguities) ? card.ambiguities : [];
  const examples = Array.isArray(card.examples) ? card.examples : [];
  let resolvedAmbiguity = parentAmbiguity;

  if (el.typeName === "AmbiguityChoiceCard" || el.typeName === "PolicySimulatorTable") {
    const ambIdx = el.props.ambiguityIndex;
    if (
      typeof ambIdx !== "number" ||
      !Number.isInteger(ambIdx) ||
      ambIdx < 1 ||
      ambIdx > ambiguities.length
    ) {
      return {
        ok: false,
        error:
          el.typeName +
          " ambiguityIndex " +
          String(ambIdx) +
          " is out of bounds (valid range: 1.." +
          ambiguities.length +
          ").",
      };
    }
    resolvedAmbiguity = ambiguities[ambIdx - 1];
  }

  const projectedProps: Record<string, unknown> = { ...el.props };
  const shape = (compDef.props as { shape?: Record<string, unknown> }).shape || {};
  for (const [propKey, propSchema] of Object.entries(shape)) {
    const slotTypes = allowedSlotComponentNames(propSchema);
    if (!slotTypes) {
      continue;
    }
    const rawChildren = el.props[propKey];
    if (!Array.isArray(rawChildren)) {
      return {
        ok: false,
        error: el.typeName + "." + propKey + " must be an array of components.",
      };
    }
    const projectedChildren: Record<string, unknown>[] = [];
    for (let i = 0; i < rawChildren.length; i++) {
      const childRes = projectAndValidateNode(
        rawChildren[i],
        slotTypes,
        card,
        resolvedAmbiguity
      );
      if (!childRes.ok) {
        return childRes;
      }
      projectedChildren.push(childRes.projectedProps);
    }
    projectedProps[propKey] = projectedChildren;
  }

  const zodRes = compDef.props.safeParse(projectedProps);
  if (!zodRes.success) {
    return {
      ok: false,
      error:
        el.typeName +
        " props failed schema validation: " +
        zodRes.error.issues.map((iss: { message: string }) => iss.message).join("; "),
    };
  }

  if (el.typeName === "PolicySimulatorRow") {
    const outcomes = projectedProps.outcomes as string[];
    if (!resolvedAmbiguity || outcomes.length !== resolvedAmbiguity.options.length) {
      const wantLen = resolvedAmbiguity ? resolvedAmbiguity.options.length : 0;
      const ambId = resolvedAmbiguity ? resolvedAmbiguity.id : "unknown";
      return {
        ok: false,
        error:
          "PolicySimulatorRow outcomes must have exactly " +
          wantLen +
          " non-empty strings matching ambiguity " +
          ambId +
          " options.",
      };
    }
    const exIdx = projectedProps.exampleIndex;
    if (exIdx !== undefined) {
      if (
        typeof exIdx !== "number" ||
        !Number.isInteger(exIdx) ||
        exIdx < 1 ||
        exIdx > examples.length
      ) {
        return {
          ok: false,
          error:
            "PolicySimulatorRow exampleIndex " +
            String(exIdx) +
            " is out of bounds (valid range: 1.." +
            examples.length +
            ").",
        };
      }
    }
  }

  return { ok: true, projectedProps };
}

// validateCompletedOpenUI validates a settled openui-lang program against both
// the Scantle Decision Library schema and the active ChangeCardResult's
// 1-based ambiguity/example ordinals and option counts.
export function validateCompletedOpenUI(
  program: string,
  card: ChangeCardResult
): OpenUIValidationResult {
  if (
    card.ui_composition &&
    card.ui_composition.composed_for_revision !== card.request_revision
  ) {
    return {
      ok: false,
      error:
        "ui_composition composed_for_revision (" +
        card.ui_composition.composed_for_revision +
        ") does not match card.request_revision (" +
        card.request_revision +
        ").",
    };
  }
  const trimmed = (program || "").trim();
  if (!trimmed) {
    return { ok: false, error: "OpenUI program is empty." };
  }
  const parsed = scantleDecisionParser.parse(trimmed);
  if (!parsed.root || parsed.root.typeName !== scantleDecisionLibrary.root) {
    return {
      ok: false,
      error: "OpenUI program must define root = DecisionWorkspace(...).",
    };
  }
  if (parsed.meta.incomplete) {
    return {
      ok: false,
      error: "OpenUI program is incomplete (unclosed expression or statement).",
    };
  }
  if (parsed.meta.unresolved.length > 0) {
    return {
      ok: false,
      error: "OpenUI program has unresolved references: " + parsed.meta.unresolved.join(", "),
    };
  }
  if (parsed.meta.orphaned.length > 0) {
    return {
      ok: false,
      error: "OpenUI program has orphaned statements: " + parsed.meta.orphaned.join(", "),
    };
  }
  if (parsed.meta.errors.length > 0) {
    return {
      ok: false,
      error:
        "OpenUI schema validation failed: " +
        parsed.meta.errors.map((e) => e.message).join("; "),
    };
  }
  if (
    parsed.queryStatements.length > 0 ||
    parsed.mutationStatements.length > 0 ||
    Object.keys(parsed.stateDeclarations || {}).length > 0
  ) {
    return {
      ok: false,
      error: "OpenUI Query, Mutation, and $state declarations are not permitted in CP1 compositions.",
    };
  }

  const rootCheck = projectAndValidateNode(
    parsed.root,
    [scantleDecisionLibrary.root || "DecisionWorkspace"],
    card
  );
  if (!rootCheck.ok) {
    return { ok: false, error: rootCheck.error };
  }
  return { ok: true };
}

// generateDecisionCatalogMarkdown exports the OpenUI prompt catalog for embedding
// in the Go controlplane's CP1 semantic analyzer prompt.
export function generateDecisionCatalogMarkdown(): string {
  return scantleDecisionLibrary.prompt({
    preamble:
      "When emitting `ui_composition` for a Scantle CP1 Change Card, write a valid `openui-lang` program starting with `root = DecisionWorkspace(...)` using ONLY the components below.",
    additionalRules: [
      "Refer to ambiguities ONLY by 1-based `ambiguityIndex` (1..N matching the `ambiguities` array order in your JSON output). Never invent `<change_id>-ambiguity-NN` IDs.",
      "Refer to confirmation examples ONLY by 1-based `exampleIndex` (1..M matching the `examples` array order in your JSON output).",
      "In `PolicySimulatorRow`, `outcomes` MUST be a string array with one non-empty outcome string per option in the exact order of `ambiguities[ambiguityIndex - 1].options`.",
      "Never emit `Query(...)`, `Mutation(...)`, `$variables`, or `@Action` steps.",
    ],
    examples: [
      [
        "Example — Refund threshold ambiguity with 2 options and 2 scenarios:",
        "root = DecisionWorkspace(\"Refund Policy Decision Studio\", [sim, callout])",
        "sim = PolicySimulatorTable(\"Threshold Tax Comparison\", 1, [row1, row2])",
        "row1 = PolicySimulatorRow(\"Order with $48 subtotal and $4 tax\", \"$48 subtotal + $4 tax ($52 total)\", [\"Auto-approved (< $50 subtotal)\", \"Manager approval required (>= $50 total)\"], 1)",
        "row2 = PolicySimulatorRow(\"Order with $55 subtotal\", \"$55 subtotal + $5 tax ($60 total)\", [\"Manager approval required\", \"Manager approval required\"], 2)",
        "callout = ImpactCallout(\"info\", \"Tenant Isolation Invariant\", \"Cross-tenant refund lookups continue to return 404 Not Found.\")",
      ].join("\n"),
    ],
  });
}

export { Renderer };
