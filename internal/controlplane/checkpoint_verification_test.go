// Package controlplane tests the CP4 viewer with generated MCP wire results
// and the same parent-frame request protocol used by a real MCP Apps host.
package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genmcpadapter "github.com/goadesign/surla/gen/mcp_surla"
	gensurla "github.com/goadesign/surla/gen/surla"
	gensurlatools "github.com/goadesign/surla/gen/surla/toolsets/surla"
)

// checkpointVerificationService supplies a fixed typed result through the
// generated adapter, so fixtures exercise its real union wire encoding.
type checkpointVerificationService struct {
	// ControlPlane supplies the unrelated service methods used by the adapter.
	*ControlPlane
	// result is the exact verification snapshot this fixture returns.
	result *gensurla.VerificationStatusResult
}

// checkpointVerificationHost drives isolated guests through status reads,
// errors, concurrent card saves, resource changes and final teardown.
const checkpointVerificationHost = `
const fs = require("fs");
const {JSDOM} = require("../../ui/checkpoint/node_modules/jsdom");
const input = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const turn = () => new Promise((resolve) => setImmediate(resolve));
function expect(value, message) { if (!value) throw new Error(message); }
function open() {
  const dom = new JSDOM(input.html, {url:"https://checkpoint.example.invalid",runScripts:"outside-only",pretendToBeVisual:true});
  const win = dom.window, doc = win.document, calls = [];
  let listener;
  const parent = {postMessage(message) { if (message.method === "tools/call") calls.push(message); }};
  Object.defineProperty(win,"parent",{value:parent,configurable:true});
  const add = win.addEventListener.bind(win);
  win.addEventListener = (type, callback, options) => { if (type === "message") listener = callback; add(type,callback,options); };
  win.eval(input.script);
  const send = (data) => listener({source:parent,data});
  send({jsonrpc:"2.0",id:1,result:{hostContext:{theme:"light"}}});
  const notify = (payload) => send({jsonrpc:"2.0",method:"ui/notifications/tool-result",params:{structuredContent:payload}});
  const reply = (request, payload) => send({jsonrpc:"2.0",id:request.id,result:{structuredContent:payload}});
  const text = () => doc.querySelector("#surla-result-view").textContent;
  const field = () => doc.querySelector('textarea[data-field="expected_outcome"]');
  const button = () => doc.querySelector("#surla-cp4-check-status");
  const edit = (value) => { field().value=value; field().dispatchEvent(new win.Event("input",{bubbles:true})); };
  notify(input.approved);
  return {dom,win,doc,calls,send,notify,reply,text,field,button,edit};
}
function readRequest(request, run = input.running) {
  expect(request.params.name === "get_verification", "status action started another tool");
  expect(JSON.stringify(request.params.arguments) === JSON.stringify({run_id:run.run_id}), "status read repeated or invented run context");
}
async function main() {
  // Refreshing a run preserves the exact editable draft and produces no poller.
  {
    const h=open(); await turn(); h.edit("Unsaved CP4 draft"); h.notify(input.running);
    expect(h.doc.querySelector("#surla-cp4-title").textContent === "Verification in progress", "running state is missing");
    expect(h.calls.length===0, "showing a run dispatched an automatic request");
    h.button().click(); readRequest(h.calls[0]); expect(h.button().disabled, "status request was not disabled");
    expect(!h.field().disabled, "status read disabled independent CP1 editing");
    h.edit("Edited while checking status"); h.reply(h.calls[0],input.passed); await turn();
    expect(h.text().includes("Verification passed") && !h.button(), "completed report is not final");
    expect(h.text().includes("2 of 2 required scenarios passed") && h.text().includes("holdout-1"), "report denominator or anonymous outcome is missing");
    expect(h.text().includes("Not tested") && h.text().includes(input.passed.candidate_commit), "coverage or verified inputs are missing");
    expect(h.field().value === "Edited while checking status", "status reply erased a draft");
    expect(h.calls.length===1, "completed report started another request"); h.dom.window.close();
  }
  // All report outcomes remain visible; failure is not inferred from missing detail.
  {
    const h=open(); await turn(); h.notify(input.failed);
    expect(h.text().includes("Verification did not pass") && h.text().includes("Not run") && h.text().includes("Could not evaluate"), "failed report collapsed distinct outcomes");
    expect(h.text().includes("Exact failing diagnostic <img src=x onerror=alert(1)>"), "repair diagnostic was removed");
    expect(!h.doc.querySelector("#surla-cp4-verification img"), "diagnostic became executable HTML");
    expect(h.text().includes("3 of 6 required scenarios passed"), "failed report denominator changed"); h.dom.window.close();
  }
  // Suite failure remains a failed verdict even when every scenario passed.
  {
    const h=open(); await turn(); h.notify(input.suiteFailed);
    expect(h.text().includes("Verification did not pass") && h.text().includes("evaluation failed or ended unexpectedly"), "suite failure was converted to a passing count verdict"); h.dom.window.close();
  }
  // Execution failures and interrupted runs never masquerade as scenario reports.
  for (const [name,title] of [["executionFailed","Verification could not finish"],["interrupted","Verification interrupted"]]) {
    const h=open(); await turn(); h.notify(input[name]);
    expect(h.text().includes(title) && h.text().includes("Stopped during:"), "incomplete outcome is inaccurate");
    expect(!h.text().includes("Declared scenarios") && !h.button(), "no-report outcome offered a completed report or restarted work"); h.dom.window.close();
  }
  // A failed read retains its run and draft, and retries the same exact resource.
  {
    const h=open(); await turn(); h.edit("Draft after read error"); h.notify(input.running); h.button().click();
    h.send({jsonrpc:"2.0",method:"ui/notifications/tool-result",params:{isError:true,content:[{type:"text",text:"An unrelated preview action failed"}]}});
    expect(h.text().includes("Verification in progress") && h.button().disabled, "uncorrelated error replaced the run or completed its request");
    expect(h.doc.querySelector("#surla-checkpoint-status").textContent.includes("An unrelated preview action failed"), "uncorrelated error was hidden");
    h.send({jsonrpc:"2.0",id:h.calls[0].id,error:{code:-32000,message:"Verification service unavailable"}}); await turn();
    expect(h.text().includes("Verification service unavailable") && h.text().includes("Verification in progress"), "read failure replaced the last run");
    expect(h.field().value === "Draft after read error" && !h.button().disabled, "read failure lost draft or retry");
    h.button().click(); readRequest(h.calls[1]); h.reply(h.calls[1],input.passed); await turn();
    expect(!h.text().includes("Verification service unavailable") && h.text().includes("Verification passed"), "successful retry retained old feedback"); h.dom.window.close();
  }
  // Uncorrelated host failures do not erase a run or become its read error.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.send({jsonrpc:"2.0",method:"ui/notifications/tool-result",params:{isError:true,content:[{type:"text",text:"Another action failed"}]}});
    h.reply(h.calls[0],input.passed); await turn();
    expect(h.text().includes("Verification passed") && !h.button(), "broadcast error prevented a valid correlated completion");
    h.send({jsonrpc:"2.0",method:"ui/notifications/tool-result",params:{isError:true,content:[{type:"text",text:"An unrelated action failed after completion"}]}});
    expect(h.text().includes("Verification passed") && h.doc.querySelector("#surla-checkpoint-status").textContent.includes("after completion"), "unrelated error erased a finished run or was hidden"); h.dom.window.close();
  }
  // Input fragments and cancellation notices cannot complete a pending local read.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.send({jsonrpc:"2.0",method:"ui/notifications/tool-input-partial",params:{arguments:{run_id:input.running.run_id}}});
    h.send({jsonrpc:"2.0",method:"ui/notifications/tool-input",params:{arguments:{run_id:input.running.run_id}}});
    h.send({jsonrpc:"2.0",method:"ui/notifications/tool-cancelled",params:{reason:"Another tool call stopped"}});
    expect(h.text().includes("Verification in progress") && h.button().disabled, "uncorrelated lifecycle notice erased or completed a pending status read");
    h.reply(h.calls[0],input.passed); await turn();
    expect(h.text().includes("Verification passed") && !h.button(), "lifecycle notice prevented a correlated completion"); h.dom.window.close();
  }
  // A response is correlated to its resource, not whichever run is now displayed.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.notify(input.otherRun); h.button().click(); readRequest(h.calls[1],input.otherRun);
    h.reply(h.calls[0],input.passed); await turn();
    expect(h.doc.querySelector("#surla-result-view").dataset.runId===input.otherRun.run_id && h.button().disabled, "late run A response overwrote run B or cleared B's request");
    h.reply(h.calls[1],input.otherRun); await turn(); expect(!h.button().disabled, "run B remained disabled after its own reply"); h.dom.window.close();
  }
  // Status reads and card saves are independent and do not relabel earlier reports.
  {
    const h=open(); await turn(); h.edit("Saved edited outcome"); h.notify(input.running);
    h.doc.querySelector("#surla-confirm-submit").click(); h.button().click();
    expect(h.calls.length===2 && h.calls[0].params.name === "confirm_intent", "independent status read was blocked behind the save");
    h.reply(h.calls[1],input.passed); await turn(); expect(h.field().disabled, "status reply unlocked an unfinished card save");
    h.reply(h.calls[0],input.saved); await turn();
    expect(!h.field().disabled && h.field().value === "Saved edited outcome", "card response did not finish independently");
    expect(h.doc.querySelector("#surla-cp4-different-revision") && h.text().includes(input.running.request_revision), "report for another revision looked current after the card changed");
    h.edit("Discard this local draft"); h.doc.querySelector("#surla-discard-draft").click();
    expect(!h.field() && !h.doc.querySelector("#surla-cp4-different-revision"), "closed editor left a warning about a displayed revision"); h.dom.window.close();
  }
  // Revision inequality establishes identity, not which snapshot is newer.
  {
    const h=open(); await turn(); h.edit("Retain older editor");
    h.notify({...input.passed,request_revision:input.saved.request_revision});
    const warning=h.doc.querySelector("#surla-cp4-different-revision");
    expect(warning?.textContent.includes("different saved Change Card revision") && !warning.textContent.includes("earlier"), "revision hashes were used to invent chronology");
    expect(h.field().value === "Retain older editor", "report for a different revision erased the draft"); h.dom.window.close();
  }
  // A finished run stays finished across both stale replies and notifications.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.notify(input.passed); h.reply(h.calls[0],input.running); await turn();
    expect(h.text().includes("Verification passed") && !h.button(), "older read reverted a completed run to running");
    h.notify(input.running); expect(h.text().includes("Verification passed") && !h.button(), "older notification reverted a completed run"); h.dom.window.close();
  }
  // A running notification does not invalidate a later completed read response.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.notify(input.running); h.reply(h.calls[0],input.passed); await turn();
    expect(h.text().includes("Verification passed") && !h.button(), "another running snapshot caused a valid completion to be discarded"); h.dom.window.close();
  }
  // Another checkpoint remains displayed when an older status reply arrives.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.notify({has_changes:false,added_declarations:[],removed_declarations:[],modified_declarations:[],version_deltas:[],summary:"No schema changes"});
    h.reply(h.calls[0],input.passed); await turn();
    expect(h.doc.querySelector("#surla-result-view").dataset.state === "cp2-diff", "late status response replaced a different checkpoint"); h.dom.window.close();
  }
  // Malformed closed unions and timestamps fail at the host response boundary.
  for (const started_at of ["2028-02-29T23:59:59+05:30", "2026-10-31T12:34:56.123456789-07:00"]) {
    const h=open(); await turn(); h.notify({...input.running,state:{type:"running",value:{started_at}}});
    expect(h.text().includes("Verification in progress") && h.text().includes(started_at), "valid RFC3339 timestamp was rejected"); h.dom.window.close();
  }
  const invalid = [
    {...input.running,state:undefined},
    {...input.running,state:{type:"completed",value:{suite_id:"suite",environment:"sim"}}},
    {...input.running,state:{type:"running",value:input.passed.state.value}},
    {...input.running,state:{type:"running",value:{started_at:"2026-02-30T12:00:00Z"}}},
    {...input.running,state:{type:"running",value:{started_at:"2026-10-31T12:00Z"}}},
    {...input.running,state:{...input.running.state,private_evidence:"Hidden evaluator content"}},
    {...input.running,state:{type:"not_completed",value:{kind:"execution_failed",stage:"unknown",summary:"Unknown failure"}}},
  ];
  for (const payload of invalid) {
    const h=open(); await turn(); h.notify(input.running); h.button().click(); h.reply(h.calls[0],payload); await turn();
    expect(h.text().includes("Could not check verification status") && h.text().includes("Verification in progress"), "invalid union replaced the valid run");
    expect(!h.text().includes("Hidden evaluator content"), "unknown evidence escaped the schema boundary"); h.dom.window.close();
  }
  // A valid shape for the wrong resource also leaves the original run intact.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click(); h.reply(h.calls[0],input.otherRun); await turn();
    expect(h.text().includes("different verification run") && h.doc.querySelector("#surla-result-view").dataset.runId === input.running.run_id, "wrong run was accepted for a correlated read"); h.dom.window.close();
  }
  // Teardown ends local waiting without canceling the server or reviving the view.
  {
    const h=open(); await turn(); h.notify(input.running); h.button().click();
    h.send({jsonrpc:"2.0",id:"close",method:"ui/resource-teardown",params:{}}); h.reply(h.calls[0],input.passed); await turn();
    expect(h.doc.querySelector("#surla-checkpoint-root").dataset.state === "terminated" && !h.doc.querySelector("#surla-cp4-verification"), "late reply revived a closed viewer");
    expect(h.calls.length===1, "teardown sent another server operation"); h.dom.window.close();
  }
  process.stdout.write(JSON.stringify({passed:true}));
}
main().catch((error) => { console.error(error); process.exitCode=1; });
`

