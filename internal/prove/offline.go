package prove

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const (
	MethodNamespace = "network namespace"
	MethodSandbox   = "sandbox profile"
	MethodProxy     = "proxy variables (best effort)"
)

const offlineProfile = `(version 1)(allow default)(deny network*)(allow network-bind (local ip "localhost:*"))(allow network-inbound (local ip "localhost:*"))(allow network-outbound (remote ip "localhost:*"))(allow network-bind (local unix-socket))(allow network-inbound (local unix-socket))(allow network-outbound (remote unix-socket))`

const namespaceInner = `ip link set lo up 2>/dev/null || ifconfig lo up 2>/dev/null || exit 125
exec unshare --map-user="$1" --map-group="$2" sh -c "$3"`

type OfflineOptions struct {
	Worktree string
	Timeout  time.Duration
	Method   string
	Labels   []string
}

type OfflineFailure struct {
	Suite    string `json:"suite"`
	Evidence string `json:"evidence"`
}

type OfflineResult struct {
	Method  string           `json:"method"`
	Status  string           `json:"status"`
	Reason  string           `json:"reason,omitempty"`
	Failed  []OfflineFailure `json:"failed,omitempty"`
	Partial bool             `json:"partial,omitempty"`
	Runs    int              `json:"runs"`
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func closedPort() string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "127.0.0.1:9"
	}
	defer listener.Close()
	return listener.Addr().String()
}

func withoutNetwork(method, command string) string {
	switch method {
	case MethodSandbox:
		return "sandbox-exec -p " + shellQuote(offlineProfile) + " sh -c " + shellQuote(command)
	case MethodNamespace:
		return "unshare --net --map-root-user sh -c " + shellQuote(namespaceInner) + ` sh "$(id -u)" "$(id -g)" ` + shellQuote(command)
	}
	proxy := shellQuote("http://" + closedPort())
	names := "http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY"
	var b strings.Builder
	for _, name := range strings.Fields(names) {
		b.WriteString(name + "=" + proxy + "\n")
	}
	b.WriteString("no_proxy=localhost,127.0.0.1,::1\nNO_PROXY=$no_proxy\nexport " + names + " no_proxy NO_PROXY\n")
	return b.String() + command
}

