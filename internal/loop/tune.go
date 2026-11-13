package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TuneOmission struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

type TunePatch struct {
	File string `json:"file"`
	Kind string `json:"kind"`
}

type TuneReply struct {
	Command string         `json:"command"`
	LeftOut []TuneOmission `json:"left_out"`
	Patches []TunePatch    `json:"patches"`
}

type TuneHooks struct {
	PatchDir string
	Changed  func() ([]string, error)
	Snapshot func() (string, error)
	Prove    func(context.Context, TuneReply, int) (bool, string, error)
	Publish  func() error
}

func (e *Engine) Tune(ctx context.Context, hooks TuneHooks) (res Result, err error) {
	cfg := e.Config
	res = Result{RunID: cfg.RunID, Worktree: cfg.Worktree, RunDir: filepath.Join(cfg.Worktree, RunDirRel(cfg.RunID))}
	e.mu.Lock()
	e.usage = Usage{}
	e.mu.Unlock()
	defer func() {
		if e.Meter != nil {
			e.mu.Lock()
			total := e.usage
			e.mu.Unlock()
			res.Usage = &total
		}
		if res.Outcome == Failed {
			res.Reason = cancellationReason(ctx, res.failure)
		}
		if sig := SignalOf(ctx); sig != nil {
			e.record(Record{RunID: cfg.RunID, Step: "interrupted", Error: sig.Error()})
		}
	}()
	stop := e.startWatcher(ctx)
	defer stop()
	if err := PrepareRunDir(cfg.Worktree, cfg.RunID); err != nil {
		return res, err
	}
	terminal := ""
	defer func() {
		if terminal != "" {
			_ = e.Stepper.Release(context.WithoutCancel(ctx), terminal)
		}
	}()
	feedback := ""
	lastReason := ""
	for round := 1; round <= cfg.MaxRounds; round++ {
		res.Rounds = round
		replyPath := e.relf("tune-%d.json", round)
		out, err := e.step(ctx, Step{Name: "implement", Round: round, Chair: cfg.Chairs.Implementer, Prompt: tuneWritePrompt(cfg.Task, replyPath, feedback), ReplyPath: replyPath, KeepTerminal: true, ReuseTerminal: terminal})
		if out.TerminalID != "" {
			terminal = out.TerminalID
		}
		if err != nil {
			return e.fail(res, err), nil
		}
		feedback = e.relf("tune-feedback-%d.txt", round)
		reject := func(reason string) error {
			lastReason = reason
			e.progress("  %s", reason)
			return os.WriteFile(filepath.Join(cfg.Worktree, feedback), []byte(reason+"\n"), 0644)
		}
		changed, err := hooks.Changed()
		if err != nil {
			return e.fail(res, err), nil
		}
		if len(changed) > 0 {
			reason := "refused: tune changed files outside setup: " + strings.Join(changed, ", ")
			if err := reject(reason); err != nil {
				return e.fail(res, err), nil
			}
			if round == cfg.MaxRounds {
				return e.fail(res, errors.New(reason)), nil
			}
			continue
		}
		text, err := e.readReply(replyPath)
		if err != nil {
			return e.fail(res, err), nil
		}
		reply, err := readTuneReply(text, hooks.PatchDir)
		if err != nil {
			if err := reject("invalid tune reply: " + err.Error()); err != nil {
				return e.fail(res, err), nil
			}
			continue
		}
		before, err := hooks.Snapshot()
		if err != nil {
			return e.fail(res, err), nil
		}
		start := time.Now()
		finish := e.beginStep(round, ActiveStep{Step: "prove"})
		e.progress("→ prove[%d]", round)
		proved, proofPath, err := hooks.Prove(ctx, reply, round)
		finish()
		code := 1
		if proved {
			code = 0
		}
		rec := Record{RunID: cfg.RunID, Round: round, Step: "prove", DurationMS: time.Since(start).Milliseconds(), ExitCode: &code, Reply: proofPath}
		if err != nil {
			rec.Error = cancellationReason(ctx, err)
		}
		e.record(rec)
		if err != nil {
			return e.fail(res, err), nil
		}
		if ctx.Err() != nil {
			return e.fail(res, ctx.Err()), nil
		}
		if !proved {
			lastReason = "km prove did not report proved; see " + proofPath
			feedback = proofPath
			e.progress("  %s", lastReason)
			continue
		}
		reviewPath := e.relf("tune-review-%d.md", round)
		errs, err := e.readOnlySteps(ctx, []Step{{Name: "review", Round: round, Chair: cfg.Chairs.Reviewers[0], ReplyPath: reviewPath, Prompt: tuneReviewPrompt(cfg.Task, replyPath, proofPath, reviewPath)}})
		if err != nil {
			return e.fail(res, err), nil
		}
		if errs[0] != nil {
			return e.fail(res, errs[0]), nil
		}
		after, err := hooks.Snapshot()
		if err != nil {
			return e.fail(res, err), nil
		}
		if after != before {
			return e.fail(res, errors.New("tune worktree changed after proof or during review")), nil
		}
		review, _ := e.readReply(reviewPath)
		verdict := lastVerdictLine(review)
		if verdict != "APPROVE" {
			verdict = "CHANGES"
		}
		e.record(Record{RunID: cfg.RunID, Round: round, Step: "verdict-review", Verdict: verdict, Reply: reviewPath, VerdictSource: "reply"})
		if verdict == "APPROVE" && proved {
			if err := hooks.Publish(); err != nil {
				return e.fail(res, err), nil
			}
			res.Outcome = Approved
			return res, nil
		}
		lastReason = "review requested changes; see " + reviewPath
		feedback = reviewPath
	}
	res.Outcome, res.Reason = RoundsExhausted, lastReason
	return res, nil
}

