package loop

import (
	"fmt"
	"strings"
	"time"
)

const ground = `You are the %s in a kapellmeister engineering loop, working in this git worktree.
First read and follow every agent instruction file at the repository root (AGENTS.md, CLAUDE.md, GEMINI.md, .github/copilot-instructions.md, and any others present), including those your tool does not load by itself.
Never commit, push, stash, create branches, or switch branches.
`

func preamble(role string) string {
	return fmt.Sprintf(ground, role)
}

func taskBlock(task string) string {
	return "<task>\n" + strings.TrimSpace(task) + "\n</task>\n"
}

func partForStep(name string) Part {
	score := DefaultScore()
	part, _ := score.Part(score.StepPart(name))
	return part
}

func planPrompt(task, out string, askSize bool, lessons string) string {
	sizeLine := ""
	if askSize {
		sizeLine = sizeRequest
	}
	part := partForStep("plan")
	return preamble(part.Name) + taskBlock(task) + lessons + fmt.Sprintf(part.Prompt["plan"], out, sizeLine, out)
}

const sizeRequest = "End the plan with one line of the form `Size: small|medium|large — <one-line reason>`, rating how capable a model the implementation and review need: small for a mechanical change in one place, medium for a contained change with tests, large for cross-cutting, subtle, concurrent or security-sensitive work.\n"

func implementPrompt(task, plan, out, check, verify string, feedback []string, gate, test, lessons string) string {
	part := partForStep("implement")
	var feedbackNote string
	if len(feedback) == 0 {
		feedbackNote = "Implement the task following the plan and the repository's development rules.\n"
	} else if test != "" {
		feedbackNote = fmt.Sprintf("The previous attempt, still uncommitted in this worktree, passed review and the gate but failed the tester's live run. Read %s. The test report in %s is authoritative. For each failed scenario, add a regression test that reproduces it, then fix the code until it passes, following the repository's development rules.\n", strings.Join(feedback, ", "), test)
	} else if gate != "" {
		feedbackNote = fmt.Sprintf("The previous attempt, still uncommitted in this worktree, was not approved. Read %s. The gate's blocking findings in %s are authoritative and supersede reviewer requests. Address those blocking findings; findings classified by the gate as nits need no change, even if a review calls them required.\n", strings.Join(feedback, ", "), gate)
	} else {
		feedbackNote = fmt.Sprintf("The previous attempt, still uncommitted in this worktree, was not approved. Read %s and address every required change.\n", strings.Join(feedback, ", "))
	}
	return preamble(part.Name) + taskBlock(task) + lessons + fmt.Sprintf(part.Prompt["implement"], plan, feedbackNote, testingLine(check, verify), out)
}

func testingLine(check, verify string) string {
	line := "Run the tests that cover your change, in the foreground, and wait for them to finish. Do not start background tasks or timers, and do not end your turn while anything you started is still running."
	if check != "" {
		line = fmt.Sprintf("To run tests, use `%s`. Run it in the foreground and wait for it.", CheckUsage(check))
	}
	if verify != "" {
		line += " Do not run the full suite: kapellmeister runs the full verification after you finish."
	}
	return line + "\n"
}

func verifiedNote(command string) string {
	if command == "" {
		return ""
	}
	return "Do not re-run the test suite; kapellmeister's verification result above is authoritative.\n"
}

func verifyLine(command string, v VerifyResult, path string) string {
	if command == "" {
		return "kapellmeister did not verify this change: no verify command is configured.\n"
	}
	if v.TimedOut {
		return fmt.Sprintf("kapellmeister ran `%s`; it did not finish within %s and kapellmeister stopped it: it ran out of time, which says nothing about whether the tests pass. What it printed until then is in %s.\n", command, v.Limit, path)
	}
	line := fmt.Sprintf("kapellmeister ran `%s`; it exited %d. Its output is in %s.\n", command, v.ExitCode, path)
	if v.Attempts > 1 && v.ExitCode == 0 {
		line += fmt.Sprintf("It failed first and passed on attempt %d, so treat any test that failed as possibly flaky and say whether this change could have caused it.\n", v.Attempts)
	}
	return line
}

func reviewPrompt(task, plan, summary, command string, v VerifyResult, verifyPath, out, lessons string) string {
	part := partForStep("review")
	return preamble(part.Name) + taskBlock(task) + lessons + fmt.Sprintf(part.Prompt["review"], plan, summary, verifyLine(command, v, verifyPath), verifiedNote(command), out, out)
}

func gatePrompt(task, plan, summary string, reviews, failed []string, command string, v VerifyResult, verifyPath, out, lessons string) string {
	reviewNote := "the review in " + strings.Join(reviews, ", ")
	if len(reviews) != 1 {
		reviewNote = "every review: " + strings.Join(reviews, ", ")
	}
	failedNote := ""
	if len(failed) > 0 {
		failedNote = "These reviewers failed to complete their review: " + strings.Join(failed, ", ") + ". Weigh what their review might have caught, and check those parts of the diff yourself.\n"
	}
	approve := "Approve when the task is done and nothing is blocking."
	if command != "" {
		approve = "Approve when the task is done, the verification passed, and nothing is blocking."
	}
	part := partForStep("gate")
	return preamble(part.Name) + taskBlock(task) + lessons + fmt.Sprintf(part.Prompt["gate"], plan, summary, reviewNote, failedNote, verifyLine(command, v, verifyPath), verifiedNote(command), approve, out, out)
}

