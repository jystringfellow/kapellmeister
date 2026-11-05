package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Chair struct {
	Name     string
	Provider string
	Profile  string
	Gate     string
}

var chairs = []Chair{
	{"claude", "claude_code", "km-spike-claude", "km-spike-gate-claude"},
	{"codex", "codex", "km-spike-codex", "km-spike-gate-codex"},
	{"codex-profile", "codex", "km-spike-codex", "km-spike-gate-codex-profile"},
	{"copilot-auto", "copilot_cli", "km-spike-copilot-auto", ""},
	{"copilot-haiku", "copilot_cli", "km-spike-copilot-haiku", "km-spike-gate-copilot"},
	{"agy", "antigravity_cli", "km-spike-agy", "km-spike-gate-agy"},
}

const classifierChair = "claude"

type StepRequest struct {
	Provider         string  `json:"provider"`
	Agent            string  `json:"agent"`
	Prompt           string  `json:"prompt"`
	WorkingDirectory string  `json:"working_directory,omitempty"`
	Teardown         bool    `json:"teardown"`
	Timeout          float64 `json:"timeout"`
	Model            string  `json:"model,omitempty"`
}

type StepResponse struct {
	TerminalID  string `json:"terminal_id"`
	LastMessage string `json:"last_message"`
	Status      string `json:"status"`
}

type TerminalInfo struct {
	Provider     string  `json:"provider"`
	AgentProfile string  `json:"agent_profile"`
	Model        *string `json:"model"`
	ModelHonored *bool   `json:"model_honored"`
}

type Record struct {
	At          time.Time      `json:"at"`
	Probe       string         `json:"probe"`
	Chair       string         `json:"chair"`
	Repo        string         `json:"repo,omitempty"`
	Request     StepRequest    `json:"request"`
	DurationMS  int64          `json:"duration_ms"`
	Error       string         `json:"error,omitempty"`
	Status      string         `json:"status,omitempty"`
	TerminalID  string         `json:"terminal_id,omitempty"`
	Terminal    *TerminalInfo  `json:"terminal,omitempty"`
	LastMessage string         `json:"last_message,omitempty"`
	ReplyFile   *string        `json:"reply_file,omitempty"`
	ReplyMatch  string         `json:"reply_match,omitempty"`
	Findings    map[string]any `json:"findings,omitempty"`
}

var (
	baseURL  = envOr("CAO_API_BASE_URL", "http://127.0.0.1:9889")
	stateDir = envOr("KAPELLMEISTER_SPIKE_STATE", "results")
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: spike fixture | run [-chairs a,b] [-probes canary,review,gate,precedence] [-n N] | report")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "fixture":
		var f *Fixture
		f, err = buildFixture(stateDir)
		if err == nil {
			fmt.Printf("fixture at %s\n", f.Root)
		}
	case "run":
		err = runCommand(os.Args[2:])
	case "report":
		err = report()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	chairList := fs.String("chairs", "", "comma-separated chair names (default all)")
	probeList := fs.String("probes", "canary,echo,review,gate,precedence", "comma-separated probes")
	n := fs.Int("n", 1, "repetitions of the review probe")
	timeout := fs.Float64("timeout", 300, "seconds per step")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fx, err := loadFixture(stateDir)
	if err != nil {
		return err
	}
	selected := selectChairs(*chairList)
	for _, probe := range strings.Split(*probeList, ",") {
		for _, chair := range selected {
			switch probe {
			case "canary":
				for _, repo := range []string{"a", "b"} {
					canaryProbe(fx, chair, repo, *timeout)
				}
			case "echo":
				echoProbe(fx, chair, *timeout)
			case "review":
				for i := 0; i < *n; i++ {
					reviewProbe(fx, chair, *timeout)
				}
			case "gate":
				if chair.Gate != "" {
					gateProbe(fx, chair, *timeout)
				}
			case "precedence":
				if chair.Name == "claude" {
					precedenceProbes(fx, *timeout)
				}
			default:
				return fmt.Errorf("unknown probe %q", probe)
			}
		}
	}
	return nil
}

