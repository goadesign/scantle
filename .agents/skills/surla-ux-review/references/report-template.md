# Report template

The tester writes `report.md` using this structure. Every finding cites the
observation and the step it came from, so a reader can check it against the
transcript. Write for a product team that did not watch the session: name the
command, output, or doc passage, and quote the participant exactly.

```markdown
# Surla usability study: <task in a few words>

<date> · Surla build <commit> · Moderator: AI UX tester · Participant: AI agent

## Summary

<3–5 sentences: whether the participant reached the goal, how far it got,
the most important problems, and what to fix first.>

## Study design

- **Research questions:** <the questions this study answers>
- **Method:** Moderated think-aloud usability test with one AI participant per
  attempt. The participant had only the README and Surla's own output; the
  moderator had the user documentation and no source code.
- **Participant profile:** <persona given to the participant>
- **Task:** <the challenge text, verbatim>
- **Environment:** <OS, tool versions, credentials present (not values),
  Surla commit for each attempt>
- **Success criteria:** <the milestones defined before the session>

## Results

| Milestone | Outcome | Time on task | Hints (level) | Errors | SEQ (1–7) |
| --- | --- | --- | --- | --- | --- |
| <milestone> | Completed / Completed with help / Failed / Not reached / Not supported on this machine | <mm:ss> | <n (levels)> | <n> | <score> |

<One paragraph on overall effectiveness, efficiency, and ease, with totals.>

## Findings

Ordered by severity, highest first. Severity uses Nielsen's scale: 0 not a
problem, 1 cosmetic, 2 minor, 3 major, 4 catastrophe (blocks the task).

### F1. <Problem as the user experiences it, in one line>

- **Severity:** <0–4> · **Type:** Bug / Usability issue / Documentation gap
- **Heuristic:** <violated heuristic, if one fits>
- **Where:** <step, command, output, or README section>
- **What happened:** <observed behavior with exact quotes and commands>
- **Expected:** <what the docs or the user's goal led them to expect>
- **Impact:** <time lost, wrong action taken, hint needed, task blocked>
- **Recommendation:** <a concrete change to the tool, output, or docs>
- **Evidence:** <observation numbers and transcript attempt/step>

## Bugs fixed during the study

| Bug | Blocked at | Fix (PR) | Retested in attempt |
| --- | --- | --- | --- |

## What worked well

<Positive observations worth keeping, with evidence.>

## Recommendations

<Prioritized list: the fix, which findings it addresses, and the expected
effect on users.>

## Limitations

<The participant is an AI agent, not a human: it reads faster, and it may
know Go, Goa, or similar tools from training. One participant per attempt.
Restarts after fixes change the experience. Note any milestone this machine
could not run, and anything the facilitator recorded about its own
influence.>

## Appendix: debrief

<The participant's debrief answers, verbatim.>
```
