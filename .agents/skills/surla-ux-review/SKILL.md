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

Use the person's own task and participant profile when they give them.
Otherwise use the defaults:

- **Task:** build an agentic platform that lets end users buy and sell houses.
- **Participant profile:** a software developer who is comfortable with Go
  and the command line and uses coding agents daily, but has never used
  Surla, Goa, or Goa-AI.

## 1. Set up the run

**Where to start.** Run this skill from a folder with no Surla `AGENTS.md` in
it or above it. Some coding-agent harnesses load the working directory's
`AGENTS.md` into every subagent, and Surla's describes its internals. The skill lives in Surla's
`.agents/skills/surla-ux-review/`. To start it from elsewhere, link that
folder into your personal skills folder once:

- `~/.agents/skills/`, read by Codex and Gemini CLI;
- `~/.claude/skills/`, read by Claude Code.

**Folders.** With `<id>` as a timestamp such as `20261101-0900`, create the
participant's folder and, apart from it, the tester's folder:

```text
~/src/surla-ux-<id>/
  surla/       # git clone of github.com/goadesign/surla at the commit under test
  work/        # empty; the participant builds its product here
~/surla-ux-reports/<id>/
  docs/        # README.md, PROPOSAL.md, PRESS_RELEASE.md, docs/ (with img/),
               # and examples/*/README.md from that commit, copied at their
               # repository-relative paths so the READMEs do not overwrite
               # each other
  observations.md   # the tester's notes, which it writes
  attempt-1/transcript.md   # you append every relayed message here
  report.md    # the final report
```

The participant gets a plain clone, as if it had followed the GitHub link. Do
not install or prebuild Surla for it: working out how to get Surla running is
part of what this study tests. Its brief lists exactly what it may read. That
list, not the folder layout, is what keeps the tester's notes and Surla's
source out of its view.

**Facts to record.** Before starting the subagents, write these at the top of
`attempt-1/transcript.md`:

- the Surla commit, the date, and the OS;
- the `-surla-version` that the README tells `surla init` to use. A scaffolded
  project downloads that version, not the commit under test;
- the versions of `go`, `goa`, and `temporal`, or that they are missing;
- which credentials are set, such as `ANTHROPIC_API_KEY`. Record that each is
  set, never its value;
- any personal instruction files your harness loads into every subagent, such
  as a global `CLAUDE.md` or `AGENTS.md`. These can tell the participant
  things the README does not, so the report lists them as a limitation;
- whether commands run inside an agent sandbox and whether they have network
  access. `surla init` downloads modules, and Surla's verification uses macOS
  `sandbox-exec`, which usually cannot run inside another sandbox. Lift these
  restrictions for the participant where you can. The tester marks the
  milestones that the remaining restrictions block as not supported;
- **how the participant reaches Surla's MCP server.** The README tells a user
  to point a coding agent at `http://<addr>/mcp`. A subagent cannot add an MCP
  server to its own session. The participant therefore runs its own
  coding-agent CLI non-interactively in `work/`, with an MCP configuration it
  writes itself, for example `claude -p`, `codex exec`, or `gemini -p`.
  Record which CLIs it may use, or that none can run here. Do not hand it an
  undocumented HTTP route, because working that out from outside the README
  is coaching.

## 2. Start the two subagents

Read `references/tester-brief.md` and `references/participant-brief.md` and
fill in every placeholder:

| Placeholder | Value |
| --- | --- |
| `{{TASK}}` | the task |
| `{{PARTICIPANT_PROFILE}}` | the participant profile |
| `{{ENVIRONMENT_FACTS}}` | the recorded facts that matter to a user: installed tools, credentials set, network and sandbox limits |
| `{{CODING_AGENTS}}` | the coding-agent CLIs the participant may run, or "none" |
| `{{PARTICIPANT}}` | the participant's folder |
| `{{TESTER}}` | the tester's folder |
| `{{CHALLENGE}}` | the tester's challenge text, word for word |

Start each role as its own subagent, with the model set explicitly to your
harness's frontier reasoning model, such as Opus in Claude Code or the
high-reasoning model in Codex or Gemini CLI. Both roles need judgment, and a
weaker model turns its own confusion into false usability findings.

