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
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const (
	MethodNamespace = "network namespace"
	MethodSandbox   = "sandbox profile"
	MethodProxy     = "proxy variables (best effort)"
)

const offlineProfile = "(version 1)(allow default)(deny network*)"

type OfflineOptions struct {
	Worktree string
	Timeout  time.Duration
	Method   string
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
		return `unshare --net --map-user="$(id -u)" --map-group="$(id -g)" sh -c ` + shellQuote(command)
	}
	proxy := shellQuote("http://" + closedPort())
	names := "http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY"
	var b strings.Builder
	for _, name := range strings.Fields(names) {
		b.WriteString(name + "=" + proxy + "\n")
	}
	b.WriteString("no_proxy=\nNO_PROXY=\nexport " + names + " no_proxy NO_PROXY\n")
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
	scratch, err := os.MkdirTemp("", "km-offline-")
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
	var skipped []string
	for {
		result.Runs++
		name := fmt.Sprintf("offline-%d", result.Runs)
		dir, err := cleanCopy(ctx, root, head, scratch, name, setup)
		if err != nil {
			return fail(err)
		}
		tmp := filepath.Join(scratch, name+"-tmp")
		if err := os.Mkdir(tmp, 0700); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(skip, []byte(strings.Join(skipped, "\n")+"\n"), 0600); err != nil {
			return fail(err)
		}
		record := filepath.Join(scratch, name+"-steps")
		prefix := "unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_OBJECT_DIRECTORY\nTMPDIR=" + shellQuote(tmp) + "\n" + initrepo.StepsEnv + "=" + shellQuote(record) + "\n" + initrepo.SkipEnv + "=" + shellQuote(skip) + "\nexport TMPDIR " + initrepo.StepsEnv + " " + initrepo.SkipEnv + "\n"
		run := initrepo.Run(ctx, dir, prefix+withoutNetwork(result.Method, command), timeout)
		if ctx.Err() != nil {
			return finish("interrupted", "interrupted")
		}
		if run.Status == "timed_out" {
			return finish("timeout", fmt.Sprintf("verify timed out without network after %s", timeout))
		}
		if run.ExitCode == 0 {
			if len(result.Failed) == 0 {
				return finish("passed", "verify passes on a clean copy without network")
			}
			return finish("failed", "")
		}
		failed, _ := os.ReadFile(record + ".failed")
		step := strings.TrimSuffix(string(failed), "\n")
		if step == "" {
			return finish("unknown", fmt.Sprintf("verify fails on a clean copy without network and did not record which step failed; its run wrapper must write the failing label to the file named by $%s with .failed added, as the wrapper km init writes does: %s", initrepo.StepsEnv, bounded(run.Output)))
		}
		if slices.Contains(skipped, step) {
			result.Partial = true
			return finish("failed", "")
		}
		result.Failed = append(result.Failed, OfflineFailure{Suite: step, Evidence: offlineEvidence(run.Output)})
		skipped = append(skipped, step)
	}
}
