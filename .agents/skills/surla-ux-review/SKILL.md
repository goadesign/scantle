---
name: surla-ux-review
description: Run a moderated usability study of Surla. One subagent plays a first-time developer who must build a real product with Surla alone. A second subagent moderates as a UX tester and writes a UX research report with severity-rated findings. Use this whenever someone asks for a UX review, usability test, user test, onboarding or first-run review, or dogfooding of Surla, or wants to know where users get stuck. Not for reviewing UI source code.
---

# Surla UX review

This skill runs a moderated, think-aloud usability test of Surla and turns it
into a UX research report. Two subagents take part:

- **The participant** plays a developer who has never used Surla. It may read
  only Surla's `README.md` and whatever Surla itself prints or generates, and
  it must reach the goal using Surla alone.
- **The UX tester** moderates. It may read Surla's user documentation but never
  Surla's source code. It sets the challenge, watches each step, asks
  questions, gives hints when the participant is stuck, records bugs and
  usability problems, and writes the report.

You, the agent running this skill, are the session facilitator. You set up the
run, pass messages between the two subagents, and fix blocking bugs. You can
read Surla's code, so pass their messages on word for word, and add only the
facts this skill lists. A facilitator who explains or coaches ends up
measuring itself, not Surla. When you are tempted to explain something, write
it in your own notes for the report's limitations section instead.

The default task is to build an agentic platform that lets end users buy and
sell houses. The person invoking the skill may give a different task or
participant profile. If they do, use theirs.

## 1. Set up the run

Start this session from outside the Surla checkout, for example from the run
folders below. Coding-agent harnesses may load the working directory's
`AGENTS.md` into every subagent, and Surla's `AGENTS.md` describes Surla's
internals.

Create two separate folders, so neither subagent finds the other's material
by browsing nearby. With `<id>` as a timestamp such as `20261101-0900`:

```text
~/src/surla-ux-<id>-participant/
  surla/       # git clone of github.com/goadesign/surla at the commit under test
  work/        # empty; the participant builds its product here
~/src/surla-ux-<id>-tester/
  docs/        # copies of README.md, PROPOSAL.md, PRESS_RELEASE.md, docs/
               # (with img/), and examples/*/README.md from that commit
  observations.md   # the tester's notes, which it writes
  attempt-1/transcript.md   # you append every relayed message here
  report.md    # the final report
```

The participant gets a plain clone, as if it had followed the GitHub link. It
may run commands from the clone, as the README does, but may read only
`README.md` in it. Do not install or prebuild Surla for it: working out how to
get Surla running is part of the experience being tested.

Before starting the subagents, record these facts at the top of
`transcript.md`:

- the Surla commit, the date, and the OS;
- the versions of `go`, `goa`, and `temporal`, or that they are missing;
- which credentials are set, such as `ANTHROPIC_API_KEY`. Record that each is
  set, never its value;
- whether commands run inside an agent sandbox and whether they have network
  access. `surla init` downloads modules, and Surla's verification uses macOS
  `sandbox-exec`, which usually cannot run inside another sandbox. If these
  are restricted, either lift the restriction for the participant or mark the
  affected milestones as not supported;
- **how the participant reaches Surla's MCP server.** The README tells the
  user to point a coding agent at `http://<addr>/mcp`. A subagent cannot add
  an MCP server to its own session. Choose the route that matches the docs
  most closely, in this order:
  1. The participant runs its own coding-agent CLI non-interactively in
     `work/` with an MCP configuration it writes, such as `claude -p`,
     `codex exec`, or `gemini -p`.
  2. The participant calls the HTTP API directly.

  Record which route it is and what it cannot reach.

The participant receives the machine facts a real developer would know: which
tools are installed, which keys are set, which coding-agent CLIs it may use.
It gets no instructions.

## 2. Start the two subagents

Read `references/tester-brief.md` and `references/participant-brief.md` and
fill in the placeholders. Start each as its own subagent. Your harness's
subagent tool must let you send follow-up messages to a subagent that is
still running, because the session runs over many turns. In Claude Code,
that is the Agent tool followed by SendMessage. If your harness cannot do
this, stop and say so. Do not fake the dialogue yourself.

