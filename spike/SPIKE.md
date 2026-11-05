# kapellmeister spike: CAO seat behaviour

Run 2026-11-03 against CAO 2.5.4 (`main` at `ab9bbaab`), tmux backend. Raw records are in
`results/results.jsonl`; `go run . report` prints them.

Seats: Claude Code `claude-haiku-5-5` (effort low), Codex `gpt-6-luna` (effort low), Copilot
`auto` (Fusion), Copilot `claude-haiku-5.5`, Antigravity `Gemini 3.8 Flash (Low)`.

## Matrix

| Question | Claude Code | Codex | Copilot | agy |
|---|---|---|---|---|
| Instruction files loaded from the worktree root | `CLAUDE.md` only | `AGENTS.md` only | `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.github/copilot-instructions.md` | `AGENTS.md`, `GEMINI.md` |
| No leakage between repos | yes | yes | yes | yes |
| Model honoured | yes | yes (per terminal record) | yes; `auto` hides the real pick | yes |
| Screen capture faithful | no: Markdown rendered away | no | no: TUI chrome, and once a mid-task screen | no |
| Reply file faithful | yes, tabs become spaces | whitespace only, tabs kept | yes, tabs become spaces | exact |
| Found the planted bug (2 runs) | 2/2 | 2/2 | Haiku 2/2, Fusion 1/2 | 2/2 |
| Classifier verdict matched | 2/2 | 2/2 | 3/4; the miss was UNCLEAR on a bad capture | 2/2 |
| Read-only gate | enforced (`role: reviewer`) | **not** with `sandbox_mode`; enforced with `codexProfile` | enforced (shell denied) | complied, prompt only |

## Findings

1. **Starting each seat at its repo's worktree root works across repos.** No seat saw the other
   repo's canary.
2. **Each CLI reads a different set of instruction files.** Claude Code needs `CLAUDE.md`; agy
   reads `AGENTS.md` and `GEMINI.md`; Copilot reads all four. A repo with only `AGENTS.md` gives
   Claude Code nothing.
3. **Screen capture is not a reply channel.** Every CLI's `last_message` differed from the text it
   was asked to repeat, and one Copilot review captured only `Read clamp_test.go 9 lines read`.
   **A file the agent writes in the worktree is exact**, apart from tab expansion.
4. **Lenient verdicts work.** Prose reviews plus a one-word classifier on Haiku gave the right
   verdict every time the capture held the review, and UNCLEAR when it did not, which fails safe.
   With the review read from a file instead of the screen, the bad-capture case goes away.
5. **A step's `model` overrides the profile's model** (`claude-sonnet-5-5` over the profile's
   Haiku, honoured). The per-call `model` must match `^[A-Za-z0-9._:/-]+$`, so agy's display names
   go in the profile, or use agy's IDs such as `gemini-3.8-flash-low`.
6. **Copilot `auto` is Copilot Fusion.** It chose GPT-6 Luna both times, and says so only on
   screen. CAO records the model as `auto`, so telemetry must parse the Fusion line or avoid `auto`.
   Copilot also sometimes refuses to disclose instruction content.
7. **Copilot reasoning effort cannot be set through a CAO profile**, though the CLI has
   `--reasoning-effort`.
8. **agy cannot be a gate.** It declined to edit only because the prompt asked it to.
9. **`workflow_return` output is not visible to script-tier workflows**: only YAML steps with an
   `output_schema` read it. The file is the exact channel for scripts too.
10. **Script workflows declare inputs** in a module-level `INPUTS` dict, and the CLI types `--input`
    values (`seed=5` is an int). Plan approval is on and refuses the first run of each new plan;
    the plan changes with the inputs, so every new seed or worktree path needs a fresh approval.
11. **Codex was blocked by its own UI**: "1 hook needs review before it can run" (a `SessionStart`
    hook), with CAO launching it in YOLO mode. Each step waited out its timeout. Trust is held per
    launch context: hooks trusted in the user's own `codex` still showed as modified under CAO, and
    had to be trusted once inside a CAO-launched session. After that every Codex step ran in 14–33 s.

