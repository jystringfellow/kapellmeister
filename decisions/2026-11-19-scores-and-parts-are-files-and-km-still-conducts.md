# Scores and parts are files, and km still conducts

Today only the seating is configurable. The six parts, their prompts and the order of steps are Go
code. This note decides how a person supplies their own parts and score, before any engine code is
written. Three things do not change:

- **km conducts.** km runs every step and picks the next one from the score. No chair chooses a
  step or messages another chair.
- **Files are the whole handoff.** A step gets what it needs from files km names. No run depends on
  what a session remembers.
- **Today's workflow is the default score**, built into the binary. Under it nothing a caller sees
  changes, and existing ensemble and config files need no edit.

## 1. What is borrowed

OpenRig is the inspiration. km borrows ideas, not code or names.

| OpenRig | km | | Why |
|---|---|---|---|
| rig | ensemble | adopt (exists) | A named file that seats agents. |
| pod | section | adopt (exists) | A group of chairs. km declines pod-level policy blocks: OpenRig validates them and nothing acts on them. |
| seat / member | chair, with its occupant | adapt | The chair keeps its address and the occupant can be replaced. Under `km run` a chair is not a long-lived resident: it gets a new terminal per step unless its part resumes (section 5). |
| `agent_ref` (a directory with `agent.yaml`, guidance, skills, profiles, imports) | part file | adapt | One Markdown file of instructions. No imports, resource pools or profiles: those are the user's agent setup. |
| `runtime` | agent | decline as a field | Which CLI and model plays is machine-bound. Repository files keep naming agents only (the 11-05 decision stands). |
| workflow spec | score | adapt | Steps with declared exits and an exit → next-step map. km keeps no queue, no packets and no owner selection by backlog. |
| the agent picks an exit, the runtime picks the next step | same | adopt | The chair's reply file carries the exit; km reads it and routes. The chair calls no command to route. |
| `loop_guards.max_hops` | `rounds` | adopt | A loop is legal only with a limit, checked before the run. |
| `step_cannot_finish` and other pre-run checks | score validation | adopt | Section 3. |
| edges (`delegates_to`, `can_observe`, …) | none | decline | OpenRig's edges route nothing. In km the score's exit map is the only routing. |
| `restore_policy`, four compaction strategies | `memory = fresh \| resume` | adapt | Two values. A failed resume falls back to fresh and is recorded; OpenRig stops for a person, which it must because its seats are the state. km's state is in files. |
| keys that are declared but not enforced | none | decline | Every key in a km file does something or the file is refused. |
| three status axes with an honest `unknown` | chair `state` in `km status -json` | adopt | Section 7. |

## 2. Parts as files

A part file is plain Markdown: the standing instructions for one role. It has no front matter.
Everything structural (step kind, memory, what is read and written) is in the score, so a part file
cannot change routing or parsing.

- **Where they live.** Built in: embedded in the binary beside the profiles. User:
  `~/.kapellmeister/parts/NAME.md`. Repository: `.kapellmeister/parts/NAME.md`.
- **How a chair references one.** Unchanged: `part = "NAME"` in the ensemble file. The score the
  ensemble names says which parts exist; the name resolves to a file.
- **What km wraps around it.** The prompt for a step is, in order: km's preamble (who you are,
  read the instruction files, never commit), the task, the lessons, the part's text, and a block km
  writes for the step: the files to read, the file to write, and for a step whose reply km parses,
  the reply contract (`Put APPROVE or CHANGES alone on the last line`). A part file cannot remove
  or reword the contract.
- **Placeholders.** A part's text may use a closed list that km fills with paths and lines it
  generated itself: `{plan}`, `{reply}`, `{inputs}`, `{verify}`, `{check}`, `{scratch}`. An unknown
  placeholder is refused when the part loads. Nothing a chair wrote is ever substituted.
- **A part that plays two steps** (the tester plans, then tests) puts each step's instructions
  under a heading `## step: NAME`; text above the first heading applies to both.
- **Built-ins.** `planner`, `implementer`, `reviewer`, `gate`, `tester` carry today's wording.
  `classifier` stays a built-in that is not replaceable (section 9).

## 3. Scores as files

