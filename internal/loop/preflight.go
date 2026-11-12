package loop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

type PreflightResult struct {
	SeatRetries    int      `json:"seat_retries,omitempty"`
	SeatRetryError string   `json:"seat_retry_error,omitempty"`
	Unseated       int      `json:"unseated,omitempty"`
	TeardownErrors []string `json:"teardown_errors,omitempty"`
	Chair          Chair    `json:"chair"`
	OK             bool     `json:"ok"`
	DurationMS     int64    `json:"duration_ms"`
	TerminalID     string   `json:"terminal_id,omitempty"`
	Session        string   `json:"session,omitempty"`
	Blocker        string   `json:"blocker,omitempty"`
	Error          string   `json:"error,omitempty"`
	Retried        bool     `json:"retried,omitempty"`
	Model          string   `json:"model,omitempty"`
	Honored        *bool    `json:"model_honored,omitempty"`
}

var blockerRE = regexp.MustCompile(`(?i)requires confirmation|do you want to proceed|needs review|do you trust|trust (this|all|the)|sign in|log ?in|authenticat|approve|allow .* to`)

func UniqueChairs(chairs ...Chair) []Chair {
	seen := map[Chair]bool{}
	var out []Chair
	for _, s := range chairs {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func Preflight(ctx context.Context, stepper interface {
	Stepper
	Seater
}, worktree string, chairs []Chair, timeout time.Duration) ([]PreflightResult, error) {
	if err := PrepareRunDir(worktree, "preflight"); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	prefix := "cao-km-preflight-" + hex.EncodeToString(nonce[:])
	results := make([]PreflightResult, len(chairs))
	sessions := make([][]string, len(chairs))
	seatSlot := make(chan struct{}, 1)
	var booting atomic.Int64
	var wg sync.WaitGroup
	for i, chair := range chairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("%s-%d", prefix, i)
			results[i], sessions[i] = preflightChairAttempts(ctx, stepper, worktree, i, chair, timeout, name, &booting, seatSlot)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		if n := booting.Load(); n > 0 {
			fmt.Fprintf(os.Stderr, "interrupted; waiting for %d chair(s) still booting before taking them down\n", n)
		}
		<-done
	}
	if ctx.Err() != nil {
		unseatInterruptedPreflight(ctx, stepper, results, sessions)
		return results, ctx.Err()
	}
	return results, nil
}

func preflightChairAttempts(ctx context.Context, stepper interface {
	Stepper
	Seater
}, worktree string, i int, chair Chair, timeout time.Duration, name string, booting *atomic.Int64, seatSlot chan struct{}) (PreflightResult, []string) {
	var sessions []string
	var unseated int
	var teardownErrors []string
	unseat := func(session string) {
		seatSlot <- struct{}{}
		defer func() { <-seatSlot }()
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := stepper.Unseat(cleanupCtx, session); err != nil {
			teardownErrors = append(teardownErrors, fmt.Sprintf("%s: %v", session, err))
			sessions = append(sessions, session)
		} else {
			unseated++
		}
	}

	var res PreflightResult
	var err error
	seatRetries := 0
	seatRetryError := ""
	for attempt := 0; ; attempt++ {
		attemptName := name
		if attempt == 1 {
			attemptName += "r"
		} else if attempt > 1 {
			attemptName += fmt.Sprintf("r%d", attempt)
		}
		res, err = preflightChair(ctx, stepper, worktree, i, chair, timeout, attemptName, booting, seatSlot)
		unseated += res.Unseated
		teardownErrors = append(teardownErrors, res.TeardownErrors...)
		res.Retried = attempt > 0
		if res.OK || res.Blocker != "" || ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			break
		}
		var seatErr *seatFailure
		transient := errors.As(err, &seatErr) && transientSeatFailure(err, attemptName)
		if !transient && attempt >= 1 || transient && attempt >= len(seatRetryBackoff) {
			break
		}
		if res.Session != "" {
			unseat(res.Session)
			res.Session, res.TerminalID = "", ""
		}
		if transient {
			if seatRetryError == "" {
				seatRetryError = err.Error()
			}
			wait := seatRetryBackoff[attempt]
			timer := time.NewTimer(wait + time.Duration(mathrand.Int64N(int64(wait)/2+1)))
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			if ctx.Err() != nil {
				break
			}
			seatRetries++
		}
	}
	res.SeatRetries, res.SeatRetryError = seatRetries, seatRetryError

	if res.Session != "" {
		if res.OK && ctx.Err() == nil {
			unseat(res.Session)
			res.TerminalID, res.Session = "", ""
		} else {
			sessions = append(sessions, res.Session)
		}
	}
	res.Unseated, res.TeardownErrors = unseated, teardownErrors
	return res, sessions
}