func readTuneReply(text, patchDir string) (TuneReply, error) {
	var reply TuneReply
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return reply, err
	}
	if strings.TrimSpace(reply.Command) == "" {
		return reply, errors.New("command is required")
	}
	if len(reply.Patches) < 2 {
		return reply, errors.New("at least two break patches are required, one source and one test")
	}
	root, err := filepath.EvalSymlinks(patchDir)
	if err != nil {
		return reply, err
	}
	kinds := map[string]bool{}
	seen := map[string]bool{}
	for i, patch := range reply.Patches {
		if patch.Kind != "source" && patch.Kind != "test" {
			return reply, errors.New("patch kind must be source or test")
		}
		path := patch.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return reply, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return reply, fmt.Errorf("patch %s must be in %s", patch.File, root)
		}
		info, err := os.Stat(path)
		if err != nil {
			return reply, err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return reply, fmt.Errorf("patch %s must be a non-empty regular file", path)
		}
		if seen[path] {
			return reply, fmt.Errorf("patch %s is repeated", path)
		}
		seen[path], kinds[patch.Kind] = true, true
		reply.Patches[i].File = path
	}
	if !kinds["source"] || !kinds["test"] {
		return reply, errors.New("one source patch and one test patch are required")
	}
	return reply, nil
}

func tuneWritePrompt(task, reply, feedback string) string {
	more := ""
	if feedback != "" {
		more = "Read the previous feedback in " + feedback + " and address it.\n"
	}
	return preamble("implementer") + taskBlock(task) + "\n" + more + fmt.Sprintf(`Write the verify and the break patches, following the procedure above.
Write your reply as JSON with this shape:
{"command":"the command inside scripts/verify","left_out":[{"what":"omitted check","why":"reason"}],"patches":[{"file":"absolute patch path","kind":"source"},{"file":"absolute patch path","kind":"test"}]}
Use an empty left_out list when nothing was omitted. Do not put patches in the checkout.
Write the JSON to %s. When done, reply with one short sentence.
`, reply)
}

func tuneReviewPrompt(task, reply, proof, out string) string {
	return preamble("reviewer") + taskBlock(task) + fmt.Sprintf(`
Read %s and the conductor's proof in %s. Read scripts/verify against CI workflows, the Makefile or task runner and README test instructions.
Check whether any CI checks are missing, whether each omission explains why it cannot run in a clean checkout with no services, and whether the source and test patches exercise what they claim.
Do not rerun verification. The conductor's proof is authoritative.
Write your findings to %s. Put APPROVE or CHANGES alone on its last line.
Do not modify any other file. When done, reply with one short sentence.
`, reply, proof, out)
}