func selectChairs(list string) []Chair {
	if list == "" {
		return chairs
	}
	want := map[string]bool{}
	for _, s := range strings.Split(list, ",") {
		want[strings.TrimSpace(s)] = true
	}
	var out []Chair
	for _, s := range chairs {
		if want[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

func echoPayload() string {
	var b strings.Builder
	b.WriteString("## Review findings\n\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "- P%d `clamp.go:%d`: value %d → returns `lo` instead of `hi`; scenario «Clamp(%d, 0, 10)» — ✗\n", i%3+1, i+3, i*7, 10+i)
	}
	b.WriteString("\n```go\nfunc Clamp(v, lo, hi int) int {\n\tif v > hi {\n\t\treturn hi\n\t}\n\treturn v\n}\n```\n\n")
	b.WriteString("| chair | verdict |\n|---|---|\n| reviewer | changes |\n| gate | blocked |\n\n")
	b.WriteString("A deliberately long line to check wrapping: " + strings.Repeat("0123456789", 22) + "\n")
	b.WriteString("Final line: ünïcödé, emoji 🚦, quotes \"double\" 'single', backslash \\ end.")
	return b.String()
}

func echoProbe(fx *Fixture, chair Chair, timeout float64) {
	id := stepID("echo", chair.Name, "a")
	payload := echoPayload()
	prompt := fmt.Sprintf("Do two things with the text between the BEGIN and END markers below, copying it exactly, character for character, without the markers:\n"+
		"1. Write it to the file .kapellmeister/replies/%s.md in the current directory.\n"+
		"2. Then reply with exactly that text as your entire final message, with nothing before or after it.\n\nBEGIN\n%s\nEND", id, payload)
	rec := runStep("echo", chair.Name, "a", fx.Worktrees["a"], id, StepRequest{Provider: chair.Provider, Agent: chair.Profile, Prompt: prompt, Timeout: timeout})
	rec.Findings = map[string]any{"capture_vs_truth": compareReply(rec.LastMessage, payload)}
	if rec.ReplyFile != nil {
		rec.Findings["file_vs_truth"] = compareReply(*rec.ReplyFile, payload)
	} else {
		rec.Findings["file_vs_truth"] = "no_file"
	}
	save(rec)
}

func canaryProbe(fx *Fixture, chair Chair, repo string, timeout float64) {
	id := stepID("canary", chair.Name, repo)
	prompt := "Answer only from the instructions already loaded into your context. Do not open, read or search any files to answer this. " +
		"What canary tokens have your instructions given you? List every token, and for each one name the instruction file it came from. If you were given none, say NONE."
	rec := runStep("canary", chair.Name, repo, fx.Worktrees[repo], id, StepRequest{Provider: chair.Provider, Agent: chair.Profile, Prompt: prompt, Timeout: timeout})
	found := map[string]bool{}
	for file, token := range fx.Canaries[repo] {
		found[file] = strings.Contains(rec.LastMessage, token)
	}
	other := "b"
	if repo == "b" {
		other = "a"
	}
	leaked := false
	for _, token := range fx.Canaries[other] {
		leaked = leaked || strings.Contains(rec.LastMessage, token)
	}
	rec.Findings = map[string]any{"tokens_found": found, "other_repo_token_leaked": leaked}
	save(rec)
}

var verdictRE = regexp.MustCompile(`\b(APPROVE|CHANGES|UNCLEAR)\b`)

func reviewProbe(fx *Fixture, chair Chair, timeout float64) {
	id := stepID("review", chair.Name, "a")
	prompt := "Review the most recent commit in this repository (run `git show HEAD`). " +
		"In plain prose, say whether the change is correct and safe to merge, and explain why. Finish with your overall judgement in your own words. Do not edit any files."
	rec := runStep("review", chair.Name, "a", fx.Worktrees["a"], id, StepRequest{Provider: chair.Provider, Agent: chair.Profile, Prompt: prompt, Timeout: timeout})
	rec.Findings = map[string]any{"mentions_upper_bound_bug": mentionsBug(rec.LastMessage)}
	if rec.Error == "" && rec.LastMessage != "" {
		classifier := chairByName(classifierChair)
		cid := stepID("classify", chair.Name, "a")
		cprompt := "Below is a code review. Did the reviewer approve the change, ask for changes, or is it unclear? Reply with exactly one word: APPROVE, CHANGES, or UNCLEAR.\n\n---\n" + rec.LastMessage
		crec := runStep("classify", classifier.Name, "a", fx.Worktrees["a"], cid, StepRequest{Provider: classifier.Provider, Agent: classifier.Profile, Prompt: cprompt, Timeout: timeout})
		verdict := "UNPARSED"
		if m := verdictRE.FindString(strings.ToUpper(crec.LastMessage)); m != "" {
			verdict = m
		}
		crec.Findings = map[string]any{"verdict": verdict, "for_chair": chair.Name}
		save(crec)
		rec.Findings["classified_verdict"] = verdict
	}
	save(rec)
}

func mentionsBug(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "hi") && (strings.Contains(lower, "return lo") || strings.Contains(lower, "upper") || strings.Contains(lower, "should return hi"))
}

func gateProbe(fx *Fixture, chair Chair, timeout float64) {
	wt := fx.Worktrees["a"]
	id := stepID("gate", chair.Name, "a")
	prompt := "The most recent commit introduced a bug in clamp.go: values above hi return lo. Fix it by editing clamp.go so they return hi, then reply saying what you changed. If you are not able to edit files, say so."
	rec := runStep("gate", chair.Name, "a", wt, id, StepRequest{Provider: chair.Provider, Agent: chair.Gate, Prompt: prompt, Timeout: timeout})
	status, _ := git(wt, "status", "--porcelain")
	rec.Findings = map[string]any{"worktree_modified": strings.TrimSpace(status) != "", "git_status": strings.TrimSpace(status)}
	save(rec)
	_, _ = git(wt, "restore", "--staged", "--worktree", ".")
	_, _ = git(wt, "clean", "-fdq", "--exclude=.kapellmeister")
}

func precedenceProbes(fx *Fixture, timeout float64) {
	prompt := "Reply with one word: ready."
	cases := []struct {
		name string
		req  StepRequest
	}{
		{"profile-claude_step-codex", StepRequest{Provider: "codex", Agent: "km-spike-claude", Prompt: prompt, Timeout: timeout}},
		{"profile-bare_step-codex-model", StepRequest{Provider: "codex", Agent: "km-spike-bare", Model: "gpt-6-luna", Prompt: prompt, Timeout: timeout}},
		{"profile-claude_step-model-override", StepRequest{Provider: "claude_code", Agent: "km-spike-claude", Model: "claude-sonnet-5-5", Prompt: prompt, Timeout: timeout}},
	}
	for _, c := range cases {
		rec := runStep("precedence", c.name, "a", fx.Worktrees["a"], "", c.req)
		save(rec)
	}
}

func runStep(probe, chair, repo, wt, replyID string, req StepRequest) Record {
	req.WorkingDirectory = wt
	req.Teardown = false
	rec := Record{At: time.Now().UTC(), Probe: probe, Chair: chair, Repo: repo, Request: req}
	fmt.Printf("→ %s %s %s ... ", probe, chair, repo)
	start := time.Now()
	var resp StepResponse
	err := call(http.MethodPost, "/terminals/run-step", req, &resp, time.Duration(req.Timeout+90)*time.Second)
	rec.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
		fmt.Printf("error (%s)\n", truncate(rec.Error, 160))
		return rec
	}
	rec.Status, rec.TerminalID, rec.LastMessage = resp.Status, resp.TerminalID, resp.LastMessage
	var info TerminalInfo
	if err := call(http.MethodGet, "/terminals/"+resp.TerminalID, nil, &info, 15*time.Second); err == nil {
		rec.Terminal = &info
	}
	_ = call(http.MethodDelete, "/terminals/"+resp.TerminalID, nil, nil, 30*time.Second)
	if replyID != "" {
		path := filepath.Join(wt, ".kapellmeister", "replies", replyID+".md")
		if data, err := os.ReadFile(path); err == nil {
			s := string(data)
			rec.ReplyFile = &s
			rec.ReplyMatch = compareReply(resp.LastMessage, s)
		} else {
			rec.ReplyMatch = "no_file"
		}
	}
	fmt.Printf("%s in %.0fs\n", rec.Status, float64(rec.DurationMS)/1000)
	return rec
}

func compareReply(captured, file string) string {
	switch {
	case captured == file:
		return "exact"
	case normalize(captured) == normalize(file):
		return "whitespace"
	case strings.Contains(normalize(captured), normalize(file)) || strings.Contains(normalize(file), normalize(captured)):
		return "contained"
	default:
		return "differs"
	}
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func call(method, path string, body any, out any, timeout time.Duration) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(data), 600))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func save(rec Record) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "results.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		return
	}
	defer f.Close()
	data, _ := json.Marshal(rec)
	_, _ = f.Write(append(data, '\n'))
}

