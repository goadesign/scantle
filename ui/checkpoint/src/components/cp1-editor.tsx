// Package components provides the Checkpoint 1 Change Card editor, combining
// the OpenUI Decision Workspace with the always-available canonical shadcn/ui
// CP1 inspector over a single shared draft context.

import React, { useEffect, useRef } from "react";
import { flushSync } from "react-dom";
import {
  areExamplesModified,
  CP1DraftContext,
  CP1DraftContextValue,
  isDraftDirty,
  PreservedDraft,
} from "../context/cp1-draft";
import {
  Renderer,
  surlaDecisionLibrary,
  validateCompletedOpenUI,
} from "../openui/library";
import { ChangeCardResult, ExampleItem } from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

export interface CP1EditorProps {
  card: ChangeCardResult;
  draftAnswers: Record<string, string>;
  draftExamples: ExampleItem[];
  rpcInFlight: boolean;
  preservedDraft: PreservedDraft | null;
  formError: string | null;
  onSetAnswer: (ambiguityId: string, option: string) => void;
  onSetExampleField: (
    exampleId: string,
    field: "scenario" | "input_summary" | "expected_outcome" | "verification_kind",
    value: string
  ) => void;
  onSubmitConfirm: () => void;
  onDiscardDraft: () => void;
  onLoadLatestCard: () => void;
}

interface FieldTextareaProps {
  exampleId: string;
  field: "scenario" | "input_summary" | "expected_outcome";
  label: string;
  value: string;
  disabled: boolean;
  onChangeValue: (next: string) => void;
}

// FieldTextarea renders a controlled multiline textarea that preserves exact
// whitespace and newlines while rejecting mutations when an RPC is in flight.
function FieldTextarea({
  exampleId,
  field,
  label,
  value,
  disabled,
  onChangeValue,
}: FieldTextareaProps) {
  const elRef = useRef<HTMLTextAreaElement | null>(null);
  const valueRef = useRef(value);
  const disabledRef = useRef(disabled);
  const onChangeRef = useRef(onChangeValue);
  valueRef.current = value;
  disabledRef.current = disabled;
  onChangeRef.current = onChangeValue;

  useEffect(() => {
    const node = elRef.current;
    if (!node) {
      return;
    }
    const handleInput = (evt: Event) => {
      const target = (evt.target as HTMLTextAreaElement) || node;
      if (disabledRef.current) {
        target.value = valueRef.current;
        return;
      }
      if (typeof target.value === "string" && target.value !== valueRef.current) {
        flushSync(() => {
          onChangeRef.current(target.value);
        });
      }
    };
    node.addEventListener("input", handleInput);
    return () => {
      node.removeEventListener("input", handleInput);
    };
  }, []);

  useEffect(() => {
    if (elRef.current && elRef.current.value !== value) {
      elRef.current.value = value;
    }
  }, [value]);

  return (
    <label className="surla-field-label">
      <span>{label + ": "}</span>
      <textarea
        ref={elRef}
        rows={2}
        className="surla-input shadcn-textarea"
        aria-label={exampleId + " " + label}
        data-example-id={exampleId}
        data-field={field}
        disabled={disabled}
        value={value}
        onChange={(e) => {
          if (disabled) {
            e.target.value = value;
            return;
          }
          onChangeValue(e.target.value);
        }}
      />
    </label>
  );
}

