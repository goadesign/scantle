// Package components provides the Checkpoint 1 Change Card editor, combining
// the OpenUI Decision Workspace with the always-available canonical shadcn/ui
// CP1 inspector over a single shared draft context.

import React, { useEffect, useRef } from "react";
import { flushSync } from "react-dom";
import {
  areExamplesModified,
  CP1DraftContext,
  CP1DraftContextValue,
} from "../context/cp1-draft";
import {
  Renderer,
  scantleDecisionLibrary,
  validateCompletedOpenUI,
} from "../openui/library";
import { ChangeCardResult, ExampleItem, PreservedDraft } from "../types";
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
    <label className="scantle-field-label">
      <span>{label + ": "}</span>
      <textarea
        ref={elRef}
        rows={2}
        className="scantle-input shadcn-textarea"
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
          id="scantle-openui-workspace"
          data-composed-for-revision={card.ui_composition.composed_for_revision}
        >
          <Renderer
            library={scantleDecisionLibrary}
            response={card.ui_composition.program}
            isStreaming={false}
            publishObservability={false}
          />
        </div>
      );
    } else {
      openUINode = (
        <Alert id="scantle-openui-error" variant="destructive" role="alert">
          <strong>OpenUI Composition Rejected: </strong>
          {check.error} — Use the canonical CP1 inspector below to resolve ambiguities and lock
          examples.
        </Alert>
      );
    }
  }

  return (
    <CP1DraftContext.Provider value={ctxValue}>
      <Card className="scantle-meta">
        <h2>{"CP1 Change Card: " + card.change_id}</h2>
        <p>
          <Badge>{card.status}</Badge>
          request_revision: <code>{card.request_revision}</code>
        </p>
        <p>
          <strong>Summary: </strong>
          {card.summary}
        </p>
        <p>
          <strong>Prompt: </strong>
          {card.prompt}
        </p>
        {card.confirmed_by ? (
          <p>
            <strong>Confirmed by: </strong>
            {card.confirmed_by}
          </p>
        ) : null}
      </Card>

      {preservedDraft && Array.isArray(preservedDraft.lines) ? (
        <Card
          id="scantle-preserved-draft"
          className="scantle-card"
          role="region"
          aria-label="Preserved Unsaved Draft Edits"
        >
          <h3>
            {"Preserved Unsaved Draft Edits (from superseded revision " +
              preservedDraft.fromRevision +
              ")"}
          </h3>
          <p>
            Review the latest saved Change Card below before re-applying any of your unsaved edits:
          </p>
          <ul>
            {preservedDraft.lines.map((line, idx) => (
              <li key={String(idx)}>{line}</li>
            ))}
          </ul>
        </Card>
      ) : null}

      {openUINode}

      <Card className="scantle-card">
        <h3>{"Ambiguities (" + ambiguities.length + ")"}</h3>
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
                  <code>{amb.id}</code>
                  {": " + amb.question + " (options: " + amb.options.join(", ") + ")"}
                  {answerBadge}
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
                </li>
              );
            })}
          </ul>
        ) : (
          <p>No open domain ambiguities.</p>
        )}

        <h3>{"Concrete Examples (" + draftExamples.length + ")"}</h3>
        <ul>
          {draftExamples.map((ex, idx) => (
            <li key={ex.id} data-example-index={String(idx)}>
              <div>
                <code>{ex.id}</code>
                {" [" + ex.verification_kind + "] "}
                <strong>{ex.scenario}</strong>
                {" — Input: " + ex.input_summary + " -> Expected: " + ex.expected_outcome}
              </div>
              <div className="scantle-field-row">
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

        <div className="scantle-field-row">
          <Button
            id="scantle-confirm-submit"
            variant="primary"
            disabled={rpcInFlight}
            onClick={onSubmitConfirm}
          >
            {card.status === "needs_confirmation"
              ? "Confirm Intent & Lock Examples"
              : "Refine Confirmed Answers & Examples"}
          </Button>
          <Button
            id="scantle-discard-draft"
            disabled={rpcInFlight}
            onClick={onDiscardDraft}
          >
            Discard Unsaved Edits
          </Button>
        </div>

        <div id="scantle-confirm-feedback">
          {formError ? (
            <Alert variant="destructive" role="alert">
              <strong>Confirm Intent Error: </strong>
              {formError}
              {
                " — Your edited answers and examples remain in this form. Use Load Latest Card (get_change) to inspect the current saved Change Card alongside your unsaved edits before resubmitting."
              }
              <div className="scantle-field-row">
                <Button
                  id="scantle-load-latest-card"
                  disabled={rpcInFlight}
                  onClick={onLoadLatestCard}
                >
                  Load Latest Card (get_change)
                </Button>
              </div>
            </Alert>
          ) : null}
        </div>
      </Card>
    </CP1DraftContext.Provider>
  );
}
