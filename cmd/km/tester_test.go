package main

import (
	"bytes"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestTheTesterFlagAndTheChairsTesterConfigBothResolve(t *testing.T) {
	for _, config := range []string{"", "[chairs]\ntester = 'claude'\n", "[ensembles.live]\ntester = 'claude'\n"} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprint(config, override), func(t *testing.T) {
				s, _ := ensembleWorktree(t, config)
				fs := flag.NewFlagSet("run", flag.ContinueOnError)
				roles := chairFlags(fs, loop.AllRoles()...)
				if override {
					if err := fs.Parse([]string{"-tester", "codex"}); err != nil {
						t.Fatal(err)
					}
				}
				name := ""
				if strings.Contains(config, "ensembles") {
					name = "live"
				}
				_, chairs, err := runChairs(s, name, roles)
				if err != nil {
					t.Fatal(err)
				}
				if config == "" && !override {
					if chairs.Tester != nil {
						t.Fatalf("unexpected tester: %+v", chairs.Tester)
					}
					return
				}
				provider := "claude_code"
				if override {
					provider = "codex"
				}
				if chairs.Tester == nil || chairs.Tester.Provider != provider {
					t.Fatalf("tester = %+v, want %s", chairs.Tester, provider)
				}
			})
		}
	}
	s := loop.DefaultSettings()
	s.Chairs["tester"] = "claude,codex"
	if _, err := resolveChairs(s.Chairs); err == nil || !strings.Contains(err.Error(), "tester") || !strings.Contains(err.Error(), "only reviewer") {
		t.Fatalf("tester list error = %v", err)
	}
}

func TestATesterFlagCannotOverrideAnEnsembleFile(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	roles := chairFlags(fs, loop.AllRoles()...)
	if err := fs.Parse([]string{"-tester", "claude"}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := lineUp(s, wt, "sectioned", roles)
	if err == nil || !strings.Contains(err.Error(), "-tester cannot override ensemble") {
		t.Fatal(err)
	}
	if _, _, _, err := lineUp(s, wt, "sectioned", noRoleFlags(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAPlayRecordWithATesterRebuildsItAndRunsItsStepsInItsOccupant(t *testing.T) {
	s := playStanding(t)
	address := "leads.tonmeister@score"
	s.Chairs = append(s.Chairs, ensemble.StandingChair{
		Address: address, Section: "leads", Name: "tonmeister", Part: "tester", TerminalID: "terminal-tonmeister", Briefed: true,
		Binding: loop.Chair{Provider: "claude_code", Profile: "km-claude", Model: "standing-test-model", Address: address},
	})
	savePlayStanding(t, s)
	chairs, err := s.RebuildChairs()
	if err != nil || chairs.Tester == nil || chairs.Tester.Address != address {
		t.Fatalf("rebuilt: %+v, %v", chairs, err)
	}
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil || !strings.Contains(out, "approved after 1 round(s)") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	found := map[string]bool{}
	for i, path := range fake.replies {
		if path == "test-plan.md" || path == "test-1.md" {
			req := fake.requests[i]
			if req.ReuseTerminalID != "terminal-tonmeister" || req.Provider != "claude_code" || req.Model != "standing-test-model" || req.Teardown {
				t.Errorf("%s request = %+v", path, req)
			}
			found[path] = true
		}
	}
	if !found["test-plan.md"] || !found["test-1.md"] {
		t.Fatal(fake.replies)
	}
	requirePlayStillStanding(t, s, fake, out)
}

func TestConfigPrintsTheTesterOnlyWhenSeated(t *testing.T) {
	for _, seated := range []bool{false, true} {
		s := loop.DefaultSettings()
		if seated {
			s.Chairs["tester"] = "claude"
			s.Ensembles = map[string]loop.Ensemble{"live": {Tester: loop.ChairSpec{Spec: "claude"}}}
		}
		var out bytes.Buffer
		printChairs(&out, s)
		printEnsembles(&out, s)
		if strings.Contains(out.String(), "tester=") != seated {
			t.Fatalf("seated %t: %s", seated, out.String())
		}
	}
}

func TestPreflightAndEffortProfilesCoverTheTester(t *testing.T) {
	s := loop.DefaultSettings()
	s.Chairs["tester"] = "claude@large"
	chairs, err := resolveChairs(s.Chairs)
	if err != nil {
		t.Fatal(err)
	}
	if chairs.Tester == nil {
		t.Fatal("tester not resolved")
	}
	steps := stepChairs(chairs)
	if len(steps["test"]) != 1 {
		t.Fatal(steps)
	}
	if len(defaultChairs(&s.Policy, chairs)) != 6 {
		t.Fatal("tester not included in preflight")
	}
	var found bool
	for _, variant := range s.Policy.EffortVariants(steps) {
		if variant.Name() == "km-claude-xhigh" {
			found = true
		}
	}
	if !found {
		t.Fatal("tester effort profile missing")
	}
	if err := checkPins(s.Policy, s.Chairs, chairs); err != nil {
		t.Fatal(err)
	}
	tiers := s.Policy.Tiers["claude_code"]
	delete(tiers, "large")
	if err := checkPins(s.Policy, s.Chairs, chairs); err == nil || !strings.Contains(err.Error(), "tester") {
		t.Fatalf("tester pin error = %v", err)
	}
}

func TestRunAuditionAndRehearsalAcceptTheTesterFlag(t *testing.T) {
	for _, command := range []struct {
		name string
		run  func([]string) error
	}{{"run", runCommand}, {"audition", auditionCommand}, {"rehearse", rehearseCommand}} {
		t.Run(command.name, func(t *testing.T) {
			err := command.run([]string{"-tester", "claude", "-help"})
			if err != flag.ErrHelp {
				t.Fatalf("tester flag: %v", err)
			}
		})
	}
}

func TestAFileEnsembleSeatsTheTesterWithItsTestTier(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	writeFile(t, filepath.Join(wt, ensemble.RepoDir, "sectioned.toml"), sectionedEnsemble+`
[sections.leads.chairs.tonmeister]
part = "tester"
agents = ["claude"]
`)
	_, chairs, members, err := lineUp(s, wt, "sectioned", noRoleFlags(t))
	if err != nil || chairs.Tester == nil {
		t.Fatalf("chairs: %+v, %v", chairs, err)
	}
	sizeMembers(s.Policy, members)
	for _, m := range members {
		if m.Part == "tester" && (m.Chair.Model != s.Policy.Tiers["claude_code"]["medium"].Model || m.Chair.Profile != "km-claude-high") {
			t.Fatalf("tester binding: %+v", m)
		}
	}
}
