# kapellmeister Agents Guide

kapellmeister conducts coding agents through CAO: a plan → implement → review → gate loop with
an optional tester and configurable chairs, tiers and ensembles. The binary is `km`.

## Tests are the spec

Behavior is defined by tests, and only by tests. There are no spec documents.

- Start every behavior change with a failing test that states the behavior. Name tests as
  statements, such as `TestEffortProfilesAreGeneratedOnlyForKapellmeistersOwnBases`.
- Run it and confirm it fails for the right reason before implementing.
- For a failure found while running km live, add a reproducing regression test and confirm it
  fails before fixing the code.
- Implement the least code that makes it pass.
- Never weaken a test to make it pass. If the intended behavior changed, change the test on
  purpose and say why in the commit.
- Work out the design in the conversation or the PR description. Do not check it in.
- Put the reason for a change in its commit message and PR description.
- Refactors, docs and tooling changes keep behavior unchanged and need only the existing tests.

## Where things live

- **Behavior:** tests.
- **Why a change was made:** commit messages and PR descriptions.
- **Decisions that span many changes:** `DECISIONS.md`, one short entry each, with the date and
  the evidence. Read it before proposing to reverse one.
- **How to use `km`:** `README.md`. It describes, it does not specify; when it disagrees with the
  tests, fix the README.
- **CAO behaviour findings:** `spike/SPIKE.md`.

## Vocabulary

kapellmeister names things from orchestral practice. Use these words in code, tests, commands and
docs, and do not coin a synonym for one of them.

- **podium:** the idle terminal that keeps a run's CAO session alive between steps.
- **conductor:** km itself. It seats an ensemble's chairs, cues them and waits for them to close
  their cues.
- **ensemble:** the chairs seated together in one CAO session.
- **section:** a group of chairs within an ensemble, recorded as a CAO group label.
- **chair:** a stable seat, addressed `section.chair@ensemble`, that plays one part.
- **occupant:** the CAO terminal sitting in a chair. It can be replaced while the chair keeps its
  address. `km reseat` replaces one.
- **part:** what a chair plays in the score: `planner`, `implementer`, `reviewer`, `gate`,
  `classifier` or the optional `tester`.
- **score:** what an ensemble plays. The plan → implement → review → gate loop is one score.
- **cue:** an obligation km tracks until a chair closes it with `km cue done`.
- **send:** untracked chatter between chairs through CAO's `send_message`.
- **audition, rehearse, play:** check that an ensemble can play without doing work; brief it and
  leave it standing; run the score.

Parts are roles in the score and keep plain names; chairs are seats and may carry orchestral names,
such as `tonmeister` for the chair that plays `tester`.

Name a new concept from orchestral practice, and pick a word with one meaning. `hall` is reserved
for something that holds many ensembles.

## Exercising km live

These rules bind every chair that runs `km`, `cao`, `cao-server` or `tmux` while a km conducts it.
The conductor is a process named `km`; by default it uses `~/.kapellmeister`, the CAO home
`~/.aws/cli-agent-orchestrator`, port 9889 and the default tmux socket, and you inherit all of them.

- **Isolate first, in the same shell, or do not run those commands.** A failed step leaves no file
  to source.

  ```sh
  env_file=$(scripts/nested-km-env "$scratch" "$conductor_cao_url") && . "$env_file" || exit 1
  ```

  `$scratch` is your own scratch directory (the tester's briefing names one; otherwise make a
  short temp directory). The URL is optional; pass the conductor's when it is not on port 9889.
- **Isolate everything together.** The script puts `KAPELLMEISTER_HOME`, `KAPELLMEISTER_STATE_DIR`,
  `CAO_HOME_DIR` and `TMUX_TMPDIR` under `<scratch>/n`, and sets `CAO_API_HOST=127.0.0.1`, a
  free `CAO_API_PORT` that is neither 9889 nor the conductor's, and the matching
  `CAO_API_BASE_URL`. That last one is the URL km calls; it does not move a server's port.
- **A separate state directory with a shared CAO home is not isolation.** `cao install` writes
  profiles into the calling shell's `CAO_HOME_DIR` whatever server URL is in use, so it overwrites
  the profiles the conductor's chairs are playing from.
- **`TMUX`, `TMUX_PANE` and `CAO_TERMINAL_ID` stay unset.** CAO starts tmux without socket flags,
  and an inherited `TMUX` leads tmux to the conductor's socket despite `TMUX_TMPDIR`;
  `CAO_TERMINAL_ID` would give your commands this chair's identity.
- **Keep the scratch path short.** tmux's socket, `<scratch>/n/t/tmux-<uid>/default`, must
  fit in 100 bytes; the script refuses a longer one. Pick a shorter directory, do not link around it.
- **Never signal a process found by name** (`pgrep`, `pkill`, `killall`, `ps | grep`): the first
  `km` you find is the conductor. Record the PID of each process you start and signal only those.
- **Change nothing the conductor uses:** its CAO home and profiles, its tmux server and sessions,
  its km home and state directory, and the ensemble you are sitting in.
- **Work in your own throwaway worktree or scratch repository.**
- **Take down what you start:** every session with `km ensemble down` (or the audition's teardown),
  every server by its recorded PID, and `tmux kill-server` only in a shell that sourced the file.

## Checks

```sh
gofmt -l .
go vet ./...
go test ./...
```

## Constraints

- kapellmeister drives its own loop over CAO `run-step`; see `DECISIONS.md`.
- Own as little as possible. Use a CAO primitive wherever one exists; put anything km must own
  behind a narrow seam shaped like the CAO primitive it stands in for, and propose the gap upstream
  to CAO. See `DECISIONS.md`.
- Windows runs under WSL2, because CAO refuses native Windows.
- Each run works in a throwaway detached worktree and leaves its change uncommitted.
- Prefer small, explicit functions and simple implementations; add an interface only when a
  second implementation exists.

## Code comments

Code, names, types and tests explain behavior. Add a comment only for an essential constraint the
code cannot show, describe the code as it is now, and never narrate past implementations, the
current change, or future work. No TODO or FIXME comments.
