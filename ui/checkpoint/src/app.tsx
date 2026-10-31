// Package main implements the Scantle Checkpoint Studio MCP Apps guest bridge,
// coordinating the two-region layout (#scantle-cp1-editor and #scantle-result-view),
// JSON-RPC 2.0 host lifecycle notifications, theme synchronization, and
// synchronous React 19 rendering.

import React from "react";
import { flushSync } from "react-dom";
import { createRoot, Root } from "react-dom/client";
import {
  ArchitecturePreviewView,
  DiffResultView,
  DraftOverrideView,
  IncomingChangeCardView,
  ToolInputPartialView,
} from "./components/c4-graph";
import { CP1EditorView } from "./components/cp1-editor";
import { Alert, Card } from "./components/shadcn";
import { isDraftDirty } from "./context/cp1-draft";
import {
  ArchitecturePreviewResult,
  ChangeCardResult,
  DiffResult,
  DraftOverrideResult,
  ExampleItem,
  isArchitecturePreviewResult,
  isChangeCardResult,
  isDiffResult,
  isDraftOverrideResult,
  isPlainObject,
  PreservedDraft,
} from "./types";

const INIT_REQUEST_ID = 1;
const PROTOCOL_VERSION = "2026-01-26";

interface PendingRPC {
  validate: (payload: unknown) => boolean;
  onSuccess: (payload: unknown) => void;
  onError: (message: string) => void;
}

interface ActiveEditorState {
  card: ChangeCardResult;
  draftAnswers: Record<string, string>;
  draftExamples: ExampleItem[];
  rpcInFlight: boolean;
  preservedDraft: PreservedDraft | null;
  formError: string | null;
}

type ActiveResultState =
  | { kind: "cp2-architecture-preview"; preview: ArchitecturePreviewResult }
  | { kind: "cp2-diff"; diff: DiffResult }
  | { kind: "cp3-draft-override"; draft: DraftOverrideResult }
  | { kind: "cp1-incoming-card"; card: ChangeCardResult }
  | { kind: "partial-input"; partialArgs: Record<string, unknown> }
  | { kind: "error"; message: string }
  | { kind: "cancelled"; reason: string };

function formatDraftLines(
  rev: string,
  answers: Record<string, string>,
  examples: ExampleItem[]
): string[] {
  return Object.keys(answers || {})
    .map((k) => "[" + rev + "] " + k + "=" + answers[k])
    .concat(
      (examples || []).map(
        (ex) =>
          "[" +
          rev +
          "] " +
          ex.id +
          ": " +
          ex.scenario +
          " | " +
          ex.input_summary +
          " -> " +
          ex.expected_outcome +
          " [" +
          ex.verification_kind +
          "]"
      )
    );
}

function snapshotDraft(ed: ActiveEditorState): PreservedDraft {
  const priorLines = ed.preservedDraft ? ed.preservedDraft.lines : [];
  const dirty = isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples);
  const currentLines =
    dirty || priorLines.length === 0
      ? formatDraftLines(ed.card.request_revision, ed.draftAnswers, ed.draftExamples)
      : [];
  return {
    fromRevision: ed.card.request_revision,
    lines: priorLines.concat(currentLines),
  };
}

function extractErrorText(params: Record<string, unknown>): string {
  if (Array.isArray(params.content)) {
    for (let i = 0; i < params.content.length; i++) {
      const item = params.content[i];
      if (
        isPlainObject(item) &&
        item.type === "text" &&
        typeof item.text === "string" &&
        item.text.length > 0
      ) {
        return item.text;
      }
    }
  }
  return "Tool execution failed.";
}

function applyHostTheme(candidate: unknown) {
  if (!isPlainObject(candidate)) {
    return;
  }
  const direct = candidate.theme;
  const nested = isPlainObject(candidate.hostContext) ? candidate.hostContext.theme : undefined;
  const theme = direct === "dark" || direct === "light" ? direct : nested;
  if (theme === "dark" || theme === "light") {
    if (document.documentElement && document.documentElement.dataset) {
      document.documentElement.dataset.theme = theme;
    }
  }
}

