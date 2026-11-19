package loop

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	StrictFailed = "strict_failed"
	StrictNotRun = "strict_not_run"
)

type StrictResult struct {
	Status     string   `json:"status"`
	Command    string   `json:"command"`
	Ready      string   `json:"ready,omitempty"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Reply      string   `json:"reply,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Changed    []string `json:"changed,omitempty"`
	TimedOut   bool     `json:"timed_out,omitempty"`
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func ScriptPath(command string) string {
	rest := command
	for {
		word, raw, after, ok := shellWord(rest)
		if !ok {
			return ""
		}
		if assignment.MatchString(raw) {
			rest = after
			continue
		}
		if !strings.Contains(word, "/") || !filepath.IsLocal(word) {
			return ""
		}
		return filepath.Clean(word)
	}
}

func shellWord(text string) (word, raw, rest string, ok bool) {
	text = strings.TrimLeft(text, " \t\n")
	var out strings.Builder
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|':
			return out.String(), text[:i], text[i:], i > 0
		case c == '\'':
			end := strings.IndexByte(text[i+1:], '\'')
			if end < 0 {
				return "", "", "", false
			}
			out.WriteString(text[i+1 : i+1+end])
			i += end + 2
		case c == '"':
			end := strings.IndexByte(text[i+1:], '"')
			if end < 0 || strings.ContainsAny(text[i+1:i+1+end], "$`\\") {
				return "", "", "", false
			}
			out.WriteString(text[i+1 : i+1+end])
			i += end + 2
		case c == '\\':
			if i+1 >= len(text) {
				return "", "", "", false
			}
			out.WriteByte(text[i+1])
			i += 2
		case strings.IndexByte("$`*?[~(){}<>", c) >= 0:
			return "", "", "", false
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), text, "", i > 0
}

func StrictScripts(test TestConfig) []string {
	var names []string
	for _, command := range []string{test.Verify, test.Strict, test.StrictReady} {
		if name := ScriptPath(command); name != "" && name != "scripts/verify" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func ProbeFault(worktree, command string) string {
	name := ScriptPath(command)
	if name == "" {
		return ""
	}
	info, err := os.Stat(filepath.Join(worktree, name))
	switch {
	case os.IsNotExist(err):
		return name + " does not exist"
	case err != nil:
		return fmt.Sprintf("%s cannot be read: %s", name, err)
	case info.IsDir():
		return name + " is a directory"
	case info.Mode().Perm()&0o111 == 0:
		return name + " is not executable"
	}
	return ""
}

func emptyCommand(command string) bool {
	if command == "" {
		return false
	}
	for _, line := range strings.Split(command, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return false
		}
	}
	return true
}

func (e *Engine) strictScriptStates() map[string]string {
	states := map[string]string{}
	for _, name := range StrictScripts(TestConfig{Strict: e.Config.StrictCommand, StrictReady: e.Config.StrictReadyCommand}) {
		states[name] = scriptState(filepath.Join(e.Config.Worktree, name))
	}
	return states
}

func scriptState(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "missing"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return "link " + target
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
	}
	return fmt.Sprintf("%s %x", info.Mode(), sha256.Sum256(body))
}

func (e *Engine) strictChanged() []string {
	var changed []string
	now := e.strictScriptStates()
	for _, name := range StrictScripts(TestConfig{Strict: e.Config.StrictCommand, StrictReady: e.Config.StrictReadyCommand}) {
		if e.strictBefore[name] != now[name] {
			changed = append(changed, name)
		}
	}
	return changed
}

func (e *Engine) strictNotReached(res Result) Result {
	res.Strict = &StrictResult{Status: "not_reached", Command: e.Config.StrictCommand, Ready: e.Config.StrictReadyCommand, Reason: "strict not reached: the tester did not finish"}
	e.progress("  %s", res.Strict.Reason)
	return res
}

