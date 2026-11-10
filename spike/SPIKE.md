# kapellmeister spike: CAO chair behaviour

Run 2026-11-03 against CAO 2.5.4 (`main` at `ab9bbaab`), tmux backend. Raw records are in
`results/results.jsonl`; `go run . report` prints them.

Chairs: Claude Code `claude-haiku-5-5` (effort low), Codex `gpt-6-luna` (effort low), Copilot
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

1. **Starting each chair at its repo's worktree root works across repos.** No chair saw the other
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
    random chair pick, a `subprocess` `go test`, `working_directory` set to the detached worktree, and
    a review written to a file, which was complete and correct. The same step's captured
    `last_message`, stored in `workflow_run_step.result_json`, was Copilot's
    `$ Shell Show most recent commit…` progress line, which confirms finding 3 inside workflows.
13. **CAO keeps little telemetry about the run itself.** `cao workflow result` and `status` show only the
    step state and attempts; the `emit_output` payload did not appear in either, and
    `workflow_run_event` had no rows for this script run. CAO does record `terminals`
    (model, `model_honored`), `workflow_run` (inputs, plan, git baseline) and `decision_records`.
    kapellmeister must record its own rounds, verdicts, durations and outcomes.
14. **Codex's `codexConfig.sandbox_mode: read-only` does not hold.** CAO launches Codex with
    `--yolo`, and the gate chair edited `clamp.go`. A `codexProfile` pointing at a
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
    CAO-launched chair cannot answer. The per-chair preflight caught it in under a minute, where the
    rig before it lost three 40-minute timeouts.
17. **Chairs end their turn while background work is still running.** Claude Code ("1 shell still
    running") and agy (a running `go test` plus a 30 s timer) both backgrounded the full suite and
    went idle, so CAO reported `completed` before the reply file existed. A nudge sent straight away
    recovered Claude but returned from agy in 4 s, with agy still busy. kapellmeister now waits up to
    `-reply-grace` (15 min) for the reply file before nudging, and tells implementers to run
    targeted tests in the foreground and leave the full suite to kapellmeister's verify.
18. **The full workgraph suite flakes under load.** With four ensembles verifying back to back, a timing
    fact (`TestGitHubPRWatchPushHintWakesThePoller`) failed once and passed on retry. The baseline
    and one retry tell real failures from flakes; chairs were told about the retry and correctly
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

- **Chairs inherit the user's local CLI setup by default** (their hooks, plugins, skills and
  instructions), and each chair can opt into an isolated home instead (`CODEX_HOME`,
  `CLAUDE_CONFIG_DIR` and similar) per chair in config.
- **kapellmeister runs a preflight per chair** that launches it once and fails fast on a trust or hook-review
  screen, telling the user to trust it inside a CAO-launched session, rather than waiting out a
  step's timeout.
- **Replies come from files, never from screen capture**; a missing or empty reply file fails the
  step whatever CAO's status says.
- **Reviewer and gate chairs stay writable; kapellmeister enforces read-only itself**: it snapshots the tree
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
- **Chairs run on tiers (small, medium, large), not fixed models.** Each provider maps a tier to a
  model and, for Claude and Codex, an effort; kapellmeister installs `km-<agent>-<effort>` profiles as
  needed and passes the model per step. Each step has a default tier and bounds (gate never below
  medium, classify always small), all overridable in `.kapellmeister/config.toml`. A chair given an explicit
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
12. **A deleted terminal can answer DELETE with 409 while GET says 404.** After `DELETE
    /terminals/{id}` succeeded on an agy terminal, CAO 2.5.4 answered a second DELETE with 409
    "cleanup deferred for terminal ...; retry delete after residual Grok processes exit" (naming
    Grok for an agy terminal), while `GET /terminals/{id}` already returned 404. `km reseat` treats
    a failed delete as done when the lookup is 404. Upstream candidate: make DELETE idempotent (404
    or 200 once the terminal is unregistered) and name the right provider.