12. **The script-tier workflow works for us once approved** (`run-c21a79b41df24b79`, 46 s): a seeded
    random seat pick, a `subprocess` `go test`, `working_directory` set to the detached worktree, and
    a review written to a file, which was complete and correct. The same step's captured
    `last_message`, stored in `workflow_run_step.result_json`, was Copilot's
    `$ Shell Show most recent commit…` progress line, which confirms finding 3 inside workflows.
13. **CAO keeps little telemetry about the run itself.** `cao workflow result` and `status` show only the
    step state and attempts; the `emit_output` payload did not appear in either, and
    `workflow_run_event` had no rows for this script run. CAO does record `terminals`
    (model, `model_honored`), `workflow_run` (inputs, plan, git baseline) and `decision_records`.
    kapellmeister must record its own rounds, verdicts, durations and outcomes.
14. **Codex's `codexConfig.sandbox_mode: read-only` does not hold.** CAO launches Codex with
    `--yolo`, and the gate seat edited `clamp.go`. A `codexProfile` pointing at a
    `$CODEX_HOME/<name>.config.toml` with `sandbox_mode = "read-only"` drops `--yolo`, and that gate
    refused: "I'm not able to edit files in this read-only workspace." Codex gates must use
    `codexProfile`.
15. **A step's `provider` overrides the profile's provider, but the profile's model comes along.**
    The Claude profile run with `provider: codex` launched Codex on `claude-haiku-5-5`, and Codex
    replied "The 'claude-haiku-5-5' model is not supported when using Codex with a ChatGPT account."
    CAO still recorded the step as `completed` with `model_honored: true`. A bare profile with
    `provider: codex` and `model: gpt-6-luna` worked. So kapellmeister must send provider and model together,
    and cannot trust `completed` or `model_honored` as success; it needs its own check of the reply
    file.
16. **Marketplace plugins re-trigger Codex hook review.** Codex keys hook trust to a content hash in
    `~/.codex/config.toml`. A git marketplace that auto-updates re-extracts the plugin, the hash
    changes, and the next launch stops at "1 hook needs review before it can run", which a
    CAO-launched seat cannot answer. The per-seat preflight caught it in under a minute, where the
    rig before it lost three 40-minute timeouts.
