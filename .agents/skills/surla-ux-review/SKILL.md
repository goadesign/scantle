---
name: surla-ux-review
description: Run a moderated usability study of Surla in which one subagent plays a first-time user who must build a real product with Surla alone, and a second subagent plays the UX tester who watches every step, asks think-aloud questions, gives hints only when the user is stuck, and writes a UX research report with severity-rated findings. Use this skill whenever someone asks for a UX review, usability test, user test, first-run or onboarding review, "dogfooding", or wants to know how easy Surla is to learn or where users get stuck — even if they do not say "usability study". Do not use it to review the checkpoint viewer's React code or for a code review; it judges the experience, not the source.
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

You (the agent running this skill) are the session facilitator. You set up
the run, pass messages between the two subagents, and fix blocking bugs. You
can read Surla's code, so you must never let what you know reach either
subagent: forward their messages word for word and add nothing of your own.
A study where the facilitator quietly coaches the participant measures the
facilitator, not Surla.

The default task is: build an agentic platform that lets end users buy and
sell houses. The person invoking the skill may supply a different task or
participant profile; use theirs if given.

## 1. Set up the run

Create a run directory outside the Surla checkout so neither subagent finds
the source by browsing nearby files, for example
`~/src/surla-ux-runs/<YYYYMMDD-HHMM>/`. Build it like this, from an up-to-date
checkout of `origin/main` (or from the fix branch on a restart, see section 4):

```text
<run>/
  participant/
    README.md        # copy of the repo README, the only Surla doc it gets
    bin/surla        # go build -o <run>/participant/bin/surla ./cmd/surla
    work/            # empty; the participant's project lives here
  tester/
    docs/            # copies of README.md, docs/ (with img/), examples/*/README.md
    observations.md  # the tester's running notes (it writes this)
  session/
    attempt-1/transcript.md   # you append every relayed message here
  report.md          # the final report (written in section 5)
```

Record in `session/attempt-1/transcript.md` the Surla commit you built, the
date, the OS, and which prerequisites exist on this machine (`go version`,
`goa version`, `temporal --version`, and whether credentials such as
`ANTHROPIC_API_KEY` are set; record presence only, never values). The
participant gets the same facts a real developer would know about their own
machine: which tools are installed and which keys are set. It does not get
instructions.

## 2. Start the two subagents

Read `references/tester-brief.md` and `references/participant-brief.md` and
fill in the placeholders. Start each as a separate subagent with your
harness's subagent tool. The tool must let you send follow-up messages to an
agent that is still running, because the session is a conversation of many
turns. In Claude Code, that is the Agent tool, then SendMessage. Set the
model explicitly to your harness's frontier reasoning model, such as Opus in
Claude Code or the high-reasoning model in Codex or Gemini CLI. Both roles
need judgment, and a weaker model turns its own confusion into false
usability findings. If your harness cannot keep a subagent running across
turns, stop and say so rather than faking the dialogue yourself.

Start the tester first. Its first reply contains the success criteria (kept
private in `observations.md`) and the challenge text for the participant.
Then spawn the participant with that challenge text inserted into its brief.

## 3. Run the session loop

Each round trip works like this:

1. The participant works until it reaches a natural pause: a milestone is
   done, it is blocked, it is choosing between ways to use Surla, or about ten
   actions have passed. It then replies with a step report (format in its
   brief).
2. Append the report to the transcript and send it unchanged to the tester.
   Include the wall-clock time since the previous report so
   the tester can record time on task.
3. The tester replies with up to three questions, an optional hint, and a
   decision: `continue`, `stop_for_bug`, or `end_session`.
4. Append the reply to the transcript. On `continue`, send the participant
   only the questions and hint, word for word. The tester's notes and
   reasoning stay with the tester.

Keep going until the tester decides `end_session` or `stop_for_bug`. If the
participant starts long-running processes (such as `surla serve` or a
Temporal dev server), it reports their PIDs and ports. You stop those
processes when the attempt ends so the next attempt starts clean.

Two failure modes are worth watching for while relaying:

- **Scope leaks.** If the participant reads anything outside its README, its
  own `work/` directory, and Surla's own output (for example the Surla
  checkout or the module cache under `$(go env GOMODCACHE)`), or builds the
  product without Surla, tell the tester. The tester records it as a finding,
  because reaching for the source usually means the docs failed, and reminds
  the participant of the rules.
- **Facilitator bias.** If you catch yourself wanting to explain something to
  either agent, don't. Write it in your own notes for the report's
  limitations section instead.

## 4. Handle a blocking bug

When the tester decides `stop_for_bug`, it names the bug and how to
reproduce it. A blocking bug is Surla behaving differently from its
documentation, or crashing, with no documented workaround, so the
participant cannot make progress however well it uses Surla.

1. Reproduce the bug yourself from the tester's steps. If Surla behaves as
   designed, the problem is with the UX, not the code. Tell the tester
   "reproduced; behavior is as designed", with no code details. The tester
   then records a usability finding and may give a hint, and the session
   continues.
2. If it is a real bug, fix it following the repository's `AGENTS.md`: a
   separate clone and branch, the design gate, tests, and an independent
   review before the fix is published. Fix the cause, not the symptom the
   participant happened to hit.
3. Restart from scratch. Stop the participant's processes, then rebuild
   `participant/bin/surla` from the fix branch. Clear `participant/work/`,
   start `session/attempt-<N+1>/`, and spawn a **fresh** participant. A
   participant who remembers the failed attempt would skip the very steps
   that confused it. Keep the same tester (send it a message that a new
   attempt has begun and with which build), so its findings from earlier
   attempts carry into the report.

If a third blocking bug appears, stop and report. That many blockers in one
study means the report and the fixes are worth more than another restart.

## 5. Debrief and report

When the tester decides `end_session`, it sends debrief questions to the
participant, such as overall ease, the hardest moment, and what it would
change. Relay them and relay the answers back. Then send the tester the text
of `references/report-template.md` and ask it to write `report.md` from its
observations.

Before handing the report over, check it against the transcript. Every
finding must point to a step that happened, quotes must be exact, and the
counts must match: hints, errors, milestones. Then add each bug fixed during
the study, with its PR link. Correct mismatches with the tester rather than
editing its conclusions yourself.

Finish by giving the person who invoked the skill:

- the path to `report.md`;
- the top three findings in plain words;
- the bugs fixed, with PR links;
- anything left unverified, such as a milestone the machine could not run.
