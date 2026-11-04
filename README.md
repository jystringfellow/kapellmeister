# oko

oko runs a plan → implement → review → gate loop over coding agents (Claude Code, Codex, Copilot
CLI, Antigravity) through [CLI Agent Orchestrator](https://github.com/awslabs/cli-agent-orchestrator)
(CAO), repeating implement and review until the gate approves or the rounds run out. Every run
works in a throwaway detached worktree and leaves its change uncommitted for you to ship.

```sh
oko run -repo ~/Code/app -task-file task.md        # one pod
oko rig -repo ~/Code/app -task-file task.md        # every agent in every seat, one pod each
oko preflight -worktree DIR [-tier large]          # check each agent starts and answers
oko config [-worktree DIR]                         # show the effective settings and their files
```

Seats default to claude plans, codex implements, copilot reviews, claude gates and classifies.
Override any seat with `-planner`, `-implementer`, `-reviewer`, `-gate` or `-classifier`, as an
agent (`claude`, `codex`, `copilot`, `agy`) or `provider:profile[:model]`. A seat with an explicit
model always runs that model.

## Configuration

oko layers two files over its built-in defaults; flags override both for one run. Every key is
optional, and unknown keys are an error.

- **`~/.oko/config.toml`** (`$OKO_HOME` moves it) holds what depends on your machine and accounts:
  `[cao]`, `[seats]`, `[rig]`, timeouts, and the model each tier means for you.
- **`.oko/config.toml`** in the repository holds the contract everyone working in it shares:
  `[test]`, step tiers and floors, rounds and sizing.

The repository's values win for `[run]` rounds and retries, `[steps]` and `[sizing]`; yours win
for `[tiers]`, because only you know what your accounts can run. `[cao]`, `[seats]` and `[rig]`
are machine-only and `[test]` is repository-only; putting one in the wrong file is an error.
`$CAO_API_BASE_URL` overrides `[cao] url`. State lives in `~/.oko/state` (`$OKO_STATE_DIR`).

```toml
# ~/.oko/config.toml
[cao]
url = "http://127.0.0.1:9889"

[run]
timeout = "40m"                     # each agent step
reply_grace = "15m"
preflight_timeout = "4m"

[seats]                             # an agent, or provider:profile[:model]
implementer = "claude"
reviewer = "copilot"

[rig]
agents = ["claude", "codex", "copilot", "agy"]

[tiers.copilot_cli]
large = { model = "claude-sonnet-5.5" }
```

## Repository configuration

```toml
[test]
verify = "scripts/verify"           # oko runs this before planning and after each round
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

Without `[test]`, oko falls back to executable `.oko/verify` and `.oko/check` scripts; `-verify`
overrides both.

### Tiers

Each step runs on a tier (`small`, `medium` or `large`). `oko config` prints the full table; the
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

Models must be IDs (CAO rejects display names). Effort applies to Claude Code and Codex only,
because CAO cannot pass it to the others; `effort = "inherit"` uses the CLI's own setting. oko
installs the `oko-claude-<effort>` and `oko-codex-<effort>` CAO profiles it needs on first use;
seats on other profiles need a `<profile>-<effort>` copy installed by hand.

### Sizing

After planning, a decider rates the task `small`, `medium` or `large`:

- `planner`: the planner ends its plan with `Size: <tier> — <reason>`.
- `command`: oko runs the command in the worktree with `{"task", "plan", "tiers"}` as JSON on
  stdin, and reads `{"size", "reason"}` from stdout. This is how an external decider, such as a
  CAO `model.route` package, plugs in. The command sees the task and plan, so point it only at a
  service you trust with them.

In `shadow` mode the answer is recorded and the steps keep their defaults; in `on` mode the
implement, review and gate steps use it, clamped to each step's bounds. A missing or invalid
answer falls back to the defaults. `-sizing` overrides the mode for one run.

After every `escalate_after` rounds without approval, the implementer moves up one tier (within
its bounds) in a fresh terminal.

## Telemetry

Each run writes `telemetry.jsonl` under `~/.oko/state/runs/<run>/`: one record per step with its seat, tier, effort, tier source (`default`,
`sized`, `escalated` or `explicit`), the model CAO reported, durations and verdicts, plus `size`
and `escalate` records for each decision.