A score is a TOML file. Built in: `default`. User: `~/.kapellmeister/scores/NAME.toml`. Repository:
`.kapellmeister/scores/NAME.toml`. An ensemble file names its score with a top-level
`score = "NAME"`; without the key it plays `default`. `km run -score NAME` does the same for a run
seated from `[chairs]`. The default score, abridged (it also has the background `test-plan` step):

```toml
rounds = 3

[parts.implementer]
memory = "resume"

[parts.tester]
optional = true

[[steps]]
name = "plan"
kind = "produce"
part = "planner"
writes = "reply"
next = { done = "implement" }

[[steps]]
name = "implement"
kind = "produce"
part = "implementer"
reads = ["plan", "review", "gate", "test"]
escalate = true
next = { done = "review" }

[[steps]]
name = "review"
kind = "review"
part = "reviewer"
reads = ["plan", "implement"]
approve_when = "any"
next = { approve = "gate", changes = "implement" }

[[steps]]
name = "gate"
kind = "decide"
part = "gate"
reads = ["plan", "implement", "review"]
next = { approve = "test", changes = "implement" }

[[steps]]
name = "test"
kind = "test"
part = "tester"
reads = ["test-plan"]
next = { approve = "approved", changes = "implement", absent = "approved" }
```

**Step kinds: a small fixed set, as proposed.** I agree with produce, review, decide and test, with
one clarification. The fixed set is what lets km own the reply contract, the read-only guard, the
rule for several chairs and the set of exits. A free-form kind would make each of those the score
author's problem, and validation could no longer say that a step can finish.

| Kind | Who plays | May change the worktree | Exits |
|---|---|---|---|
| produce | one chair | yes, unless `writes = "reply"` | `done` |
| review | every chair of the part, in parallel | no | `approve`, `changes` |
| decide | one chair | no | `approve`, `changes` |
| test | one chair, in its scratch directory | no | `approve`, `changes` |

The clarification: **`test` means the tester's step, not verify.** check, verify and strict are not
steps a score lists. km runs the baseline verify before the first step, verify after every produce
step that may change the worktree, and strict once after the score ends approved, exactly as today.

**A produce step that writes only its reply** sets `writes = "reply"`. The planner is one: it
writes `plan.md` and nothing else. km does not run verify after such a step, as it runs none after
plan today. Every other produce step is followed by verify. The background step is always of this
sort. Today the planner is told to modify no other file and km does not check; the default score
keeps that, and giving these steps the read-only guard would be a change made on purpose, with its
own test.
A score cannot leave them out, move them or reword what chairs are told about them. That keeps
"km runs the tests and hands agents a short result" true under every score, and it means no score
can approve a change that verify never saw. A decide step does not run while the latest verify is
failing; it takes its `changes` exit, as the gate does today.

Other rules:

- **Exits are complete and explicit.** Every exit of a step's kind has a target; there is no
  "next step in the file". A target is a step name or an ending: `approved` or `changes_requested`.
- **`changes_requested` is a new outcome** for a score with nobody to send changes to. The default
  score cannot reach it. It gets its own exit status.
- **A step that fails or times out ends the run** as `failed` (or `untested` for a test step), as
  today. Those are not exits a score can route.
- **Rounds.** An exit that targets the same or an earlier step starts a new round. A score with
  such an exit must set `rounds`; `[run] rounds` and `-rounds` override it as today. Past the limit
  the outcome is `rounds_exhausted`.
- **Reply files are named by km:** `STEP.md` before the loop, `STEP-ROUND.md` inside it,
  `STEP-ROUND-N.md` when several chairs play. These are today's names.
- **`reads` is the handoff.** A step is handed the newest file of each step it lists. A step that
  lists nothing from review is blind to reviews, which is how the tester stays blind.
- **`optional` parts.** A step of a part no chair plays takes its `absent` exit.
- **One background step.** `background = true` on a produce step with `writes = "reply"`:
  it starts when the step before it exits and is joined before the first step that reads it. The
  default score needs this for `test-plan`. A score may have at most one.
- **`gate_when` stays an ensemble key.** Under the default score `any` and `all` set the review
  step's `approve_when`, and `always` sends both review exits to the gate.

**Checked before a run starts**, with the file, the step and the reason in the message:

- an unknown key, kind, part, target or placeholder;
- a step no path reaches, or a step from which no path reaches an ending;
- a backward exit without `rounds`;
- a part with a step of kind review and `memory = "resume"`;
- a required part no chair plays, or a chair whose part the score does not have;
- a verdict step in a score seated without a classifier.

