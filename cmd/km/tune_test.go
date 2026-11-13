package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/tune"
)

func tuneCall(t *testing.T, args ...string) (int, string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	original := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = original }()
	var diag bytes.Buffer
	code := execute(append([]string{"tune"}, args...), &diag)
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range args {
		if arg == "-json" && len(body) > 0 {
			return code, string(body)
		}
	}
	return code, string(body) + diag.String()
}

func tuneFixture(t *testing.T) string {
	t.Helper()
	root := proofFixture(t, "")
	proofWrite(t, root, "answer.sh", "answer() { echo 42; }\n", 0644)
	proofWrite(t, root, "answer_test.sh", ". ./answer.sh\ntest \"$(answer)\" = 42\n", 0644)
	proofWrite(t, root, ".github/workflows/ci.yml", "jobs:\n  test:\n    steps:\n      - run: sh answer_test.sh\n", 0644)
	proofGit(t, root, "add", ".")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "test and CI")
	if out, err := initCall(t, root); err != nil {
		t.Fatal(out, err)
	}
	t.Setenv("KAPELLMEISTER_HOME", t.TempDir())
	t.Setenv("KAPELLMEISTER_STATE_DIR", t.TempDir())
	proofWrite(t, loop.Home(), "config.toml", "[agents]\navailable = ['mock']\n[agents.define]\nmock = 'test:mock'\n[run]\nrounds = 1\npreflight_timeout = '0s'\nreply_grace = '0s'\n", 0644)
	return root
}

type tuneCAO struct {
	mu          sync.Mutex
	mode        string
	steps       []string
	writes      []string
	profiles    []cao.Profile
	permissions map[string]cao.ProfilePermissions
	seated      []string
	hold        <-chan struct{}
}