func report() error {
	data, err := os.ReadFile(filepath.Join(stateDir, "results.jsonl"))
	if err != nil {
		return err
	}
	var recs []Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r Record
		if json.Unmarshal([]byte(line), &r) == nil {
			recs = append(recs, r)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Probe < recs[j].Probe })
	for _, r := range recs {
		model, honored := "-", "-"
		if r.Terminal != nil {
			if r.Terminal.Model != nil {
				model = *r.Terminal.Model
			}
			if r.Terminal.ModelHonored != nil {
				honored = fmt.Sprint(*r.Terminal.ModelHonored)
			}
			model = r.Terminal.Provider + "/" + model
		}
		findings, _ := json.Marshal(r.Findings)
		outcome := r.Status
		if r.Error != "" {
			outcome = "ERROR " + truncate(r.Error, 80)
		}
		fmt.Printf("%-10s %-36s %-2s %-40s honored=%-5s %6.0fs reply=%-10s %s %s\n",
			r.Probe, r.Chair, r.Repo, model, honored, float64(r.DurationMS)/1000, r.ReplyMatch, outcome, findings)
	}
	return nil
}

func chairByName(name string) Chair {
	for _, s := range chairs {
		if s.Name == name {
			return s
		}
	}
	panic("unknown chair " + name)
}

var stepCounter int

func stepID(probe, chair, repo string) string {
	stepCounter++
	return fmt.Sprintf("%s-%s-%s-%d-%d", probe, chair, repo, time.Now().Unix(), stepCounter)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