13. **Run-step needs an existing named session; a podium keeps it alive.** Checked the installed
    CAO 2.5.4 `services/agent_step.py`, `services/session_service.py`, `api/main.py`,
    `clients/database.py` and `providers/claude_code.py` on 2026-11-06. The default shell Python
    cannot import CAO; the interpreter in `~/.local/share/uv/tools/cli-agent-orchestrator/bin/python`
    can. Live probes used isolated temporary scratch directories and state, sessions
    `cao-km-blocker-probe-5imho6vo` and `cao-km-blocker-lifecycle-ydg5973k`, and no existing sessions.
    `POST /sessions` creates its initial terminal; there is no empty-session operation. Run-step
    with `session_name` joined that session and its terminal's description was `km probe · tester`.
    An unknown session returned HTTP 404, `Session 'km-blocker-probe-5imho6vo-absent' not found`.
14. **Teardown removes the step terminal, leaving the session's podium.** A live run-step with
    `teardown:true` replied `OK` and completed; the session lookup afterwards contained just its
    initial terminal. Explicitly deleting the permission-blocked step also left its initial terminal
    and session alive. Both probe sessions were taken down with `km ensemble down` using their own
    temporary standing records and state directories. The second session lookup then returned 404.
    The temporary permission profile was removed. No prompt was answered.
15. **The session endpoint carries live status; the terminals endpoint does not.** Live
    `GET /sessions/{name}` returned a `session` object plus `terminals`, each with `status`,
    `description`, `id` and `tmux_session`. `GET /sessions/{name}/terminals` returned the DB rows
    with description and tmux session but no status. The initial Claude terminal reported `idle`.
16. **Claude's Bash permission prompt reports processing.** A temporary Claude profile with
    `permissionMode: default` and scratch `.claude/settings.json` asking for `Bash(cp*)` produced
    the task's exact permission dialog when asked to run `cp a b`. Both terminal and session
    lookups reported `processing` while the dialog remained visible. A separate `AskUserQuestion`
    dialog reported `waiting_user_answer`. This confirms both watcher paths are necessary.
17. **Full output is a rolling stream of terminal updates, not a rendered screen.** The live
    permission output placed cursor-column escapes between `Permission`, `rule`, `requires` and
    `confirmation`, and cursor-row updates between lines. Later spinner updates appended enough
    nonblank fragments to hide the dialog from a last-12-lines scan after naive escape stripping.
    Rendering the captured stream with a terminal emulator recovered the exact permission line;
    km renders it with `charmbracelet/x/vt` behind its CAO watcher, at CAO's default 220×50 pane
    size, draining the emulator's replies to terminal queries (an undrained reply blocks the write)
    and treating a render panic as no blocker. Replaying the captured live output through the
    watcher passed. No live unblock-after-answer probe was attempted; no prompt was answered.
    Rolling-buffer truncation or a resized pane can still limit reconstruction. CAO already renders
    the viewport internally (`get_history(visible_only=True)`, tmux `capture-pane -S 0`) for its
    status monitor, but `GET /terminals/{id}/output` offers only `full` and `last`.
18. **Run-step ignores description on reused terminals.** The reuse branch of CAO's
    `run_agent_step` validates the occupant but never updates its description. km must use
    `PATCH /terminals/{id}/description` before a reused cue so its standing chairs and kept
    implementer can be matched to the current step by the same label as new terminals.
    Upstream candidates: honour description on reuse; provide empty sessions,
    and pushed terminal status changes (or a status filter on the session listing). Pushed status
    would replace km's polling seam with a subscriber.

19. **Live km progress sees the permission prompt and cleans up on interrupt.** Ran the changed
    binary in scratch repository `km-blocker-e2e-htfkecfg` with isolated km home and state,
    `blocker_poll = "1s"`, preflight disabled, and an explicit-model Claude permission profile.
    An AGENTS instruction to run `cp a b` before writing the plan blocked the planner.
    Progress printed exactly one `blocked: plan planner` with the permission-rule line and
    `tmux attach -t cao-km-20261106-063834-30ded8`. Three seconds later the harness sent SIGINT;
    progress printed one `unblocked`, the run ended as failed, and session lookup returned 404.
    Both telemetry records named `plan`, `planner`, and the same terminal. No prompt was answered.
    The scratch repository had no commit, so its final diff-stat command reported missing HEAD;
    that did not prevent session cleanup. The temporary profile was removed (lookup 404).

