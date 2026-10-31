// Package types declares the wire payload interfaces and runtime validators for
// Scantle Checkpoint 1 through Checkpoint 3 MCP tool results.

export interface AmbiguityQuestion {
  id: string;
  question: string;
  options: string[];
}

export interface ExampleItem {
  id: string;
  scenario: string;
  input_summary: string;
  expected_outcome: string;
  verification_kind: "deterministic" | "model_judged";
}

export interface UICompositionItem {
  program: string;
  composed_for_revision: string;
}

export interface ChangeCardResult {
  change_id: string;
  status: "needs_confirmation" | "confirmed";
  prompt: string;
  summary: string;
  ambiguities?: AmbiguityQuestion[];
  answers?: Record<string, string>;
  examples: ExampleItem[];
  preserved_behaviors: string[];
  confirmed_by?: string;
  request_revision: string;
  ui_composition?: UICompositionItem;
}

export interface C4ContainerItem {
  id: string;
  name: string;
  kind: "service" | "agent" | "toolset" | "person" | "system" | "container" | "component";
  description: string;
}

export interface C4EdgeItem {
  from_id: string;
  to_id: string;
  protocol: string;
}

export interface AgentPolicyItem {
  agent_id: string;
  max_execution_seconds: number;
  max_tool_calls: number;
  max_recovery_turns: number;
  compress_at_turns: number;
  keep_recent_turns: number;
  cache_after_system: boolean;
  cache_after_tools: boolean;
}

export interface ToolInputBindingItem {
  args_schema: string;
  text_only_args_schema?: string;
  lookup_args_schema?: string;
  lookup_text_only_args_schema?: string;
  injected_fields: string[];
}

export interface ToolContractItem {
  id: string;
  toolset: string;
  name: string;
  description: string;
  args_schema: string;
  text_only_args_schema?: string;
  input_binding?: ToolInputBindingItem;
  return_schema: string;
  injected_fields: string[];
  requires_ui: boolean;
  requires_confirmation: boolean;
  bounded_result: boolean;
}

export interface SwarmRoleItem {
  handle: string;
  role: "architect" | "builder" | "eval_author" | "reviewer";
  scope_name: string;
  profile_id: string;
  provider: string;
  model: string;
  family: string;
  owned_paths: string[];
  access_mode: "read_only_after_cp2" | "read_write_worktree" | "isolated_eval_workspace" | "read_only_review";
}

export interface SwarmPlanItem {
  status: "configured" | "unconfigured" | "missing_capability";
  assignments: SwarmRoleItem[];
  cross_family_review: boolean;
  cross_family_eval: boolean;
  single_family: boolean;
  missing_roles?: string[];
  summary: string;
  manifest?: string;
}

export interface ArchitecturePreviewResult {
  change_id: string;
  request_revision: string;
  design_digest: string;
  system_name: string;
  containers: C4ContainerItem[];
  edges: C4EdgeItem[];
  agent_policies: AgentPolicyItem[];
  tools: ToolContractItem[];
  swarm: SwarmPlanItem;
  banner: string;
}

export interface DiffResult {
  has_changes: boolean;
  added_declarations: string[];
  removed_declarations: string[];
  modified_declarations: string[];
  version_deltas: string[];
  summary: string;
}

export interface DraftOverrideResult {
  applied_in_store: boolean;
  requires_rebuild: boolean;
  prompt_version?: string;
  message: string;
}

export interface PreservedDraft {
  fromRevision: string;
  answers: Record<string, string>;
  examples: ExampleItem[];
  lines: string[];
}

export function isPlainObject(v: unknown): v is Record<string, unknown> {
  return Boolean(v) && typeof v === "object" && !Array.isArray(v);
}

export function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((item) => typeof item === "string");
}

export function isStringMap(v: unknown): v is Record<string, string> {
  return isPlainObject(v) && Object.keys(v).every((k) => typeof v[k] === "string");
}

// isValidUIComposition validates an optional UICompositionItem on a Change Card.
function isValidUIComposition(v: unknown): boolean {
  if (v === undefined) {
    return true;
  }
  return (
    isPlainObject(v) &&
    typeof v.program === "string" &&
    typeof v.composed_for_revision === "string" &&
    v.composed_for_revision.length > 0
  );
}

// isChangeCardResult validates that a payload conforms to ChangeCardResult.
export function isChangeCardResult(p: unknown): p is ChangeCardResult {
  if (!isPlainObject(p)) return false;
  if (
    typeof p.change_id !== "string" ||
    (p.status !== "needs_confirmation" && p.status !== "confirmed") ||
    typeof p.prompt !== "string" ||
    typeof p.summary !== "string" ||
    typeof p.request_revision !== "string" ||
    !Array.isArray(p.examples) ||
    !isStringArray(p.preserved_behaviors) ||
    (p.confirmed_by !== undefined && typeof p.confirmed_by !== "string") ||
    (p.answers !== undefined && !isStringMap(p.answers)) ||
    !isValidUIComposition(p.ui_composition)
  ) {
    return false;
  }
  if (
    p.ambiguities !== undefined &&
    (!Array.isArray(p.ambiguities) ||
      !p.ambiguities.every(
        (a) =>
          isPlainObject(a) &&
          typeof a.id === "string" &&
          typeof a.question === "string" &&
          isStringArray(a.options)
      ))
  ) {
    return false;
  }
  return p.examples.every(
    (ex) =>
      isPlainObject(ex) &&
      typeof ex.id === "string" &&
      typeof ex.scenario === "string" &&
      typeof ex.input_summary === "string" &&
      typeof ex.expected_outcome === "string" &&
      (ex.verification_kind === "deterministic" || ex.verification_kind === "model_judged")
  );
}

