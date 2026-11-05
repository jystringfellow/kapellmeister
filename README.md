# kapellmeister

kapellmeister runs a plan → implement → review → gate loop over coding agents (Claude Code, Codex, Copilot
CLI, Antigravity) through [CLI Agent Orchestrator](https://github.com/awslabs/cli-agent-orchestrator)
(CAO), repeating implement and review until the gate approves or the rounds run out. Every run
works in a throwaway detached worktree and leaves its change uncommitted for you to ship.

```sh
km run -repo ~/Code/app -task-file task.md [-ensemble thorough]  # one pod
km preflight -worktree DIR [-tier large]          # check each agent starts and answers
km config [-worktree DIR]                         # show the effective settings and their files
```

Seats default to claude plans, codex implements, copilot reviews, claude gates and classifies.
Override any seat with `-planner`, `-implementer`, `-reviewer`, `-gate` or `-classifier`, as an
agent (`claude`, `codex`, `copilot`, `agy`) or `provider:profile[:model]`. Append `@small`,
`@medium` or `@large` to pin a seat's tier, for example `-reviewer claude@large`. A seat with an
explicit model always runs that model and cannot also use `@tier`.

The reviewer role can have several seats. Use `-reviewer copilot,claude@large` to replace the
whole configured reviewer list for one run. Each reviewer runs concurrently in its own CAO
terminal and resolves its tier separately: pinned reviewers keep their tier, while unpinned
reviewers follow sizing. Other roles have one seat.

## Configuration

kapellmeister layers two files over its built-in defaults; flags override both for one run. Every key is
optional, and unknown keys are an error.

- **`~/.kapellmeister/config.toml`** (`$KAPELLMEISTER_HOME` moves it) holds what depends on your machine and accounts:
  `[cao]`, `[seats]`, `[ensembles]`, timeouts, and the model each tier means for you.
- **`.kapellmeister/config.toml`** in the repository holds the contract everyone working in it shares:
  `[test]`, step tiers and floors, rounds and sizing.

The repository's values win for `[run]` rounds and retries, `[steps]` and `[sizing]`; yours win
for `[tiers]`, because only you know what your accounts can run. `[cao]`, `[seats]` and `[ensembles]`
are machine-only and `[test]` is repository-only; putting one in the wrong file is an error.
`$CAO_API_BASE_URL` overrides `[cao] url`. State lives in `~/.kapellmeister/state` (`$KAPELLMEISTER_STATE_DIR`).

```toml
# ~/.kapellmeister/config.toml
[cao]
url = "http://127.0.0.1:9889"

[run]
timeout = "40m"                     # each agent step
reply_grace = "15m"
preflight_timeout = "4m"

[seats]                             # agent or provider:profile; optional @tier, or :model
implementer = "claude"
reviewer = "copilot"

[ensembles.thorough]
reviewer = ["copilot", "claude@large"]
gate_when = "any"
gate = "claude@large"
escalate = false

[tiers.copilot_cli]
large = { model = "claude-sonnet-5.5" }
```

Select a named ensemble for a run with `km run -ensemble thorough` alongside the usual task and
repository flags. Its seats overlay `[seats]`, then built-in defaults for omitted roles. Without
`-ensemble`, the run uses `[seats]` as usual. Role flags override the selected ensemble's seats;
unknown ensemble names are an error listing the defined names. `km config` lists each ensemble
and its seats. Ensembles belong only in `~/.kapellmeister/config.toml`, because they name agents
that depend on your accounts. In `[seats]` and ensembles, `reviewer` accepts either a single
string or an array such as `reviewer = ["copilot", "claude@large"]`. Other roles reject arrays.
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
If approval is withheld, the implementer gets the gate file when present, every available review
file, and the verification output. When the gate ran, its blocking findings supersede reviewer
requests; findings it classifies as nits need no change. Each round without approval still counts
toward escalation.

A reviewer whose step errors or leaves no reply after its one nudge is recorded as `FAILED`.
It counts as not approving for `gate_when`; surviving reviews continue through the round, and
the gate is told which reviewers failed so it can weigh the missing review. If every reviewer
fails, the run fails. Reviewers share the worktree: a snapshot before they start and another
after they all finish enforce read-only access as a group. A worktree change fails the run and
names the whole group, including when a reviewer's step also errors.

With one reviewer, replies remain `review-<round>.md` and the terminal label remains
`km <run> · reviewer`. Multiple reviewers are numbered in config order as `reviewer-1`,
`reviewer-2`, and so on, with replies `review-<round>-<n>.md` and labels
`km <run> · reviewer-<n>`. Each review has a sibling `.verdict` file. Replies live under
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
| classify | small | small |

A pin such as `claude@large` or `codex:km-codex@small` replaces the step's default tier and sizing
does not move it. The repository's `[steps.<step>]` min and max still clamp it: a gate pinned to
small runs at medium with the default gate floor. Pins work in `[seats]`, ensembles and role flags.
A pin needs a `[tiers.<provider>]` table (built in or configured) with the tier it resolves to;
`km run` refuses to start otherwise.
An explicit model (`provider:profile:model`) cannot also carry `@tier`.

Models must be IDs (CAO rejects display names). Effort applies to Claude Code and Codex only,
because CAO cannot pass it to the others; `effort = "inherit"` uses the CLI's own setting. kapellmeister
installs the `km-claude-<effort>` and `km-codex-<effort>` CAO profiles it needs on first use;
seats on other profiles need a `<profile>-<effort>` copy installed by hand.

### Sizing

After planning, a decider rates the task `small`, `medium` or `large`:

- `planner`: the planner ends its plan with `Size: <tier> — <reason>`.
- `command`: kapellmeister runs the command in the worktree with `{"task", "plan", "tiers"}` as JSON on
  stdin, and reads `{"size", "reason"}` from stdout. This is how an external decider, such as a
  CAO `model.route` package, plugs in. The command sees the task and plan, so point it only at a
  service you trust with them.

In `shadow` mode the answer is recorded and the steps keep their defaults; in `on` mode the
implement, review and gate steps use it, clamped to each step's bounds. A missing or invalid
answer falls back to the defaults. `-sizing` overrides the mode for one run.

After every `escalate_after` rounds without approval, the implementer moves up one tier (within
its bounds) in a fresh terminal, including when its starting tier is pinned. Set `escalate = false`
in the selected ensemble to turn escalation off for that run. `true` or an omitted key leaves
`[sizing] escalate_after` in charge; an ensemble cannot enable escalation when the repository sets
`escalate_after = 0`.

## Telemetry

Each run writes `telemetry.jsonl` under `~/.kapellmeister/state/runs/<run>/`: one record per step with its seat, tier, effort, tier source (`default`,
`pinned`, `sized`, `escalated` or `explicit`), the model CAO reported, durations and verdicts, plus `size`
and `escalate` records for each decision. With several reviewers, their review, nudge,
classification and verdict records include `seat_name` (`reviewer-1`, `reviewer-2`, etc.).
A failed review has verdict `FAILED` and its error; single-reviewer records omit `seat_name`.