func (f *tuneCAO) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if profileProbe(w, r, os.Getenv("CAO_HOME_DIR")) {
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/agents/profiles":
			profiles := f.profiles
			if profiles == nil {
				profiles = []cao.Profile{{Name: "mock", Loadable: true}}
			}
			json.NewEncoder(w).Encode(profiles)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/agents/profiles/"):
			name := filepath.Base(r.URL.Path)
			if f.permissions != nil {
				p, ok := f.permissions[name]
				if !ok {
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(p)
			} else {
				json.NewEncoder(w).Encode(map[string]string{"name": name})
			}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/output"):
			json.NewEncoder(w).Encode(map[string]string{"output": "Permission rule requires confirmation. Do you want to proceed?"})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/terminals/"):
			json.NewEncoder(w).Encode(cao.Terminal{ID: "writer"})
		case r.Method == "POST" && r.URL.Path == "/sessions":
			f.seated = append(f.seated, r.URL.Query().Get("agent_profile"))
			json.NewEncoder(w).Encode(cao.Terminal{ID: "podium", SessionName: "tune-fake"})
		case r.Method == "DELETE" || r.Method == "PATCH":
			w.Write([]byte(`{}`))
		case r.Method == "POST" && r.URL.Path == "/terminals/run-step":
			var req cao.StepRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if f.hold != nil {
				<-f.hold
			}
			if f.mode == "timeout" {
				f.steps = append(f.steps, "preflight")
				http.Error(w, preflightTimeoutBody, 504)
				return
			}
			if f.mode == "blocked" {
				f.steps = append(f.steps, "preflight")
				http.Error(w, "chair needs confirmation", 409)
				return
			}
			paths := playReplyPath.FindAllString(req.Prompt, -1)
			if len(paths) == 0 {
				t.Errorf("no reply path: %s", req.Prompt)
				w.WriteHeader(500)
				return
			}
			reply := paths[len(paths)-1]
			f.steps = append(f.steps, filepath.Base(reply))
			text := "APPROVE\n"
			if strings.HasSuffix(reply, ".json") {
				_, rest, ok := strings.Cut(req.Prompt, "Break patch directory: ")
				if !ok {
					t.Error("no break directory")
					w.WriteHeader(500)
					return
				}
				patchDir, _, _ := strings.Cut(rest, "\n")
				if err := os.MkdirAll(patchDir, 0755); err != nil {
					t.Error(err)
				}
				source := "diff --git a/answer.sh b/answer.sh\n--- a/answer.sh\n+++ b/answer.sh\n@@ -1 +1 @@\n-answer() { echo 42; }\n+answer() { echo 0; }\n"
				test := "diff --git a/answer_test.sh b/answer_test.sh\n--- a/answer_test.sh\n+++ b/answer_test.sh\n@@ -1,2 +1,2 @@\n . ./answer.sh\n-test \"$(answer)\" = 42\n+test \"$(answer)\" = 0\n"
				command := "sh answer_test.sh"
				if f.mode == "output" {
					command = "sh answer_test.sh || { echo BREAK_FAILURE_$(. ./answer.sh; answer); false; }"
				}
				if f.mode == "surviving output" {
					command = "sh answer_test.sh || echo SURVIVING_BREAK_$(. ./answer.sh; answer)"
				}
				if f.mode == "survives" {
					command = "true"
				}
				script, _ := initrepo.RenderVerify(command)
				if f.mode != "stub" {
					proofWrite(t, req.WorkingDirectory, "scripts/verify", script, 0755)
				}
				proofWrite(t, patchDir, "source.patch", source, 0644)
				proofWrite(t, patchDir, "test.patch", test, 0644)
				text = fmt.Sprintf(`{"command":%q,"left_out":[{"what":"integration","why":"requires a service"}],"patches":[{"file":%q,"kind":"source"},{"file":%q,"kind":"test"}]}`, command, filepath.Join(patchDir, "source.patch"), filepath.Join(patchDir, "test.patch"))
				switch f.mode {
				case "bypass script":
					proofWrite(t, req.WorkingDirectory, loop.ConfigFile, "[test]\nverify = 'sh answer_test.sh'\n", 0644)
					proofWrite(t, req.WorkingDirectory, "scripts/verify", initrepo.RenderStub(), 0755)
				case "leading tracked":
					proofWrite(t, req.WorkingDirectory, " leading", "bad\n", 0644)
				case "tracked":
					proofWrite(t, req.WorkingDirectory, "file", "bad\n", 0644)
				case "untracked":
					proofWrite(t, req.WorkingDirectory, "extra", "bad\n", 0644)
				case "deleted":
					if err := os.Remove(filepath.Join(req.WorkingDirectory, "other")); err != nil {
						t.Error(err)
					}
				case "malformed":
					text = "APPROVE"
				case "one patch":
					text = fmt.Sprintf(`{"command":%q,"patches":[{"file":%q,"kind":"source"}]}`, command, filepath.Join(patchDir, "source.patch"))
				}
			} else if f.mode == "review writes" {
				proofWrite(t, req.WorkingDirectory, "scripts/verify", "#!/bin/sh\ntrue\n", 0755)
			}
			proofWrite(t, req.WorkingDirectory, reply, text, 0644)
			json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "writer", Status: "completed"})
		default:
			f.writes = append(f.writes, r.Method+" "+r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnUnknownTuneSubjectIsNamedBeforeTheSubjectsAreListed(t *testing.T) {
	for _, subject := range []string{"unknown", "bad\nsubject"} {
		code, out := tuneCall(t, subject)
		if code != 2 || out != fmt.Sprintf("km tune: unknown subject %q\n%s\n", subject, tuneSubjects) {
			t.Fatal(code, out)
		}
	}
}

func TestBareTuneListsTheSubjectsWithoutNamingAnUnknownOne(t *testing.T) {
	code, out := tuneCall(t)
	if code != 2 || out != tuneSubjects+"\n" {
		t.Fatal(code, out)
	}
}

func TestTuneVerifyGuidePrintsTheProcedureWithoutARepositoryOrCAO(t *testing.T) {
	t.Setenv("KAPELLMEISTER_HOME", t.TempDir())
	code, out := tuneCall(t, "verify", "-guide", "-worktree", "/does/not/exist", "-cao", unreachableCAO)
	if code != 0 || !strings.Contains(out, "1. Read") || !strings.Contains(out, "4. Run `km prove`") {
		t.Fatal(code, out)
	}
}

func TestTuneVerifyWithNoSetupSaysToRunKmInitWithoutContactingCAO(t *testing.T) {
	root := proofFixture(t, "")
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", unreachableCAO)
	if code != 1 || !strings.Contains(out, "run km init") {
		t.Fatal(code, out)
	}
}

func TestTuneVerifyRefusesAFinishedVerifyUnlessAgainIsGiven(t *testing.T) {
	root := tuneFixture(t)
	script, _ := initrepo.RenderVerify("true")
	proofWrite(t, root, "scripts/verify", script, 0755)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", unreachableCAO)
	if code != 1 || !strings.Contains(out, "-again") {
		t.Fatal(code, out)
	}
	code, out = tuneCall(t, "verify", "-worktree", root, "-again", "-cao", unreachableCAO)
	if code != 1 || !strings.Contains(out, "cao-server") || strings.Contains(out, "pass -again") {
		t.Fatal(code, out)
	}
}

func TestTuneVerifyPreflightFailsFirstAndNamesOneFixPerFailure(t *testing.T) {
	for _, kind := range []string{"no chairs", "server", "profile"} {
		t.Run(kind, func(t *testing.T) {
			root := tuneFixture(t)
			f := &tuneCAO{}
			srv := f.serve(t)
			url := srv.URL
			want := ""
			switch kind {
			case "no chairs":
				proofWrite(t, loop.Home(), "config.toml", "", 0644)
				want = "[agents] available"
			case "server":
				url = unreachableCAO
				want = "cao-server"
			case "profile":
				proofWrite(t, loop.Home(), "config.toml", "[agents]\navailable = ['claude']\n", 0644)
				f.profiles = []cao.Profile{}
				want = "km profiles install claude"
			}
			before := initSnapshot(t, root)
			config := initRead(t, loop.Home(), "config.toml")
			code, out := tuneCall(t, "verify", "-worktree", root, "-cao", url)
			if code != 1 || !strings.Contains(out, want) {
				t.Fatal(code, out)
			}
			if len(f.steps) != 0 || len(f.writes) != 0 || !reflect.DeepEqual(before, initSnapshot(t, root)) || config != initRead(t, loop.Home(), "config.toml") {
				t.Fatal("preflight mutated state", f.steps, f.writes)
			}
			entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
			if err == nil && len(entries) != 0 {
				t.Fatal(entries)
			}
		})
	}
}

func TestTuneVerifyIsApprovedOnlyWhenKmProveReportsProvedAndLeavesOnlySetupUncommitted(t *testing.T) {
	root := tuneFixture(t)
	f := &tuneCAO{}
	srv := f.serve(t)
	head := proofGit(t, root, "rev-parse", "HEAD")
	count := proofGit(t, root, "rev-list", "--count", "HEAD")
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL, "-json")
	var sum map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &sum) != nil {
		t.Fatal(code, out)
	}
	if sum["outcome"] != "approved" || sum["command"] != "tune verify" {
		t.Fatal(sum)
	}
	proof := sum["tune"].(map[string]any)
	if proof["status"] != "proved" || proof["command"] != "sh answer_test.sh" || len(proof["runs"].([]any)) != 2 || len(proof["breaks"].([]any)) != 2 || len(proof["left_out"].([]any)) != 1 {
		t.Fatal(sum)
	}

	for i, kind := range []string{"source", "test"} {
		if proof["breaks"].([]any)[i].(map[string]any)["kind"] != kind {
			t.Fatal(proof)
		}
	}
	if _, err := os.Stat(sum["run_dir"].(string)); err != nil {
		t.Fatal("approved run directory is unavailable", err)
	}
	if got := proofGit(t, root, "rev-parse", "HEAD"); got != head || proofGit(t, root, "rev-list", "--count", "HEAD") != count || proofGit(t, root, "diff", "--cached") != "" {
		t.Fatal("git history or index changed")
	}
	if !strings.Contains(initRead(t, root, "scripts/verify"), "sh answer_test.sh") {
		t.Fatal(out)
	}
	info, err := os.Stat(filepath.Join(root, "scripts/verify"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal(info, err)
	}
	entries, _ := os.ReadDir(filepath.Join(loop.StateDir(), "runs", sum["run_id"].(string), "breaks"))
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	if len(f.steps) != 2 || len(f.writes) != 0 {
		t.Fatal(f.steps, f.writes)
	}
	var report, diag bytes.Buffer
	if err := reportCommand([]string{"-json", loop.StateDir()}, &report, &diag); err != nil || !strings.Contains(report.String(), `"command":"tune verify"`) {
		t.Fatal(report.String(), diag.String(), err)
	}
	var waited bytes.Buffer
	if err := waitCommand([]string{sum["run_id"].(string), loop.StateDir()}, &waited, &diag); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sh answer_test.sh", "requires a service", "clean run 1", "clean run 2", "source.patch", "test.patch", "left uncommitted"} {
		if !strings.Contains(waited.String(), want) {
			t.Fatal(want, waited.String())
		}
	}
}