// isArchitecturePreviewResult validates that a payload conforms to ArchitecturePreviewResult.
export function isArchitecturePreviewResult(p: unknown): p is ArchitecturePreviewResult {
  if (!isPlainObject(p)) return false;
  if (
    typeof p.change_id !== "string" ||
    typeof p.request_revision !== "string" ||
    typeof p.design_digest !== "string" ||
    typeof p.system_name !== "string" ||
    typeof p.banner !== "string" ||
    !Array.isArray(p.containers) ||
    !Array.isArray(p.edges) ||
    !Array.isArray(p.agent_policies) ||
    !Array.isArray(p.tools) ||
    !isPlainObject(p.swarm)
  ) {
    return false;
  }
  const containersOk = p.containers.every(
    (c) =>
      isPlainObject(c) &&
      typeof c.id === "string" &&
      typeof c.name === "string" &&
      typeof c.kind === "string" &&
      typeof c.description === "string"
  );
  const edgesOk = p.edges.every(
    (e) =>
      isPlainObject(e) &&
      typeof e.from_id === "string" &&
      typeof e.to_id === "string" &&
      typeof e.protocol === "string"
  );
  const policiesOk = p.agent_policies.every(
    (pol) =>
      isPlainObject(pol) &&
      typeof pol.agent_id === "string" &&
      typeof pol.max_execution_seconds === "number" &&
      typeof pol.max_tool_calls === "number" &&
      typeof pol.max_recovery_turns === "number" &&
      typeof pol.compress_at_turns === "number" &&
      typeof pol.keep_recent_turns === "number" &&
      typeof pol.cache_after_system === "boolean" &&
      typeof pol.cache_after_tools === "boolean"
  );
  const toolsOk = p.tools.every((t) => {
    if (!isPlainObject(t)) return false;
    if (
      typeof t.id !== "string" ||
      typeof t.toolset !== "string" ||
      typeof t.name !== "string" ||
      typeof t.description !== "string" ||
      typeof t.args_schema !== "string" ||
      typeof t.return_schema !== "string" ||
      !isStringArray(t.injected_fields) ||
      typeof t.requires_ui !== "boolean" ||
      typeof t.requires_confirmation !== "boolean" ||
      typeof t.bounded_result !== "boolean"
    ) {
      return false;
    }
    if (
      (t.text_only_args_schema !== undefined && typeof t.text_only_args_schema !== "string") ||
      (t.input_binding !== undefined &&
        (!isPlainObject(t.input_binding) ||
          typeof t.input_binding.args_schema !== "string" ||
          !isStringArray(t.input_binding.injected_fields)))
    ) {
      return false;
    }
    return true;
  });
  const s = p.swarm;
  const swarmOk =
    typeof s.status === "string" &&
    Array.isArray(s.assignments) &&
    s.assignments.every(
      (a) =>
        isPlainObject(a) &&
        typeof a.handle === "string" &&
        typeof a.role === "string" &&
        typeof a.scope_name === "string" &&
        typeof a.profile_id === "string" &&
        typeof a.provider === "string" &&
        typeof a.model === "string" &&
        typeof a.family === "string" &&
        isStringArray(a.owned_paths) &&
        typeof a.access_mode === "string"
    ) &&
    typeof s.cross_family_review === "boolean" &&
    typeof s.cross_family_eval === "boolean" &&
    typeof s.single_family === "boolean" &&
    (s.missing_roles === undefined || isStringArray(s.missing_roles)) &&
    typeof s.summary === "string" &&
    (s.manifest === undefined || typeof s.manifest === "string");
  return containersOk && edgesOk && policiesOk && toolsOk && swarmOk;
}

// isDiffResult validates that a payload conforms to DiffResult.
export function isDiffResult(p: unknown): p is DiffResult {
  return (
    isPlainObject(p) &&
    typeof p.has_changes === "boolean" &&
    isStringArray(p.added_declarations) &&
    isStringArray(p.removed_declarations) &&
    isStringArray(p.modified_declarations) &&
    isStringArray(p.version_deltas) &&
    typeof p.summary === "string"
  );
}

// isDraftOverrideResult validates that a payload conforms to DraftOverrideResult.
export function isDraftOverrideResult(p: unknown): p is DraftOverrideResult {
  return (
    isPlainObject(p) &&
    typeof p.applied_in_store === "boolean" &&
    typeof p.requires_rebuild === "boolean" &&
    typeof p.message === "string" &&
    (p.prompt_version === undefined || typeof p.prompt_version === "string")
  );
}
