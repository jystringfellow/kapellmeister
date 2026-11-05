package loop

import (
	"fmt"
	"strings"
)

const ground = `You are the %s in a kapellmeister engineering loop, working in this git worktree.
First read and follow every agent instruction file at the repository root (AGENTS.md, CLAUDE.md, GEMINI.md, .github/copilot-instructions.md, and any others present), whichever your tool loads by default.
Never commit, push, stash, create branches, or switch branches.
`

func preamble(role string) string {
	return fmt.Sprintf(ground, role)
}

func taskBlock(task string) string {
	return "<task>\n" + strings.TrimSpace(task) + "\n</task>\n"
}

func planPrompt(task, out string, askSize bool) string {
	sizeLine := ""
	if askSize {
		sizeLine = sizeRequest
	}
	return preamble("planner") + taskBlock(task) + fmt.Sprintf(`
Study the code this task touches, then write an implementation plan to %s covering:
- the behaviour to add or change, in concrete terms (inputs, outputs, edge cases)
- the specs, tests or facts to add or change first, following the repository's development rules
- the files to change
- how to verify the change
%sDo not modify any file other than %s. When done, reply with one short sentence.
`, out, sizeLine, out)
}

const sizeRequest = "End the plan with one line of the form `Size: small|medium|large — <one-line reason>`, rating how capable a model the implementation and review need: small for a mechanical change in one place, medium for a contained change with tests, large for cross-cutting, subtle, concurrent or security-sensitive work.\n"

func implementPrompt(task, plan, out, check string, feedback []string, gate string) string {
	var b strings.Builder
	b.WriteString(preamble("implementer"))
	b.WriteString(taskBlock(task))
	fmt.Fprintf(&b, "\nThe plan is in %s.\n", plan)
	if len(feedback) == 0 {
		b.WriteString("Implement the task following the plan and the repository's development rules.\n")
	} else if gate != "" {
		fmt.Fprintf(&b, "The previous attempt, still uncommitted in this worktree, was not approved. Read %s. The gate's blocking findings in %s are authoritative and supersede reviewer requests. Address those blocking findings; findings classified by the gate as nits need no change, even if a review calls them required.\n", strings.Join(feedback, ", "), gate)
	} else {
		fmt.Fprintf(&b, "The previous attempt, still uncommitted in this worktree, was not approved. Read %s and address every required change.\n", strings.Join(feedback, ", "))
	}
	b.WriteString(testingLine(check))
	fmt.Fprintf(&b, `Leave every change uncommitted in the worktree.
Then write a short summary to %s: what you changed, which tests you added or ran and their results, and anything left unresolved.
When done, reply with one short sentence.
`, out)
	return b.String()
}

func testingLine(check string) string {
	if check != "" {
		return fmt.Sprintf("To run tests, use `%s`; it prints only failures. Run it in the foreground and wait for it. Do not run the full suite: kapellmeister runs the full verification after you finish.\n", CheckUsage(check))
	}
	return "Run only the tests that cover your change, in the foreground, and wait for them to finish. Do not start background tasks or timers, and do not end your turn while anything you started is still running. Do not run the full suite: kapellmeister runs the full verification after you finish.\n"
}

const verifiedNote = "Do not re-run the test suite; kapellmeister's verification result above is authoritative.\n"

func verifyLine(command string, v VerifyResult, path string) string {
	if command == "" {
		return "No verify command is configured.\n"
	}
	line := fmt.Sprintf("kapellmeister ran `%s`; it exited %d. Its output is in %s.\n", command, v.ExitCode, path)
	if v.Attempts > 1 && v.ExitCode == 0 {
		line += fmt.Sprintf("It failed first and passed on attempt %d, so treat any test that failed as possibly flaky and say whether this change could have caused it.\n", v.Attempts)
	}
	return line
}

func reviewPrompt(task, plan, summary, command string, v VerifyResult, verifyPath, out string) string {
	return preamble("reviewer") + taskBlock(task) + fmt.Sprintf(`
Review the uncommitted changes in this worktree. Inspect them with `+"`git status`"+` and `+"`git diff HEAD`"+`.
The plan is in %s and the implementer's summary is in %s.
%s
`+verifiedNote+`Judge whether the change does what the task asks, follows the repository's rules, is correct, and is safe to merge. Look for bugs, missing tests, and scope creep.
Write your review to %s: state plainly whether you approve the change as it stands, then list every required change with file and line.
Do not modify any file other than %s. When done, reply with one short sentence.
`, plan, summary, verifyLine(command, v, verifyPath), out, out)
}

func gatePrompt(task, plan, summary string, reviews, failed []string, command string, v VerifyResult, verifyPath, out string) string {
	reviewNote := "the review in " + strings.Join(reviews, ", ")
	if len(reviews) != 1 {
		reviewNote = "every review: " + strings.Join(reviews, ", ")
	}
	failedNote := ""
	if len(failed) > 0 {
		failedNote = "These reviewers failed to complete their review: " + strings.Join(failed, ", ") + ". Weigh what their review might have caught, and check those parts of the diff yourself.\n"
	}
	return preamble("gate") + taskBlock(task) + fmt.Sprintf(`
You decide whether the uncommitted changes in this worktree are ready to merge.
Read the plan in %s, the implementer's summary in %s, and %s.
%s%s
`+verifiedNote+`Check the diff yourself with `+"`git diff HEAD`"+`. Sort every point the reviews raised into **blocking** (the change must not merge until it is fixed) or **nit** (it can merge as is), each with a reason, and add any blocking point you find yourself. Approve when the task is done, the verification passed, and nothing is blocking. List the nits in your decision; they do not stop approval.
Write to %s: your decision first, then the blocking points and the nits, each with its reason.
Do not modify any file other than %s. When done, reply with one short sentence.
`, plan, summary, reviewNote, failedNote, verifyLine(command, v, verifyPath), out, out)
}

func classifyPrompt(subject, path, out string) string {
	nitNote := ""
	if subject == "gate" {
		nitNote = "Points it lists as nits or non-blocking are not required changes.\n"
	}
	return fmt.Sprintf(`Read %s. It is a %s of a code change.
Write exactly one word to %s:
APPROVE if it approves the change as it stands with no required changes,
CHANGES if it asks for any change,
UNCLEAR if you cannot tell.
%sDo not modify any other file. Then reply with that same word.
`, path, subject, out, nitNote)
}

func nudgePrompt(path string) string {
	return fmt.Sprintf("You have not written %s yet. Write it now exactly as the previous instructions asked, then reply with one short sentence.\n", path)
}

func preflightPrompt(path string) string {
	return fmt.Sprintf("This is a readiness check. Write the single word ready to %s and do nothing else. Then reply with: ready\n", path)
}