// CP1EditorView renders the active CP1 Change Card, its OpenUI Decision Studio
// workspace (when present), and the canonical shadcn/ui CP1 inspector.
export function CP1EditorView({
  card,
  draftAnswers,
  draftExamples,
  rpcInFlight,
  preservedDraft,
  formError,
  onSetAnswer,
  onSetExampleField,
  onSubmitConfirm,
  onDiscardDraft,
  onLoadLatestCard,
}: CP1EditorProps) {
  const ambiguities = Array.isArray(card.ambiguities) ? card.ambiguities : [];
  const localExamplesEdited = areExamplesModified(card.examples, draftExamples);
  const draftDirty = isDraftDirty(card, draftAnswers, draftExamples);
  const artifactNames = card.approval
    ? Object.keys(card.approval.artifacts || {}).sort()
    : [];

  const ctxValue: CP1DraftContextValue = {
    card,
    draftAnswers,
    draftExamples,
    rpcInFlight,
    localExamplesEdited,
    setAnswer: onSetAnswer,
  };

  let openUINode: React.ReactNode = null;
  if (card.ui_composition) {
    const check = validateCompletedOpenUI(card.ui_composition.program, card);
    if (check.ok) {
      openUINode = (
        <div
          id="surla-openui-workspace"
          data-composed-for-revision={card.ui_composition.composed_for_revision}
        >
          <Renderer
            library={surlaDecisionLibrary}
            response={card.ui_composition.program}
            isStreaming={false}
            publishObservability={false}
          />
        </div>
      );
    } else {
      openUINode = (
        <Alert id="surla-openui-error" variant="destructive" role="alert">
          <strong>Interactive decision preview unavailable: </strong>
          You can still answer every open question and edit or confirm every example directly in the
          form below.
          <details className="surla-details">
            <summary>Details</summary>
            <p>{check.error}</p>
          </details>
        </Alert>
      );
    }
  }

  return (
    <CP1DraftContext.Provider value={ctxValue}>
      <Card className="surla-meta">
        <h2>
          {card.status === "confirmed"
            ? "Confirmed Change Decision"
            : "Review & Confirm Change Intent"}
        </h2>
        <p>
          <Badge>{card.status === "confirmed" ? "Confirmed" : "Needs confirmation"}</Badge>
          {card.approval ? <Badge variant="accent">Approval recorded</Badge> : null}
          <strong>Summary: </strong>
          {card.summary}
        </p>
        <p>
          <strong>Requested change: </strong>
          {card.prompt}
        </p>
        {card.confirmed_by ? (
          <p>
            <strong>Confirmed by: </strong>
            {card.confirmed_by}
          </p>
        ) : null}
        <details className="surla-details">
          <summary>Details</summary>
          <p>
            change_id: <code>{card.change_id}</code> · status: <code>{card.status}</code> ·
            request_revision: <code>{card.request_revision}</code>
          </p>
        </details>
      </Card>

      {card.approval ? (
        <Card
          id="surla-cp5-approval"
          className="surla-card"
          role="region"
          aria-label="Checkpoint 5 Promotion Approval"
        >
          <div className="surla-field-row">
            <h3>Checkpoint 5 Promotion Approval</h3>
            <Badge variant="accent">In-memory approval recorded</Badge>
            <Badge>No merge or deploy side effect</Badge>
          </div>
          <p>
            Approval recorded by bearer principal <code>{card.approval.approved_by}</code> for this
            confirmed <code>request_revision</code> in control-plane memory. Recording approval does
            not merge branches or deploy binaries. This record describes the inputs below;
            check the current approval to see whether they still match the service.
          </p>
          <Button
            id="surla-cp5-refresh-approval"
            disabled={rpcInFlight}
            onClick={onLoadLatestCard}
          >
            Check current approval
          </Button>
          {draftDirty ? (
            <Alert id="surla-cp5-approval-dirty-warning" variant="warning" role="status">
              <strong>Unsaved draft edits detected: </strong>
              Saving refined answers or examples will mint a new <code>request_revision</code> and
              clear this approval.
            </Alert>
          ) : null}
          <p>
            <strong>Approved by: </strong>
            <code>{card.approval.approved_by}</code> · <strong>Environment: </strong>
            <code>{card.approval.environment}</code> · <strong>Scenarios passed: </strong>
            <code>{String(card.approval.scenario_count)}</code>
          </p>
          <p>
            <strong>Candidate commit: </strong>
            <code>{card.approval.candidate_commit}</code>
          </p>
          <p>
            <strong>Verification report digest: </strong>
            <code>{card.approval.report_digest}</code>
          </p>
          <p>
            <strong>Evaluator suite: </strong>
            <code>{card.approval.suite_id}</code> @ <code>{card.approval.suite_commit}</code>
          </p>
          <h4>{"Verified Candidate Artifacts (" + artifactNames.length + ")"}</h4>
          <ul>
            {artifactNames.map((name) => (
              <li key={name}>
                <code>{name}</code>: <code>{card.approval!.artifacts[name]}</code>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      {preservedDraft && Array.isArray(preservedDraft.lines) ? (
        <Card
          id="surla-preserved-draft"
          className="surla-card"
          role="region"
          aria-label="Preserved Unsaved Draft Edits"
        >
          <h3>Preserved Unsaved Draft Edits</h3>
          <p>
            Review the latest saved Change Card below before re-applying any of your unsaved edits:
          </p>
          <ul>
            {preservedDraft.lines.map((line, idx) => (
              <li key={String(idx)}>{line}</li>
            ))}
          </ul>
          <details className="surla-details">
            <summary>Details</summary>
            <p>
              Superseded revision: <code>{preservedDraft.fromRevision}</code>
            </p>
          </details>
        </Card>
      ) : null}

      {openUINode}

      <Card className="surla-card">
        <h3>{"Open Questions (" + ambiguities.length + ")"}</h3>
        {ambiguities.length > 0 ? (
          <ul>
            {ambiguities.map((amb) => {
              const selected = draftAnswers[amb.id] || "";
              const saved = (card.answers && card.answers[amb.id]) || "";
              const answerBadge = !selected
                ? ""
                : card.status === "confirmed" && selected === saved
                ? " [Confirmed answer: " + selected + "]"
                : " [Selected draft answer: " + selected + "]";
              return (
                <li key={amb.id}>
                  <div>
                    <strong>{amb.question}</strong>
                    {answerBadge}
                  </div>
                  <div role="group" aria-label={amb.question}>
                    {amb.options.map((opt) => (
                      <Button
                        key={opt}
                        variant="pill"
                        disabled={rpcInFlight}
                        data-ambiguity-id={amb.id}
                        data-option={opt}
                        aria-pressed={selected === opt ? "true" : "false"}
                        onClick={() => onSetAnswer(amb.id, opt)}
                      >
                        {opt}
                      </Button>
                    ))}
                  </div>
                  <details className="surla-details">
                    <summary>Details</summary>
                    <p>
                      <code>{amb.id}</code>
                      {" (options: " + amb.options.join(", ") + ")"}
                    </p>
                  </details>
                </li>
              );
            })}
          </ul>
        ) : (
          <p>No open domain questions.</p>
        )}

        <h3>{"Concrete Examples (" + draftExamples.length + ")"}</h3>
        <ul>
          {draftExamples.map((ex, idx) => (
            <li key={ex.id} data-example-index={String(idx)}>
              <div>
                <strong>{ex.scenario}</strong>
                {" — Input: " + ex.input_summary + " -> Expected: " + ex.expected_outcome + " "}
                <Badge>
                  {ex.verification_kind === "deterministic"
                    ? "Deterministic check"
                    : "Model-judged check"}
                </Badge>
              </div>
              <div className="surla-field-row">
                <FieldTextarea
                  exampleId={ex.id}
                  field="scenario"
                  label="Scenario"
                  value={ex.scenario}
                  disabled={rpcInFlight}
                  onChangeValue={(v) => onSetExampleField(ex.id, "scenario", v)}
                />
                <FieldTextarea
                  exampleId={ex.id}
                  field="input_summary"
                  label="Input"
                  value={ex.input_summary}
                  disabled={rpcInFlight}
                  onChangeValue={(v) => onSetExampleField(ex.id, "input_summary", v)}
                />
                <FieldTextarea
                  exampleId={ex.id}
                  field="expected_outcome"
                  label="Expected result"
                  value={ex.expected_outcome}
                  disabled={rpcInFlight}
                  onChangeValue={(v) => onSetExampleField(ex.id, "expected_outcome", v)}
                />
                <Button
                  variant="pill"
                  disabled={rpcInFlight}
                  data-example-id={ex.id}
                  data-field="verification_kind"
                  onClick={() =>
                    onSetExampleField(
                      ex.id,
                      "verification_kind",
                      ex.verification_kind === "deterministic" ? "model_judged" : "deterministic"
                    )
                  }
                >
                  {"Kind: " + ex.verification_kind}
                </Button>
              </div>
              <details className="surla-details">
                <summary>Details</summary>
                <p>
                  <code>{ex.id}</code>
                  {" [" + ex.verification_kind + "]"}
                </p>
              </details>
            </li>
          ))}
        </ul>

        <h3>{"Preserved Contract Invariants (" + card.preserved_behaviors.length + ")"}</h3>
        {card.preserved_behaviors.length > 0 ? (
          <ul>
            {card.preserved_behaviors.map((pb, idx) => (
              <li key={String(idx)}>{pb}</li>
            ))}
          </ul>
        ) : (
          <p>None declared.</p>
        )}

        <div className="surla-field-row">
          <Button
            id="surla-confirm-submit"
            variant="primary"
            disabled={rpcInFlight}
            onClick={onSubmitConfirm}
          >
            {card.status === "needs_confirmation"
              ? "Confirm Intent & Lock Examples"
              : "Refine Confirmed Answers & Examples"}
          </Button>
          <Button
            id="surla-discard-draft"
            disabled={rpcInFlight}
            onClick={onDiscardDraft}
          >
            Discard Unsaved Edits
          </Button>
        </div>

        <div id="surla-confirm-feedback">
          {formError ? (
            <Alert variant="destructive" role="alert">
              <strong>Change Card request failed: </strong>
              <p>{formError}</p>
              Your edited answers and examples remain in this form. Click{" "}
              <strong>Load Latest Card</strong> to inspect the current saved Change Card alongside
              your unsaved edits.
              <div className="surla-field-row">
                <Button
                  id="surla-load-latest-card"
                  disabled={rpcInFlight}
                  onClick={onLoadLatestCard}
                >
                  Load Latest Card
                </Button>
              </div>
            </Alert>
          ) : null}
        </div>
      </Card>
    </CP1DraftContext.Provider>
  );
}