20. **The right upstream fix is Claude's status, and CAO main already has it.** Checked on
    2026-11-06 against the installed 2.5.4 source and `awslabs/cli-agent-orchestrator` on GitHub.
    km reads screens only because Claude's tool-permission dialog reports `processing` (finding 16).
    In 2.5.4, `providers/claude_code.py` reports `waiting_user_answer` only for the footers
    `↑/↓ to navigate` and `Enter to confirm ·`, or a plan-approval dialog; the permission dialog's
    footer is `Esc to cancel · Tab to amend`. CAO PR #1107 (closes #1106, merged 2026-10-28) adds
    `Esc to cancel[ \t]*·[ \t]*Tab to amend` to that pattern, with Bash and Write permission
    fixtures. It is on `main`, 149 commits past `v2.5.4`, and PyPI's latest is still 2.5.4. Once a
    release carries it, km's watcher can treat `waiting_user_answer` as blocked and drop its
    screen reads and terminal emulator. Related open issue: #405 (AskUserQuestion and plan dialogs
    classifying as idle or completed). The other chairs km seats, from source only, not probed live:
    - **Codex** reports `waiting_user_answer` for its approval prompts (`APPROVAL_PROMPT_PATTERN`,
      numbered approval menu with a footer, the "Command Approval Required" modal; PR #567). It
      prompts only when launched without `--yolo`, which CAO uses when `allowedTools` contains `*`.
    - **Copilot** is always launched with `--allow-all`, so tool calls do not prompt. Its waiting
      detection covers folder trust, `press enter to continue` and `[y/n]` only, on 2.5.4 and main.
    - **Antigravity** is always launched with `--dangerously-skip-permissions`. Its waiting pattern
      also matches `Allow once`, `Allow always`, `Do you want to (allow|run|proceed)` and `[y/n]`
      for the prompts that flag does not suppress.

21. **Chair permission fields are available over HTTP; effort variants must preserve them.**
    Read the installed CAO 2.5.4 source on 2026-11-06 (no live sessions or profile writes):
    - `api/main.py:2875-2898`, `GET /agents/profiles/{name}`, returns
      `load_agent_profile(name).model_dump(exclude_none=True)`. It includes `permissionMode` and
      `allowedTools` when set, preserving `allowedTools: []`. The response resolves environment
      variables and includes other profile content; km decodes only `permissionMode`, `allowedTools`
      and `codexProfile`, and never prints the rest. The endpoint covers km's needs; no local profile reader or upstream
      profile-read candidate is needed.
    - `providers/claude_code.py:435-455`: explicit `permissionMode` selects
      `--permission-mode <mode>`; otherwise CAO uses `--dangerously-skip-permissions`. Exception:
      `allowedTools` containing `*` plus root euid omits the skip flag. km does not model the root
      exception. `bypassPermissions` is the explicit mode that bypasses prompts. Lines 566-576
      apply `--disallowedTools` when `allowedTools` is present without `*`, even if it is empty.
      Tool restrictions are separate from the permission mode.
    - `providers/copilot_cli.py:203,229-238`: always `--allow-all`; a present `allowedTools` list
      without `*` adds `--deny-tool` restrictions (including for an empty list).
    - `providers/antigravity_cli.py:318`: always `--dangerously-skip-permissions`.
    - `providers/codex.py:1375-1419`: `allowedTools` containing `*` forces `--yolo`, overriding
      `codexProfile`. Without `*`, `codexProfile` selects `--profile <name>`; without either,
      CAO still defaults to `--yolo`. This qualifies finding 20 and the task's assumption that
      Codex prompts without `*`. km uses `codexProfile` as well as `allowedTools` to identify the
      launch mode and copies `codexProfile` into effort variants. The `approvals` label means CAO
      uses `--profile`; the named Codex configuration controls the actual approval policy.
    - `services/install_service.py:1872-1895` guards ownership, then reimports a supplied profile
      with `write_profile(..., overwrite=True)`. Reinstall uses km's existing `cao install` CLI
      path and propagates failures; it is not an unconditional overwrite. As finding 19 notes,
      `POST /agents/profiles/install` rejects local paths, so HTTP cannot install km's generated
      file directly. A fake CLI tests installation and replacement without changing CAO's store.
    - A read-only `km config` check against local CAO, using temporary `KAPELLMEISTER_HOME` and
      `KAPELLMEISTER_STATE_DIR` in this throwaway worktree, printed all five default chair labels
      and one note counting four unguarded chairs. No session was created or profile installed.

22. **Permission labels and inheritance must include Codex's named policy.** The tester's first
    run found the original two-field mapping falsely reported default Codex launches as guarded,
    and effort variants lost the base's `codexProfile`. Rechecked the installed source on
    2026-11-06: `providers/codex.py:1387-1419` selects `--profile` only when `codexProfile` is set
    and `allowedTools` lacks `*`; otherwise it selects `--yolo`. The profile HTTP response exposes
    this field too. km carries it through reading, generation and mismatch comparison. Config
    projects the base's synchronized permissions for generated variants instead of reporting a
    stale installed variant; it does not install anything. Custom profiles keep their installed
    permissions. `models/agent_profile.py:9` permits only `default`, `acceptEdits`, `plan`, `auto`
    and `bypassPermissions`; km displays other modes as `unknown` without echoing arbitrary text,
    so malformed HTTP responses cannot inject notes or terminal escapes into permission labels.
    A follow-up read-only `km config` check with temporary km home/state directories now reports
    default Codex as `yolo` and counts all five default chairs in one closing note. No live
    session or profile was created or modified.

23. **km's watcher loses a standing permission dialog after about six minutes; pausing the step
    timeout belongs in CAO.** Probed 2026-11-06 against CAO 2.5.4 with a throwaway repository, a
    `km-claude` chair whose scratch settings asked for `Bash(chmod*)`, and a 10-minute window
    polled every 5 s. Each poll compared km's `FindPermissionDialog` over the rendered full output
    with the same check over tmux `capture-pane` of the chair's window. The dialog appeared at
    15:54:43 and both agreed until 16:00:41. From 16:00:47 to the end of the window, 44 polls
    disagreed: the pane still showed `Permission rule Bash(chmod*) requires confirmation for this
    command.`, but km found nothing. Claude redraws only a small blinking update (about 90 B/s)
    while the dialog stands, so the dialog's own bytes rolled out of the 32 KB rolling buffer
    (`state_buffer_max`, settable with `CAO_STATE_BUFFER_MAX`) and the rendered screen came back
    blank. km would have reported `unblocked`. CAO reported `processing` throughout (finding 16).
    `GET /terminals/{id}/output/range` reads the on-disk log instead, but no route returns the
    rendered viewport. km keeps its screen reads unchanged until a release carries #1107
    (finding 20). A larger `CAO_STATE_BUFFER_MAX` delays the loss in the meantime, and chairs run
    with prompts off never show the dialog. The session was taken down and no prompt was
    answered. A step blocked on a prompt also spends its run-step `timeout`, which killed a tester
    step at 2,415 s on 2026-11-06. Upstream request: awslabs/cli-agent-orchestrator#1249, an
    opt-in `timeout_excludes_waiting` plus a cap on total waiting time.

24. **Preflight can seat first and reuse a fresh terminal, then delete its entire session.**
    Checked 2026-11-10 against the local CAO server with tmux, scratch repositories, isolated km
    home/state directories, and HTTP request recording. `POST /sessions` returned the requested
    `km-preflight-<nonce>-<chair>` name prefixed with `cao-`. Both `km-claude` and `km-codex`
    accepted `run-step` with the just-returned `reuse_terminal_id`. SIGINT during two concurrent
    launches waited for both responses and deleted both returned sessions; SIGINT during two
    in-flight steps also deleted both. Every session present before these checks remained.
    An uncancelled Claude chair wrote `ready` and its session was deleted. With a 1ms step timeout,
    preflight retried once, deleted the first session, retained the second, and printed that
    session's exact tmux attach target. Its terminal still existed and reported `processing` until
    explicit teardown. After cleanup, all seven created terminals returned HTTP 404 and all seven
    tmux sessions were absent. No standing ensemble or profile was modified.

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