func unseatInterruptedPreflight(ctx context.Context, seater Seater, results []PreflightResult, sessions [][]string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, session := range sessions[i] {
				if err := seater.Unseat(cleanupCtx, session); err != nil {
					results[i].TeardownErrors = append(results[i].TeardownErrors, fmt.Sprintf("%s: %v", session, err))
				} else {
					results[i].Unseated++
					if results[i].Session == session {
						results[i].TerminalID, results[i].Session = "", ""
					}
				}
			}
		}()
	}
	wg.Wait()
}

func preflightChair(ctx context.Context, stepper interface {
	Stepper
	Seater
}, worktree string, i int, chair Chair, timeout time.Duration, name string, booting *atomic.Int64, seatSlot chan struct{}) (PreflightResult, error) {
	rel := filepath.Join(RunDirRel("preflight"), fmt.Sprintf("%d-%s.txt", i, chair.Provider))
	abs := filepath.Join(worktree, rel)
	_ = os.Remove(abs)
	res := PreflightResult{Chair: chair}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	select {
	case seatSlot <- struct{}{}:
	case <-ctx.Done():
		return res, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-seatSlot
		return res, err
	}
	start := time.Now()
	booting.Add(1)
	// CAO returns the owned session only after launch completes.
	seatCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seatTimeout(timeout))
	seat, err := stepper.Seat(seatCtx, name, chair, worktree)
	cancel()
	booting.Add(-1)
	res.TerminalID, res.Session = seat.TerminalID, seat.Session
	res.Model, res.Honored = seat.Model, seat.Honored
	if err == nil && (seat.TerminalID == "" || seat.Session == "") {
		err = errors.New("preflight seat returned no terminal or session")
	}
	if err != nil {
		err = &seatFailure{err}
	}
	if err != nil && seat.Session == "" {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if cleanupErr := stepper.Unseat(cleanupCtx, name); cleanupErr != nil {
			res.TeardownErrors = append(res.TeardownErrors, fmt.Sprintf("%s: %v", name, cleanupErr))
		} else {
			res.Unseated++
		}
		cleanupCancel()
	}
	<-seatSlot
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if err == nil {
		var out StepResult
		out, err = stepper.Run(ctx, Step{Name: "preflight", Label: TerminalLabel("preflight", chair.Provider), Chair: chair, Prompt: preflightPrompt(rel), Dir: worktree, ReplyPath: rel, Timeout: timeout, ReuseTerminal: seat.TerminalID})
		if out.ReportedModel != "" {
			res.Model = out.ReportedModel
		}
		if out.ModelHonored != nil {
			res.Honored = out.ModelHonored
		}
		if err == nil {
			if awaitPreflightReply(ctx, abs, preflightReplyGrace) {
				res.OK = true
				res.DurationMS = time.Since(start).Milliseconds()
				return res, nil
			}
			err = fmt.Errorf("%w at %s", errNoReply, rel)
		}
	}
	res.DurationMS = time.Since(start).Milliseconds()
	res.Error = err.Error()
	if ctx.Err() == nil {
		if sc, ok := stepper.(Screener); ok && res.TerminalID != "" {
			screenCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if _, screen, serr := sc.Screen(screenCtx, res.TerminalID); serr == nil {
				res.Blocker = FindBlocker(screen)
			}
		}
	}
	return res, err
}

const blockerLines = 12

var preflightReplyGrace = 30 * time.Second

func awaitPreflightReply(ctx context.Context, path string, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(strings.ToLower(string(data)), "ready") {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(min(500*time.Millisecond, remaining)):
		}
	}
}

var (
	permissionQuestionRE = regexp.MustCompile(`(?i)requires confirmation|do you want to|do you trust`)
	permissionFooterRE   = regexp.MustCompile(`(?i)esc to cancel`)
)

func FindPermissionDialog(screen string) string {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	lines = lines[max(0, len(lines)-blockerLines):]
	question := -1
	for i, line := range lines {
		if question < 0 && permissionQuestionRE.MatchString(line) {
			question = i
		}
		if question >= 0 && i > question && permissionFooterRE.MatchString(line) {
			return lines[question]
		}
	}
	return ""
}

func FindBlocker(screen string) string {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	for _, line := range lines[max(0, len(lines)-blockerLines):] {
		if blockerRE.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

var seatRetryBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

type seatFailure struct{ error }

func (e *seatFailure) Unwrap() error { return e.error }

func transientSeatFailure(err error, name string) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *cao.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.Status >= 500 && httpErr.Status < 600 || httpErr.Status == 400 && strings.Contains(httpErr.Body, "Session '"+name+"' not found")
}

func seatTimeout(preflight time.Duration) time.Duration {
	return max(preflight*3/8, 10*time.Second)
}