func TestAnUnprovedOrOutOfScopeTuneIsRefusedAndLeavesTheCheckoutUntouched(t *testing.T) {
	for _, mode := range []string{"survives", "tracked", "untracked", "deleted", "stub", "malformed", "one patch", "review writes"} {
		t.Run(mode, func(t *testing.T) {
			root := tuneFixture(t)
			before := map[string]string{}
			for _, name := range []string{"scripts/verify", loop.ConfigFile, "file", "other"} {
				before[name] = initRead(t, root, name)
			}
			f := &tuneCAO{mode: mode}
			srv := f.serve(t)
			code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL, "-json")
			var sum map[string]any
			if code != 1 {
				t.Fatal(code, out)
			}
			text := strings.TrimSuffix(out, "km: run was not approved\n")
			if json.Unmarshal([]byte(text), &sum) != nil || sum["outcome"] == "approved" {
				t.Fatal(out)
			}
			for name, body := range before {
				if initRead(t, root, name) != body {
					t.Fatal("checkout changed", name)
				}
			}
			if mode != "review writes" && len(f.steps) != 1 {
				t.Fatal("review ran before proof", f.steps)
			}
			if len(f.writes) != 0 {
				t.Fatal(f.writes)
			}
		})
	}
}

func TestTheStubMessageAndInitsNextStepsNameKmTuneVerify(t *testing.T) {
	root := proofFixture(t, "")
	out, err := initCall(t, root)
	if err != nil || !strings.Contains(out, "km tune verify -worktree") || !strings.Contains(initRead(t, root, "scripts/verify"), "km tune verify") {
		t.Fatal(out, err)
	}
}

