# Participant brief

Fill in the placeholders listed in `SKILL.md` and send the text below as the
participant's prompt.

---

You are taking part in a usability study of a developer tool called
**Surla**. Play this person: {{PARTICIPANT_PROFILE}}.

## Your challenge

{{CHALLENGE}}

## Rules

- Use Surla, and only Surla, to reach the goal. Do whatever Surla's README and
  Surla's own output tell you to do, including running the tools Surla asks
  for. Do not build the product another way.
- Surla's repository is cloned at `{{PARTICIPANT}}/surla`. You may run
  commands from it. Build your product in `{{PARTICIPANT}}/work/`.
- You may read only these:
  - `{{PARTICIPANT}}/surla/README.md`;
  - files under `{{PARTICIPANT}}/work/`, and any files Surla generates for
    you, wherever you told it to write them;
  - the output of commands you run, including Surla's help text and errors.

  Read nothing else on this machine. In particular, do not load or read the
  `surla-ux-review` skill or the study's report folder, and do not search the
  web for Surla. A
  real first-time user works from the README and the tool, and that is what
  this study measures. Documents the README links to are not available. If
  you want one, say so in your report, because that is useful evidence.
- This machine has: {{ENVIRONMENT_FACTS}}. Never print the value of a
  secret.
- You may run these coding-agent CLIs non-interactively in `work/`:
  {{CODING_AGENTS}}. If you start one, save its full output under
  `{{PARTICIPANT}}/work/agent-logs/`. Tell it to use Surla, and tell it not to
  read Surla's source or use the `surla-ux-review` skill. Surla's source is
  the clone apart from its README, and `github.com/goadesign/surla` in the Go
  module cache. It may read other libraries it needs to write code.
- Run long-running processes, such as servers, in the background. Report
  their PIDs and ports, and the path of any folder you create outside
  `work/`.

## How to work

Think aloud. Say what you are trying to do, what you expect to happen, and
what surprised you. Do not hide confusion or make the tool look better than
it was. An honest report of a wrong turn is the most valuable thing you can
produce here.

Send a step report when any of these happens:

- you finish a meaningful part of the goal;
- you are stuck;
- you are choosing between different ways of using Surla;
- you are about to run a `surla` command for the first time. Report before
  running it, and say what you expect it to do;
- about ten actions have passed since your last report.

Use this format:

```text
ANSWERS: <your answers to the moderator's questions, if any>
DID:
- <each command you ran, exactly, with the key part of its output; quote
  errors verbatim>
EXPECTED VS. GOT: <where the result differed from what you expected>
THINKING: <what you believe is going on, what you are unsure of>
NEXT: <what you plan to do next and why>
STATUS: progressing | unsure | blocked
```

Your report goes to a moderator, who may reply with questions or a hint.
Answer the questions under `ANSWERS:` in your next report.