// bootstrapCheckpointApp mounts the two-region Checkpoint Studio inside the
// MCP Apps host document and registers the window postMessage bridge.
export function bootstrapCheckpointApp() {
  const rawRootEl = document.getElementById("scantle-checkpoint-root");
  const rawStatusEl = document.getElementById("scantle-checkpoint-status");
  const viewEl = document.getElementById("scantle-checkpoint-view");
  if (!rawRootEl || !rawStatusEl || !viewEl) {
    return;
  }
  const rootEl: HTMLElement = rawRootEl;
  const statusEl: HTMLElement = rawStatusEl;

  const editorRegionEl = document.createElement("div");
  editorRegionEl.setAttribute("id", "scantle-cp1-editor");
  const resultRegionEl = document.createElement("div");
  resultRegionEl.setAttribute("id", "scantle-result-view");
  while (viewEl.firstChild) {
    viewEl.removeChild(viewEl.firstChild);
  }
  viewEl.appendChild(editorRegionEl);
  viewEl.appendChild(resultRegionEl);

  const editorRoot: Root = createRoot(editorRegionEl);
  const resultRoot: Root = createRoot(resultRegionEl);

  let initPending = false;
  let nextRequestId = 2;
  let pendingRequests: Record<string, PendingRPC> = {};
  let activeEditor: ActiveEditorState | null = null;
  let activeResult: ActiveResultState | null = null;

  function hasUnsavedEditorState(): boolean {
    if (!activeEditor) {
      return false;
    }
    return (
      isDraftDirty(
        activeEditor.card,
        activeEditor.draftAnswers,
        activeEditor.draftExamples
      ) ||
      activeEditor.preservedDraft !== null ||
      activeEditor.formError !== null
    );
  }

  function shouldRetainEditor(): boolean {
    return (
      activeEditor !== null &&
      (Object.keys(pendingRequests).length > 0 || hasUnsavedEditorState())
    );
  }

  function clearEditorDataset() {
    delete editorRegionEl.dataset.state;
    delete editorRegionEl.dataset.changeId;
    delete editorRegionEl.dataset.requestRevision;
  }

  function clearResultDataset() {
    delete resultRegionEl.dataset.state;
    delete resultRegionEl.dataset.changeId;
    delete resultRegionEl.dataset.requestRevision;
    delete resultRegionEl.dataset.designDigest;
    delete resultRegionEl.dataset.hasChanges;
  }

  function syncEditorRegion() {
    if (!activeEditor) {
      clearEditorDataset();
      flushSync(() => {
        editorRoot.render(null);
      });
      return;
    }
    const ed = activeEditor;
    editorRegionEl.dataset.state = "cp1-change-card";
    editorRegionEl.dataset.changeId = ed.card.change_id;
    editorRegionEl.dataset.requestRevision = ed.card.request_revision;
    flushSync(() => {
      editorRoot.render(
        <CP1EditorView
          card={ed.card}
          draftAnswers={ed.draftAnswers}
          draftExamples={ed.draftExamples}
          rpcInFlight={ed.rpcInFlight}
          preservedDraft={ed.preservedDraft}
          formError={ed.formError}
          onSetAnswer={(ambId, opt) => {
            if (!activeEditor || activeEditor.rpcInFlight) {
              return;
            }
            activeEditor = {
              ...activeEditor,
              draftAnswers: { ...activeEditor.draftAnswers, [ambId]: opt },
            };
            syncEditorRegion();
          }}
          onSetExampleField={(exId, field, value) => {
            if (!activeEditor || activeEditor.rpcInFlight) {
              return;
            }
            activeEditor = {
              ...activeEditor,
              draftExamples: activeEditor.draftExamples.map((item) =>
                item.id === exId ? { ...item, [field]: value } : item
              ),
            };
            syncEditorRegion();
          }}
          onSubmitConfirm={handleConfirmSubmit}
          onDiscardDraft={handleDiscardDraft}
          onLoadLatestCard={handleLoadLatestCard}
        />
      );
    });
  }

  function syncResultRegion() {
    clearResultDataset();
    if (!activeResult) {
      flushSync(() => {
        resultRoot.render(null);
      });
      return;
    }
    const res = activeResult;
    resultRegionEl.dataset.state = res.kind;
    if (res.kind === "cp2-architecture-preview") {
      resultRegionEl.dataset.changeId = res.preview.change_id;
      resultRegionEl.dataset.requestRevision = res.preview.request_revision;
      resultRegionEl.dataset.designDigest = res.preview.design_digest;
      flushSync(() => {
        resultRoot.render(<ArchitecturePreviewView preview={res.preview} />);
      });
      return;
    }
    if (res.kind === "cp2-diff") {
      resultRegionEl.dataset.hasChanges = String(res.diff.has_changes);
      flushSync(() => {
        resultRoot.render(<DiffResultView diff={res.diff} />);
      });
      return;
    }
    if (res.kind === "cp3-draft-override") {
      flushSync(() => {
        resultRoot.render(<DraftOverrideView draft={res.draft} />);
      });
      return;
    }
    if (res.kind === "cp1-incoming-card") {
      resultRegionEl.dataset.changeId = res.card.change_id;
      resultRegionEl.dataset.requestRevision = res.card.request_revision;
      flushSync(() => {
        resultRoot.render(
          <IncomingChangeCardView
            incomingCard={res.card}
            onSwitch={() => {
              if (Object.keys(pendingRequests).length > 0) {
                statusEl.textContent =
                  "Wait for the pending CP1 Change Card request to finish before switching cards.";
                return;
              }
              activeResult = null;
              syncResultRegion();
              mountChangeCard(res.card, null);
            }}
          />
        );
      });
      return;
    }
    if (res.kind === "partial-input") {
      flushSync(() => {
        resultRoot.render(<ToolInputPartialView partialArgs={res.partialArgs} />);
      });
      return;
    }
    if (res.kind === "error") {
      flushSync(() => {
        resultRoot.render(
          <Alert variant="destructive" role="alert">
            <strong>Error: </strong>
            {res.message}
          </Alert>
        );
      });
      return;
    }
    if (res.kind === "cancelled") {
      const detail =
        res.reason.length > 0
          ? "Tool call was cancelled (" +
            res.reason +
            "). Re-run the Scantle checkpoint tool to inspect a fresh result."
          : "Tool call was cancelled before completion. Re-run the Scantle checkpoint tool to inspect a fresh result.";
      flushSync(() => {
        resultRoot.render(
          <Card className="scantle-meta" role="status">
            <strong>Cancelled: </strong>
            {detail}
          </Card>
        );
      });
    }
  }

  function callServerTool(
    toolName: string,
    args: Record<string, unknown>,
    validateFn: (payload: unknown) => boolean,
    onSuccess: (payload: unknown) => void,
    onError: (message: string) => void
  ) {
    if (!window.parent || window.parent === window) {
      onError("Cannot invoke " + toolName + ": no MCP Apps host frame is attached.");
      return;
    }
    const reqId = nextRequestId++;
    pendingRequests[String(reqId)] = {
      validate: validateFn,
      onSuccess,
      onError,
    };
    window.parent.postMessage(
      {
        jsonrpc: "2.0",
        id: reqId,
        method: "tools/call",
        params: { name: toolName, arguments: args },
      },
      "*"
    );
  }

  function showFormError(msg: string) {
    if (!activeEditor) {
      return;
    }
    const stillPending = Object.keys(pendingRequests).length > 0;
    if (!stillPending) {
      statusEl.textContent = "CP1 Change Card update failed; unsaved edits preserved.";
    }
    activeEditor = {
      ...activeEditor,
      rpcInFlight: stillPending,
      formError: String(msg),
    };
    syncEditorRegion();
  }

  function handleConfirmSubmit() {
    if (!activeEditor || activeEditor.rpcInFlight) {
      return;
    }
    const ambiguities = Array.isArray(activeEditor.card.ambiguities)
      ? activeEditor.card.ambiguities
      : [];
    for (let i = 0; i < ambiguities.length; i++) {
      const ambId = ambiguities[i].id;
      if (!activeEditor.draftAnswers[ambId]) {
        showFormError("Select an option for ambiguity " + ambId + " before confirming.");
        return;
      }
    }
    for (let j = 0; j < activeEditor.draftExamples.length; j++) {
      const item = activeEditor.draftExamples[j];
      if (!item.scenario.trim() || !item.input_summary.trim() || !item.expected_outcome.trim()) {
        showFormError(
          "Example " + item.id + " requires non-empty scenario, input_summary, and expected_outcome."
        );
        return;
      }
    }
    activeEditor = {
      ...activeEditor,
      rpcInFlight: true,
      formError: null,
    };
    statusEl.textContent = "Submitting confirm_intent to MCP Apps host...";
    syncEditorRegion();

    const submittedCard = activeEditor.card;
    const submittedAnswers = { ...activeEditor.draftAnswers };
    const submittedExamples = activeEditor.draftExamples.map((item) => ({
      id: item.id,
      scenario: item.scenario,
      input_summary: item.input_summary,
      expected_outcome: item.expected_outcome,
      verification_kind: item.verification_kind,
    }));

    callServerTool(
      "confirm_intent",
      {
        change_id: submittedCard.change_id,
        request_revision: submittedCard.request_revision,
        answers: submittedAnswers,
        examples: submittedExamples,
      },
      isChangeCardResult,
      (updatedCard) => {
        mountChangeCard(updatedCard as ChangeCardResult, null);
      },
      showFormError
    );
  }

  function handleDiscardDraft() {
    if (!activeEditor || activeEditor.rpcInFlight) {
      return;
    }
    if (activeResult && activeResult.kind !== "cp1-incoming-card") {
      activeEditor = null;
      syncEditorRegion();
      rootEl.dataset.state = activeResult.kind;
      return;
    }
    mountChangeCard(activeEditor.card, null);
  }

  function handleLoadLatestCard() {
    if (!activeEditor || activeEditor.rpcInFlight) {
      return;
    }
    const savedDraft = snapshotDraft(activeEditor);
    const changeId = activeEditor.card.change_id;
    activeEditor = {
      ...activeEditor,
      rpcInFlight: true,
    };
    statusEl.textContent = "Loading latest Change Card via get_change...";
    syncEditorRegion();

    callServerTool(
      "get_change",
      { change_id: changeId },
      isChangeCardResult,
      (latestCard) => {
        mountChangeCard(latestCard as ChangeCardResult, savedDraft);
      },
      showFormError
    );
  }

  function mountChangeCard(card: ChangeCardResult, preservedDraft: PreservedDraft | null) {
    activeEditor = {
      card,
      draftAnswers: isPlainObject(card.answers) ? { ...card.answers } : {},
      draftExamples: card.examples.map((ex) => ({
        id: ex.id,
        scenario: ex.scenario,
        input_summary: ex.input_summary,
        expected_outcome: ex.expected_outcome,
        verification_kind: ex.verification_kind,
      })),
      rpcInFlight: false,
      preservedDraft,
      formError: null,
    };
    statusEl.textContent = "Rendered Checkpoint 1 Change Card (" + card.status + ")";
    rootEl.dataset.state = "cp1-change-card";
    syncEditorRegion();
  }

  function renderWaiting(message: string) {
    activeEditor = null;
    activeResult = null;
    syncEditorRegion();
    syncResultRegion();
    rootEl.dataset.state = "waiting";
    statusEl.textContent = message;
  }

  function renderCancelled(reason: string) {
    pendingRequests = {};
    activeEditor = null;
    activeResult = { kind: "cancelled", reason };
    syncEditorRegion();
    syncResultRegion();
    rootEl.dataset.state = "cancelled";
    statusEl.textContent = "Checkpoint tool call cancelled";
  }

  function renderError(message: string) {
    if (!shouldRetainEditor()) {
      activeEditor = null;
      syncEditorRegion();
    }
    activeResult = { kind: "error", message: String(message) };
    syncResultRegion();
    rootEl.dataset.state = "error";
    statusEl.textContent = "Checkpoint payload error";
  }

  function handleToolResult(params: unknown) {
    if (!isPlainObject(params)) {
      renderError("Malformed ui/notifications/tool-result: params must be an object.");
      return;
    }
    if (params.isError === true) {
      renderError(extractErrorText(params));
      return;
    }
    if (!isPlainObject(params.structuredContent)) {
      renderError("Tool result is missing structuredContent object.");
      return;
    }
    const payload = params.structuredContent;
    if (isArchitecturePreviewResult(payload)) {
      if (!shouldRetainEditor()) {
        activeEditor = null;
        syncEditorRegion();
      }
      activeResult = { kind: "cp2-architecture-preview", preview: payload };
      syncResultRegion();
      rootEl.dataset.state = "cp2-architecture-preview";
      statusEl.textContent =
        "Rendered Checkpoint 2 Architecture Preview (" + payload.system_name + ")";
      return;
    }
    if (isChangeCardResult(payload)) {
      if (activeEditor) {
        if (
          payload.change_id === activeEditor.card.change_id &&
          payload.request_revision === activeEditor.card.request_revision
        ) {
          return;
        }
        if (shouldRetainEditor()) {
          activeResult = { kind: "cp1-incoming-card", card: payload };
          syncResultRegion();
          rootEl.dataset.state = "cp1-change-card";
          statusEl.textContent =
            "Received Checkpoint 1 Change Card (" +
            payload.request_revision +
            "); unsaved editor draft preserved.";
          return;
        }
      }
      activeResult = null;
      syncResultRegion();
      mountChangeCard(payload, null);
      return;
    }
    if (isDiffResult(payload)) {
      if (!shouldRetainEditor()) {
        activeEditor = null;
        syncEditorRegion();
      }
      activeResult = { kind: "cp2-diff", diff: payload };
      syncResultRegion();
      rootEl.dataset.state = "cp2-diff";
      statusEl.textContent =
        "Rendered Structural Design Diff (has_changes=" + payload.has_changes + ")";
      return;
    }
    if (isDraftOverrideResult(payload)) {
      if (!shouldRetainEditor()) {
        activeEditor = null;
        syncEditorRegion();
      }
      activeResult = { kind: "cp3-draft-override", draft: payload };
      syncResultRegion();
      rootEl.dataset.state = "cp3-draft-override";
      statusEl.textContent = "Rendered Checkpoint 3 Fast Draft Override";
      return;
    }
    renderError("Unsupported or malformed Scantle checkpoint payload.");
  }

  function handleMessage(event: MessageEvent) {
    if (!event || event.source !== window.parent) {
      return;
    }
    const msg = event.data;
    if (!isPlainObject(msg) || msg.jsonrpc !== "2.0") {
      return;
    }
    if (typeof msg.method === "string") {
      if (msg.id !== undefined && msg.id !== null) {
        if (msg.method === "ui/resource-teardown") {
          pendingRequests = {};
          activeEditor = null;
          activeResult = null;
          syncEditorRegion();
          syncResultRegion();
          rootEl.dataset.state = "terminated";
          statusEl.textContent = "Checkpoint resource torn down.";
          window.parent.postMessage({ jsonrpc: "2.0", id: msg.id, result: {} }, "*");
          return;
        }
        if (msg.method === "ping") {
          window.parent.postMessage({ jsonrpc: "2.0", id: msg.id, result: {} }, "*");
          return;
        }
        window.parent.postMessage(
          {
            jsonrpc: "2.0",
            id: msg.id,
            error: { code: -32601, message: "Method not found: " + msg.method },
          },
          "*"
        );
        return;
      }
      if (msg.method === "ui/notifications/host-context-changed") {
        if (msg.params !== undefined && !isPlainObject(msg.params)) {
          return;
        }
        applyHostTheme(msg.params);
        return;
      }
      if (msg.method === "ui/notifications/tool-input-partial") {
        if (msg.params !== undefined && !isPlainObject(msg.params)) {
          renderError("Malformed ui/notifications/tool-input-partial notification.");
          return;
        }
        if (shouldRetainEditor()) {
          return;
        }
        const partialArgs =
          isPlainObject(msg.params) && isPlainObject(msg.params.arguments)
            ? msg.params.arguments
            : isPlainObject(msg.params)
            ? msg.params
            : {};
        activeEditor = null;
        activeResult = { kind: "partial-input", partialArgs };
        syncEditorRegion();
        syncResultRegion();
        rootEl.dataset.state = "waiting";
        statusEl.textContent = "Streaming partial tool input; awaiting validated checkpoint result...";
        return;
      }
      if (msg.method === "ui/notifications/tool-input") {
        if (msg.params !== undefined && !isPlainObject(msg.params)) {
          renderError("Malformed ui/notifications/tool-input notification.");
          return;
        }
        if (shouldRetainEditor()) {
          return;
        }
        renderWaiting("Received tool input; awaiting validated checkpoint result...");
        return;
      }
      if (msg.method === "ui/notifications/tool-cancelled") {
        if (msg.params !== undefined && !isPlainObject(msg.params)) {
          renderError("Malformed ui/notifications/tool-cancelled notification.");
          return;
        }
        const cancelReason =
          isPlainObject(msg.params) && typeof msg.params.reason === "string"
            ? msg.params.reason
            : "";
        if (activeEditor) {
          statusEl.textContent = cancelReason
            ? "Host reported tool cancellation (" + cancelReason + "); CP1 draft preserved."
            : "Host reported tool cancellation; CP1 draft preserved.";
          return;
        }
        renderCancelled(cancelReason);
        return;
      }
      if (msg.method === "ui/notifications/tool-result") {
        handleToolResult(msg.params);
      }
      return;
    }

    const reqKey = msg.id !== undefined && msg.id !== null ? String(msg.id) : "";
    if (reqKey && Object.prototype.hasOwnProperty.call(pendingRequests, reqKey)) {
      const activeReq = pendingRequests[reqKey];
      delete pendingRequests[reqKey];
      if (msg.error) {
        const rpcErr =
          isPlainObject(msg.error) && typeof msg.error.message === "string"
            ? msg.error.message
            : "Host rejected tool call.";
        activeReq.onError(rpcErr);
        return;
      }
      if (!isPlainObject(msg.result)) {
        activeReq.onError("Host returned malformed tools/call result.");
        return;
      }
      if (msg.result.isError === true) {
        activeReq.onError(extractErrorText(msg.result));
        return;
      }
      if (
        !isPlainObject(msg.result.structuredContent) ||
        (typeof activeReq.validate === "function" &&
          !activeReq.validate(msg.result.structuredContent))
      ) {
        activeReq.onError("Tool response is missing a valid structured checkpoint payload.");
        return;
      }
      activeReq.onSuccess(msg.result.structuredContent);
      return;
    }

    if (initPending && msg.id === INIT_REQUEST_ID) {
      initPending = false;
      if (msg.error) {
        const errMsg =
          isPlainObject(msg.error) && typeof msg.error.message === "string"
            ? msg.error.message
            : "MCP Apps host rejected ui/initialize.";
        renderError(errMsg);
        return;
      }
      if (!isPlainObject(msg.result)) {
        renderError("MCP Apps host returned invalid ui/initialize result.");
        return;
      }
      applyHostTheme(msg.result);
      rootEl.dataset.initialized = "true";
      statusEl.textContent = "MCP Apps handshake complete; awaiting checkpoint tool result...";
      window.parent.postMessage(
        { jsonrpc: "2.0", method: "ui/notifications/initialized", params: {} },
        "*"
      );
    }
  }

  window.addEventListener("message", handleMessage);

  if (window.parent && window.parent !== window) {
    initPending = true;
    window.parent.postMessage(
      {
        jsonrpc: "2.0",
        id: INIT_REQUEST_ID,
        method: "ui/initialize",
        params: {
          protocolVersion: PROTOCOL_VERSION,
          appInfo: { name: "scantle-checkpoint", version: "1.0.0" },
          appCapabilities: {},
        },
      },
      "*"
    );
  }
}

bootstrapCheckpointApp();
