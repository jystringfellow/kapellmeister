package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type stepReply struct {
	files  []string
	handed []string
	failed []string
}

type feedbackFile struct{ step, path string }

type handoff struct {
	inputs       []stepReply
	files        []string
	feedback     []string
	decide, test string
	verify       VerifyResult
	verifyPath   string
}

func (h handoff) path(i int) string {
	if i < len(h.inputs) && len(h.inputs[i].files) > 0 {
		return h.inputs[i].files[0]
	}
	return ""
}

func (e *Engine) stepReply(name string, round int) string {
	if round == 0 {
		return e.rel(name + ".md")
	}
	return e.relf(name+"-%d.md", round)
}

func reviewChairName(part string, i, n int) string {
	if n == 1 {
		return part
	}
	return fmt.Sprintf("%s-%d", part, i+1)
}

func (e *Engine) walkScore(ctx context.Context, res Result, verify VerifyResult, verifyPath string) (Result, error) {
	score, cfg := e.playingScore(), e.Config
	played := map[string]stepReply{}
	var feedback []feedbackFile
	var decide, test feedbackFile
	var backgroundDone chan error
	var cancelBackground context.CancelFunc
	backgroundName := ""
	joinBackground := func() error {
		if backgroundDone == nil {
			return nil
		}
		err := <-backgroundDone
		backgroundDone = nil
		return err
	}
	defer e.releaseKept(ctx)
	defer func() {
		if cancelBackground != nil {
			cancelBackground()
		}
		if backgroundDone != nil {
			<-backgroundDone
		}
	}()
	handoffFor := func(spec ScoreStep) handoff {
		h := handoff{verify: verify, verifyPath: verifyPath}
		for _, name := range spec.Reads {
			reply := played[name]
			h.inputs = append(h.inputs, reply)
			h.files = append(h.files, reply.files...)
		}
		for _, file := range feedback {
			if slices.Contains(spec.Reads, file.step) {
				h.feedback = append(h.feedback, file.path)
			}
		}
		if slices.Contains(spec.Reads, decide.step) {
			h.decide = decide.path
		}
		if slices.Contains(spec.Reads, test.step) {
			h.test = test.path
		}
		return h
	}
	head := score.LoopHead()
	round, misses := 0, 0
	for index := 0; index < len(score.Steps); {
		spec := score.Steps[index]
		if spec.Background {
			return e.fail(res, fmt.Errorf("background step %q cannot be an exit target", spec.Name)), nil
		}
		if round == 0 && index >= head {
			if cfg.MaxRounds < 1 {
				break
			}
			if err := e.lowDisk(); err != nil {
				return e.fail(res, err), nil
			}
			round = 1
		}
		res.Rounds = round
		chairs := cfg.Chairs.Of(spec.Part)
		exit := ""
		if spec.Kind == KindTest && len(chairs) == 0 {
			exit = "absent"
		} else {
			if slices.Contains(spec.Reads, backgroundName) {
				if err := joinBackground(); err != nil {
					return e.fail(res, err), nil
				}
			}
			hand := handoffFor(spec)
			path := e.stepReply(spec.Name, round)
			if len(chairs) == 0 && spec.Kind != KindReview {
				return e.fail(res, fmt.Errorf("no chairs configured for part %q", spec.Part)), nil
			}
			var chair Chair
			if len(chairs) > 0 {
				chair = chairs[0]
			}
			step := Step{Name: spec.Name, Round: round, Chair: chair, ReplyPath: path, Prompt: e.brief(spec, hand, path, chair)}
			switch spec.Kind {
			case KindProduce:
				var err error
				if spec.Chairs == ChairsAll {
					replies, parallelErr := e.parallelReplies(ctx, spec, round, hand, nil)
					if parallelErr != nil {
						return e.fail(res, parallelErr), nil
					}
					reply := stepReply{}
					for _, r := range replies {
						if r.Err != nil {
							reply.failed = append(reply.failed, fmt.Sprintf("%s (%s)", r.Name, r.Chair))
						} else {
							reply.files = append(reply.files, r.Path)
						}
					}
					played[spec.Name] = reply
					exit = "done"
					break
				}
				if spec.Writes == "reply" {
					var errs []error
					errs, err = e.readOnlySteps(ctx, []Step{step})
					if err == nil {
						err = errs[0]
					}
				} else {
					_, err = e.step(ctx, step)
				}
				if err != nil {
					return e.fail(res, err), nil
				}
				played[spec.Name] = stepReply{files: []string{path}}
				if spec.Sizes {
					e.decideSize(ctx, path)
				}
				if spec.Writes != "reply" {
					if err := e.git("add", "--intent-to-add", "--all"); err != nil {
						return e.fail(res, err), nil
					}
					verifyPath = e.relf("verify-%d.txt", round)
					verify, err = e.runVerify(ctx, round, verifyPath)
					if err != nil {
						return e.fail(res, err), nil
					}
				}
				exit = "done"
			case KindReview:
				reviews, err := e.reviewRound(ctx, spec, round, hand)
				if err != nil {
					return e.fail(res, err), nil
				}
				reply := stepReply{}
				var verdicts []string
				for _, review := range reviews {
					if required, ok := e.readReview(round, review.Path); ok {
						reply.files = append(reply.files, review.Path)
						reply.handed = append(reply.handed, required)
					}
					if review.Err != nil {
						reply.failed = append(reply.failed, fmt.Sprintf("%s (%s)", review.Name, review.Chair))
					}
					verdicts = append(verdicts, review.Verdict)
				}
				played[spec.Name] = reply
				feedback = nil
				for _, path := range append(slices.Clone(reply.handed), verifyPath) {
					feedback = append(feedback, feedbackFile{spec.Name, path})
				}
				decide, test = feedbackFile{}, feedbackFile{}
				exit = "changes"
				if gateRuns(firstSet(cfg.GateWhen, spec.ApproveWhen), verify.ExitCode == 0, verdicts) {
					exit = "approve"
				}
			case KindDecide, KindTest:
				exit = "changes"
				if spec.Kind == KindDecide && verify.ExitCode != 0 {
					break
				}
				var verdict string
				var err error
				if spec.Kind == KindTest {
					verdict, err = e.testRound(ctx, step)
				} else {
					var errs []error
					errs, err = e.readOnlySteps(ctx, []Step{step})
					if err == nil {
						err = errs[0]
					}
					if err == nil {
						verdict, err = e.verdict(ctx, round, spec.Name, path, "")
					}
				}
				if err != nil {
					return e.testFailure(ctx, res, err), nil
				}
				played[spec.Name] = stepReply{files: []string{path}}
				if spec.Kind == KindDecide {
					e.gateRound = round
				} else {
					e.measured = len(e.measurements)
				}
				if verdict == "APPROVE" {
					exit = "approve"
				} else {
					file := feedbackFile{spec.Name, path}
					feedback = append([]feedbackFile{file}, feedback...)
					if spec.Kind == KindDecide {
						decide = file
					} else {
						test = file
					}
				}
			default:
				return e.fail(res, fmt.Errorf("unknown kind %q for step %q", spec.Kind, spec.Name)), nil
			}
		}
		if next := index + 1; next < len(score.Steps) && score.Steps[next].Background && backgroundName == "" {
			background := score.Steps[next]
			backgroundName = background.Name
			if chairs := cfg.Chairs.Of(background.Part); len(chairs) > 0 {
				path := e.stepReply(background.Name, round)
				played[background.Name] = stepReply{files: []string{path}}
				step := Step{Name: background.Name, Round: round, Chair: chairs[0], ReplyPath: path, Prompt: e.brief(background, handoffFor(background), path, chairs[0])}
				backgroundDone, cancelBackground = e.backgroundStep(ctx, step)
			}
		}
		target := ""
		for _, route := range spec.Exits {
			if route.Name == exit {
				target = route.To
				break
			}
		}
		if target == "" {
			return e.fail(res, fmt.Errorf("step %q has no target for exit %q", spec.Name, exit)), nil
		}
		if target == Approved || target == ChangesRequested {
			if err := joinBackground(); err != nil {
				return e.fail(res, err), nil
			}
			res.Outcome = target
			if target == ChangesRequested {
				files := played[spec.Name].files
				if decide.path != "" {
					files = []string{decide.path}
				}
				res.Reason = "changes requested: see " + strings.Join(files, ", ")
				if verify.ExitCode != 0 {
					res.Reason = fmt.Sprintf("verify exited %d: see %s; %s", verify.ExitCode, verifyPath, res.Reason)
				}
			}
			return res, nil
		}
		next := score.Index(target)
		if next < 0 {
			return e.fail(res, fmt.Errorf("step %q targets unknown step %q", spec.Name, target)), nil
		}
		if next <= index {
			misses++
			e.escalate(round, misses)
			if round+1 > cfg.MaxRounds {
				break
			}
			if err := e.lowDisk(); err != nil {
				return e.fail(res, err), nil
			}
			round++
		}
		index = next
	}
	if err := joinBackground(); err != nil {
		return e.fail(res, err), nil
	}
	res.Outcome = RoundsExhausted
	return res, nil
}

