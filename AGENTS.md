# kapellmeister Agents Guide

kapellmeister conducts coding agents through CAO: a plan → implement → review → gate loop with
configurable seats, tiers and ensembles. The binary is `km`.

## Tests are the spec

Behavior is defined by tests, and only by tests. There are no spec documents.

- Start every behavior change with a failing test that states the behavior. Name tests as
  statements, such as `TestEffortProfilesAreGeneratedOnlyForKapellmeistersOwnBases`.
- Run it and confirm it fails for the right reason before implementing.
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