// TestCheckpointUIVerificationStatesAndStatusReads checks the generated closed
// union, report visibility, request correlation and independent CP1 ownership.
func TestCheckpointUIVerificationStatesAndStatusReads(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to exercise the checkpoint viewer")
	}
	ensureCheckpointUIDeps(t)
	approved, saved := approvalRefreshCards(t)
	card, err := gensurlatools.UnmarshalGetChangeResult(approved)
	require.NoError(t, err)
	base := gensurla.VerificationStatusResult{
		RunID: "11111111-1111-4111-8111-111111111111", ChangeID: card.ChangeID,
		RequestRevision: card.RequestRevision, CandidateCommit: strings.Repeat("1", 40),
		SuiteCommit: strings.Repeat("2", 40),
	}
	passed := &gensurla.BuilderVerificationReport{
		ReportDigest: strings.Repeat("3", 64), Passed: true,
		Counts: &gensurla.VerificationCounts{Required: 2, Passed: 2},
		Scenarios: []*gensurla.VerificationScenario{
			{ID: "visible_refund", Outcome: "passed", Checks: []*gensurla.VerificationCheck{{Name: "one_refund", Passed: true}}},
			{ID: "holdout-1", Outcome: "passed"},
		},
	}
	diagnostic := "Exact failing diagnostic <img src=x onerror=alert(1)>"
	environmentError := "Test dependency was unavailable"
	failed := &gensurla.BuilderVerificationReport{
		ReportDigest: strings.Repeat("4", 64), SuiteFailed: true,
		Counts: &gensurla.VerificationCounts{Required: 6, Passed: 3, Failed: 1, NotRun: 1, HarnessError: 1},
		Scenarios: []*gensurla.VerificationScenario{
			{ID: "visible_refund", Outcome: "failed", Checks: []*gensurla.VerificationCheck{{Name: "one_refund", Diagnostic: &diagnostic}}},
			{ID: "not_executed", Outcome: "not_run"},
			{ID: "dependency_failed", Outcome: "harness_error", Error: &environmentError},
			{ID: "visible_pass", Outcome: "passed"},
			{ID: "holdout-1", Outcome: "passed"},
			{ID: "holdout-2", Outcome: "passed"},
		},
	}
	suiteFailed := *passed
	suiteFailed.Passed, suiteFailed.SuiteFailed = false, true
	suiteFailed.ReportDigest = strings.Repeat("5", 64)
	states := map[string]gensurla.State{
		"running":         gensurla.NewStateRunning(&gensurla.VerificationRunning{StartedAt: "2026-10-31T12:34:56.123Z"}),
		"passed":          gensurla.NewStateCompleted(&gensurla.VerificationCompleted{SuiteID: "orders_acceptance", Environment: "sim", Report: passed}),
		"failed":          gensurla.NewStateCompleted(&gensurla.VerificationCompleted{SuiteID: "orders_acceptance", Environment: "sim", Report: failed}),
		"suiteFailed":     gensurla.NewStateCompleted(&gensurla.VerificationCompleted{SuiteID: "orders_acceptance", Environment: "sim", Report: &suiteFailed}),
		"executionFailed": gensurla.NewStateNotCompleted(&gensurla.VerificationNotCompleted{Kind: "execution_failed", Stage: "build", Summary: "The candidate could not be built."}),
		"interrupted":     gensurla.NewStateNotCompleted(&gensurla.VerificationNotCompleted{Kind: "interrupted", Stage: "supervise", Summary: "Verification stopped before a report was saved."}),
	}
	input := map[string]any{
		"html": checkpointUIHTML, "script": extractCheckpointScript(t, checkpointUIHTML),
		"approved": approved, "saved": saved,
	}
	for name, state := range states {
		result := base
		result.State = state
		input[name] = checkpointVerificationWire(t, &result)
	}
	other := base
	other.RunID = "22222222-2222-4222-8222-222222222222"
	other.State = states["running"]
	input["otherRun"] = checkpointVerificationWire(t, &other)
	fixture, err := json.Marshal(input)
	require.NoError(t, err)
	fixturePath := filepath.Join(t.TempDir(), "verification-host.json")
	require.NoError(t, os.WriteFile(fixturePath, fixture, 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", checkpointVerificationHost, fixturePath)
	cmd.Env = append(os.Environ(), "CI=true")
	// The host reads a finite fixture file; nil stdin is /dev/null.
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "CP4 viewer failed:\n%s", out)
	require.JSONEq(t, `{"passed":true}`, string(out))
}

// GetVerification returns the typed snapshot the generated adapter serializes.
func (s *checkpointVerificationService) GetVerification(
	_ context.Context, _ *gensurla.GetVerificationPayload,
) (*gensurla.VerificationStatusResult, error) {
	return s.result, nil
}

// checkpointVerificationWire encodes the result through the actual MCP adapter,
// preserving Goa's tagged-union representation rather than hand-authoring it.
func checkpointVerificationWire(t *testing.T, result *gensurla.VerificationStatusResult) json.RawMessage {
	t.Helper()
	svc := &checkpointVerificationService{ControlPlane: newControlPlane(t, Options{}), result: result}
	adapter := genmcpadapter.NewMCPAdapter(gensurla.NewEndpoints(svc), nil)
	args, err := json.Marshal(map[string]string{"run_id": result.RunID})
	require.NoError(t, err)
	wire, err := adapter.ToolsCall(t.Context(), &genmcpadapter.ToolsCallPayload{Name: "get_verification", Arguments: args})
	require.NoError(t, err)
	complete, ok := wire.Outcome.AsComplete()
	require.True(t, ok)
	return json.RawMessage(complete.StructuredContent)
}