func TestTheSkillIsAThinWrapperAndTheReadmeDocumentsItsInstallation(t *testing.T) {
	skill, err := os.ReadFile("../../skills/km-tune-verify/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: km-tune-verify", "km tune verify -guide", "km prove"} {
		if !strings.Contains(string(skill), want) {
			t.Fatal(want, string(skill))
		}
	}
	if strings.Contains(string(skill), "1. Read") || strings.Contains(string(skill), "two break patches") {
		t.Fatal("skill duplicates procedure")
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"km tune verify [-worktree", "km tune verify -guide", "cp -R skills/km-tune-verify", ".claude/skills/"} {
		if !strings.Contains(string(readme), want) {
			t.Fatal("README missing", want)
		}
	}
}

func TestATuneIsRecordedAsARunThatStatusWaitAndReportSee(t *testing.T) {
	root := tuneFixture(t)
	hold := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(hold)
		}
	}()
	f := &tuneCAO{hold: hold, mode: "output"}
	srv := f.serve(t)
	cmd := kmSubprocess("tune", "verify", "-worktree", root, "-cao", srv.URL, "-detach", "--")
	var diag bytes.Buffer
	cmd.Stderr = &diag
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err, string(output), diag.String())
	}
	var id string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "run" {
			id = fields[1]
		}
	}
	if id == "" {
		t.Fatal(string(output))
	}
	var status, report bytes.Buffer
	if err := statusCommand([]string{loop.StateDir()}, &status, &diag); err != nil || !strings.Contains(status.String(), id) || !strings.Contains(status.String(), "tune verify") {
		t.Fatal(err, status.String(), diag.String())
	}
	if err := reportCommand([]string{"-json", loop.StateDir()}, &report, &diag); err != nil || !strings.Contains(report.String(), `"command":"tune verify"`) {
		t.Fatal(err, report.String())
	}
	close(hold)
	released = true
	out, errors, code := waitSubprocess(t, id, "-timeout", "10s", loop.StateDir())
	if code != 0 || !strings.Contains(out, "left uncommitted") || !strings.Contains(out, "proved:") {
		t.Fatal(code, out, errors)
	}
	assertCompleteTuneSummary(t, out)
	var sum ensembleSummary
	data, err := os.ReadFile(filepath.Join(loop.StateDir(), "runs", id, "result.json"))
	if err != nil || json.Unmarshal(data, &sum) != nil || sum.Outcome != loop.Approved {
		t.Fatal(err, string(data))
	}
}