The session runs over many turns, so you need to send each subagent more
messages while it is still running. In Claude Code, start it with the Agent
tool and continue it with SendMessage. If your harness cannot continue a
running subagent, start a fresh one each round instead. Give it its brief,
plus every message it has sent and received so far. The tester also picks up
from `observations.md`. Note the fallback in the limitations, because a
participant restarted every round loses whatever it had not written down.

Start the tester first. It writes its private success criteria to
`observations.md` and replies with the challenge text. Then start the
participant.

## 3. Run the session loop

Each round trip works like this:

1. The participant works until a natural pause and replies with a step
   report. Its brief lists when it pauses, including before the first run of
   each new `surla` command.
2. Append the report to the transcript and send it unchanged to the tester,
   together with the elapsed time since the previous report.
3. The tester replies with up to three questions, an optional hint, and a
   decision: `continue`, `stop_for_bug`, or `end_session`.
4. Append the reply to the transcript. On `continue`, send the participant
   only the questions and the hint, word for word.

A level-2 hint names a doc section that the participant cannot otherwise
open. Copy that one document into `work/` and say where it is. Record the
copy as part of the hint.

**Check for scope leaks** after every few rounds. The participant's reports
are its own account, so compare them with its actual tool calls wherever your
harness keeps a subagent transcript. Also read the logs its coding agent saved
in `work/agent-logs/`. A leak is reading anything outside the participant's
allowed list, loading this skill, or building the product without Surla.

A participant cannot unlearn what it read, so a leak ends the attempt. Tell
the tester what was read and when. The tester records the leak as a finding,
because reaching for the source usually means the docs failed, and marks
later milestones in that attempt as not valid. Then restart, as in section
4 step 4, with the same build. If nothing can be checked, note in the
limitations that leaks were only self-reported.

Stop the long-running processes the participant reports, such as `surla
serve` or a Temporal dev server, when an attempt ends.

## 4. Handle a blocking bug

When the tester decides `stop_for_bug`, it names the bug and gives the steps
to reproduce it. A blocking bug is one where Surla crashes, or behaves
differently from its documentation, and no documented workaround exists. The
participant cannot get past it however well it uses Surla.

1. Reproduce the bug yourself, outside any agent sandbox and with network
   access, so a harness restriction is not mistaken for a Surla bug.
   - If the failure comes from the harness, tell the tester "environment
     limitation, not a Surla bug" and which milestones it affects.
   - If Surla behaves as designed, tell the tester "reproduced; behavior is as
     designed", with no code details. The tester records a usability finding,
     may give a hint, and the session continues.
2. If it is a real bug, fix it following the repository's `AGENTS.md`: a
   separate clone and branch, the design gate, tests, an independent review,
   and a pull request. Fix the cause, not the symptom the participant
   happened to hit.
3. If the fix is in code that a scaffolded project downloads, the participant
   would still get the old version through the README's `-surla-version`.
   Push the fix. Then add a follow-up commit that sets `-surla-version` to the
   fix commit's Go pseudo-version (`v0.0.0-<yyyymmddhhmmss>-<12-char-sha>`,
   as `go list -m github.com/goadesign/surla@<sha>` prints it). This is the
   same way main updates `examples/orders` after a release.
4. Restart from scratch:
   1. Stop the participant's processes.
   2. Replace `surla/` with a clone at the newest commit of the fix,
      including any `-surla-version` follow-up.
   3. Copy the user docs from that commit into the tester's `docs/` again, so
      the tester judges the participant against the docs it is reading.
   4. Clear `work/`, and remove any folder outside it that the participant
      reported creating for its project, such as `/tmp/shop` from the
      README's `surla init -dir` example. `surla init` refuses a folder that
      already exists. Then start `attempt-<N+1>/transcript.md` with the newly
      recorded facts.
   5. Start a **fresh** participant. One that remembers the failed attempt
      would skip the steps that confused it.
   6. Keep the same tester. Tell it that a new attempt has begun on that
      commit and which docs changed. Its earlier findings carry into the
      report.

Fix and restart after every blocking bug. If the person invoking the skill
set a limit on attempts, stop and report when it is reached.

## 5. Debrief and report

When the tester decides `end_session`, it sends the participant its debrief
questions. Relay the questions and the answers. Then send the tester the text
of `references/report-template.md` and ask it to write `report.md`.

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
