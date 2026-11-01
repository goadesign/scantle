// Package cp4-verification displays the evaluator's declared scenario outcomes
// and the exact saved revision they describe, without exposing private evidence.

import React from "react";
import {
  BuilderVerificationReport,
  VerificationNotCompleted,
  VerificationScenario,
  VerificationStatusResult,
} from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

// VerificationViewProps supplies the saved run and one explicit status read.
interface VerificationViewProps {
  // run is the generated, validated snapshot received from the server.
  run: VerificationStatusResult;
  // displayedCardRevision is the saved revision shown for this same card, if any.
  displayedCardRevision: string | null;
  // checking reflects an outstanding host request for this run.
  checking: boolean;
  // requestError leaves the last valid run visible when a read fails.
  requestError: string | null;
  // onCheckStatus reads this exact run; it never restarts verification.
  onCheckStatus: () => void;
}

// scenarioOutcomeLabels translates the evaluator's outcomes without treating
// missing or unusable scenario evidence as a failed product assertion.
const scenarioOutcomeLabels: Record<VerificationScenario["outcome"], string> = {
  passed: "Passed",
  failed: "Failed",
  not_run: "Not run",
  harness_error: "Could not evaluate",
};

// failureStageLabels explains which operation stopped before a report existed.
const failureStageLabels: Record<VerificationNotCompleted["stage"], string> = {
  checkout: "Loading the saved source",
  build: "Building the programs",
  describe: "Reading the declared checks",
  suite: "Running the evaluator",
  report: "Saving the verification report",
  supervise: "Running the verification",
};

// VerificationView renders one validated run. Only a completed report supplies
// a verdict; a failed status read preserves the run rather than changing it.
export function VerificationView({
  run,
  displayedCardRevision,
  checking,
  requestError,
  onCheckStatus,
}: VerificationViewProps) {
  const state = run.state;
  const title = state.type === "running"
    ? "Verification in progress"
    : state.type === "completed"
    ? state.value.report.passed
      ? "Verification passed"
      : "Verification did not pass"
    : state.value.kind === "interrupted"
    ? "Verification interrupted"
    : "Verification could not finish";

  return (
    <Card id="surla-cp4-verification" aria-labelledby="surla-cp4-title">
      <h2 id="surla-cp4-title">{title}</h2>
      {displayedCardRevision !== null && displayedCardRevision !== run.request_revision ? (
        <Alert variant="warning" role="status" id="surla-cp4-different-revision">
          This result applies to a different saved Change Card revision
          than the one shown here.
        </Alert>
      ) : null}
      {state.type === "running" ? (
        <p>
          Building and evaluating the saved candidate. You can leave this view
          and check the result later.
        </p>
      ) : state.type === "completed" ? (
        <CompletedReport report={state.value.report} />
      ) : (
        <>
          <p>{state.value.summary}</p>
          <p>
            <strong>Stopped during: </strong>{failureStageLabels[state.value.stage]}.
          </p>
        </>
      )}

      {requestError !== null ? (
        <Alert variant="destructive" role="alert">
          <strong>Could not check verification status. </strong>
          {requestError}
          <p>The last received result is still shown.</p>
        </Alert>
      ) : null}
      {state.type === "running" || requestError !== null ? (
        <Button
          id="surla-cp4-check-status"
          variant="primary"
          disabled={checking}
          onClick={onCheckStatus}
        >
          {checking ? "Checking status..." : "Check status"}
        </Button>
      ) : null}

      <details className="surla-details" id="surla-cp4-details">
        <summary>Candidate and run details</summary>
        <dl className="surla-verification-details">
          <dt>Candidate commit</dt><dd><code>{run.candidate_commit}</code></dd>
          <dt>Change Card revision</dt><dd><code>{run.request_revision}</code></dd>
          <dt>Evaluator commit</dt><dd><code>{run.suite_commit}</code></dd>
          <dt>Verification run</dt><dd><code>{run.run_id}</code></dd>
          <dt>Change Card</dt><dd><code>{run.change_id}</code></dd>
          {state.type === "running" ? (
            <>
              <dt>Started</dt>
              <dd><time dateTime={state.value.started_at}>{state.value.started_at}</time></dd>
            </>
          ) : state.type === "completed" ? (
            <>
              <dt>Evaluator suite</dt><dd><code>{state.value.suite_id}</code></dd>
              <dt>Environment</dt><dd>{state.value.environment}</dd>
              <dt>Report digest</dt><dd><code>{state.value.report.report_digest}</code></dd>
            </>
          ) : null}
        </dl>
      </details>
    </Card>
  );
}

// CompletedReport shows the entire declared denominator and every builder-safe
// outcome. It never derives a verdict from counts or missing diagnostics.
function CompletedReport({ report }: { report: BuilderVerificationReport }) {
  const counts = report.counts;
  return (
    <>
      <p>{counts.passed} of {counts.required} required scenarios passed.</p>
      {report.suite_failed ? (
        <Alert variant="destructive" role="status">
          The evaluation failed or ended unexpectedly.
          Individual scenario outcomes are shown below.
        </Alert>
      ) : null}
      <dl className="surla-verification-counts" aria-label="Scenario outcome counts">
        <div><dt>Required</dt><dd>{counts.required}</dd></div>
        <div><dt>Passed</dt><dd>{counts.passed}</dd></div>
        <div><dt>Failed</dt><dd>{counts.failed}</dd></div>
        <div><dt>Not run</dt><dd>{counts.not_run}</dd></div>
        <div><dt>Could not evaluate</dt><dd>{counts.harness_error}</dd></div>
      </dl>
      <h3>Declared scenarios</h3>
      <p>Unseen audit scenarios show only an anonymous outcome.</p>
      <ul className="surla-verification-scenarios">
        {report.scenarios.map((scenario) => (
          <li key={scenario.id}>
            {scenario.checks?.length || scenario.error ? (
              <details>
                <summary><ScenarioOutcome scenario={scenario} /></summary>
                {scenario.error ? <p>{scenario.error}</p> : null}
                {scenario.checks?.length ? (
                  <ul>
                    {scenario.checks.map((check, index) => (
                      <li key={index}>
                        <strong>{check.name}</strong>{" "}
                        <Badge variant={check.passed ? "default" : "destructive"}>
                          {check.passed ? "Passed" : "Failed"}
                        </Badge>
                        {check.diagnostic ? <p>{check.diagnostic}</p> : null}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </details>
            ) : <ScenarioOutcome scenario={scenario} />}
          </li>
        ))}
      </ul>
      <h3>Not tested</h3>
      <p>
        Behavior outside the suite&apos;s declared scenarios. Adding an example
        to the Change Card does not add it to this report.
      </p>
    </>
  );
}

// ScenarioOutcome keeps the evaluator's scenario name and outcome together
// whether its check details were provided or deliberately withheld.
function ScenarioOutcome({ scenario }: { scenario: VerificationScenario }) {
  return (
    <>
      <span>{scenario.id}</span>{" "}
      <Badge variant={scenario.outcome === "passed" ? "default" : "destructive"}>
        {scenarioOutcomeLabels[scenario.outcome]}
      </Badge>
    </>
  );
}