func TestDetachedTuneKeepsRefusalsAndPreflightFailuresInTheForeground(t *testing.T) {
	root := tuneFixture(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", unreachableCAO, "-detach")
	if code != 1 || !strings.Contains(out, "cao-server") || strings.Contains(out, "pid ") {
		t.Fatal(code, out)
	}
	if dirs, _ := os.ReadDir(filepath.Join(loop.StateDir(), "runs")); len(dirs) > 0 {
		t.Fatal(dirs)
	}
}

func TestTheGuideAndTheTaskUseExactlyTheSameProcedure(t *testing.T) {
	code, out := tuneCall(t, "verify", "-guide")
	if code != 0 || out != tune.VerifyProcedure+"\n" {
		t.Fatal(code, out)
	}
	root := tuneFixture(t)
	f := &tuneCAO{}
	srv := f.serve(t)
	code, out = tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL, "-json")
	if code != 0 {
		t.Fatal(code, out)
	}
	var sum ensembleSummary
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(loop.StateDir(), "runs", sum.RunID, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config loop.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if strings.Count(config.Task, tune.VerifyProcedure) != 1 {
		t.Fatal(config.Task)
	}
}

func TestTuneRefusesAnExecutableVerifyHookEvenWithAgain(t *testing.T) {
	root := proofFixture(t, "")
	proofWrite(t, root, loop.VerifyHook, "#!/bin/sh\ntrue\n", 0755)
	code, out := tuneCall(t, "verify", "-again", "-worktree", root, "-cao", unreachableCAO)
	if code != 1 || !strings.Contains(out, "move it to scripts/verify") || strings.Contains(out, "cao-server") {
		t.Fatal(code, out)
	}
}

func TestTuneStopsAtABlockedSeatAndNamesTheSessionToFix(t *testing.T) {
	root := tuneFixture(t)
	proofWrite(t, loop.Home(), "config.toml", "[agents]\navailable = ['mock']\n[agents.define]\nmock = 'test:mock'\n[run]\npreflight_timeout = '1s'\n", 0644)
	config := initRead(t, loop.Home(), "config.toml")
	before := initRead(t, root, "scripts/verify")
	f := &tuneCAO{mode: "blocked"}
	srv := f.serve(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL)
	if code != 1 || strings.Count(out, "tmux attach -t tune-fake") != 1 || len(f.steps) != 1 || f.steps[0] != "preflight" {
		t.Fatal(code, out, f.steps)
	}
	want := "km: chair test:mock cannot answer preflight: Permission rule requires confirmation. Do you want to proceed?; resolve it with `tmux attach -t tune-fake`, then detach with Ctrl-b d\n"
	if !strings.HasSuffix(out, want) {
		t.Fatal(out)
	}
	if initRead(t, root, "scripts/verify") != before || initRead(t, loop.Home(), "config.toml") != config || len(f.writes) != 0 {
		t.Fatal("preflight mutated configuration")
	}
	if entries, _ := os.ReadDir(filepath.Join(loop.StateDir(), "runs")); len(entries) != 0 {
		t.Fatal(entries)
	}
	if got := proofGit(t, root, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatal(got)
	}
}

func TestTuneRefusesChangesToATrackedPathBeginningWithASpace(t *testing.T) {
	root := tuneFixture(t)
	proofWrite(t, root, " leading", "original\n", 0644)
	proofGit(t, root, "add", ".")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "path with space")
	f := &tuneCAO{mode: "leading tracked"}
	srv := f.serve(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL, "-json")
	if code != 1 || !strings.Contains(out, " leading") || strings.Contains(out, `"outcome":"approved"`) {
		t.Fatal(code, out)
	}
	if initRead(t, root, " leading") != "original\n" {
		t.Fatal("checkout changed")
	}
}

func TestTuneCannotProveADifferentCommandWhileLeavingTheScriptUnfinished(t *testing.T) {
	root := tuneFixture(t)
	before := initRead(t, root, "scripts/verify")
	f := &tuneCAO{mode: "bypass script"}
	srv := f.serve(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL, "-json")
	if code != 1 || strings.Contains(out, `"outcome":"approved"`) {
		t.Fatal(code, out)
	}
	if initRead(t, root, "scripts/verify") != before {
		t.Fatal("unproved script copied back")
	}
}