func (e *Engine) testFailure(ctx context.Context, res Result, err error) Result {
	var interrupted *testInterruptedError
	if errors.As(err, &interrupted) {
		res.Outcome = Untested
		res.Reason = "change approved by the gate but the tester did not finish: " + interrupted.Signal.Error() + "; the gate's approval stands unverified"
		if info, err := os.Stat(filepath.Join(e.Config.Worktree, interrupted.Path)); err == nil && info.Mode().IsRegular() {
			res.Reason += "; partial report: " + interrupted.Path
		}
		e.progress("  %s", res.Reason)
		e.record(Record{RunID: e.Config.RunID, Round: res.Rounds, Step: "unverified", Detail: res.Reason})
		return res
	}
	var timeout *testTimeoutError
	if errors.As(err, &timeout) {
		res.Outcome = Untested
		res.Reason = fmt.Sprintf("change approved by the gate but testing did not finish: test step timed out: %s", timeout.Err)
		if info, err := os.Stat(filepath.Join(e.Config.Worktree, timeout.Path)); err == nil && info.Mode().IsRegular() {
			res.Reason += "; partial report: " + timeout.Path
		}
		return res
	}
	return e.fail(res, err)
}

func (e *Engine) backgroundStep(ctx context.Context, step Step) (chan error, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := e.step(ctx, step); done <- err }()
	return done, cancel
}
