# kapellmeister

kapellmeister runs a plan → implement → review → gate loop over coding agents (Claude Code, Codex, Copilot
CLI, Antigravity) through [CLI Agent Orchestrator](https://github.com/awslabs/cli-agent-orchestrator)
(CAO), with an optional tester that exercises the change live after gate approval. Rounds repeat
until the gate and any tester approve or the rounds run out. Every run works in a throwaway detached
worktree and leaves its change uncommitted for you to ship.

```sh
km run -repo ~/Code/app -task-file task.md [-ensemble thorough]  # one ensemble
km audition -worktree DIR [-ensemble thorough]   # can this ensemble play? no real work
km rehearse -worktree DIR -task-file task.md [-ensemble thorough]  # brief and leave standing
km play SESSION [-max-rounds N] [-verify CMD] [-timeout D] [-cao URL]  # run the score
km reseat SESSION ADDRESS [-agent SPEC] [-timeout D] [-cao URL]  # replace one occupant
km ensemble list                                # list standing ensembles
km ensemble down SESSION                        # take one down
km report [-json] [-since DATE] [-repository NAME] [DIR...]  # summarize run records
km preflight -worktree DIR [-tier large]          # check each agent starts and answers
km config [-worktree DIR] [-cao URL]              # show the effective settings and their files
```

Chairs default to claude plans, codex implements, copilot reviews, claude gates and classifies.
Override any chair with `-planner`, `-implementer`, `-reviewer`, `-gate`, `-classifier` or the optional
`-tester`, as an agent (`claude`, `codex`, `copilot`, `agy`) or `provider:profile[:model]`. Append `@small`,
`@medium` or `@large` to pin a chair's tier, for example `-reviewer claude@large`. A chair with an
explicit model always runs that model and cannot also use `@tier`.

The reviewer role can have several chairs. Use `-reviewer copilot,claude@large` to replace the
whole configured reviewer list for one run. Each reviewer runs concurrently in its own CAO
terminal and resolves its tier separately: pinned reviewers keep their tier, while unpinned
reviewers follow sizing. Other required parts have one chair; the optional tester has at most one.

## Audition

`km audition -worktree DIR [-ensemble NAME]` checks that an ensemble can play without doing any
work. It boots every chair into one CAO session, in the worktree, at the tier its part uses by
default. Each chair is labelled with the CAO group `[ensemble, section]` and the metadata address
`section.chair@ensemble`, for example `review.security@thorough`. Then km cues each chair once
through CAO's inbox, and the chair closes the cue by running `km cue done`, which appends to
the audition's queue under `.kapellmeister/runs/audition-*/`.

A chair passes when its cue is closed within `-timeout` (default `[run] preflight_timeout`).
A chair fails if it does not boot, if its screen shows a trust or hook-review prompt before the
cue, or if it never closes the cue. The report names the problem, and `report.json` sits next to
the queue. The session is torn down afterwards unless you pass `-keep`. A chair's own `timeout`
does not apply to its audition, which does no work. The table and `report.json` include each
chair's permission label; see "Permissions" below.

## Permissions

`km audition` shows a `PERMISSIONS` column for every chair. `km config` reads CAO to show the
permission label for each default chair at its configured tier, including each reviewer and an
optional tester. Use `-cao URL` to choose the CAO server. For generated effort profiles, config
shows the base's permissions that the next run will synchronize, including when an installed variant
is stale. Config only reads profiles. If CAO cannot be read, it prints `permissions unavailable` and
still shows the configuration.

These labels describe CAO 2.5.4's provider launches:

| Provider | Profile setting | Label and launch |
| --- | --- | --- |
| `claude_code` | No `permissionMode` | `skip-permissions (CAO default)`; `--dangerously-skip-permissions` |
| `claude_code` | `permissionMode: <mode>` | `permission-mode <mode>`; `--permission-mode <mode>` |
| `copilot_cli` | Any | `allow-all (fixed by CAO)`; always `--allow-all` |
| `antigravity_cli` | Any | `skip-permissions (fixed by CAO)`; always `--dangerously-skip-permissions` |
| `codex` | No `codexProfile`, or `allowedTools` contains `*` | `yolo`; `--yolo` |
| `codex` | `codexProfile` set and `allowedTools` has no `*` | `approvals`; `--profile <name>` uses that Codex policy |

Claude's supported modes are `default`, `acceptEdits`, `plan`, `auto` and `bypassPermissions`.
`bypassPermissions` turns permission prompts off. Unsupported modes display as `unknown`; km does
not echo their text or infer whether prompts are enabled. The `ask` and `deny` rules in
Claude's own settings still apply when permissions are skipped. CAO can also restrict tools through
`allowedTools`: Claude's `--disallowedTools` and Copilot's `--deny-tool` still apply. For Claude
running as root with `allowedTools: ["*"]` and no explicit mode, CAO omits the skip flag; km's label
does not model this root exception.

For Codex, `approvals` means CAO launches with a named `codexProfile` instead of `--yolo`.
The named profile in Codex's own configuration controls the effective approval policy; km does not
inspect that configuration. Without `codexProfile`, CAO defaults to `--yolo`, including when
`allowedTools` is restricted or empty. `allowedTools: ["*"]` forces `--yolo` even with a named
profile. To keep a chosen Codex policy through every effort variant, set `codexProfile: safe` on
`km-codex` (where `safe` names your configured Codex profile) and omit `*` from `allowedTools`.

To keep prompts on for km's Claude chairs, add this frontmatter field to `profiles/km-claude.md`,
keeping its other fields and body:

```yaml
permissionMode: default
```

Then install the base:

```sh
cao install profiles/km-claude.md --provider claude_code
```

Every km Claude chair using `km-claude`, including generated `km-claude-<effort>` profiles,
inherits that mode. km copies `permissionMode`, `allowedTools` and `codexProfile` from its Claude
and Codex bases when generating effort profiles, and reinstalls existing variants when any of these
fields differ.
An unset `allowedTools` stays unset; an explicit empty list stays empty.

When any chair has an unguarded label, km prints one closing note counting those chairs.
km reports permissions and never refuses to run because of a permission mode. The `blocked:` lines
from the session watcher show when a chair waits on a prompt; resolve it in that chair's terminal.

## Rehearse

`km rehearse -worktree DIR (-task TEXT | -task-file FILE) [-ensemble NAME]` prepares an ensemble
for work and leaves its CAO session standing. Before booting any chair, it runs the baseline verify
command: `-verify CMD`, then `[test] verify`, then an executable `.kapellmeister/verify`. A failing
baseline stops the rehearsal; when no command resolves, km reports that it skipped the baseline.

Each chair receives one briefing through CAO's inbox with its address, part, section-mates and the
full task. The briefing asks it to read and understand the task without changing files, then close
its cue with `km cue done`. `-timeout` defaults to `[run] preflight_timeout`. The per-chair report and
queue live under `.kapellmeister/runs/rehearse-*/` in the worktree.

The session stays up even when a chair fails. km records the standing ensemble in
`<state>/ensembles/<session>.json` for `km play` to use later.
Use `km ensemble list` to see recorded sessions and `km ensemble down SESSION` to delete one and
remove its record. `down` also accepts `-cao URL` before the session name and removes the record
when CAO says the session is already gone.

## Play

`km play SESSION [-max-rounds N] [-verify CMD] [-timeout D] [-cao URL]` runs the score on a
standing ensemble. It uses the recorded worktree, task and chairs, rereading the task file when
one was recorded. Every step runs in its chair's standing occupant, including each reviewer in
its own chair. Steps sharing an occupant run one at a time.

Sizing and tier escalation are off: each occupant keeps its recorded provider, profile and model.
Before planning, km refuses an ensemble with any chair that did not close its rehearsal briefing,
naming those chairs, and then checks every occupant with CAO; if any is missing, it names the chairs and asks
you to replace each occupant with `km reseat SESSION ADDRESS`, or take the session down and rehearse again. Records from an older km without bindings also
need a new rehearsal. Verify, check, progress and run records work as with `km run -worktree`.

The session and its record stay standing on success, failure and interrupt. Take it down with
`km ensemble down SESSION`. If a step times out or is interrupted, CAO may leave its occupant
mid-turn; play does not reset it.

## Reseat

`km reseat SESSION ADDRESS [-agent SPEC] [-timeout D] [-cao URL]` replaces one chair's occupant
in a standing ensemble. Use it to recover a dead, stuck or never-booted occupant. The chair keeps
its address and, by default, its recorded binding. `-agent` selects an agent with an optional tier
(such as `codex@large`), or `provider:profile[:model]`, using the same preparation as rehearse.
Flags may come before or after the session and address.

The new occupant receives the recorded task and closes a briefing before km deletes the old
terminal and records the replacement. A failed boot, unclosed briefing or interruption leaves the
old occupant and record intact and deletes any new terminal. `-timeout` defaults to
`[run] preflight_timeout`; the cue queue lives in `.kapellmeister/runs/reseat-*/` in the recorded
worktree. Reseat never deletes the session or changes the other chairs. Run `km play SESSION`
when the ensemble is ready.

## Ensemble files

An ensemble file seats chairs in sections. A repository can check one in at
`.kapellmeister/ensembles/NAME.toml` for everyone who works in it, and you can keep your own at
`~/.kapellmeister/ensembles/NAME.toml`. Yours wins when both have the same name. Select one with
`-ensemble NAME` on `km run`, `km audition` or `km rehearse`.

```toml
# .kapellmeister/ensembles/thorough.toml
gate_when = "all"
timeout = "30m"                     # every chair's step, unless its section or chair says otherwise

[sections.leads.chairs.planner]
part = "planner"
agents = ["claude@large"]

[sections.leads.chairs.implementer]
part = "implementer"
agents = ["codex", "claude"]        # the first agent you have plays the chair
timeout = "90m"

[sections.review]
timeout = "15m"

[sections.review.chairs.security]
part = "reviewer"
agents = ["claude"]

[sections.review.chairs.style]
part = "reviewer"
agents = ["copilot", "claude"]

[sections.leads.chairs.gate]
part = "gate"
agents = ["claude"]

[sections.leads.chairs.classifier]
part = "classifier"
agents = ["claude@small"]

[sections.leads.chairs.tonmeister]
part = "tester"
agents = ["claude"]
```

Each chair has the address `section.chair@ensemble`, such as `review.security@thorough`, and
plays one part: `planner`, `implementer`, `reviewer`, `gate`, `classifier` or the optional `tester`.
The score needs exactly one planner, implementer, gate and classifier, at least one reviewer,
and at most one tester. Parts are roles with plain names; chairs may carry orchestral names,
such as `tonmeister` for the tester. Section and chair names are lowercase letters, digits and dashes. Sections keep the order they first appear in the file,
and chairs keep their order within a section.

A chair plays the first of its `agents` that is in your `[agents] available`, which defaults to
every agent km knows. A repository's file names only agents, each with an optional `@tier`,
because profiles and models depend on your machine. Your own files may also name
`provider:profile[:model]`. A chair's `timeout` wins over its section's, which wins over the
file's, which wins over `[run] timeout`. Role flags cannot override an ensemble file; copy it to
`~/.kapellmeister/ensembles/` and edit that instead. A name defined both as a file and in
`[ensembles]` is an error. `km config` lists the ensemble files it finds and where each is from.

## Configuration

kapellmeister layers two files over its built-in defaults; flags override both for one run. Every key is
optional, and unknown keys are an error.

- **`~/.kapellmeister/config.toml`** (`$KAPELLMEISTER_HOME` moves it) holds what depends on your machine and accounts:
  `[cao]`, `[chairs]`, `[agents]`, `[ensembles]`, timeouts, and the model each tier means for you.
- **`.kapellmeister/config.toml`** in the repository holds the contract everyone working in it shares:
  `[test]`, step tiers and floors, rounds, lessons budget and sizing.

The repository's values win for `[run]` rounds, retries, `lessons`, `lessons_timeout` and `lessons_budget`, `[steps]` and `[sizing]`; yours win
for `[tiers]`, because only you know what your accounts can run. `[cao]`, `[chairs]`, `[agents]` and
`[ensembles]` are machine-only and `[test]` is repository-only; putting one in the wrong file is an error.
`$CAO_API_BASE_URL` overrides `[cao] url`. State lives in `~/.kapellmeister/state` (`$KAPELLMEISTER_STATE_DIR`).

km bounds each CAO call: short calls at 30s, launches at 90s, and run-step at the step’s own timeout
plus 90s for CAO’s readiness wait; hitting a bound reports the method, path and limit.

```toml
# ~/.kapellmeister/config.toml
[cao]
url = "http://127.0.0.1:9889"

[run]
timeout = "60m"                     # each agent step
reply_grace = "15m"
preflight_timeout = "4m"
blocker_poll = "20s"
lessons = true
lessons_timeout = "5m"
lessons_budget = 4000              # bytes of lesson text per chair; at least 1

[chairs]                            # agent or provider:profile; optional @tier, or :model
implementer = "claude"
reviewer = "copilot"
tester = "claude"

[agents]
available = ["claude", "copilot"]   # agents you can run; ensemble files pick from these

[ensembles.thorough]
reviewer = ["copilot", "claude@large"]
gate_when = "any"
gate = "claude@large"
tester = "claude"
escalate = false

[tiers.copilot_cli]
large = { model = "claude-sonnet-5.5" }
```

Select a named ensemble for a run with `km run -ensemble thorough` alongside the usual task and
repository flags. Its chairs overlay `[chairs]`, then built-in defaults for omitted roles. Without
`-ensemble`, the run uses `[chairs]` as usual. Role flags override the selected ensemble's chairs;
unknown ensemble names are an error listing the defined names. `km config` lists each ensemble
and its chairs. `[ensembles]` belongs only in `~/.kapellmeister/config.toml`, because it names
agents that depend on your accounts; a repository shares an ensemble as an
[ensemble file](#ensemble-files) instead. In `[chairs]` and `[ensembles]`, `reviewer` accepts either a single
string or an array such as `reviewer = ["copilot", "claude@large"]`. Other parts, including `tester`,
reject arrays. Omit `tester` to keep the score without live testing.
`km config` shows the reviewer lists and `gate_when`.

An ensemble's `gate_when` controls whether a round that passed verification goes to the gate:

- `any` (the default): at least one reviewer approves.
- `all`: every reviewer approves.
- `always`: the gate runs even when all reviewers request changes.

An omitted `gate_when` defaults to `any`; an explicitly empty value is invalid.
A run without an ensemble uses `any`. Failed verification always skips the gate and sends the
round back to the implementer. With one reviewer, `any` and `all` both require its approval.

The gate reads every available review, checks the diff itself, and sorts each finding into
**blocking** or **nit**, with a reason. It approves when the task is done, verification passed,
and nothing blocks. Its gate file lists nits without sending them back for another round.

Each successful review is classified as soon as it lands, while other reviewers may still be
running. km waits for every review and classification before deciding whether to run the gate.
Review and gate prompts ask for `APPROVE` or `CHANGES` alone on the reply file's last line.
A bare verdict on the last non-blank line of a review, gate decision or test report is taken as is;
otherwise the classifier reads the reply.

If approval is withheld, the implementer gets the gate file when present, every available review
file, and the verification output. When the gate ran, its blocking findings supersede reviewer
requests; findings it classifies as nits need no change. Each round without approval still counts
toward escalation.

When a tester is seated, it writes `test-plan.md` from the task and plan alone, blind to the
implementation, while round 1 implements. The plan targets end to end use in the real environment,
including missing, stale or concurrent state and misbehaving services. Both tester prompts state the
test step's effective time limit, including ensemble, section and chair overrides. The plan orders
scenarios by risk and sizes them to fit that limit. The tester runs the highest-risk scenarios first
and stops starting new ones with about the last tenth remaining, leaving time to clean up and write
its verdict. It appends each scenario's result and evidence to the report as soon as the scenario
finishes, and writes the verdict line last. After the gate approves, km waits for the test plan,
then runs `test` in the tester's chair and reads the verdict from `test-<round>.md`: a bare `APPROVE`
or `CHANGES` last line is taken as is; otherwise the classifier reads the report. Only a test
APPROVE yields an `approved` outcome. Any other verdict sends the test report back as the leading
feedback, with instructions to reproduce each failure in a failing regression test before fixing
it. Later rounds reuse the same test plan.
A test-step timeout after gate approval ends `untested`: the change was approved but testing did
not finish. The outcome names the partial report if it exists and preserves the worktree and its
uncommitted change as an approved run would. Other test-step failures, and a timeout or failure in
any other score step, still fail the run. The test step gets the same worktree check as the gate and
reviewers; a tester that modifies the worktree still fails the run even if it also times out.
Both tester prompts restrict scratch files, build output, scratch repositories and server state to
the absolute `<state dir>/scratch/<run id>` directory, outside the worktree.
`km run` and `km play` create it before starting the score and refuse to start if it is inside a git
work tree; set `KAPELLMEISTER_STATE_DIR` outside any repository. The test prompt permits git commands
that write only in repositories the tester created inside scratch, always using `git -C <repository>`.
The tester must append every long-running process's PID to `<scratch>/pids`, one per line, and stop
those processes and its sessions before finishing, without touching anyone else's. After every
test step, including failure, timeout or interruption, km reports each distinct listed process still alive as
`  left running: pid <pid> (<command>)` and records its PID and command in `left-running` telemetry.
km never signals these processes. It silently skips non-regular `pids` files (including FIFOs) and
files larger than 1 MiB. The concurrent test-plan step
relies on its prompt to forbid file changes because implementation is editing at the same time.
Without a tester, gate approval finishes the run as usual.

After the score ends, km asks the gate for lessons when it has given a verdict, whether or not
it approved the run. The question is: from this run's plan, reviews and verdicts, what would have
saved a round if a chair had known it at the start? Zero lessons is a good answer. Each candidate
names its part (`planner`, `implementer`, `reviewer`, `gate`, `classifier` or `tester`), states a lesson
in one or two sentences, and cites the step and finding that support it. The gate returns a JSON
array of objects with `part`, `lesson` and `evidence` fields.

This is shadow mode: km never reads `lessons.json` back and never puts a lesson in a briefing.
The lessons step always plays in a fresh terminal that is released afterwards, so under `km play`
the standing gate never sees what was proposed and cannot carry it into the next play.
It writes `lessons.json` only in the run's state directory, beside `result.json`, with km-stamped
`run_id`, `repository` and `ensemble` on each lesson. It never writes that file into the repository's
worktree. The gate's reply uses the ignored `.kapellmeister/runs/<run>/lessons-reply.json` record,
like other step replies; a regular reply within the size limit is copied to the state's `replies/`
for audit, including malformed answers. Non-regular and oversized replies are not copied.

Collection is on by default. Set `[run] lessons = false` in either config layer to turn it off;
`lessons_timeout = "5m"` sets its own positive time limit, covering the question, reply wait
and nudge. Terminal inspection, diagnostic screening and release still run after a timeout with
independent limits of 15s, 15s and 30s respectively, so cleanup can extend beyond the lessons deadline.
Lessons use the gate's chair and tier policy. At most five lessons are kept,
with 400 characters per lesson, 300 per evidence field, and 4096 bytes across stored fields.
Replies are capped at 32 KiB. Control and invisible formatting characters are removed and whitespace
is collapsed. Unknown parts, empty or oversized fields and lessons beyond the count or total size
limit are dropped; malformed JSON, extra fields or trailing data reject the answer.

`result.json` records `lessons.proposed` and, when needed, `lessons.reason`; zero proposals writes
an empty `lessons.json` array. A timeout, step error, unreadable or oversized reply, invalid answer,
interrupt or missing gate verdict records zero with a reason. A lessons step that changes the
worktree discards its proposals and prints a warning. A failed lessons-file write also records
zero and warns. Lessons never change the run's outcome or exit code. With collection disabled,
there is no lessons step, file or result key.

A reviewer whose step errors or leaves no reply after its one nudge is recorded as `FAILED`.
It counts as not approving for `gate_when`; surviving reviews continue through the round, and
the gate is told which reviewers failed so it can weigh the missing review. If every reviewer
fails, the run fails. Reviewers share the worktree: a snapshot before they start and another
after all reviews and their classifications finish enforce read-only access as a group across
that whole window. A worktree change fails the run and names the whole group, including when a
reviewer's step also errors or another review has already been classified.

With one reviewer, replies remain `review-<round>.md` and the terminal label remains
`km <run> · reviewer`. Multiple reviewers are numbered in config order as `reviewer-1`,
`reviewer-2`, and so on, with replies `review-<round>-<n>.md` and labels
`km <run> · reviewer-<n>`. A classified review has a sibling `.verdict` file; a bare verdict skips
that file and the classifier step. Replies live under
`.kapellmeister/runs/<run>/` in the worktree.

## Repository configuration

```toml
[test]
verify = "scripts/verify"           # kapellmeister runs this before planning and after each round
check  = "scripts/check {args}"     # implementers use this for targeted, failures-only runs

[tiers.claude_code]                 # override any provider's tier model or effort
large = { model = "claude-fable-5-1" }

[steps.implement]                   # a step's default tier and the bounds sizing stays within
tier = "medium"
min  = "medium"
max  = "large"

[sizing]
mode = "shadow"                     # off, shadow (record only) or on (apply)
decider = "planner"                 # planner, or command
command = ""                        # for decider = "command"
escalate_after = 2                  # rounds without approval before the implementer moves up; 0 is off
```

Without `[test]`, kapellmeister falls back to executable `.kapellmeister/verify` and `.kapellmeister/check` scripts; `-verify`
overrides both.

### Tiers

Each step runs on a tier (`small`, `medium` or `large`). `km config` prints the full table; the
defaults are:

| Provider | small | medium | large |
|---|---|---|---|
| claude_code | claude-haiku-5-5, medium effort | claude-sonnet-5-5, high | claude-opus-5-5, xhigh |
| codex | gpt-6-luna, medium | gpt-6.1-sol, high | gpt-6-astra, high |
| copilot_cli | claude-haiku-5.5 | claude-sonnet-5.5 | claude-opus-5.5 |
| antigravity_cli | gemini-3.8-flash-low | gemini-3.8-flash-high | gemini-4-argon-high |

| Step | default | bounds |
|---|---|---|
| plan | medium | medium–large |
| implement | medium | small–large |
| review | medium | small–large |
| gate | medium | medium–large |
| test | medium | medium–large |
| classify | small | small |

A pin such as `claude@large` or `codex:km-codex@small` replaces the step's default tier and sizing
does not move it. The repository's `[steps.<step>]` min and max still clamp it: a gate pinned to
small runs at medium with the default gate floor. Pins work in `[chairs]`, ensembles and role flags.
A pin needs a `[tiers.<provider>]` table (built in or configured) with the tier it resolves to;
`km run` refuses to start otherwise.
An explicit model (`provider:profile:model`) cannot also carry `@tier`.

Models must be IDs (CAO rejects display names). Effort applies to Claude Code and Codex only,
because CAO cannot pass it to the others; `effort = "inherit"` uses the CLI's own setting. kapellmeister
installs the `km-claude-<effort>` and `km-codex-<effort>` CAO profiles it needs on first use;
chairs on other profiles need a `<profile>-<effort>` copy installed by hand.

### Sizing

After planning, a decider rates the task `small`, `medium` or `large`:

- `planner`: the planner ends its plan with `Size: <tier> — <reason>`.
- `command`: kapellmeister runs the command in the worktree with `{"task", "plan", "tiers"}` as JSON on
  stdin, and reads `{"size", "reason"}` from stdout. This is how an external decider, such as a
  CAO `model.route` package, plugs in. The command sees the task and plan, so point it only at a
  service you trust with them.

In `shadow` mode the answer is recorded and the steps keep their defaults; in `on` mode the
implement, review, gate and test steps use it, clamped to each step's bounds. A missing or invalid
answer falls back to the defaults. `-sizing` overrides the mode for one run.

After every `escalate_after` rounds without approval, the implementer moves up one tier (within
its bounds) in a fresh terminal, including when its starting tier is pinned. Set `escalate = false`
in the selected ensemble to turn escalation off for that run. `true` or an omitted key leaves
`[sizing] escalate_after` in charge; an ensemble cannot enable escalation when the repository sets
`escalate_after = 0`.

`km run` and `km play` exit 0 only for an `approved` outcome. Every other outcome, including `untested`
(approved but testing timed out), a failed, interrupted or rounds-exhausted run, exits 1 after
printing the outcome and diff-stat. Missing or unknown subcommands exit 2.

## Checked-in lessons

A person can give chairs guidance from earlier runs by editing Markdown files under
`.kapellmeister/learned/` and opening a pull request for review. km only reads these files; it never
creates or updates them, and never copies proposed lessons or agent memory into them.

A part file (`planner.md`, `implementer.md`, `reviewer.md`, `gate.md`, `classifier.md` or `tester.md`)
reaches every chair playing that part. When playing an ensemble file, a chair also receives
`<section>.<chair>.md`, using its existing section and chair names: for example,
`review.security.md` for `review.security@thorough`. The part file comes first. Plain `[chairs]`
and `[ensembles.NAME]` tables receive only part files. Both `km run` and `km play` add the lessons
to each step's briefing as guidance from earlier runs reviewed by a person; the task and the
repository's agent instruction files take precedence. No files means no added section.

km reads lessons once from the committed tree at the run's base commit, before verification or
any chair starts. Working files, including edits or new lessons made during the run, cannot
change that run's briefings. Merge the lesson pull request before starting a run that needs it.

`[run] lessons_budget` limits lesson text to 4,000 bytes per chair by default, shared between the
part and chair files. Set it in your user config or the repository config; the repository wins.
km keeps whole lines from the top, counting a newline for each line, and stops at the first line
that does not fit. It reports a cut in the output, briefing and telemetry without failing the run.
If the first line exceeds the budget, no lesson section is added, but the cut is still recorded.
Symbolic links, non-regular files and files containing control characters other than newline and
tab are skipped with a warning naming the file. This includes CRLF files.

## Report

`km report [DIR...]` summarizes run records, oldest first, followed by totals by outcome,
repository and ensemble. With no directory it reads `<state>/runs`. Each directory is searched
at any depth, so `km report ~/kept-sandboxes` works across kept sandbox copies. A directory
holding `result.json` is a run; one holding only `telemetry.jsonl` counts as unfinished. The
search stops inside each run directory, ignores directory symlinks and never follows record-file
symlinks. An explicitly chosen root directory may itself be a symlink. Copies with the same run
id count once, preferring a finished copy over an unfinished or unreadable one.

Started comes from the first telemetry record's timestamp, shown in UTC; it is unknown without
usable telemetry. Missing repository or ensemble fields show as `unknown`, as with an older km,
plain chairs that omit the ensemble, or a repository without a hosted origin. Unfinished runs
have unknown rounds and duration. Malformed records are diagnosed on stderr and counted as
unreadable without stopping the report. Text values are escaped for safe terminal output.
Unreadable or disappearing subdirectories are also diagnosed and counted as unreadable;
other directories are still searched. Directory handles keep searches and record reads from
following a symlink even if a directory is renamed during the report.
The command only reads the supplied directories.

`-since 2026-11-09` keeps runs started at or after midnight UTC on that date; RFC3339 timestamps
are also accepted. Runs without a known start are excluded by this filter.
`-repository github.com/acme/app` matches the recorded repository exactly and excludes unknown
repositories. Both filters can be combined, before or after directories. `-json` emits the same
ordered runs and totals as a JSON document, omitting unrecorded fields from individual runs.
Nonprinting characters are escaped in JSON too, preserving their values when decoded.
With no matches the table says `no runs found`; JSON contains an empty runs array and zero totals.

For example, `km report ~/kept-sandboxes` can print:

```text
STARTED              REPOSITORY           ENSEMBLE  OUTCOME     ROUNDS   TOOK
2026-11-09 14:02:11  github.com/acme/app  thorough  approved    2        18m31s
2026-11-10 09:40:52  github.com/acme/app  unknown   failed      1        4m2s
2026-11-10 16:15:03  unknown              unknown   unfinished  unknown  unknown

runs: 3
by outcome
  approved    1
  failed      1
  unfinished  1
by repository
  github.com/acme/app  2
  unknown              1
by ensemble
  unknown   2
  thorough  1
```

## Telemetry

Run records (`config.json`, `telemetry.jsonl`, `result.json` and `replies/`) live in
`<state dir>/runs/<run id>/`. Tester scratch lives separately in `<state dir>/scratch/<run id>/`
and is left in place after the run.

Each run writes `telemetry.jsonl` under `~/.kapellmeister/state/runs/<run>/`: one record per step with its chair, tier, effort, tier source (`default`,
`pinned`, `sized`, `escalated` or `explicit`), the model CAO reported, durations and verdicts, plus `size`
and `escalate` records for each decision. Lessons add a `lessons` step record and a
`lessons-proposed` summary (also recorded when collection is skipped); the step uses the `gate` tier. A seated tester adds `test-plan`, `test`,
`classify-test`, `verdict-test` and any `left-running` records; both tester steps use the `test` tier. With several reviewers,
their review, nudge, classification and verdict records include `chair_name` (`reviewer-1`, `reviewer-2`, etc.).
A failed review has verdict `FAILED` and its error; single-reviewer records omit `chair_name`.
`verdict-review`, `verdict-gate` and `verdict-test` records include `verdict_source: "reply"` when a
bare last line supplies the verdict; these replies produce no corresponding `classify-*` record.
Classified verdicts omit `verdict_source`. A review's `classify-review` and verdict records can
interleave with other reviewers' progress and telemetry while their reviews are still running.

Steps also record `usage` from Claude Code and Codex session logs when readable. A known usage has
`known: true`, separate `input_tokens`, `output_tokens`, `cache_write_tokens` and `cache_read_tokens`,
and a `models` map with the same counters for each model named in the log. Claude input excludes
cache reads and writes. Codex input has cached reads subtracted; its reported cache writes are kept
separate without subtracting them from input (the real logs inspected reported zero cache writes).
Output includes reasoning tokens. km records tokens, without converting them to money.

Usage covers complete log lines appended during one step attempt, including the reply wait. Earlier
steps on a reused occupant are excluded, and each nudge has its own usage. Windows for steps sharing
a chair or an explicitly reused terminal are serialized. Claude streamed messages are counted once
by message id, using the last usage; Codex cumulative counts are differenced for the attempt. Claude
logs are found under `CLAUDE_CONFIG_DIR` (default `~/.claude`); Codex logs under `CODEX_HOME` (default
`~/.codex`), in today's and yesterday's session directories, matched to the worktree. If several
logs grow, exactly one must name the step's reply path in its appended lines.

Unavailable usage has `known: false` and a `reason`: `unsupported-provider` (Copilot CLI and
Antigravity), `no-log`, `ambiguous-log`, `unreadable-log` or `no-usage`. Unknown usage omits token
counters; it never means zero. Missing, unreadable, truncated, malformed or oversized logs never
fail the step. Reads are limited to 64 MiB of growth per log and 4 MiB per line; partial last lines
and writes arriving after the attempt closes are excluded. km reads log metadata, counts and model
names, and checks reply paths in memory; it never stores prompts, replies or file contents from
these logs. Non-step records omit usage, and older records without usage remain readable.

`result.json` includes `usage` summed over known steps, with per-model counters and `unknown_steps`
when any steps have unknown usage. Its `known` is true when at least one step has known usage; totals
can therefore be incomplete when `unknown_steps` is nonzero. Without a configured usage meter,
records and results omit usage.

A `lessons` record identifies each briefed chair by `part` and `address` (or `chair_name` for
unaddressed reviewers), and records the lesson `base` commit, ordered `files` with their `path`,
`sha256` of the entire committed content and original `bytes`, any `skipped` paths and reasons,
the number of kept `bytes` and whether text was `cut`. Hashes cover the whole file even when its
text was cut. Chairs with no lesson files or skipped candidates add no record.

A step record's `chair` carries its `address`, `section.chair@ensemble`, when the run plays an ensemble
file or `km play` plays a standing ensemble; with plain `[chairs]` or an `[ensembles.NAME]` table the
address is omitted. In a run from an ensemble file, `blocked` and `unblocked` name a chair by that address;
reviewers keep `reviewer-N`.

Beside the telemetry, `result.json` records the `ensemble` the run used (the `-ensemble` name, or the
standing ensemble's for `km play`; omitted for plain `[chairs]`) and the `repository`: the worktree's
`origin` remote as `host/owner/name`, without scheme, credentials, port or `.git`. It is omitted when
there is no `origin` or it names no host, such as a local path.

`result.json` also records the worktree's starting commit as `base`. Its `diff_stat`
compares the final tree against that base, including untracked non-ignored files and changes committed
during the run. Generating this report uses a temporary index and leaves the worktree's index unchanged.
If HEAD moves, the result also records `head` and `head_moved: true`, and km prints
`  HEAD moved: <base short> → <head short> "<subject>" — the run's changes are in that commit`.
km leaves the commit and worktree in place; the existing tree check still fails a read-only step
that moves HEAD.

`km run` keeps every step in one CAO session, held open by an idle podium terminal, and deletes
that session when the run finishes or is interrupted. `km play` watches its standing session and
leaves it standing.

While steps run, `[run] blocker_poll` sets how often km checks the session for blocked chairs
(default `"20s"`; `"0"` disables watching). Both user and repository config may set it; the repository
value wins. Progress reports a new or changed blocker once:

```text
  blocked: test[1] tester — Permission rule Bash(cp*) requires confirmation for this command. — attach with tmux attach -t cao-km-...
  unblocked: test[1] tester
```

Use the attachment command to inspect and answer the prompt. km only reports it. Telemetry records
`blocked` and `unblocked` with the step, round, chair, terminal and blocker line. A forced kill or crash
can leave the session running.

## License

Apache License 2.0. See [LICENSE](LICENSE).