func TestAForegroundTuneShowsABreakPatchsFailureOutputOnce(t *testing.T) {
	for _, mode := range []string{"output", "surviving output"} {
		t.Run(mode, func(t *testing.T) {
			root := tuneFixture(t)
			srv := (&tuneCAO{mode: mode}).serve(t)
			code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL)
			wantCode := 0
			if mode == "surviving output" {
				wantCode = 1
			}
			if code != wantCode {
				t.Fatal(code, out)
			}
			if mode == "surviving output" {
				if strings.Count(out, "\nverify ok: verify\n") != 2 {
					t.Fatal(out)
				}
				return
			}
			for _, value := range []string{"0", "42"} {
				if strings.Count(out, "\nBREAK_FAILURE_"+value+"\n") != 1 {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestTheForegroundTuneSummaryHasOneLinePerBreakPatchWithItsResult(t *testing.T) {
	root := tuneFixture(t)
	srv := (&tuneCAO{mode: "output"}).serve(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL)
	if code != 0 {
		t.Fatal(code, out)
	}
	_, summary, found := strings.Cut(out, "approved")
	if !found {
		t.Fatal(out)
	}
	for _, want := range []string{"tune verify: sh answer_test.sh", "left out: integration — requires a service", "clean run 1: passed (exit 0) in ", "clean run 2: passed (exit 0) in ", "source.patch: breaks (exit 1) in ", "test.patch: breaks (exit 1) in ", "proved:", "left uncommitted"} {
		if strings.Count(summary, want) != 1 {
			t.Fatalf("%q: %s", want, summary)
		}
	}
	for _, unwanted := range []string{"verify failed, as required", "\nBREAK_FAILURE_"} {
		if strings.Contains(summary, unwanted) {
			t.Fatal(summary)
		}
	}
}

func assertCompleteTuneSummary(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"tune verify: sh answer_test.sh", "left out: integration — requires a service", "clean run 1: passed", "clean run 2: passed", "source.patch: breaks", "test.patch: breaks", "verify failed, as required", "\nBREAK_FAILURE_0\n", "\nBREAK_FAILURE_42\n", "left uncommitted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}

func TestKmWaitOnAFinishedTuneStillShowsACompleteSummaryOnItsOwn(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprint(asJSON), func(t *testing.T) {
			root := tuneFixture(t)
			srv := (&tuneCAO{mode: "output"}).serve(t)
			args := []string{"verify", "-worktree", root, "-cao", srv.URL}
			if asJSON {
				args = append(args, "-json")
			}
			code, out := tuneCall(t, args...)
			if code != 0 {
				t.Fatal(code, out)
			}
			entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
			var waited, diag bytes.Buffer
			if err := waitCommand([]string{entries[0].Name(), loop.StateDir()}, &waited, &diag); err != nil {
				t.Fatal(err, diag.String())
			}
			assertCompleteTuneSummary(t, waited.String())
			waited.Reset()
			if err := waitCommand([]string{entries[0].Name(), "-json", loop.StateDir()}, &waited, &diag); err != nil {
				t.Fatal(err)
			}
			var result waitResult
			if err := json.Unmarshal(waited.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Result == nil || result.Result.Tune == nil {
				t.Fatal(waited.String())
			}
			sum := result.Result
			if len(sum.Tune.Runs) != 2 || len(sum.Tune.Breaks) != 2 {
				t.Fatal(sum.Tune)
			}
			for i, value := range []string{"0", "42"} {
				if !strings.Contains(sum.Tune.Breaks[i].Output, "\nBREAK_FAILURE_"+value+"\n") {
					t.Fatal(sum.Tune.Breaks)
				}
			}
			if asJSON {
				var foreground ensembleSummary
				if err := json.Unmarshal([]byte(out), &foreground); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(foreground.Tune, sum.Tune) {
					t.Fatal("JSON facts changed", out, waited.String())
				}
			}
		})
	}
}

func TestTuneVerifyExplainsAPreflightTimeoutInPlainWords(t *testing.T) {
	root := tuneFixture(t)
	proofWrite(t, loop.Home(), "config.toml", "[agents]\navailable = ['mock']\n[agents.define]\nmock = 'test:mock'\n[run]\npreflight_timeout = '1s'\n", 0644)
	srv := (&tuneCAO{mode: "timeout"}).serve(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL)
	if code != 1 {
		t.Fatal(code, out)
	}
	assertPreflightTimeoutAdvice(t, out, "tune-fake")
	if entries, _ := os.ReadDir(filepath.Join(loop.StateDir(), "runs")); len(entries) != 0 {
		t.Fatal(entries)
	}
	if got := proofGit(t, root, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatal(got)
	}
}