Set the model explicitly to your harness's frontier reasoning model, such as
Opus in Claude Code or the high-reasoning model in Codex or Gemini CLI. Both
roles need judgment, and a weaker model turns its own confusion into false
usability findings.

Start the tester first. It writes its private success criteria to
`observations.md` and replies with the challenge text for the participant.
Then start the participant, with that challenge pasted into its brief.

## 3. Run the session loop

Each round trip works like this:

1. The participant works until a natural pause. It might have finished a
   milestone, be blocked, or be choosing between ways to use Surla. It also
   pauses before the first run of each new `surla` command, or after about
   ten actions. It then replies with a step report, in the format its brief
   defines.
2. Append the report to the transcript and send it unchanged to the tester,
   together with the wall-clock time since the previous report.
3. The tester replies with up to three questions, an optional hint, and a
   decision: `continue`, `stop_for_bug`, or `end_session`.
4. Append the reply to the transcript. On `continue`, send the participant
   only the questions and the hint, word for word.

A level-2 hint names a doc section, and the participant cannot open other
docs. Copy that one document into `work/` and say where it is. Record the
copy as part of the hint.

Watch for scope leaks. The participant's report is its own account, so
whenever your harness keeps a subagent transcript, check its actual tool
calls as well. A scope leak is reading outside the README, `work/`, and
Surla's own output: Surla source files, `AGENTS.md`, or the module cache.
Building the product without Surla is one too. If you find a leak, tell the
tester. It records the leak as a finding, because reaching for the source
usually means the docs failed, and it reminds the participant of the rules.
If your harness keeps no transcripts, note in the limitations that leaks
were self-reported.

The participant reports the PIDs and ports of any long-running processes it
starts, such as `surla serve` or a Temporal dev server. Stop those processes
when an attempt ends.

## 4. Handle a blocking bug

When the tester decides `stop_for_bug`, it names the bug and gives the steps
to reproduce it. A blocking bug is one where Surla crashes, or behaves
differently from its documentation, and no documented workaround exists. The
participant cannot get past it however well it uses Surla.

1. Reproduce the bug yourself, outside any agent sandbox and with network
   access, so a harness restriction is not mistaken for a Surla bug.
   - If the failure comes from the harness, tell the tester "environment
     limitation, not a Surla bug" and how it affects the milestones.
   - If Surla behaves as designed, tell the tester "reproduced; behavior is as
     designed", with no code details. The tester records a usability finding,
     may give a hint, and the session continues.
2. If it is a real bug, fix it following the repository's `AGENTS.md`: a
   separate clone and branch, the design gate, tests, and an independent
   review. Fix the cause, not the symptom the participant happened to hit.
   Push the branch, because a scaffolded project downloads Surla by its
   commit.
3. Restart from scratch:
   1. Stop the participant's processes.
   2. Replace `participant/surla` with a clone at the pushed fix commit.
   3. Clear `work/` and start `attempt-<N+1>/transcript.md`, recording the
      new commit.
   4. Start a **fresh** participant. One that remembers the failed attempt
      would skip the steps that confused it.
   5. Keep the same tester, and tell it that a new attempt has begun on that
      commit. Its earlier findings carry into the report.

If a third blocking bug appears, stop and report instead of restarting
again. With that many blockers, the report and the fixes are worth more than
another attempt.

## 5. Debrief and report

When the tester decides `end_session`, it sends the participant debrief
questions: how easy it was overall, the hardest moment, and what it would
change. Relay the questions and the answers. Then send the tester the text of
`references/report-template.md` and ask it to write `report.md` from its
observations.

Before handing the report over, check it against the transcripts:

- Every finding points to a step that happened.
- Quotes are exact.
- The counts of hints, errors, and milestones match the transcripts.

Add each bug fixed during the study, with its PR link. Correct mismatches
with the tester rather than rewriting its conclusions yourself. Then give the
person who invoked the skill:

- the path to `report.md`;
- the top three findings, in plain words;
- the bugs fixed, with PR links;
- what could not be tested on this machine.