## 4. Chairs per part

Any part may have several chairs. What that means depends on the step's kind:

- **review:** all play, in parallel, under one snapshot. Each writes its own file and the step's
  exit comes from `approve_when`. This is today's behaviour.
- **produce, decide, test:** first available. The first chair in file order plays. A later chair
  takes over only when the earlier one's step fails to finish, and the takeover is recorded. Two
  chairs never write in one worktree at once, and a decision is one voice: a score that wants
  several verdicts uses a review step.

Today `ChairsOf` refuses two planners. Under this rule the second is an alternate. That is a
relaxation; no existing file changes meaning.

## 5. Memory

`memory` is set per part in the score: `fresh` (the default) or `resume`.

**What the note found: the implementer already resumes.** `km run` keeps the implementer's
terminal across rounds and releases it only when escalation changes its tier. So the default score
declares `memory = "resume"` for the implementer, and the rule below about a changed tier is
today's behaviour, not a new one. The measurement in the slices therefore compares today's
behaviour with a fresh implementer, not the other way round.

- **Resume means km keeps the chair's terminal** between its steps and sends the next step to it.
  It is CAO's reuse of a terminal, which km uses already. km does not restore an agent's stored
  session into a new terminal: that would be a second mechanism per agent, owned by km.
- **A resumed chair continues across its own steps**, not only across rounds: a resumed tester
  tests in the session that wrote the test plan.
- **Review-kind parts are always fresh.** A score that says otherwise is refused. A reviewer that
  remembers its last verdict reviews its memory.
- **A changed agent or tier falls back to fresh**, and so does a takeover by another chair. km
  records it on the step (`memory: "fresh"`, with the reason).
- **Which agents can resume.** Any agent CAO can send a second step to on a kept terminal; the four
  built-in agents are used that way by `km play` today. When the reuse fails, km starts a fresh
  terminal, records why, and the run goes on. A run never fails for lack of memory.
- **Length limit.** A resumed session starts fresh when the chair's last step used more input
  tokens than `resume_tokens`, or, when usage is not measured for that agent, after `resume_steps`
  steps. The default score sets neither, because today has no limit; the measurement decides
  whether it should.
- **A resumed chair receives every file a fresh one would.** The prompt does not depend on memory.

## 6. Trust

A checked-in score or part is instructions that agents follow with permission prompts off. Two
facts decide the design:

1. A repository already supplies instructions agents follow (its agent instruction files) and shell
   commands km runs (`[test]`, `[sizing] command`). A part file adds no power a repository lacks.
2. The new risk is inside a run: an implementer can edit `.kapellmeister/parts/reviewer.md` and the
   reviewer of the same run would follow it.

So:

- **A repository may supply scores and parts.** km reads them once, when the run starts, from the
  committed files at the run's base commit, as it reads lessons. The worktree's copies are never
  read. When a run's change touches one, the summary names it, as it does for a strict script.
- **Only a user supplies** agents, profiles, models and permissions. A repository score cannot name
  any of them.
- **A user file shadows the repository file of the same name**, whole, as ensembles do.
- **Built-in names are reserved.** No file may be named `default`, and no part file may take a
  built-in part's name. A repository that could replace `gate` would change what every plain
  `km run` in it means. To change a built-in, copy it to a new name; km can print each built-in for that.
- **Approval: none inside km.** km prints, before the run, each score and part that
  comes from the repository with a short content hash, and records the hashes in the result. A
  user who wants a hard stop sets `[run] repository_scores = false` in their own config, and km
  then refuses a repository-supplied score or part. A per-worktree approval step was one reason km
  left CAO workflows, and an approval here would protect less than the `[test]` commands already
  expose. First-use approval recorded by content hash was the alternative, and was
  rejected.

## 7. Status as data

`km status -json` keeps today's fields and adds, per run:

```json
{
  "schema": 1,
  "run_id": "…", "command": "run", "repository": "…", "liveness": "alive",
  "cao": "http://127.0.0.1:9889",
  "ensemble": {
    "name": "thorough", "source": "repository",
    "sections": [{
      "name": "review",
      "chairs": [{
        "address": "review.lead@thorough", "name": "lead", "part": "reviewer",
        "agent": "claude", "provider": "claude_code", "model": "…", "tier": "medium",
        "state": "playing", "step": "review", "since": "…", "memory": "fresh",
        "terminal": "06a3cc61",
        "needs_input": { "count": 0, "reason": "" },
        "usage": { "input_tokens": 0, "output_tokens": 0, "measured": "all" }
      }]
    }]
  },
  "score": {
    "name": "default", "source": "built-in", "rounds": 3,
    "steps": [
      { "name": "verify", "kind": "verify" },
      { "name": "plan", "kind": "produce", "part": "planner", "next": { "done": "implement" } }
    ]
  },
  "round": 2, "max_rounds": 3,
  "current": [{ "step": "review", "round": 2, "chairs": ["review.lead@thorough"], "started": "…" }],
  "trail": [{ "step": "plan", "round": 0, "exit": "done", "chair": "leads.planner@thorough", "duration_ms": 0 }],
  "usage": { "input_tokens": 0, "output_tokens": 0, "measured": "some" }
}
```

- **Nothing assumes the six parts.** A part is a string on a chair and on a step. A page draws
  sections, chairs and steps from the lists and needs no table of names.
- **`cao` is the run's CAO server address and each chair carries its `terminal`,** the id of the
  CAO terminal its occupant sits in. A page links a chair to its terminal with the two, and a run
  may use its own CAO server, so the address is per run. `terminal` is empty while no occupant is
  seated: under `km run` a fresh chair has a terminal only while it plays, and a resumed or
  standing chair keeps one.
- **`current` is a list** because review chairs and the background step play at once.
- **Chair `state`** is `waiting`, `playing`, `done` or `unknown`. Waiting for a person is not a
  state: it is `needs_input` with a count and a reason.
- **`measured`** is `all`, `some` or `none`, so a token count never looks more complete than it is.
- **verify and strict appear in `score.steps`** with their own kinds and no part, so a page can
  draw them where they run.
- **A run seated from `[chairs]`** reports one section named `chairs`.
- The data comes from files the run already writes, plus the sections and the score, which the
  run's config must start recording.

## 8. Report and cost

- **Each step record carries** the score, the step, the part and the chair's address.
- **`km report -parts` groups by score, part, provider and model.** Row order follows the score's
  parts, not a fixed list. Rows never merge across scores: the same part name can hold different
  instructions.
- **`km report -steps` is new:** the same grouping by step, which is where "tokens per step" lives.
- **Records written before this change** have no score and are read as the default score, so its
  report output does not change.
- **Cost is unchanged:** per model from the user's `[report.prices]`, with the same lower-bound
  marks. Totals gain a breakdown by score beside ensemble and repository.

## 9. Classifier, tiers, lessons and a crashed run

- **Classifier.** Not a step. It is a service for every step with a verdict: when a reply lacks a
  bare last-line verdict, the classifier chair reads it. Its part name is reserved, it stays on the
  small tier, and an ensemble must seat one when the score has a verdict step.
- **Tiers.** Each step in a score carries its default tier and range. `[steps.NAME]` in a config
  overrides by step name, as today. A key that names no step of the score in use is ignored with a
  note, since one config serves several scores.
- **Sizing.** `sizes = true` marks the step whose reply carries the size (plan). `sized = true`
  marks the steps the size applies to.
- **Escalation.** `escalate = true` on a produce step: its tier rises after every `escalate_after`
  rounds without approval. Only the implement step has it in the default score.
- **Lessons in** are already keyed by part and chair address. The part is checked against the
  score's parts, not a fixed list.
- **Lessons out.** `lessons_by = "PART"` in the score names who writes proposals after the run
  (the gate, by default). A score without the key has no lessons step.
- **A crashed run.** Nothing resumes a run today, and these slices do not add it. The trail makes
  it possible later: each finished step records its exit and its reply file, so a restart can begin
  at the step after the last record, with every chair fresh.

## 10. Other commands under a non-default score

- **`km prove`:** no chairs, no score. Unchanged.
- **`km tune`:** keeps its own fixed loop (write, km proves, review). The proof is km's evidence, so
  that loop is not a score a file can replace. It seats its two chairs from flags or `[chairs]`, as
  today, and never reads the run's score.
- **`km audition` and `km rehearse`:** check the ensemble against its score first. Rehearse briefs
  each chair from its part file.