func testPlanPrompt(task, plan, out, scratch string, timeout time.Duration, conductorPID int, lessons, caoURL, stateDir string) string {
	limit := ""
	if timeout > 0 {
		limit = fmt.Sprintf("The test step has a time limit of %s. Plan scenarios ordered by risk (highest first) and sized to fit that limit.\n", timeout)
	}
	part := partForStep("test-plan")
	return preamble(part.Name) + taskBlock(task) + lessons + testerProcessBriefing(conductorPID, scratch, caoURL, stateDir) + fmt.Sprintf(part.Prompt["test-plan"], plan, limit, scratch, out)
}

func testPrompt(task, plan, testPlan, summary, command string, v VerifyResult, verifyPath, scratch, out string, timeout time.Duration, conductorPID int, lessons, caoURL, stateDir string, measurements []string) string {
	limit := ""
	if timeout > 0 {
		limit = fmt.Sprintf("The test step has a time limit of %s. Stop starting new scenarios when about %s remains (the last tenth of the limit), leaving time to clean up and write your verdict.\n", timeout, timeout/10)
	}
	part := partForStep("test")
	return preamble(part.Name) + taskBlock(task) + lessons + testerProcessBriefing(conductorPID, scratch, caoURL, stateDir) + fmt.Sprintf(part.Prompt["test"], plan, testPlan, summary, verifyLine(command, v, verifyPath), verifiedNote(command), limit, measurementsBlock(measurements), scratch, scratch, scratch, out)
}

func classifyPrompt(subject, path, out, lessons string) string {
	score := DefaultScore()
	part, _ := score.Part(score.Classifier)
	step, _ := score.Step(subject)
	return classifyPartPrompt(part, step.Kind, subject, path, out, lessons)
}

func classifyPartPrompt(part Part, kind StepKind, subject, path, out, lessons string) string {
	nitNote := ""
	if kind == KindDecide {
		nitNote = "Points it lists as nits or non-blocking are not required changes.\n"
	}
	if kind == KindTest {
		subject = "test report"
		nitNote = "APPROVE only when every scenario passed.\n"
	}
	return lessons + fmt.Sprintf(part.Prompt["classify"], path, subject, out, nitNote)
}

func nudgePrompt(path string) string {
	return fmt.Sprintf("You ended your turn without writing %s. If you have not yet done what the previous instructions asked, do that now, then write %s and reply with one short sentence.\n", path, path)
}

func preflightPrompt(path string) string {
	return fmt.Sprintf("This is a readiness check. Write the single word ready to %s and do nothing else. Then reply with: ready\n", path)
}

func lessonsPrompt(task string, res Result, files []string, out string, parts []string) string {
	return lessonsPartPrompt(DefaultScore().LessonsBy, task, res, files, out, parts)
}

func lessonsPartPrompt(part, task string, res Result, files []string, out string, parts []string) string {
	return preamble(part) + taskBlock(task) + fmt.Sprintf(`
The run ended with outcome %s after %d rounds. Read this run's records:
%s
From this run's plan, reviews and verdicts, what would have saved a round if a chair had known it at the start?
An empty list is a good and common answer. Propose a lesson only when it would plainly transfer to later runs on this repository.
Propose at most 5 lessons, each in one or two sentences, naming the part it is for and citing which step and which finding support it.
Write only a JSON array to %s, using this format:
[{"part":"<%s>","lesson":"<one or two sentences>","evidence":"<which step and which finding>"}]
Each lesson is limited to 400 characters and each evidence field to 300, with at most 4096 bytes of stored fields in total and a 32 KiB reply limit. Do not include control characters.
Do not modify any file other than %s. When done, reply with one short sentence.
`, res.Outcome, res.Rounds, strings.Join(files, "\n"), out, strings.Join(parts, "|"), out)
}

const lessonsOpen = "<lessons>"
const lessonsClose = "</lessons>"

func lessonsSection(l chairLessons) string {
	if l.text == "" {
		return ""
	}
	paths := make([]string, len(l.Files))
	for i, file := range l.Files {
		paths[i] = file.Path
	}
	section := lessonsOpen + "\nGuidance from earlier runs, checked in to this repository (" + strings.Join(paths, ", ") + ") and reviewed by a person. Weigh it as advice;\nthe task and the repository's agent instruction files take precedence.\n"
	if l.Cut {
		section += "Cut to the lesson budget; later lines were left out.\n"
	}
	return section + l.text + lessonsClose + "\n"
}

func testerProcessBriefing(pid int, scratch, orchestratorURL, stateDir string) string {
	return fmt.Sprintf("\nThe process %d is conducting this run; the orchestrator at %s and the state directory %s belong to that conductor; none of them is part of what you are testing, and none may be signalled, stopped or changed.\nYou may signal only processes you started yourself and listed in %s/pids.\n", pid, orchestratorURL, stateDir, scratch)
}

func readOnlyRetryPrompt(runDir string) string {
	return "Note: your previous attempt at this step changed the worktree, and km undid it. You must not create, modify, delete or stage any file in the worktree outside " + runDir + "; this is a read-only step."
}
