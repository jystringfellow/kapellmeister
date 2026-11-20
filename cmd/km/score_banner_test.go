package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestTheDefaultScoreBannerIsUnchangedByteForByte(t *testing.T) {
	var out bytes.Buffer
	p := loop.DefaultPolicy()
	printPolicy(&out, &p, loop.DefaultScore(), true)
	want := "tiers  plan medium (medium–large), implement medium (small–xlarge), review medium (small–large), gate medium (medium–large), classify small, test medium (medium–large)\n       sizing shadow by planner; the implementer moves up a tier after every 2 rounds without approval\n"
	if out.String() != want {
		t.Fatalf("banner = %q", out.String())
	}
}

func TestTheBannerListsThePlayingScoresStepsAndOnlyItsSizingAndEscalation(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		sizes, escalates, classifier bool
		after                        int
		tail                         string
	}{
		{name: "linear"},
		{name: "classifier", classifier: true},
		{name: "sizing", sizes: true, tail: "       sizing shadow by drafter\n"},
		{name: "escalation", escalates: true, after: 2, tail: "       draft moves up a tier after every 2 rounds without approval\n"},
		{name: "sizing and escalation", sizes: true, escalates: true, after: 2, tail: "       sizing shadow by drafter; draft moves up a tier after every 2 rounds without approval\n"},
		{name: "disabled escalation", escalates: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			score := loop.Score{Source: "user", Classifier: "classifier", Steps: []loop.ScoreStep{
				{Name: "inspect", Kind: loop.KindReview, Part: "reviewer"},
				{Name: "draft", Kind: loop.KindProduce, Part: "drafter", Sizes: tc.sizes, Escalate: tc.escalates},
				{Name: "probe-plan", Tier: "probe", Kind: loop.KindProduce, Part: "tester"},
				{Name: "probe", Kind: loop.KindTest, Part: "tester"},
			}}
			p, err := loop.DefaultPolicy().ForScore(score)
			if err != nil {
				t.Fatal(err)
			}
			p.Sizing.Decider = "drafter"
			p.Sizing.EscalateAfter = &tc.after
			var out bytes.Buffer
			printPolicy(&out, &p, score, tc.classifier)
			max := "large"
			if tc.escalates {
				max = "xlarge"
			}
			want := "tiers  inspect medium (small–large), draft medium (small–" + max + "), probe medium (small–large)"
			if tc.classifier {
				want += ", classify small"
			}
			want += "\n" + tc.tail
			if out.String() != want {
				t.Fatalf("banner = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestTheBannerNamesAllEscalatingSteps(t *testing.T) {
	score := loop.Score{Source: "user", Steps: []loop.ScoreStep{{Name: "draft", Kind: loop.KindProduce, Escalate: true}, {Name: "revise", Kind: loop.KindProduce, Escalate: true}}}
	p, err := loop.DefaultPolicy().ForScore(score)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPolicy(&out, &p, score, false)
	if !strings.Contains(out.String(), "draft, revise move up a tier after every 2 rounds without approval") {
		t.Fatal(out.String())
	}
}

func TestReplyNudgeFlagsAreMergedWithTheReplyGrace(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want time.Duration
		bad  bool
	}{
		{name: "set", args: []string{"-reply-nudge", "2s"}, want: 2 * time.Second},
		{name: "zero", args: []string{"-reply-nudge", "0s"}},
		{name: "clamp", args: []string{"-reply-grace", "1ms"}, want: time.Millisecond},
		{name: "too long", args: []string{"-reply-nudge", "1h", "-reply-grace", "1m"}, bad: true},
		{name: "negative", args: []string{"-reply-nudge", "-1s"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			fs := flag.NewFlagSet("run", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			c := commonFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			s, err := c.settings("")
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "reply_nudge") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || s.ReplyNudge != tc.want {
				t.Fatalf("nudge = %s, error = %v", s.ReplyNudge, err)
			}
		})
	}
}

func TestConfigShowsTheReplyNudge(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-cao", unreachableCAO}) })
	if err != nil || !strings.Contains(out, "reply nudge 30s") {
		t.Fatalf("config = %s, %v", out, err)
	}
}

func TestARunPassesTheReplyNudgeIntoTheRecordedConfiguration(t *testing.T) {
	f := &runCAO{fake: playCAO{}}
	_, args := runSessionArgs(t, f)
	_, err := captureStdout(t, func() error { return runCommand(append(args, "-reply-nudge", "1ms")) })
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(onlyRunRecord(t), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if value, ok := cfg["ReplyNudge"]; !ok || value != float64(time.Millisecond) {
		t.Fatalf("reply nudge = %v, present = %v", value, ok)
	}
}

func TestStatusShowsTheRoundWithoutAMaximumUnderAScoreWithNoBackwardExit(t *testing.T) {
	s, chairs, members, wt := smallRepositoryScore(t)
	for i := range s.Score.Score.Steps {
		for j := range s.Score.Score.Steps[i].Exits {
			exit := &s.Score.Score.Steps[i].Exits[j]
			if exit.Name == "changes" {
				exit.To = "changes_requested"
			}
		}
	}
	dir := filepath.Join(loop.StateDir(), "runs", "linear")
	f := &fileScoreStepper{}
	checked := false
	f.inspect = func(step loop.Step) {
		rows := statusRows(t, filepath.Dir(dir))
		if len(rows) != 1 || rows[0].Round != 1 || rows[0].MaxRounds != 0 {
			t.Errorf("status = %+v", rows)
		}
		out, _ := captureStatus(t, filepath.Dir(dir))
		fields := strings.Fields(strings.Split(strings.TrimSpace(out), "\n")[1])
		if len(fields) < 6 || fields[5] != "1" {
			t.Errorf("table = %s", out)
		}
		checked = true
	}
	_, err := runEnsemble(context.Background(), ensembleRun{Score: s.Score.Score, Members: members, RunID: "linear", StateDir: dir, Stepper: f, Progress: io.Discard, Config: loop.Config{RunID: "linear", Worktree: wt, Chairs: chairs, MaxRounds: 3}})
	if err != nil || !checked {
		t.Fatalf("run = %v, checked = %v", err, checked)
	}
}

func TestStatusStillShowsTheMaximumUnderTheDefaultScore(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "default")
	s.Round = 1
	saveStatusFixture(t, root, "default", s)
	out, _ := captureStatus(t, root)
	if !strings.Contains(out, "1/3") || statusRows(t, root)[0].MaxRounds != 3 {
		t.Fatal(out)
	}
}