- **`km play`:** plays the score the standing ensemble names. A standing chair keeps its terminal,
  so a reviewer under `km play` remembers earlier rounds today. `km play` will reseat each review
  chair's occupant before its step: the chair keeps its address, and review is always fresh under
  every command. This changes `km play` on purpose.

## 11. Slices

One PR each. The order differs from the proposal in two places, marked.

1. **Pin the default score:** step order, each part's prompt, state files, report output, and
   (added) the implementer's terminal reuse and its release on a tier change. No production change.
2. **Score and parts as internal types** with the defaults built in; `ChairsOf`, `stepRoles` and
   the prompts read from them. Proves the engine can run from data with every pinned test passing.
3. **(moved up) `km status -json`** per section 7, for the default score. It needs only slice 2,
   and the page that draws it can start earlier.
4. **Load scores and parts from files:** validation, base-commit reading, reserved names, the
   printed sources. Proves the checks in sections 3 and 6.
5. **A second shipped score, review-only:** reviewers and a gate over an existing diff, ending
   `approved` or `changes_requested`, with no engine change. Its tests include the status output,
   which is the proof that nothing assumes six parts.
6. **Memory:** `resume` for any non-review part, the fallbacks, the limits, and the measurement:
   the same task with the implementer resumed (today) and fresh, comparing rounds, tokens and
   outcome.
7. **(added) `km play` reseats review chairs.**
8. **README and `km init`.**

## Rejected

- **Agent-chosen sequencing and chairs messaging chairs.** The conductor decides.
- **Free-form step kinds.** Section 3.
- **verify and strict as steps a score places.** A score could then skip them.
- **Front matter in part files.** A part file could then change routing.
- **Restoring an agent's stored session.** A second mechanism per agent, owned by km.
- **Edges, queues and owner selection.** Nothing in km would read them.

## What building it changed

The note above is as approved. These are the places where building it showed the note wrong or
incomplete. Tests hold the behaviour; this list is so nobody reads the sections above as exact.

- **Section 7, `ensemble`.** The note shows `ensemble` as an object and also says today's fields
  are kept. They could not both hold: `ensemble` was the name as a string. It is now the object,
  with the name at `ensemble.name`, and `schema: 1` marks the change.
- **Section 7, `terminal`.** The note says a fresh chair has a terminal while it plays. km learned
  the id only when the step returned. It now looks the id up a few seconds after a step starts,
  from the listing the watcher already used, with no change to CAO.
- **Section 7, verify in `score.steps`.** The example shows `verify` once. It is listed where it
  runs, so twice in the default score, and only when the run has a verify command; the same for
  strict. A step name is therefore not unique in the outline.
- **Section 7, before preflight.** The note says nothing of a run that is still checking its
  chairs. It is in status from the start, with `current` holding one entry named `preflight`.
- **Section 11, slice 2.** Making the score data did not make the engine play it: the order of
  steps stayed in code. A slice the list does not have, the engine walking a score's steps and
  exits, had to come before loading scores from files.
- **Section 11, slice 5.** The second score was expected to need no engine change. It needed one
  rule, as data: when no step of a score may edit the worktree, the baseline verify is the verify
  its steps are handed, and a failing one sends a decide step to `changes` without playing.
- **Section 2, placeholders.** There is a seventh, `{verification}`: km's short line about the
  verify result. A placeholder with nothing to fill it, such as `{inputs}` in a step that reads
  nothing, is refused.
- **Section 3.** `changes_requested` exits 7. A score file is limited to 64 KiB and a part file to
  256 KiB. `km score show NAME` prints a built-in; a copy of the default score cannot yet say
  which step sizes the work, who writes lesson proposals, or that `test-plan` uses the `test`
  step's tier policy.
- **Section 5, limits.** `resume_tokens` counts the uncached input tokens of the chair's last
  step, not the size of its context.
- **Section 5, the measurement.** One run each way on one small task with a forced second round:
  the resumed implementer's second round used about a fifth of the uncached input and half the
  time of the fresh one, with the same rounds and outcome. `resume` stays the implementer's default.
- **Tiers, which the note does not cover.** There are four, with `xlarge` above `large`. A step
  with no tier policy of its own gets one by its kind, so a chair of a custom part plays on the
  `medium` tier and may be pinned.
- **Not built:** takeover by a second chair of a non-review part (section 4).

