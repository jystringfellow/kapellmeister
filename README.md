# kapellmeister

kapellmeister runs a plan → implement → review → gate loop over coding agents (including Claude Code, Codex, Copilot
CLI and Antigravity) through [CLI Agent Orchestrator](https://github.com/awslabs/cli-agent-orchestrator)
(CAO), with an optional tester that exercises the change live after gate approval. Rounds repeat
until the gate and any tester approve or the rounds run out. Every run works in a throwaway detached
worktree and leaves its change uncommitted for you to ship.

## Install

Download the archive for your OS and architecture and `SHA256SUMS` from the same
[GitHub release](https://github.com/jystringfellow/kapellmeister/releases). Releases support
macOS and Linux on amd64 and arm64; Windows uses WSL2. For example, on Linux arm64,
replace `vX.Y.Z` with the release tag:

```sh
version=vX.Y.Z
archive="km_${version}_linux_arm64.tar.gz"
url="https://github.com/jystringfellow/kapellmeister/releases/download/$version"
curl -fLO "$url/$archive"
curl -fLO "$url/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf "$archive"
mkdir -p "$HOME/.local/bin"
cp "km_${version}_linux_arm64/km" "$HOME/.local/bin/km"
export PATH="$HOME/.local/bin:$PATH"
km version
```

On macOS, choose `darwin` and use `shasum -a 256 -c --ignore-missing SHA256SUMS`
for the checksum check. You can also verify the archive's build provenance with
`gh attestation verify "$archive" -R jystringfellow/kapellmeister`.

With Go installed, the alternative is:

```sh
CGO_ENABLED=0 go install github.com/jystringfellow/kapellmeister/cmd/km@vX.Y.Z
```

Put `$(go env GOPATH)/bin` on your PATH (or the directory selected by `GOBIN`).
`km version` prints the release version, including for `go install ...@version`.
Workspace builds print `dev`, with the VCS revision and dirty marker when available.
CAO, cao-server and the agent CLIs you use must also be installed; start cao-server before
installing the base chair profiles below.

## Commands

```sh
km init [-worktree DIR] [-dry-run] [-json] [-verify CMD] [-check CMD] [-no-run] [-timeout D]  # write config and an unfinished verify script
km tune verify [-worktree DIR] [-detach] [-timeout D] [-budget D] [-verify-timeout D] [-json] [-again] [-guide]  # write and prove verify
km prove [-worktree DIR] [-break PATCH]... [-timeout D] [-json]  # prove verify on clean copies and broken code
km run -repo ~/Code/app [-base BRANCH] -task-file task.md [-ensemble thorough] [-detach]  # one ensemble
km audition -worktree DIR [-ensemble thorough]   # can this ensemble play? no real work
km rehearse -worktree DIR -task-file task.md [-ensemble thorough]  # brief and leave standing
km play SESSION [-max-rounds N] [-verify CMD] [-timeout D] [-cao URL]  # run the score
km reseat SESSION ADDRESS [-agent SPEC] [-timeout D] [-cao URL]  # replace one occupant
km ensemble list                                # list standing ensembles
km ensemble down SESSION                        # take one down
km report [-parts] [-json] [-since DATE] [-repository NAME] [-max-depth N] [-max-entries N] [DIR...]  # summarize run records
km wait RUN [-timeout D] [-json] [-max-depth N] [-max-entries N] [DIR...]  # wait for one run
km status [-json] [-max-depth N] [-max-entries N] [DIR...]  # show runs in progress
km mcp                                                  # serve run tools over MCP stdio
km preflight -worktree DIR [-agents NAME,...] [-tier large]  # check the available agents
km config [-worktree DIR] [-cao URL]              # show the effective settings and their files
km profiles install [-cao URL] [-replace] [AGENT ...]  # install base chair profiles
km version                                       # print the version
```

To start a repository, run `km init` in its checkout (or `km init -worktree DIR`). It writes
repository configuration pointing to `scripts/verify` and an executable, unfinished verify script.
Then run `km tune verify`: your ensemble reads the repository's CI and test instructions, writes
verify and proves it with deliberate source and test breaks. The result stays uncommitted for you
to read. This needs one configured agent and `km profiles install AGENT` to install its base
profile; no earlier `km run` is needed. Tune prepares its effort variants automatically. Two
chairs write and review, and one agent can play both. `km tune verify -guide` prints the same
procedure to follow by hand.
With `-verify CMD`, init wraps the supplied command and runs it once. Init and the manual route,
`km prove -break PATCH`, need no CAO or agents.
Review and commit the setup, then use `km preflight -worktree .` and
`km run -repo . -task-file task.md`. Preview exact contents with `km init -dry-run`.

## Run

`km run -repo` leaves its detached worktree under the system temp directory (`wt-km-*`) and
prints its path in the final summary whenever it leaves changes. Set `[run] worktree_dir` in your
user config to choose another absolute directory (`~/` is expanded); km creates it if needed.
The approved change stays uncommitted there. The clone's checkout stays as it was; km fetches only
the branches needed to resolve the base and updates the worktree records, without pruning
remote-tracking branches.

The starting branch is `-base`, then `[run] base` in the repository config committed on the remote's
default branch, then the branch recorded by Git as `origin/HEAD`. If Git does not know the remote's
default, km uses `main` and prints a note. To set the repository's base, commit:

```toml
# .kapellmeister/config.toml
[run]
base = "develop"
```

An explicit `-base` skips the config lookup for the base.
To stack work on an unmerged branch, name it with `-base`:

```sh
km run -repo ~/Code/app -base feature/parser -task-file next.md
```

The branch is fetched from `origin`; a missing branch fails before any chair starts.
`-base` takes a branch name such as `feature/parser`, and cannot be combined with `-worktree`,
which plays on the checkout it is given. `km play` uses its standing ensemble's recorded checkout.

Default chairs follow the ordered `[agents] available` list in your user config, ignoring duplicates:

- One agent plays every required part.
- With two agents, the first plans, reviews, gates and classifies; the second implements. Review
  stays independent of the implementer's agent whenever there is a choice.
- With three or more agents, the first plans, gates and classifies, the second implements and the
  third reviews.
- When `claude`, `codex` and `copilot` are all available, in any order or with extras, claude plans,
  gates and classifies, codex implements and copilot reviews.

There is no default tester. Configured `[chairs]` override these defaults, followed by a selected
ensemble and role flags. With no available agents, supply every required chair explicitly;
otherwise km refuses before contacting CAO and names the user config to edit.

Override any chair with `-planner`, `-implementer`, `-reviewer`, `-gate`, `-classifier` or the optional
`-tester`, as a built-in or user-defined agent, or `provider:profile[:model]`. Append `@small`,
`@medium` or `@large` to pin a chair's tier, for example `-reviewer claude@large`. A chair with an
explicit model always runs that model and cannot also use `@tier`.

The reviewer role can have several chairs. Use `-reviewer copilot,claude@large` to replace the
whole configured reviewer list for one run. Each reviewer runs concurrently in its own CAO
terminal and resolves its tier separately: pinned reviewers keep their tier, while unpinned
reviewers follow sizing. Other required parts have one chair; the optional tester has at most one.

### Detached runs

`km run … -detach` starts a run and returns a handle, including when its caller has no terminal:

```text
run       20261113-035437-5eab65
pid       95805
worktree  /tmp/wt-km-example
output    /home/me/.kapellmeister/state/runs/20261113-035437-5eab65/output.log
```

Before returning, it validates flags, task text, configuration and chairs, resolves the base,
creates the worktree, resolves verify/check hooks, prepares scratch and checks/installs effort
profiles. A refusal still prints its error and exits 1 in the caller. Successful launch exits 0
once the run process has recorded its PID and is visible to `km status`; it does not imply approval.
The task text is retained, so the caller can remove its `-task-file` after launch.

Preflight and baseline verification happen in the run process **after detachment**. They may take
minutes and require CAO and the chairs; returning a handle does not wait for their readiness.
Their failures are recorded for `km wait`. The run has its own process and session, needs no daemon
or terminal, and survives the caller exiting or closing its terminal. Stop it with `kill -TERM PID`;
SIGINT and SIGHUP also use the foreground run's interruption and ensemble teardown path.

The complete stdout and stderr, including output from before detachment, stay in the printed
`output.log`. Read the whole file with `cat`, or watch it during the run with `tail -f`.
`km status -json` includes the run record's `path`; append `/output.log` to find this file.
Foreground runs keep their existing terminal output.

`km wait RUN [-timeout D] [-json] [DIR...]` waits for one run, then prints the same final summary as
`km run`. RUN must be non-empty and cannot consist only of whitespace. It is an exact run id or
a unique prefix of at least two characters; an exact match wins even for an older record with a
one-character id. Blank ids, shorter prefixes, and ambiguous or unknown ids fail with status 1.
With no directory it searches `<state>/runs` plus the user config's [`[report] dirs`](#report),
with the same handling of missing directories as `km status`. Named directories replace both
without reading the user config. It accepts status's `-max-depth` and `-max-entries` limits.
Once found, it polls that run's record.

```sh
km wait 20261113-035437-5eab65
km wait 20261113-035437 -timeout 10m -json
```

`-timeout` defaults to 0 (no deadline). A timeout leaves the run alone. Without a result, a gone
process or unverifiable liveness (such as a record copied from another host or an older record
without a PID) is reported as unfinished, so waiting cannot silently turn an abandoned run into
success or wait on it forever.

| Exit status | Meaning |
| --- | --- |
| 0 | Finished and approved |
| 1 | Finished without approval, or an error such as an unknown run |
| 3 | Wait timed out; the run continues |
| 4 | Unfinished; no result and the process is gone or liveness cannot be checked |

`-json` prints `{"run_id":"…","state":"finished","exit_status":0,"reason":"","result":{…}}`.
The state can also be `timeout` or `unfinished`; those omit `result` and explain why in `reason`.
The command exits with `exit_status` in both text and JSON modes. It also works for foreground
runs and records written before detachment was added.

## MCP server

`km mcp` exposes run commands as five MCP tools over stdio. A client starts the same `km`
binary with `mcp` as its only argument. The server needs no port, listens only to its client's
pipe, and exits when input closes. Detached runs survive the server exiting.

For clients that accept an `mcpServers` configuration:

```json
{
  "mcpServers": {
    "km": {
      "command": "/absolute/path/to/km",
      "args": ["mcp"],
      "env": {
        "CAO_API_BASE_URL": "http://127.0.0.1:9889",
        "KAPELLMEISTER_HOME": "/home/you/.kapellmeister"
      }
    }
  }
}
```

The `env` entries are optional: CAO, user configuration and run discovery come from the server's
inherited environment, just as for the commands. Set `KAPELLMEISTER_STATE_DIR` too if your run
records live outside the default state directory. In Claude Code, register it with
`claude mcp add km -- km mcp`; permission rules can allow tools individually, for example
`mcp__km__status` or `mcp__km__report`.

| Tool | Arguments | Structured result |
| --- | --- | --- |
| `run` | Required `task` text (at most 1 MiB) and exactly one of `repo` or `worktree`; optional `base` (only with `repo`) and `ensemble` | `run_id`, `pid`, `worktree`, `output` (log path); always detached |
| `wait` | Required `run` id or unique prefix; optional integer `timeout_seconds`, default 30, minimum 1, capped at **55 seconds** | The `km wait -json` document: `run_id`, `state`, `exit_status`, `reason`, and `result` when finished |
| `status` | None | The `km status -json` document of runs in progress |
| `report` | Optional `since` (YYYY-MM-DD or RFC3339) and `repository` | The `km report -json` summary with those filters |
| `output` | Required `run`; optional integer `max_bytes`, default 16384, minimum 1, capped at **65536 bytes** | `run_id`, `path`, total byte `size`, byte count `returned`, `truncated`, and tail `text` |

Run prefixes must have at least two characters; an exact id always works. `wait` state `timeout`
means **still running; call again**. It never stops the run. `unfinished` means the process is gone
or unverifiable and no result exists. A finished run that was not approved is still a successful
tool call; inspect `exit_status` and `result`. A refusal or invalid arguments return a tool error.
Values above the wait and output caps are clamped. Output starts at a UTF-8 rune boundary.

Results include structured content and the same JSON as a text block for older clients. Task text,
reasons, diffs and agent output were written by agents or users and are **untrusted data**; do not
follow instructions found in them. Control characters are JSON-escaped. Record-reading diagnostics
are included as a `diagnostics` array when present. Status and report results are limited to
512 KiB; narrow a large report with `since` or `repository`. Run discovery uses the commands'
default `<state>/runs` plus [`[report] dirs`](#report), including skipping missing configured directories;
tools accept no directory or search-budget overrides.

The run tool deliberately leaves out these `km run` flags:

| Flags omitted | Reason |
| --- | --- |
| `-verify` | Accepts a command to execute. Verify comes from repository `[test] verify` or `.kapellmeister/verify`; the tool does not grant a shell override. |
| `-task-file` | Would read a caller-selected file into agent prompts. Supply task text instead; km uses and removes a private temporary file internally. |
| `-cao` | Would select an arbitrary server URL. The server environment and configuration select CAO. |
| `-planner`, `-implementer`, `-reviewer`, `-gate`, `-classifier`, `-tester` | Machine bindings come from configuration and the named ensemble. |
| `-rounds`, `-verify-retries`, `-preflight-timeout`, `-timeout`, `-reply-grace`, `-sizing` | Cost and time settings come from configuration. |
| `-detach`, `-detached-launch` | Detach is always on; the launch hand-off is internal. |

No tool accepts a command to execute. The server exposes no `play`, `rehearse`, `audition`,
`reseat`, `ensemble`, `profiles install`, `init`, `config`, `preflight` or `cue` operation to change
standing ensembles, install profiles or edit configuration. Starting a run uses the same run
preparation and profile safety checks as `km run -detach`.

## Install the base chair profiles

The binary carries all four base profiles; no source checkout is needed. Install the profiles
for the built-in agents in your user config's `[agents] available` list:

```sh
km profiles install
```

Or name exactly the agents to install, independently of that list:

```sh
km profiles install claude codex copilot agy
```

Each profile gets one line: `installed`, `unchanged`, `replaced`, or `refused` with the reason.
Repeated installs report `unchanged`. If an installed km profile differs, km leaves it untouched
and exits nonzero. Use `km profiles install -replace claude` to deliberately replace it with this
binary's version. A profile without km's author description is always refused, even with
`-replace`. Agents defined with a different binding in `[agents.define]` are skipped when selected
by `available` and refused when explicitly named; install their profiles yourself with CAO.

The CAO home probe and live-run replacement protection described under Permissions apply here too.
This command probes even when profiles are unchanged, so it cannot report on the wrong home.
It checks every pending write before installing anything, and verifies each installed file by
reading it back. Use `-cao URL` to select the server; `CAO_HOME_DIR` must name that server's home.
Runs never install missing base profiles implicitly; the error names this command instead.

## In a scoring-stage sandbox

[scoring-stage](https://github.com/jystringfellow/scoring-stage) runs agents in Docker Sandboxes.
This repository carries a stage book, [`book/`](book), that makes any repository's sandbox ready
for km: it downloads a km release and checks its SHA-256, installs tmux and CAO, starts
`cao-server` on every boot, writes `[agents] available` from the agent CLIs the sandbox has
(unless a km user config already exists), and runs `km profiles install` for them. It chooses no
ensemble and no chairs; those come from your config and the repository's.

Install the book once, from a checkout of this repository, in `~/.scoring-stage/config.toml`:

```toml
books = ["~/Code/kapellmeister/book"]
```

A repository then names it, and says how km verifies a change:

```toml
# .scoring-stage/config.toml
books = ["kapellmeister"]
```

```toml
# .kapellmeister/config.toml
[test]
verify = "scripts/verify"
```

`stage up <repo> <slug>`, then inside the sandbox `km preflight -worktree .` and
`km run -repo . -task-file task.md`. To try the book without installing it, pass
`--book ~/Code/kapellmeister/book` to `stage up`.

The book keeps km's run records and CAO's database and logs as takes. Add the session
directories of the agents you use to your own takes if you want token usage to survive the
sandbox. The kit's `km` argument and its two checksum arguments pin the release; change all three
together. The kit in `.scoring-stage/kits/` is different: it is for working on km itself and
builds km from the workspace.

## Preflight

`km preflight -worktree DIR [-agents claude,codex]` seats each chair in its own temporary CAO
session, one at a time. It defaults to your `[agents] available`; `-agents` selects any defined
names, including user-defined ones. With neither available agents nor `-agents`, it asks you to
configure them before contacting CAO. Checks run concurrently once their chairs are seated. Session-creation
5xx errors and a 400 saying the requested session was not found get up to three retries, after
1, 2 and 4 seconds plus up to half as much again at random, under distinct names ending in `r`, `r2` and `r3`. The `ok` or `BLOCKED` line
reports `seat retried N time(s) after <first error>`. Other failures keep one immediate retry;
a preflight check timeout gets one fresh chair and one retry. km reports whether that second
attempt passed or failed. Two timeouts can take two full `preflight_timeout`s plus CAO's readiness
margins and seating time. Seat call timeouts, including an expired launch context, and permission
blockers are not retried. Real work steps get no additional retry.
A seat launch is bounded at three-eighths of `preflight_timeout` (90s by default, at least 10s),
and never longer than the CAO client's 90s launch bound; other seats wait for that launch to finish.
Deleting a session also waits its turn, so a deletion never overlaps another chair's creation.

When a chair starts but does not answer before the preflight limit, `km preflight`, `km run`
and `km tune verify` explain that this can happen when the agent or the server has only just
started, or a trust or approval prompt may have consumed its first message. See
[Trusting the folder](#trusting-the-folder) for the known Codex case. Run the command again first. If it happens again, use the reported session name to
attach and see what the agent is waiting for, then detach with Ctrl-b d. For example:

```text
chair mock started but did not answer within 4m0s; this can happen when the agent or the server has only just started
  1. run the command again
  2. if it happens again, attach with `tmux attach -t cao-km-preflight-example` to see what the agent is waiting for, then detach with Ctrl-b d
the agent may be waiting at a trust or approval prompt, or a prompt may have been answered automatically and km's first message lost with it
[run] trust_folder = true in ~/.kapellmeister/config.toml avoids the known Codex case (see the README)
```

The agent's screen and raw server detail follow on separate lines when available.

km cleans up failed launches by their exact chosen session names, deletes passing chairs' sessions,
and keeps blocked chairs' attach targets. SIGINT, SIGTERM or SIGHUP waits for any bounded launch already
in progress, then tears down this preflight's sessions. Cancellation also stops queued seats and
retry waits. Teardown failures are reported by session name.

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

To keep prompts on for km's Claude chairs, install the base and copy its installed file into a
separate directory for editing (`CAO_HOME_DIR` defaults to `~/.aws/cli-agent-orchestrator`):

```sh
km profiles install claude
mkdir -p "$HOME/km-profiles"
cp "${CAO_HOME_DIR:-$HOME/.aws/cli-agent-orchestrator}/agent-store/km-claude.md" "$HOME/km-profiles/km-claude.md"
```

Add this frontmatter field to that copy, keeping its other fields and body:

```yaml
permissionMode: default
```

Then install the edited base when no run is using it:

```sh
cao install "$HOME/km-profiles/km-claude.md" --provider claude_code
```

`km profiles install claude` will now report `refused km-claude: content differs` and leave
your edit intact. `-replace` deliberately restores the base carried by the binary.

Every km Claude chair using `km-claude`, including generated `km-claude-<effort>` profiles,
inherits that mode. km copies `permissionMode`, `allowedTools` and `codexProfile` from any installed
Claude or Codex base when generating effort profiles, and synchronizes variants it generated when
these fields differ. A variant that needs replacement must carry km's generated description;
km refuses to replace one it did not generate. The base's system prompt, MCP, skill and other
fields are not copied: variants carry km's chair instructions and the chosen effort.
An unset `allowedTools` stays unset; an explicit empty list stays empty.
Before an installation, km creates and deletes a temporary `km-probe-*` profile through CAO's API
and verifies its exact content in the local `CAO_HOME_DIR` (default
`~/.aws/cli-agent-orchestrator`). It refuses to install if that proof fails, including when the
server uses a different home or denies the profile write routes. Point `-cao` at the local CAO,
or set `CAO_HOME_DIR` to the selected server's home. Effort-profile generation makes no probe when no installation is needed;
`km profiles install` always probes when it selects a base profile.
km also refuses to replace an existing local chair profile with different content while another
unfinished run in `KAPELLMEISTER_STATE_DIR` is alive under `km status`'s process checks. The error
names the profile and run, and no pending variant is written or installed. A run never blocks
itself; missing or unchanged profiles remain allowed. Wait for the other run to finish, or use a
separate CAO home and km state directory for nested work.

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

A chair plays the first of its `agents` that is in your `[agents] available`, which has no default.
A repository's file names only agents defined on your machine, built-in or user-defined, each with
an optional `@tier`, because profiles and models depend on your machine. An undefined agent is an
error even if a later preference is available. Your own files may also name
`provider:profile[:model]`. A chair's `timeout` wins over its section's, which wins over the
file's, which wins over `[run] timeout`. Role flags cannot override an ensemble file; copy it to
`~/.kapellmeister/ensembles/` and edit that instead. A name defined both as a file and in
`[ensembles]` is an error. `km config` lists the ensemble files it finds and where each is from.

## Configuration

kapellmeister layers two files over its built-in defaults; flags override both for one run. Every key is
optional, and unknown keys are an error. To run without explicit chairs, configure at least one
available agent in your user config.

- **`~/.kapellmeister/config.toml`** (`$KAPELLMEISTER_HOME` moves it) holds what depends on your machine and accounts:
  `[cao]`, `[chairs]`, `[agents]`, `[ensembles]`, `[report]`, timeouts, and the model each tier means for you.
- **`.kapellmeister/config.toml`** in the repository holds the contract everyone working in it shares:
  `[test]`, step tiers and floors, rounds, learned budget and sizing.

The repository's values win for `[run]` rounds, retries, `lessons`, `lessons_timeout` and `learned_budget`, `[steps]` and `[sizing]`; yours win
for `[tiers]`, because only you know what your accounts can run. `[cao]`, `[chairs]`, `[agents]`,
`[ensembles]`, `[report]`, `[run] worktree_dir` and `[run] trust_folder` are machine-only
(see [Trusting the folder](#trusting-the-folder)); `[test]` and `[run] base` are repository-only.
Putting one in the wrong file is an error.
`$CAO_API_BASE_URL` overrides `[cao] url`. State lives in `~/.kapellmeister/state` (`$KAPELLMEISTER_STATE_DIR`).
User-only `[report] dirs` adds search directories to `<state>/runs` for bare report, status and
wait commands and their MCP tools. Command-line directories replace both; see [Report](#report).

km bounds each CAO call: short calls at 30s, launches at 90s, and run-step at the step’s own timeout
plus 90s for CAO’s readiness wait; hitting a bound reports the method, path and limit.

A one-vendor setup needs only this in `~/.kapellmeister/config.toml`, after installing the
`km-claude` base with `km profiles install claude`:

```toml
[agents]
available = ["claude"]
```

All five required chairs use Claude. `km preflight -worktree DIR` checks that one agent.

To use your own CAO profile, install it with CAO, then define its short name in your user config:

```toml
[agents]
available = ["mine"]

[agents.define]
mine = "claude_code:my-reviewer"

[chairs]
reviewer = "mine@large"
```

`mine` fills every omitted required chair. `km preflight -worktree DIR -agents mine -tier large`
and `km run -repo DIR -task-file task.md -implementer mine` accept the same name. No `km-*` base
profile is needed. Definitions are exactly `provider:profile`, without a model or tier; names use
lowercase letters, digits, dashes and underscores, starting with a letter or digit. The built-ins
are `claude = "claude_code:km-claude"`, `codex = "codex:km-codex"`,
`copilot = "copilot_cli:km-copilot"` and `agy = "antigravity_cli:km-agy"`. A definition with any of
these names overrides it. `[agents]`, including `[agents.define]`, belongs only in the user config.

A repository ensemble can use that name with `agents = ["mine@large"]` on its chairs. For example,
`.kapellmeister/ensembles/solo.toml` can seat the complete score:

```toml
[sections.all.chairs.planner]
part = "planner"
agents = ["mine"]
[sections.all.chairs.implementer]
part = "implementer"
agents = ["mine"]
[sections.all.chairs.reviewer]
part = "reviewer"
agents = ["mine@large"]
[sections.all.chairs.gate]
part = "gate"
agents = ["mine"]
[sections.all.chairs.classifier]
part = "classifier"
agents = ["mine"]
```

Select it with `km audition -worktree DIR -ensemble solo` or
`km run -repo DIR -task-file task.md -ensemble solo`. Each user supplies their own `mine` definition and available list.

```toml
# ~/.kapellmeister/config.toml
[cao]
url = "http://127.0.0.1:9889"

[report]
dirs = ["~/kept-sandboxes"]

[run]
timeout = "60m"                     # each agent step
reply_grace = "15m"
preflight_timeout = "4m"
blocker_poll = "20s"
lessons = true                     # off by default; proposals need human review
worktree_dir = "~/Code/worktrees"   # optional; defaults to the system temp directory
trust_folder = false              # optional; record Codex repository trust before launching
lessons_timeout = "5m"
learned_budget = 4000              # bytes of lesson text per chair; at least 1

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
repository flags. Its chairs overlay `[chairs]`, then defaults derived from your available agents
for omitted roles. Without
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
**blocking** or **nit**, with a reason. It approves when the task is done, configured verification passed,
and nothing blocks. Without a verify command, reviewer, gate and tester prompts say km did not verify
the change and leave them free to run tests. The implementer is told km will run full verification
only when a verify command is configured. The gate file lists nits without sending them back for
another round.

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
including the failure paths the change could plausibly hit. Both tester prompts state the
test step's effective time limit, including ensemble, section and chair overrides. The plan orders
scenarios by risk and sizes them to fit that limit. The tester runs the highest-risk scenarios first
and stops starting new ones with about the last tenth remaining, leaving time to clean up and write
its verdict. It appends each scenario's result and evidence to the report as soon as the scenario
finishes, and writes the verdict line last. After the gate approves, km waits for the test plan,
then runs `test` in the tester's chair and reads the verdict from `test-<round>.md`: a bare `APPROVE`
or `CHANGES` last line is taken as is; otherwise the classifier reads the report. Only a test
APPROVE yields an `approved` outcome. Any other verdict sends the test report back as the leading
feedback, with instructions to add a regression test reproducing each failure and fix the code, following
the repository's development rules. Later rounds reuse the same test plan.
A test-step timeout or a SIGINT/SIGTERM during the test step after gate approval ends `untested`:
the change was approved but testing did not finish. A signal names the signal and says the tester
did not finish and the gate's approval stands unverified, in both the output and the run record. The outcome names the partial report if it exists and preserves the worktree and its
uncommitted change as an approved run would. Other test-step failures, and a timeout or failure in
any other score step, still fail the run. The test step gets the same worktree check as the gate and
reviewers; a tester that modifies the worktree still fails the run even if it also times out.
Both tester prompts require everything the tester creates to stay under
the absolute `<state dir>/scratch/<run id>` directory, outside the worktree.
`km run` and `km play` create it before starting the score and refuse to start if it is inside a git
work tree; set `KAPELLMEISTER_STATE_DIR` outside any repository. The test prompt permits git commands
that write only in repositories the tester created inside scratch, always using `git -C <repository>`.
The tester must append every long-running process's PID to `<scratch>/pids`, one per line, and stop
those processes before finishing. It must clean up anything it started, without touching anyone else's.
Both tester briefings give per-run facts: the conductor's process ID, its orchestrator URL and state directory,
that none is part of what is being tested and none may be signalled, stopped or changed, and that
the tester may signal only processes it started itself and listed in `<scratch>/pids`. After every
test step, including failure, timeout or interruption, km reports each distinct listed process still alive as
`  left running: pid <pid> (<command>)` and records its PID and command in `left-running` telemetry.
km never signals these processes. It silently skips non-regular `pids` files (including FIFOs) and
files larger than 1 MiB. The concurrent test-plan step
relies on its prompt to forbid file changes because implementation is editing at the same time.
km creates the scratch directory and writes nothing into it. What a tester needs to set up belongs
in the repository's instruction files and lessons: isolated environments, tools to avoid and how
to start a second conductor are knowledge about that repository. See the [worked example](#checked-in-lessons)
in this repository's `AGENTS.md`, "Exercising km live", `scripts/nested-km-env` and
`.kapellmeister/learned/tester.md`.
Without a tester, gate approval finishes the run as usual.

When collection is enabled, after the score ends km asks the gate for lessons when it has given
a verdict, whether or not it approved the run. The question is: from this run's plan, reviews and verdicts, what would have
saved a round if a chair had known it at the start? Zero lessons is a good answer. Each candidate
names a part seated in the run (`planner`, `implementer`, `reviewer`, `gate`, `classifier`, or `tester`
when seated), states a lesson in one or two sentences, and cites the step and finding that support it. The gate returns a JSON
array of objects with `part`, `lesson` and `evidence` fields.

This is shadow mode: km never reads `lessons.json` back and never puts a lesson in a briefing.
The lessons step always plays in a fresh terminal that is released afterwards, so under `km play`
the standing gate never sees what was proposed and cannot carry it into the next play.
It writes `lessons.json` only in the run's state directory, beside `result.json`, with km-stamped
`run_id`, `repository` and `ensemble` on each lesson. It never writes that file into the repository's
worktree. The gate's reply uses the ignored `.kapellmeister/runs/<run>/lessons-reply.json` record,
like other step replies; a regular reply within the size limit is copied to the state's `replies/`
for audit, including malformed answers. Non-regular and oversized replies are not copied.

Collection is off by default. Turn it on in either the user or repository config:

```toml
[run]
lessons = true
```

Proposals are for a person to review and, if useful, check in under `.kapellmeister/learned/`.
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

### Trusting the folder

`[run] trust_folder = true` opts into recording trust before chairs launch. It defaults to false
and belongs only in your user config; a repository cannot grant itself trust. `km config` shows it.
For Codex, km appends `[projects."<repository root>"]` with `trust_level = "trusted"` to
`$CODEX_HOME/config.toml` (or `~/.codex/config.toml`). The path resolves symlinks and uses the main
repository root for linked worktrees, as Codex does, rather than the chair's worktree path.
Non-git folders need no entry. km preserves every existing byte and never changes an existing
entry for the root or launch folder. If parsing or safely appending fails, km leaves the file alone
and prints a diagnostic. It prints one line when it records trust. km and cao-server must share
`CODEX_HOME` so the chair reads the same config.

For Claude Code, Copilot CLI and Antigravity, km writes nothing: CAO 2.5.4 accepts their trust
dialogs. km also does nothing for other providers. With the setting **off**, km changes nothing,
but CAO still accepts the dialogs it recognises; off does not mean a folder stays untrusted.

Codex supports a per-launch override, `-c 'projects={"/path"={trust_level="trusted"}}'`, which saves
nothing. CAO cannot carry it: profile `codexConfig` keys must match `[A-Za-z0-9_.-]` and values must
be scalars. CAO 2.5.4 does not recognise Codex 0.167.0's "Trust this folder?" menu, so km's first
message can be lost when its trailing Enter accepts trust. The preflight timeout retry helps
because Codex saves trust on that first attempt. This is reported upstream as
[awslabs/cli-agent-orchestrator#1319](https://github.com/awslabs/cli-agent-orchestrator/issues/1319).

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

`km init` writes `.kapellmeister/config.toml` with `[test] verify = "scripts/verify"` and
executable `scripts/verify`, even when no toolchain is recognised. Without `-verify`, the script
runs no command and exits non-zero with a message: verify must pass on a clean checkout, fail
when code or a test is broken, and finish in reasonable time. Init reports `unfinished` and
succeeds without running the stub. Its exact marker line is `: km-unfinished-verify`; replace
the stub with the real command and remove that marker before proving it. A run's baseline verify
refuses the stub and shows its message.

With `km init -verify CMD`, the script wraps that command and init runs it once. The POSIX `sh`
wrapper works on Linux and macOS, prints one success line, and on failure names the step and exit
status, shows at most 40 lines of 300 characters each, and prints the full log's temporary path.
Successful logs are removed. Init reports duration and bounds existing commands' output too.
Failed or timed-out verifies leave the files in place and make init exit unsuccessfully.

Detection supplies hints for whoever writes the command: root toolchain files, a `test:` target
in the first existing `GNUmakefile`, `makefile` or `Makefile`, and CI workflow files. It reports
these in text and the JSON `hints` array, never in a written file. More than three GitHub workflow
files become one text line, such as `hint: ci 15 workflow files in .github/workflows`; `-json`
still lists every file. Toolchain files alone cannot tell km the repository's test target, CI
commands, or required services, so hints never choose a command or derive a focused `check` command.

Existing files are never edited or overwritten. Existing repository verify configuration or an
executable `.kapellmeister/verify` hook is honoured without creating a replacement script.
An existing executable `scripts/verify` is kept and used; an existing stub stays `unfinished`.
A non-executable script must be made executable or removed first. An existing config without
verify is kept, and init explains the setting to add by hand. `-verify` overrides the command
for that invocation when existing setup is kept; `-check CMD` adds a focused command using
`{args}` only to a new config. Init never commits or changes Git state.

`-no-run` writes files without running verify, `-timeout D` changes the default `10m` limit,
and `-dry-run` prints exact contents without writing or running anything. `-json` reports the
same file decisions, hints, verify result and next steps as one object, including on failure.

### Tuning verify

```sh
km init
km tune verify [-worktree DIR] [-detach] [-timeout D] [-budget D] [-verify-timeout D] [-json] [-again]
km tune verify -guide
```

Tune uses the implementer and the first reviewer from your configured chairs, with the same
available-agent defaults as run. Override them with `-implementer SPEC` and `-reviewer SPEC`;
`-cao URL` selects CAO. No planner, gate or classifier is needed. Before agent work, tune checks
configuration, CAO and installed base profiles, then prepares effort variants through the same
path as run before seating each distinct chair and asking it to answer. It prints
`→ installed CAO profile NAME from PATH` for each new variant. A failure stops there and names
one remedy. Tune never installs or replaces a base profile or edits your user configuration.
For a missing built-in base, use the reported `km profiles install AGENT` command. To inherit a
provider tier's effort, write a per-tier entry such as `medium = { effort = "inherit" }` under
`[tiers.codex]`; repeat for each tier whose effort you want to inherit.
`[run] preflight_timeout = "0s"` skips seating checks, as with run.

The checkout must have a HEAD commit and the setup from init. An already written verify is
refused unless `-again` is given. An executable `.kapellmeister/verify` hook must first be moved
to `scripts/verify` with `[test] verify` updated. Tune starts with the failing unfinished stub;
`km run` still refuses a failing baseline.

The writer follows the shared guide: aim for about two minutes on a clean copy, never more than
five, and report the measured time. When CI is slower, use its fast format, lint, type check and
build checks plus unit tests for commonly changed code or a fast marked subset. State what was
left out and the commands to run it by hand. Exclude timing, network, service and wall-clock
dependencies, and anything that fails either clean run: narrower is better than flaky. Installation
counts toward the cost; use the lockfile and the package manager's cache.

The writer keeps the bounded-output wrapper and writes at least two break patches outside the
checkout: one breaks covered source code and one breaks a test expectation. Work takes place in
a temporary detached worktree. Changes besides `scripts/verify` and `.kapellmeister/config.toml`
are refused. km itself runs `km prove`; a chair's opinion cannot prove a verify. The reading chair
checks passing commands and stated omissions, and may request only a named command change.

After review, a revision keeps its proof when a conservative comparison finds the same commands.
The comparison recognizes the bounded-output wrapper and literal `run LABEL 'COMMAND'` steps,
ignores comments and blank lines outside quotes, and ignores those diagnostic step labels.
Quoted command text is preserved, including hashes and newlines. Unrecognized wrappers, heredocs,
continuations and ambiguous shell syntax require another proof. Config bytes and mode, script mode,
and ordered break-patch contents and kinds must also match. The run records both normalized command
texts, their SHA-256 digests and the compared inputs; the summary says why the proof was kept.

Tune uses `[run] rounds` and `[run] timeout`; `-timeout D` changes each agent step's limit.
`-budget D` defaults to `30m` and stops the run between steps, never during a proof. A running
agent step finishes under its own timeout. `-verify-timeout D` defaults to `5m` for each clean or
break verification inside tune; direct `km prove` keeps its `10m` default.

Approval copies only the two setup files back and removes the temporary worktree. If later rounds
fail, are flaky or exhaust rounds, the last proved setup is copied back with approved-with-notes
and the reason the later revision was not adopted. Budget exhaustion has the distinct
`budget_exhausted` outcome and exit status 1; it copies back the last proved setup if one exists,
and otherwise leaves the checkout untouched. Its temporary worktree is kept for inspection.
Interrupts and chair failures remain failed and publish nothing. Copy-back refuses changed checkout
setup or a moved HEAD. Nothing is staged or committed. The summary
shows the chosen command, omissions and reasons, both clean durations and one line per break
patch with its result. Proof detail, including bounded failure output, appears once as progress;
the final foreground summary does not repeat it. `km wait RUN` shows the complete summary,
including that failure output, on its own. Break patches live in the run record. `km status`,
`km wait` and `km report` identify it as `tune verify`. With `-detach`, the launcher returns a run id, PID, checkout and output path;
use `km wait RUN -json` for the final JSON summary. `-json` without detach prints it directly.
Bare `km tune` lists the supported subjects and exits 2. An unknown subject first prints
`km tune: unknown subject "SUBJECT"`, then the same list, and also exits 2.

For a person or another agent, `km tune verify -guide` prints the procedure without needing a
checkout, configuration or CAO. The Claude Code skill is a thin wrapper around that command.
Install it personally from this repository:

```sh
mkdir -p ~/.claude/skills
cp -R skills/km-tune-verify ~/.claude/skills/
```

Or copy `skills/km-tune-verify` into a project's `.claude/skills/` directory. Invoke
`/km-tune-verify` there; it reads the guide and follows it, finishing with `km prove`.
The skill carries no copy of the procedure.

### Proving verify

```sh
km prove [-worktree DIR] [-break PATCH]... [-timeout D] [-json]
```

Prove runs verify twice, each time in a fresh clean scratch copy of the checkout's HEAD plus
the current `.kapellmeister/config.toml` and `scripts/verify`. Those two files may be uncommitted;
all other uncommitted changes, untracked files and ignored files are absent. The directory must
be the checkout's top level and HEAD must exist. Both runs must pass; one pass and one failure
is flaky. Each run's duration is reported. The checkout is left untouched, including when verify
writes files. Copies and their temporary logs live outside the checkout and are removed on
completion, failure, timeout or a handled interrupt.

Before either clean run, prove checks every `-break PATCH` in a fresh copy. It then requires
verify to fail in each patched copy. A patch that does not apply, is empty or unreadable, or changes `.kapellmeister/` or
`scripts/verify` is refused. A timeout never counts as a successful break. Proved means two
clean passes and at least one break patch, with every patch making verify fail. Without a patch,
it reports "passes, not proved" and asks for `-break PATCH`.

| Exit status | Meaning |
|---|---|
| 0 | Proved: two clean passes and every break patch made verify fail |
| 1 | Command, Git or filesystem error, or interrupted |
| 3 | A verify run timed out |
| 10 | Refused: unfinished stub, verify not configured, invalid checkout, or bad/setup-touching patch |
| 11 | Passes twice, not proved: no break patch supplied |
| 12 | Verify failed or was flaky on clean copies |
| 13 | A break patch left verify passing |

`-timeout D` is a positive limit per verify run, default `10m`. Output names each clean run and
patch, reports exit codes and durations, and bounds verify output to 40 lines of 300 characters.
`-json` carries the same facts in `worktree`, `command`, `status`, `exit_status`, `reason`, `runs`
and `breaks`, including failures and skipped patches. Prove needs Git and the verify command's
tools; it starts no agents or CAO sessions and makes no network requests of its own.

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
because CAO cannot pass it to the others. A per-tier entry such as
`medium = { effort = "inherit" }` under `[tiers.codex]` uses the CLI's own setting for that tier.
kapellmeister generates and installs each `<profile>-<effort>` variant it needs from the installed
base profile,
including user-named profiles. A missing base is an error naming that base. Permissions are copied
as described above; the base's system prompt and other fields are not. A tier with inherited effort
uses the base directly and needs no variant. Explicit models and providers without tier tables
also need no variants.

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
(approved but testing did not finish), a failed, interrupted or rounds-exhausted run, exits 1 after
printing the outcome and diff-stat. SIGINT, SIGTERM and SIGHUP cancel steps and allow cleanup; a run
ended by a signal names it, such as `interrupted by SIGINT`, in the output, `result.json` and
telemetry. A second signal while km is cleaning up ends it at once. `km audition`, `rehearse` and
`reseat` name the signal in their error line. Cancellation from another cause keeps its usual wording. Missing or unknown subcommands
exit 2.

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
any chair starts. Lesson content comes from git's stdout only; warnings on stderr never become
lesson text. Working files, including edits or new lessons made during the run, cannot
change that run's briefings. Merge the lesson pull request before starting a run that needs it.

`[run] learned_budget` limits lesson text to 4,000 bytes per chair by default, shared between the
part and chair files. Set it in your user config or the repository config; the repository wins.
km keeps whole lines from the top, counting a newline for each line, and stops at the first line
that does not fit. It reports a cut in the output, briefing and telemetry without failing the run.
If the first line exceeds the budget, no lesson section is added, but the cut is still recorded.
Symbolic links, non-regular files and files containing control characters other than newline and
tab are skipped with a warning naming the file. This includes CRLF files. Invalid UTF-8, invisible
or direction-changing Unicode characters (format characters and line or paragraph separators),
and files containing the `<lessons` or `</lessons` section markers (case-insensitive) are skipped
with the same warning and recorded reason. The old `[run] lessons_budget` key is an error naming
`learned_budget`; `lessons` and `lessons_timeout` still control gate-proposed lessons.

Lessons are advice for one part, and the budget can cut them. A rule that must hold belongs in the
repository's agent instruction files, which every chair is told to read and follow. km's own
repository is a worked example of the split. Developing km with km means a chair may start a
second conductor beside the one conducting it, so:

- [`AGENTS.md`](AGENTS.md), "Exercising km live", holds the rules for every chair;
- [`scripts/nested-km-env`](scripts/nested-km-env) is the repository's own tooling that those
  rules point to;
- [`.kapellmeister/learned/`](.kapellmeister/learned) holds a line or two per part, such as the
  order of the tester's scenarios and a pointer back to the rules rather than a copy of them.

What a repository needs its ensemble to know is checked in to that repository.

## Report

`km report [DIR...]` summarizes run records, oldest first, followed by totals by outcome,
repository and ensemble. With no directory it reads `<state>/runs` plus any directories in the
user config's `[report] dirs`. `km report ~/kept-sandboxes` searches across kept sandbox copies.

To add directories a bare `km report`, `km status` or `km wait` searches, add this to
`~/.kapellmeister/config.toml` (`$KAPELLMEISTER_HOME` moves this file):

```toml
[report]
dirs = ["~/kept-sandboxes"]
```

This table belongs only in the user config; putting it in a repository config is an error.
Directories on the command line replace both `<state>/runs` and the setting, without reading the
user config. The list adds directories to `<state>/runs`, which is always searched first. Listing
`<state>/runs` explicitly is harmless: it is searched once and each run is counted once. An absent
or empty list adds nothing to the output. `km config` shows the configured list as additions to
the default directory.
Bare report and status commands read the user config, so malformed TOML or unknown keys there are
errors even when no report directories are set; unrelated chair and policy settings are not validated.

Each path must be absolute or start with `~/`, which expands to the user's home. Relative paths,
a bare `~` and empty or whitespace-only entries are errors. Paths are cleaned, and exact duplicates
are removed, keeping the first position. Overlapping directories and roots reached through symlinks
count each run once, with the same handling of copies as named directories.

A configured directory that does not exist is skipped by `km report`, `km status` and `km wait`.
Each skip adds one line naming the directory on stderr and an entry in the `skipped_dirs` array
under `-json`; the field is absent when nothing was skipped. If every configured directory is
missing, the commands still search `<state>/runs`. If that too is missing, report and status
succeed with no runs, while wait reports an unknown run. Missing explicitly named directories,
unreadable paths and paths that are files remain errors; configured-path errors name the setting
and its config file. A missing `<state>/runs` is silently skipped even when explicitly listed in
the setting. Errors from the default directory are not attributed to the setting.

When the setting supplies directories, text output ends with one `searched: <path>` line per
directory actually searched, with `<state>/runs` first when it exists, then the additions in
configured order, before report's `not searched:` count if present.
These lines also appear when no runs match. JSON adds a `searched` array with the expanded,
cleaned paths; skipped missing directories appear only in `skipped_dirs`. The existing filters,
parts breakdown and search limits work with configured directories too; entry limits apply across
the whole search.

A directory holding `result.json`, `telemetry.jsonl` or
`current.json` is a run. Without a result, a run with empty or blank-only telemetry counts as
unfinished, with nothing printed on stderr. `config.json`, when present, can supply the run id; it
records no start, repository or ensemble. A directory holding only `config.json` is searched for
runs rather than counted as one. The search stops inside each run directory, ignores directory
symlinks and never follows record-file symlinks. An explicitly chosen root directory may itself be
a symlink. Agreeing copies of the same run id count once, preferring a finished copy over an
unfinished or unreadable one, then the first path in lexical order. Copies that disagree on a known
start, repository or finished outcome are listed separately; stderr names both paths and the
differing fields. JSON marks their rows `conflict: true`, and the table counts conflicting rows.
Missing values cannot hide disagreement in other copies. A start taken from `current.json` or from
the run id is approximate and never counts as a disagreement.

Searches are bounded because kept sandbox copies are untrusted. `-max-depth N` defaults to 64;
the root is level 0, and directories at the limit are not opened. `-max-entries N` defaults to
1,000,000 files and directories across the search, read in batches; it accepts 1 through 16,777,216.
Depth must be at least 1. Descent sorts the entries read by name. When a budget is exhausted, the
report continues with what it found, exits successfully and names the unsearched directories on
stderr. Depth diagnostics show at most 20 paths, then a count; entry diagnostics name the first
unsearched directory and count the rest. The table also prints the number of unsearched directories.

Started comes from the first non-blank telemetry record's timestamp, shown in UTC. For unfinished
runs, `current.json` supplies the start, repository and ensemble when available. When telemetry
is empty or blank-only and no start is recorded, the start falls back to the timestamp in the
run id (`YYYYMMDD-HHMMSS-xxxxxx`), interpreted as UTC. Older km versions generated ids in local
time, so this fallback can be off by the host's UTC offset for those records. Missing repository
or ensemble fields show as `unknown`, as with an older km, plain chairs that omit the ensemble,
or a repository without a hosted origin. Unfinished runs have unknown rounds and duration.

Without `-parts`, only the first non-blank telemetry line is read. A truncated last line after valid lines is
ignored; real content that cannot be parsed, including a lone partial first line, is diagnosed
on stderr and counts as unreadable unless a finished result is available. A malformed result
also counts as unreadable. Malformed `config.json` is diagnosed without changing an unfinished
outcome. These errors do not stop the report. Text values are escaped for safe terminal output.
Unreadable or disappearing subdirectories are also diagnosed and counted as unreadable;
other directories are still searched. Directory handles keep searches and record reads from
following a symlink even if a directory is renamed during the report.
The command only reads the supplied directories.

`TOKENS` shows recorded input/output usage in compact form (`1.2M/340k`); input excludes cache.
New steps can measure Claude Code, Codex, Copilot CLI and Antigravity; report uses the usage saved
with the run, so adding a reader does not change reports for earlier runs.
Long table cells are shortened with `…`; JSON keeps their full values. Usage comes only from
`result.json`, and is `unknown` for older or unfinished runs, unmeasured usage, missing counters,
wrong types or counters outside 0 through 2^36. Measured zero usage remains zero. A partly measured
run (`unknown_steps > 0`, for example one with chairs whose tool keeps no usage log) shows what was
measured as a lower bound, `≥1.2M/340k`, and JSON carries its `unknown_steps`. Overall, ensemble and
repository totals sum measured usage, mark the sum `≥` when any run in it is partly measured, and
say how many runs were partly measured and how many had no usage; a group with none measured shows
`unknown`.

`-since 2026-11-09` keeps runs started at or after midnight UTC on that date; RFC3339 timestamps
are also accepted. Runs without a known start are excluded by this filter.
`-repository github.com/acme/app` matches the recorded repository exactly and excludes unknown
repositories. Both filters can be combined, before or after directories. `-json` emits the same
ordered runs and totals as a JSON document, omitting unrecorded fields from individual runs.
Every run has `usage`: either `{"known": false}` or `known: true` with `input_tokens`, `output_tokens`,
`cache_read_tokens` and `cache_write_tokens`. Totals add `usage`, `ensemble_usage` and
`repository_usage`, each with all four counters plus `known_runs` and `unknown_runs`. Zero totals
with no known runs mean no measured usage, not a measured zero. `not_searched` holds up to 100
`{path, reason}` entries (`depth` or `entries`); `not_searched_total` counts all skipped directories.
Nonprinting characters are escaped in JSON too, preserving their values when decoded.
With no matches the table says `no runs found`; JSON contains an empty runs array and zero totals.

For example, `km report ~/kept-sandboxes` can print:

```text
STARTED              REPOSITORY           ENSEMBLE  OUTCOME     ROUNDS   TOOK     TOKENS
2026-11-09 14:02:11  github.com/acme/app  thorough  approved    2        18m31s   1.2M/340k
2026-11-10 09:40:52  github.com/acme/app  unknown   failed      1        4m2s     unknown
2026-11-10 16:15:03  unknown              unknown   unfinished  unknown  unknown  unknown

runs: 3
usage: in 1.2M out 340k (2 without usage)
by outcome
  approved    1
  failed      1
  unfinished  1
by repository
  github.com/acme/app  2  in 1.2M out 340k (1 without usage)
  unknown              1  unknown (1 without usage)
by ensemble
  unknown   2  unknown (2 without usage)
  thorough  1  in 1.2M out 340k
```

`km report -parts [DIR...]` appends a view of the same selected runs grouped by part,
provider and model. It accepts the same `-since`, `-repository`, `-max-depth`, `-max-entries`
and directory arguments. `-parts -json` adds a top-level `parts` object; existing keys and
plain `km report` output keep their meaning. Rows follow score order (planner, implementer,
reviewer, gate, tester, classifier), then provider and model alphabetically. They show recorded
facts, with counts and denominators; they provide no score or ranking.

A step must name a chair and be `plan`, `implement`, `review`, `gate`, `test-plan`, `test`,
`classify-review`, `classify-gate`, `classify-test` or `lessons`, optionally followed by `-nudge`.
Each nudge counts as another step with its own time and usage. `test-plan` and `test` belong to
tester; `classify-*` to classifier; the lessons proposal step to gate. Escalation, verification,
start and other metadata do not count as steps. Chairless lessons-loading records and
lessons tree-change notifications are metadata too. Other recognized steps without a chair
count as unattributed steps.

The provider comes from `chair.provider`. The model comes from `chair.model`, falling back to
the reported top-level `model`; if `model_honored` is false and a reported model exists, that
reported model takes precedence. Missing provider or model becomes `unknown`. Address, profile,
chair name, effort and tier do not split a row. Two reviewers using the same provider and model
share one row, counting both steps and verdicts but each run once. A chair whose effort changes
between runs stays in that row. Only varying effort or tier produces a note below the tables,
listing each value's step count; missing values are `unset`. Constant values are omitted.

The first table's columns (and JSON row fields) are:

| Column | Meaning |
| --- | --- |
| `PART`, `PROVIDER`, `MODEL` | The row's grouping values (`part`, `provider`, `model`). |
| `RUNS` | Distinct selected run records in which this row played a step (`runs`). Agreeing copies count once; conflicting copies remain separate as in the run table. |
| `STEPS` | Attributed step attempts, including nudges (`steps`). |
| `ERRORS` | Steps with a nonempty `error` (`step_errors`). |
| `TIME` | Sum of nonnegative recorded `duration_ms`, excluding queue and reply-wait time. |
| `TIME/STEP` | That time divided by **all** steps, including unmeasured ones (`duration_ms_per_step`). |
| `TOKENS` | Sum of recorded step input/output tokens, excluding cache from input, in the same compact format as the run table. |
| `TOKENS/STEP` | Those input/output totals divided by **all** steps (`usage_per_step.input_tokens`, `usage_per_step.output_tokens`). Fractional tokens may be displayed to one decimal place. |

Time and tokens are `unknown` when no steps were measured, and measured zero stays zero.
A sum or average with any unmeasured contribution is a lower bound marked `≥`. Step usage
uses the same counter validation as run usage: missing, malformed, `known: false` or out-of-range
usage is unknown. JSON `usage` has `known`, `unknown_steps` when nonzero, and all four token
counters when known; cache totals are retained there. Each unmeasured or partly measured
step contributes one to `unknown_steps`, regardless of a step record’s own counter. `duration_ms` is the measured sum and
`unmeasured_duration_steps` counts excluded steps; a zero sum with all steps unmeasured is
unknown. The per-step time and usage objects are omitted when unknown. `efforts` and `tiers`
are value-to-step-count maps, present only when that value varies within the row.

The work tables count verdicts only when a `verdict-review`, `verdict-gate` or `verdict-test`
record matches a `review`, `gate` or `test` step with a chair by subject, positive round and
`chair_name` (empty for a single reviewer, gate and tester). For duplicate keys the last step
and last verdict win (an incomplete last verdict removes any earlier verdict); step totals still include all attempts. A verdict without that match
belongs to no model. Downstream comparisons use verdict records of the same round, even if
their step cannot be attributed. They do not infer a verdict from a run outcome. Older runs
without verdict records contribute steps and run outcomes but no verdict counts. Counts can
also be lower than steps because a step failed, a run stopped or records are partial.

For reviewer, gate and tester, `APPROVE` (`work.approve`) counts exact `APPROVE` verdicts,
`CHANGES` (`changes`) counts exact `CHANGES`, and `UNCLEAR` (`unclear`) counts every other
nonempty verdict string. Reviewer `FAILED` (`failed`) is separate: the review step failed,
so it gave no verdict. Here “not approved” means a present verdict other than `APPROVE`.
A missing gate or test verdict never counts as a downstream failure. The comparison columns
are **numerator/denominator**, never percentages:

| Part / column | Numerator / denominator (JSON work fields) |
| --- | --- |
| Reviewer `APPROVE→GATE!` | Its approvals in rounds where the gate did not approve / its approvals in rounds with a gate verdict (`approve_then_gate_not_approved` / `approve_gate_ran`). |
| Reviewer or gate `APPROVE→TEST!` | Its approvals in rounds where the tester did not approve / its approvals in rounds with a test verdict (`approve_then_test_not_approved` / `approve_test_ran`). |
| Reviewer `CHANGES→GATE+` | Its requests for changes in rounds the gate approved / its requests for changes in rounds with a gate verdict (`changes_then_gate_approved` / `changes_gate_ran`). |
| Tester `ALL+→TEST!` | Its nonapprovals after every recorded review and the gate approved / its verdicts in those rounds (`not_approved_after_all_approved` / `all_approved_rounds_tested`). At least one review verdict is required; a failed or unclear review, or a recorded review step missing its verdict, prevents unanimity. |

A reviewer “blocked” a round here only in the sense that it gave `CHANGES`. Records do not
carry the gate policy, so these columns make no claim that the round was actually held back.
They describe agreement between recorded verdicts, not which model was right. Missing review
records cannot prove that unrecorded reviewers approved.

Planner and implementer work uses `result.json` and counts **runs**, not verdicts. Only runs
where that part was played by a single (provider, model) contribute to these outcome columns:

| Column | Meaning (JSON work field) |
| --- | --- |
| `FIRST` | Approved runs with exactly one round (`approved_first_round`). |
| `LATER` | Approved runs with more than one round (`approved_later_rounds`). |
| `ROUNDS?` | Approved runs with missing or nonpositive rounds (`approved_rounds_unknown`). |
| `NOT APPROVED` | Readable results whose outcome is anything other than `approved` (`not_approved`). |
| `UNFINISHED` | Runs with no result (`unfinished`). |
| `RESULT?` | Runs whose result exists but cannot be read (`unreadable_results`), excluded from outcomes. |
| `SPLIT` | Runs where multiple providers or models played this part (`split_runs`), excluded from every other outcome column for each participating row. |
| `ROUNDS/APPROVED` | Sum of rounds in approved runs with known positive rounds (`rounds_in_approved_runs`), shown as `sum over n runs`, where n is `FIRST + LATER`. Divide to obtain rounds per approved run. |

Classifier has no work table. JSON rows carry these same counts in `work` for the other parts.
The `parts` object contains `rows` (an empty array when none), `unattributed_steps`,
`unattributed_verdicts` and `runs_without_steps`. These last three counts print below the tables
when nonzero. Unattributed verdicts include unmatched verdict keys and records without a positive
round or nonempty verdict. Runs without attributed steps include missing, empty or unreadable
telemetry and runs containing only metadata or chairless steps. They still appear in the run table.

With `-parts`, report streams telemetry independently of the start-time read, even when the
first line is unreadable or the start came from current state. Reads retain the same regular-file,
no-symlink protections. Lines are limited to 1 MiB and files to 64 MiB; overlong lines are skipped,
and the unread remainder of an oversized file is diagnosed. Malformed objects, wrong field types
and negative rounds are skipped with one skipped-line count per run on stderr. Malformed usage
alone makes that step's usage unknown. A malformed final line without a newline is ignored as a
possible interrupted write. Reports can therefore be partial; missing records are never assumed
to be approvals or measured zeros.

For example, two approved runs with `alpha` and `beta` each playing implementer and reviewer
(one round for alpha, two for beta), with one recorded step per part and an approving review,
append this section with `km report -parts`:

<!-- report-parts-example -->
```text
by part and model
PART         PROVIDER  MODEL  RUNS  STEPS  ERRORS  TIME  TIME/STEP  TOKENS  TOKENS/STEP
implementer  p         alpha  1     1      0       1s    1s         100/20  100/20
implementer  p         beta   1     1      0       1s    1s         100/20  100/20
reviewer     p         alpha  1     1      0       1s    1s         100/20  100/20
reviewer     p         beta   1     1      0       1s    1s         100/20  100/20

work: implementer
PROVIDER  MODEL  FIRST  LATER  ROUNDS?  NOT APPROVED  UNFINISHED  RESULT?  SPLIT  ROUNDS/APPROVED
p         alpha  1      0      0        0             0           0        0      1 over 1 runs
p         beta   0      1      0        0             0           0        0      2 over 1 runs

work: reviewer
PROVIDER  MODEL  APPROVE  CHANGES  UNCLEAR  FAILED  APPROVE→GATE!  APPROVE→TEST!  CHANGES→GATE+
p         alpha  1        0        0        0       0/0            0/0            0/0
p         beta   1        0        0        0       0/0            0/0            0/0
```
<!-- /report-parts-example -->

## Status

Detached runs appear here like foreground runs, including while preflight is still running.
Use `km wait RUN` to wait for one and receive its final summary and exit status, or read the
`output.log` path printed by `km run -detach` for progress.

`km status [DIR...]` lists runs that have started and have no `result.json`. With no directory
it searches `<state>/runs` plus the user config's [`[report] dirs`](#report). Named directories
replace both without reading the user config. Configured searches skip missing
directories and add the same `searched:` lines, JSON fields and stderr diagnostics as report.
It shares report's search, limits (`-max-depth`, `-max-entries`, same
defaults), handling of copies and safe record reading. A run is not listed when any copy of it has
a result, even one that disagrees with the copy in progress. Among copies still in progress, status
shows the one that records what is playing, most recently updated first. Status rejects a root,
given, configured or default, that is itself a symlink, though the path above it may pass through one. It
escapes control characters and only reads. With no runs in progress it says so and exits 0; when a
limit cut the search short it says how many directories were not searched instead. Diagnostics go
to stderr. Runs with empty or blank-only telemetry are listed like other runs in progress, without
diagnostics.
`-json` emits `{"runs":[...]}`, including every playing step and its elapsed milliseconds;
an empty result without configured directories is `{"runs":[]}`.

Each row shows the start in UTC, repository, ensemble, round, step, chair, elapsed time and
liveness. When steps overlap, the row shows the longest-running step and `+N` for the others.
Between steps, elapsed time is measured since the last update. `alive` means the owning km
process is still running; `abandoned` means it is gone or its PID has been reused. `unknown`
means liveness cannot be checked, including records copied from another machine and runs from
before current-state recording. Other missing fields show as `unknown`.

```text
$ km status
RUN                     STARTED              REPOSITORY             ENSEMBLE  ROUND  STEP        CHAIR                       FOR     LIVENESS
20261111-041502-a1b2c3  2026-11-11 04:15:02  github.com/acme/other  thorough  1/3    implement   leads.implementer@thorough  41m5s   abandoned
20261111-054930-e7c69e  2026-11-11 05:49:30  github.com/acme/app    thorough  2/3    review +1   review.security@thorough    3m12s   alive
```

## Telemetry

Run records (`config.json`, `telemetry.jsonl`, `current.json`, `result.json` and `replies/`) live in
`<state dir>/runs/<run id>/`. Tester scratch lives separately in `<state dir>/scratch/<run id>/`
and is left in place after the run. Detached runs also keep `output.log` (complete stdout and
stderr) and `launch.json` (the run id, worktree, base branch and task text passed to their run
process). They publish `current.json` before preflight and record failures from that point.
Once `telemetry.jsonl` is created, `km run` and `km play`
write a start record and leave `result.json` on normal completion, failure or interruption, including
errors recording configuration, engine setup or the final git summary. Failures record their reason;
a panic records a failed result and is raised again. If writing the result itself fails, stderr
reports it. A process killed outright cannot finalize its record and remains unfinished.

Each run writes `telemetry.jsonl` under `~/.kapellmeister/state/runs/<run>/`: one record per step with its chair, tier, effort, tier source (`default`,
`pinned`, `sized`, `escalated` or `explicit`), the model CAO reported, durations and verdicts, plus `size`
and `escalate` records for each decision. Lessons add a `lessons` step record and a
`lessons-proposed` summary (also recorded when collection is skipped); the step uses the `gate` tier. A seated tester adds `test-plan`, `test`,
`classify-test`, `verdict-test` and any `left-running` records; both tester steps use the `test` tier. With several reviewers,
their review, nudge, classification and verdict records include `chair_name` (`reviewer-1`, `reviewer-2`, etc.).
A signal adds an `interrupted` record naming the signal. A signalled test step after gate approval
also adds `unverified`, recording that the tester did not finish and the gate's approval stands
unverified.
A failed review has verdict `FAILED` and its error; single-reviewer records omit `chair_name`.
`verdict-review`, `verdict-gate` and `verdict-test` records include `verdict_source: "reply"` when a
bare last line supplies the verdict; these replies produce no corresponding `classify-*` record.
Classified verdicts omit `verdict_source`. A review's `classify-review` and verdict records can
interleave with other reviewers' progress and telemetry while their reviews are still running.

`duration_ms` measures the step's own run time. `queue_ms`, when nonzero, records time spent opening
its usage window, including waiting for an earlier step on the same chair. Queued steps are not
watched as running, and the wait stops when the run is cancelled. A step cancelled while queued
records its error and queue time with zero duration and no usage. `reply_wait_ms` remains separate.

Steps also record `usage` from Claude Code, Codex, Copilot CLI and Antigravity session logs when
readable. A known usage has
`known: true`, separate `input_tokens`, `output_tokens`, `cache_write_tokens` and `cache_read_tokens`,
and a `models` map with the same counters for each model named in the log. Claude input excludes
cache reads and writes. Codex input has cached reads subtracted; its reported cache writes are kept
separate without subtracting them from input (the real logs inspected reported zero cache writes).
Claude, Codex and Copilot CLI output counters include reasoning tokens. km records tokens, without
converting them to money.

Usage covers complete log lines appended during one step attempt, including the reply wait. Earlier
steps on a reused occupant are excluded, and each nudge has its own usage. Windows for steps sharing
a chair or an explicitly reused terminal are serialized. Each Claude message id is counted once per
terminal or subagent log across windows, attributed to the first window with known usage that saw it. Within
that window, its last usage wins; later streamed lines of that message are excluded from subsequent
windows. Codex cumulative counts are differenced for the attempt. Claude
logs are found under `CLAUDE_CONFIG_DIR` (default `~/.claude`); Codex logs under `CODEX_HOME` (default
`~/.codex`), in today's and yesterday's session directories, matched to the worktree. Exactly one
growing session log must name the step's reply path in its appended lines, even when only one grows.
Claude usage also includes growth in `<session-id>/subagents/**/agent-*.jsonl` beside that session's
`<session-id>.jsonl`. Subagent transcripts are attributed through the matching parent session;
they need no reply-path match themselves. Existing subagent content is excluded, and new transcripts
start at byte zero. Metadata files are ignored. Parent and subagent message ids are remembered only
when the whole window has known usage; malformed subagent usage makes the window unknown.

Copilot CLI logs are under `COPILOT_HOME` (default `~/.copilot`), at
`session-state/<session-id>/events.jsonl`. The first `session.start` event's `data.context.cwd`
must match the worktree; `workspace.yaml` beside the log confirmed that directory in the sessions
examined. Copilot CLI 1.0.109 was checked in both interactive and prompt mode, including a resumed
session. Each `session.usage_record` counts one request, including subagent records in the same log.
km subtracts `cacheReadTokens` and `cacheWriteTokens` from `inputTokens` and records both cache counts
separately; `outputTokens` is output. `responseModel` names the model, falling back to `model`.
Request ids are deduplicated within the window, scoped by `accounting.sourceSessionId`;
`accounting.usageId` falls back to `apiCallId`. Cumulative `session.usage_checkpoint` and
`session.shutdown` totals are ignored, including on resume, so the same requests are not counted twice.
Only requests recorded as `session.usage_record` are counted; background title requests without
such records are excluded. `reasoningTokens` is a subset of `outputTokens`, so km keeps output
unchanged and does not add reasoning again. A real 1.0.109 session recorded 362 output tokens across
three requests, including 45 reasoning tokens; its footer and shutdown output total were both 362.
Missing or invalid request counters also yield `no-usage`. A real 1.0.86 session had cumulative
shutdown tokens but no per-request usage records, so steps on that format remain `no-usage`.

Antigravity CLI (`agy`) 1.5.6 was checked with a prompt-mode conversation and its resumed conversation.
Its append-only `~/.gemini/antigravity-cli/brain/<conversation-id>/.system_generated/logs/transcript.jsonl`
records per-request `PLANNER_RESPONSE` lines with status `DONE`. km sums `input_tokens`,
`output_tokens` and `cache_read_tokens`, deduplicated by `step_index` within the window. Input already
excludes cache reads; the sums match the CLI's printed conversation totals, which are cumulative on
resume. The transcript has no working directory, so attribution requires the unique reply-path match
in appended lines. It records neither cache writes (km's counter is zero by absence) nor separate
thinking tokens (no established separate counterpart); km keeps the recorded output unchanged and
does not add the CLI stdout's thinking count. The transcript names no model, so its model is `unknown`.
Antigravity subagents, transcript rollover into chunks, and nonzero cache writes were not examined.

For scoring-stage retention, add `~/.copilot/session-state` and
`~/.gemini/antigravity-cli/brain` to your own `takes`. These are the session directories read by km;
login and credential files are not needed.

Unavailable usage has `known: false` and a `reason`: `unsupported-provider` (no reader for the
provider), `no-log`, `ambiguous-log` (no unique growing session log names the reply path, including a single
log without a match), `unreadable-log` or `no-usage`. Unknown usage omits token
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

`result.json` also records the worktree's starting commit as `base`. For `km run -repo`,
`base_branch` names the branch fetched from `origin` (`main` by default), and `base` is the commit
it resolved to; the opening output names both. `base_branch` is omitted for `-worktree` runs and
`km play`. Older records without `base_branch` remain readable. Its `diff_stat`
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