17. **Seats end their turn while background work is still running.** Claude Code ("1 shell still
    running") and agy (a running `go test` plus a 30 s timer) both backgrounded the full suite and
    went idle, so CAO reported `completed` before the reply file existed. A nudge sent straight away
    recovered Claude but returned from agy in 4 s, with agy still busy. kapellmeister now waits up to
    `-reply-grace` (15 min) for the reply file before nudging, and tells implementers to run
    targeted tests in the foreground and leave the full suite to kapellmeister's verify.
18. **The full workgraph suite flakes under load.** With four pods verifying back to back, a timing
    fact (`TestGitHubPRWatchPushHintWakesThePoller`) failed once and passed on retry. The baseline
    and one retry tell real failures from flakes; seats were told about the retry and correctly
    judged the flake unrelated.
19. **Effort is a profile setting in CAO 2.5.4, never a run-step field.** The model goes per step
    (`model` on run-step; every provider honours it, agy only when its profile pins no model), but
    effort comes only from `claudeConfig.effort` (`--effort`) and `codexConfig.model_reasoning_effort`.
    Copilot (`--reasoning-effort`) and agy (`--effort`) have flags CAO does not pass; agy model IDs
    carry the effort instead (`gemini-3.8-flash-high`). Run-step rejects display names such as
    `Gemini 3.8 Flash (Low)`; it needs IDs matching `^[A-Za-z0-9._:/-]+$`. `POST
    /agents/profiles/install` refuses local paths, so profiles install through `cao install`.
    Checked live: every agent at every tier launched with the expected `--model`/`--effort`.
20. **Jev (CAO's `model.route` decider) sizes only ephemeral helpers, and is not released.** It
    needs CAO built from main (#1136, and #1171 for `effort.route`), sizes helpers created with
    `model_tier: "auto"` rather than installed profiles, and the hosted decider sees a redacted task
    description. Tracking: awslabs/cli-agent-orchestrator#1182.

## Decisions

- **Seats inherit the user's local CLI setup by default** (their hooks, plugins, skills and
  instructions), and each seat can opt into an isolated home instead (`CODEX_HOME`,
  `CLAUDE_CONFIG_DIR` and similar) per seat in config.
- **kapellmeister runs a preflight per seat** that launches it once and fails fast on a trust or hook-review
  screen, telling the user to trust it inside a CAO-launched session, rather than waiting out a
  step's timeout.
- **Replies come from files, never from screen capture**; a missing or empty reply file fails the
  step whatever CAO's status says.
- **Reviewer and gate seats stay writable; kapellmeister enforces read-only itself**: it snapshots the tree
  (status, `git diff HEAD --binary`, HEAD) before and after each review and gate step and fails the
  run on any change. This works for every provider, so agy can sit as gate.
- **A missing reply is awaited, then nudged once** in the same terminal before the step fails.
- **Verify runs once as a baseline before planning** and retries a failure once; a failing baseline
  stops the run before any agent spends tokens.
- **kapellmeister drives its own loop over run-step rather than a CAO workflow.** Script workflows need a
  fresh `cao workflow approve` whenever their inputs change (every new worktree), cannot read
  `workflow_return`, and record almost nothing about the run (findings 9, 10, 12, 13), so kapellmeister would
  still own replies, telemetry and approval-free runs. Rechecked 2026-11-04: CAO main has not
  changed workflows since.
- **Seats run on tiers (small, medium, large), not fixed models.** Each provider maps a tier to a
  model and, for Claude and Codex, an effort; kapellmeister installs `km-<agent>-<effort>` profiles as
  needed and passes the model per step. Each step has a default tier and bounds (gate never below
  medium, classify always small), all overridable in `.kapellmeister/config.toml`. A seat given an explicit
  `provider:profile:model` is never re-tiered.
- **A size decider picks the implement, review and gate tiers, shadow first.** The planner ends its
  plan with `Size: small|medium|large — reason`, or a configured command answers JSON on stdout,
  which is how Jev or JevK5 can slot in before CAO ships `model.route` for installed profiles. In
  shadow (the default) the answer is only recorded; `on` applies it within each step's bounds; no
  answer falls back to the defaults.
- **The implementer moves up a tier after every two rounds without approval**, in a fresh terminal,
  since a CLI cannot switch model mid-session; the earlier attempt stays uncommitted in the tree.

## Open

- OTel spans with `OTEL_SDK_DISABLED=false` (not tried).

# Ensemble spike: chairs, cues and restarts

Run 2026-11-05 against CAO 2.5.4, tmux backend, driven by hand over the HTTP API from a throwaway
repo. Chairs: `km-claude` (Haiku 5.5), `km-codex` (GPT-6 Luna), `km-copilot` (Haiku 5.5), `km-agy`
(`gemini-3.8-flash-low`), all `role: developer`, in one CAO session.

## Matrix

| Question | Claude Code | Codex | Copilot | agy |
|---|---|---|---|---|
| Boots as an extra terminal in one session | 13 s | 68 s | 7 s, once 47 s | 15 s |
| Idle cue from `sender_id=km` to reply file | 8 s | lost behind a modal; 5 s after Esc | 8 s | 12 s |
| Stays `processing` through a 45 s foreground `sleep` | no, `completed` at 14 s | no | yes | no |
| Queued cues delivered in order, none dropped by the inbox | yes | yes | yes | yes |
| Finished the task it was busy with | **no** | yes | yes | yes |
| `cao agent send-message` to a discovered sibling, `developer` role | refused | refused | refused | accepted |
| Same, with the `discovery` grant | accepted | not run | not run | not run |
| `km-send` stub (append to a file queue) from the shell | yes | yes | not run (wedged) | yes |
| Inbox works for the terminal after a `cao-server` restart | no | no | no | no |

## Findings

1. **An ensemble fits in one CAO session.** `POST /sessions` boots the first chair and
   `POST /sessions/{name}/terminals` adds the rest, each with its own provider, model and working
   directory; all four honoured their model.
2. **Group labels give sections and ensembles for free.** `PATCH /terminals/{id}/group` with
   `["ens","strings"]`; `GET /terminals/{id}/siblings` returns the section by default and the whole
   ensemble with `depth=1`, scoped to the tmux session unless `cross_session=true`.
3. **`delivered` means pasted, not read.** The inbox marks a message delivered before it types it into
   the pane, and delivers one message per idle or completed status event. A message carries no
   envelope: the receiver sees only the text, and `sender_id` is used only for hook attribution, so
   every cue must say who sent it and where to reply.
4. **The inbox is not where obligations get lost; chairs are.** Claude, Codex and agy each reported
   `completed` while their `sleep 45` still ran (spike finding 17), so queued cues A and B arrived
   and ran first, in order. Codex and agy came back to the busy task; Claude never wrote its file,
   and did the same again in the restart run. Copilot stayed `processing` and took A and B after it
   finished.
5. **Codex's hook review screen swallows cues.** The marketplace hook prompt (finding 16) was up at
   boot while CAO reported Codex `idle`; the first cue was marked delivered and typed into the modal.
   `POST /terminals/{id}/key?key=Escape` closed it, leaving the hook untrusted, and the resent cue ran
   in 5 s. Codex also showed "less than 10% of your weekly limit left", which CAO does not surface.
6. **Chair-to-chair messages need the `discovery` grant.** Without it, `cao agent send-message` from
   Claude, Codex and Copilot was refused even for a sibling each had just found. The check lives in the
   MCP/CLI process next to the agent; the HTTP API authorises by scope only and takes any
   `sender_id`. agy's message got through, apparently by posting to the endpoint directly. Booting a
   chair with `allowed_tools=…,discovery` let Claude message Copilot.
7. **Claude Code's own sandbox blocks the CAO server.** Both Claude chairs hit "Operation not
   permitted" on `127.0.0.1:9889` and reran the curl and `cao agent` calls with the sandbox disabled on
   their own initiative.
8. **One misread status wedges a chair's inbox.** Copilot finished its turn after printing CAO's
   refusal text, and CAO has read it as `error` ever since. The inbox delivers only to idle or completed
   terminals, so agy's message and a later cue stayed pending; an Enter key did not clear it and the
   reconcile sweep uses the same gate.
9. **A Copilot chair hung with "0 AIC used".** It sat at "Working" for more than nine minutes with a
   second copy of the cue in Copilot's own queue, though CAO delivered it once; Escape did not stop it.
10. **A file queue works as the reply path.** A `km-send` stub that appends a JSON line to a file,
    stamped with `CAO_TERMINAL_ID`, worked from Claude, Codex and agy within about 20 s. It needs no
    network, so Claude's sandbox does not get in the way, and km needs no terminal id.
11. **A `cao-server` restart leaves every existing terminal deaf.** The tmux panes survive and running
    turns finish (agy and Codex wrote their files after the kill), but CAO creates a terminal's FIFO
    reader only in `create_terminal`. After the restart every terminal read `unknown`, and pending
    and new inbox messages stayed pending for the 3.5 minutes watched. `POST /terminals/{id}/input`
    still reached the pane, and Claude acted on it.

## Implications for the team layer

- km owns the queue of obligations, as decided: a cue is done only when its reply lands in the km
  queue or reply file. km re-cues on a timeout and never trusts `delivered`, `completed` or `error`.
- Chairs reply through a `km send` command that appends to km's queue, not through CAO's inbox; this
  needs no km terminal and works inside each CLI's sandbox.
- km routes chair-to-chair cues itself rather than granting `discovery`, so one path carries every
  cue, every cue gets an envelope (from, cue id, reply command), and km can fall back to direct input
  when a chair's status is `unknown` or stuck.
- Preflight looks for Codex's hook review screen on boot, and km detects a restarted `cao-server`
  (all chairs `unknown`) and either drives chairs by direct input or reboots them.