func (e *Engine) strict(ctx context.Context, res Result) Result {
	cfg := e.Config
	finish := e.beginStep(res.Rounds, ActiveStep{Step: "strict"})
	defer finish()
	reply := filepath.Join(RunDirRel(cfg.RunID), "strict.txt")
	strict := &StrictResult{Command: cfg.StrictCommand, Ready: cfg.StrictReadyCommand, Reply: reply, Changed: e.strictChanged()}
	res.Strict = strict
	if len(strict.Changed) > 0 {
		e.progress("  strict: this run changed %s after it started; what runs now is not what was configured at the start", strings.Join(strict.Changed, ", "))
	}
	const stands = "; the approved change is left in place and strict still has to pass"
	notRun := func(reason string) Result {
		strict.Status, strict.Reason, strict.Reply = "not_run", reason, ""
		res.Outcome = StrictNotRun
		res.Reason = "change approved by the gate, but " + reason + stands
		e.progress("  strict not run: %s", reason)
		return res
	}
	start := time.Now()
	settle := func(step string, v VerifyResult, err error) error {
		strict.ExitCode, strict.DurationMS = v.ExitCode, time.Since(start).Milliseconds()
		rec := Record{RunID: cfg.RunID, Round: res.Rounds, Step: step, DurationMS: strict.DurationMS, ExitCode: &strict.ExitCode, Reply: reply}
		if err != nil {
			rec.Error = cancellationReason(ctx, err)
		}
		e.record(rec)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cfg.Worktree, reply), []byte(tail(v.Output, 400)), 0o644); err != nil {
			strict.Reply, strict.Reason = "", "strict's output could not be saved: "+err.Error()
		}
		return nil
	}
	if cfg.StrictReadyCommand != "" {
		if fault := ProbeFault(cfg.Worktree, cfg.StrictReadyCommand); fault != "" {
			res = notRun("the strict_ready probe is broken: " + fault)
			res.Reason += "; fix [test] strict_ready"
			return res
		}
		e.progress("→ strict-ready %s", cfg.StrictReadyCommand)
		v, err := CommandVerifierWithin(cfg.StrictReadyCommand, cfg.StrictTimeout)(ctx, cfg.Worktree)
		if err := settle("strict-ready", v, err); err != nil {
			return notRun("the strict_ready probe did not finish: " + cancellationReason(ctx, err))
		}
		if v.TimedOut {
			strict.TimedOut = true
			return notRun(fmt.Sprintf("the strict_ready probe did not finish within %s", cfg.StrictTimeout))
		}
		if v.ExitCode != 0 {
			strict.Status = "not_run"
			res.Outcome = StrictNotRun
			res.Reason = fmt.Sprintf("change approved by the gate, but strict not run: %s exited %d: %s%s", cfg.StrictReadyCommand, v.ExitCode, strings.TrimSpace(tail(v.Output, 40)), stands)
			e.progress("  strict not run: %s exited %d\n%s", cfg.StrictReadyCommand, v.ExitCode, strings.TrimRight(tail(v.Output, 40), "\n"))
			return res
		}
		start = time.Now()
	}
	e.progress("→ strict %s", cfg.StrictCommand)
	v, err := CommandVerifierWithin(cfg.StrictCommand, cfg.StrictTimeout)(ctx, cfg.Worktree)
	if err := settle("strict", v, err); err != nil {
		return notRun("strict did not finish: " + cancellationReason(ctx, err))
	}
	if v.TimedOut {
		strict.Status, strict.TimedOut = "failed", true
		strict.Reason = fmt.Sprintf("strict did not finish within %s", cfg.StrictTimeout)
		res.Outcome = StrictFailed
		res.Reason = fmt.Sprintf("change approved by the gate, but %s and km stopped it; see %s; make strict faster or raise [test] strict_timeout; the approved change is left in place", strict.Reason, reply)
		e.progress("  %s; km stopped it and every process it started", strict.Reason)
		return res
	}
	if v.ExitCode == 0 {
		var ran []string
		for _, label := range cfg.KnownFailures {
			if slices.ContainsFunc(v.Known, func(known KnownFailure) bool { return known.Label == label }) {
				ran = append(ran, label)
			}
		}
		if mismatch := KnownFailureMismatch(ran, v.Known); mismatch != "" {
			v.ExitCode, strict.ExitCode = 16, 16
			strict.Reason = mismatch
		}
	}
	see := "see " + reply
	if strict.Reason != "" {
		see = strict.Reason
	}
	if v.ExitCode != 0 {
		strict.Status = "failed"
		res.Outcome = StrictFailed
		res.Reason = fmt.Sprintf("change approved by the gate, but strict failed (exit %d); %s; the approved change is left in place", v.ExitCode, see)
		e.progress("  strict failed (exit %d) in %s; %s", v.ExitCode, secs(strict.DurationMS), see)
		return res
	}
	strict.Status = "passed"
	e.progress("  strict passed in %s", secs(strict.DurationMS))
	if strict.Reply == "" {
		e.progress("  %s", strict.Reason)
	}
	return res
}