func NoNetworkMethod(ctx context.Context) string {
	candidate := map[string]string{"darwin": MethodSandbox, "linux": MethodNamespace}[runtime.GOOS]
	if candidate == "" {
		return MethodProxy
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if exec.CommandContext(probe, "sh", "-c", withoutNetwork(candidate, "true")).Run() != nil {
		return MethodProxy
	}
	return candidate
}

var failedAt = regexp.MustCompile(`(?m)^verify failed at step: .* \(exit (\d+)\)$`)

func offlineEvidence(output string) string {
	evidence := "verify failed"
	if m := failedAt.FindStringSubmatch(output); m != nil {
		evidence = "exit " + m[1]
	}
	_, detail, found := strings.Cut(output, "\n--- last 40 lines of ")
	if !found {
		return evidence
	}
	last := ""
	for _, line := range strings.Split(detail, "\n")[1:] {
		if strings.HasPrefix(line, "full log: ") {
			break
		}
		if strings.TrimSpace(line) != "" {
			last = strings.TrimSpace(line)
		}
	}
	if last == "" {
		return evidence
	}
	if len(last) > 200 {
		last = last[:200]
	}
	return evidence + ": " + last
}

type offlineRun struct {
	passed   bool
	timedOut bool
	step     string
	evidence string
	ran      []string
	output   string
}

func Offline(ctx context.Context, options OfflineOptions) (result OfflineResult) {
	finish := func(status, reason string) OfflineResult {
		result.Status, result.Reason = status, reason
		if ctx.Err() != nil {
			result.Status, result.Reason = "interrupted", "interrupted: "+context.Cause(ctx).Error()
		}
		return result
	}
	fail := func(err error) OfflineResult { return finish("error", bounded(err.Error())) }
	root, err := filepath.Abs(options.Worktree)
	if err != nil {
		return fail(err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return fail(err)
	}
	head, err := git(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fail(err)
	}
	command, _, err := loop.ResolveHooks(root, "")
	if err != nil || command == "" {
		return finish("error", "verify is not configured")
	}
	setup, err := readSetup(root)
	if err != nil {
		return fail(err)
	}
	timeout := options.Timeout
	if timeout == 0 {
		test, err := loop.ResolveTest(root)
		if err != nil {
			return fail(err)
		}
		timeout = test.Timeout(DefaultTimeout)
	}
	result.Method = options.Method
	if result.Method == "" {
		result.Method = NoNetworkMethod(ctx)
	}
	scratch, err := os.MkdirTemp("", "km-prove-")
	if err != nil {
		return fail(err)
	}
	defer func() {
		if err := removeScratch(scratch); err != nil {
			result.Status, result.Reason = "error", "remove scratch copies: "+err.Error()
		}
	}()
	if scratch, err = filepath.EvalSymlinks(scratch); err != nil {
		return fail(err)
	}
	skip := filepath.Join(scratch, "skip")
	run := func(skipped []string, network bool, timed string) (offlineRun, error) {
		result.Runs++
		name := fmt.Sprintf("clean-%d", result.Runs)
		dir, err := cleanCopy(ctx, root, head, scratch, name, setup)
		if err != nil {
			return offlineRun{}, err
		}
		tmp := filepath.Join(scratch, name+"-tmp")
		if err := os.Mkdir(tmp, 0700); err != nil {
			return offlineRun{}, err
		}
		names := ""
		if len(skipped) > 0 {
			names = strings.Join(skipped, "\n") + "\n"
		}
		if err := os.WriteFile(skip, []byte(names), 0600); err != nil {
			return offlineRun{}, err
		}
		record := filepath.Join(scratch, name+"-steps")
		prefix := "unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_OBJECT_DIRECTORY\nTMPDIR=" + shellQuote(tmp) + "\n" + initrepo.StepsEnv + "=" + shellQuote(record) + "\n" + initrepo.SkipEnv + "=" + shellQuote(skip) + "\nexport TMPDIR " + initrepo.StepsEnv + " " + initrepo.SkipEnv + "\n"
		wrapped := command
		if !network {
			wrapped = withoutNetwork(result.Method, command)
		}
		runCtx, limit, stepTimedOut := ctx, timeout, func() bool { return false }
		if at := slices.Index(options.Labels, timed); timed != "" && at >= 0 {
			before := 0
			for _, label := range options.Labels[:at] {
				if !slices.Contains(skipped, label) {
					before++
				}
			}
			var stop context.CancelFunc
			runCtx, stop = context.WithCancel(ctx)
			defer stop()
			var fired atomic.Bool
			stepTimedOut = fired.Load
			limit = time.Duration(before+1) * timeout
			go func() {
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				var deadline <-chan time.Time
				for {
					select {
					case <-runCtx.Done():
						return
					case <-deadline:
						fired.Store(true)
						stop()
						return
					case <-tick.C:
						if deadline == nil {
							if data, _ := os.ReadFile(record); strings.Count(string(data), "\n") >= before {
								deadline = time.After(timeout)
							}
						}
					}
				}
			}()
		}
		verify := initrepo.Run(runCtx, dir, prefix+wrapped, limit)
		recorded, _ := os.ReadFile(record)
		failed, _ := os.ReadFile(record + ".failed")
		out := offlineRun{passed: verify.Status == "passed" && verify.ExitCode == 0, timedOut: verify.Status == "timed_out", step: strings.TrimSuffix(string(failed), "\n"), evidence: offlineEvidence(verify.Output), ran: strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n"), output: verify.Output}
		if stepTimedOut() {
			out.timedOut, out.step, out.evidence = true, timed, fmt.Sprintf("did not finish within %s", timeout)
		} else if out.timedOut {
			out.step, out.evidence = "", fmt.Sprintf("did not finish within %s", timeout)
			for _, label := range options.Labels {
				if !slices.Contains(out.ran, label) && !slices.Contains(skipped, label) {
					out.step = label
					break
				}
			}
		}
		return out, nil
	}
	var skipped []string
	for {
		first, err := run(skipped, false, "")
		if err != nil {
			return fail(err)
		}
		if ctx.Err() != nil {
			return finish("interrupted", "interrupted")
		}
		if first.passed {
			if len(result.Failed) == 0 {
				return finish("passed", "verify passes on a clean copy without network")
			}
			return finish("failed", "")
		}
		if first.step == "" && first.timedOut {
			return finish("timeout", fmt.Sprintf("verify timed out without network after %s and km could not tell which step was running", timeout))
		}
		if first.step == "" {
			return finish("unknown", fmt.Sprintf("verify fails on a clean copy without network and did not record which step failed; its run wrapper must write the failing label to the file named by $%s with .failed added, as the wrapper km init writes does: %s", initrepo.StepsEnv, bounded(first.output)))
		}
		if slices.Contains(skipped, first.step) {
			result.Partial = true
			return finish("failed", "")
		}
		alone := slices.Clone(skipped)
		if at := slices.Index(options.Labels, first.step); at >= 0 {
			alone = append(alone, options.Labels[at+1:]...)
		}
		again, err := run(alone, false, first.step)
		if err != nil {
			return fail(err)
		}
		if ctx.Err() != nil {
			return finish("interrupted", "interrupted")
		}
		if again.timedOut && again.step == "" {
			again.step = first.step
		}
		if first.timedOut && again.step != first.step {
			if again.passed || slices.Contains(again.ran, first.step) {
				return finish("timeout", fmt.Sprintf("verify did not finish within %s without network and was at step %q, but that step finished within %s when km timed it by itself without network: the verify as a whole is slower without network, and nothing was recorded for the step", timeout, first.step, timeout))
			}
			return finish("timeout", fmt.Sprintf("verify did not finish within %s without network at step %q, and when km ran it again to time that step it stopped at step %q first; nothing was recorded", timeout, first.step, again.step))
		}
		if again.step != first.step {
			return finish("flaky", fmt.Sprintf("step %q failed once without network (%s) and passed when run again without it: flaky, not a suite that needs network", first.step, first.evidence))
		}
		with, err := run(alone, true, first.step)
		if err != nil {
			return fail(err)
		}
		if ctx.Err() != nil {
			return finish("interrupted", "interrupted")
		}
		if with.step == first.step || !slices.Contains(with.ran, first.step) {
			return finish("failure", fmt.Sprintf("step %q failed twice without network (%s) and failed again with it: not a suite that needs network", first.step, first.evidence))
		}
		result.Failed = append(result.Failed, OfflineFailure{Suite: first.step, Evidence: first.evidence})
		skipped = append(skipped, first.step)
	}
}
