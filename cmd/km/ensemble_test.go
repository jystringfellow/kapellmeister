package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const sectionedEnsemble = `
gate_when = "all"
timeout = "20m"

[sections.leads.chairs.planner]
part = "planner"
agents = ["claude"]

[sections.leads.chairs.implementer]
part = "implementer"
agents = ["codex", "claude"]
timeout = "90m"

[sections.review.chairs.security]
part = "reviewer"
agents = ["copilot"]

[sections.review.chairs.style]
part = "reviewer"
agents = ["agy", "claude@large"]

[sections.leads.chairs.gate]
part = "gate"
agents = ["claude"]

[sections.leads.chairs.classifier]
part = "classifier"
agents = ["claude@small"]
`

func ensembleWorktree(t *testing.T, userConfig string) (loop.Settings, string) {
	t.Helper()
	if !strings.Contains(userConfig, "[agents]") {
		userConfig = builtInAgentConfig + userConfig
	}
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	if err := os.WriteFile(loop.UserConfigPath(home), []byte(userConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, ensemble.RepoDir, "sectioned.toml"), sectionedEnsemble)
	s, err := loop.LoadSettings(home, wt)
	if err != nil {
		t.Fatal(err)
	}
	return s, wt
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noRoleFlags(t *testing.T, args ...string) map[string]*string {
	t.Helper()
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	roles := chairFlags(fs, loop.ChairRoles...)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return roles
}

func TestAnEnsembleFileInTheWorktreeSeatsTheRunsChairsFromTheUsersAgents(t *testing.T) {
	s, wt := ensembleWorktree(t, "[agents]\navailable = [\"claude\", \"copilot\"]\n")
	selected, chairs, members, err := lineUp(s, wt, "sectioned", noRoleFlags(t))
	if err != nil {
		t.Fatal(err)
	}
	if selected.GateWhen != "all" {
		t.Errorf("gate_when = %s, want the file's all", selected.GateWhen)
	}
	if chairs.Implementer.Profile != "km-claude" || chairs.Implementer.Timeout != 90*time.Minute {
		t.Errorf("implementer = %+v, want claude, the first available agent, with its own 90m", chairs.Implementer)
	}
	if len(chairs.Reviewers) != 2 || chairs.Reviewers[1].Tier != "large" || chairs.Gate.Timeout != 20*time.Minute {
		t.Errorf("chairs = %+v", chairs)
	}
	if len(members) != 6 || members[4].Address("sectioned") != "review.security@sectioned" {
		t.Errorf("members = %+v", members)
	}
}

func TestARoleFlagCannotOverrideAnEnsembleFile(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	_, _, _, err := lineUp(s, wt, "sectioned", noRoleFlags(t, "-gate", "codex"))
	if err == nil || !strings.Contains(err.Error(), "-gate cannot override ensemble sectioned from "+filepath.Join(wt, ensemble.RepoDir, "sectioned.toml")) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnEnsembleInBothTheUserConfigAndAFileIsRejectedAsAmbiguous(t *testing.T) {
	s, wt := ensembleWorktree(t, "[ensembles.sectioned]\ngate = \"codex\"\n")
	_, _, _, err := lineUp(s, wt, "sectioned", noRoleFlags(t))
	if err == nil || !strings.Contains(err.Error(), "ensemble sectioned is defined both in [ensembles.sectioned] of "+loop.UserConfigPath(loop.Home())+" and in "+filepath.Join(wt, ensemble.RepoDir, "sectioned.toml")) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnUnknownEnsembleListsTheConfigAndFileEnsembles(t *testing.T) {
	s, wt := ensembleWorktree(t, "[ensembles.quick]\ngate = \"codex\"\n")
	_, _, _, err := lineUp(s, wt, "nope", noRoleFlags(t))
	if err == nil || !strings.Contains(err.Error(), `unknown ensemble "nope" (defined: quick, sectioned)`) {
		t.Fatalf("err = %v", err)
	}
}

func TestWithoutAnEnsembleNameTheChairsSeatEachPartInItsOwnSection(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	_, chairs, members, err := lineUp(s, wt, "", noRoleFlags(t, "-reviewer", "copilot,agy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chairs.Reviewers) != 2 || members[3].Address("default") != "reviewer.reviewer-2@default" {
		t.Fatalf("chairs = %+v, members = %+v", chairs, members)
	}
}

func TestAPinInAUserEnsembleFileIsCheckedAgainstTheTierTables(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	writeFile(t, filepath.Join(loop.Home(), ensemble.UserDir, "sectioned.toml"), strings.Replace(sectionedEnsemble, `agents = ["claude@small"]`, `agents = ["myprov:myprof@small"]`, 1))
	_, _, _, err := lineUp(s, wt, "sectioned", noRoleFlags(t))
	if err == nil || !strings.Contains(err.Error(), `leads.classifier@sectioned: chair "myprov:myprof@small": no [tiers.myprov] to pin a tier against`) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigListsEnsembleFilesWithWhereEachComesFrom(t *testing.T) {
	_, wt := ensembleWorktree(t, "")
	writeFile(t, filepath.Join(loop.Home(), ensemble.UserDir, "mine.toml"), "[sections.all.chairs.gate]\npart = \"gate\"\nagents = [\"claude\"]\n")
	var out bytes.Buffer
	printEnsembleFiles(&out, loop.Home(), wt)
	want := "ensemble mine  " + filepath.Join(loop.Home(), ensemble.UserDir, "mine.toml") + "\nensemble sectioned  " + filepath.Join(wt, ensemble.RepoDir, "sectioned.toml") + "\n"
	if out.String() != want {
		t.Fatalf("ensembles:\n%s\nwant:\n%s", &out, want)
	}
}

func TestARelativeWorktreeBecomesAnAbsolutePathBeforeAnyChairStartsInIt(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, path := range []string{".", "./", dir} {
		if got, err := worktreeDir(path); err != nil || got != want {
			t.Errorf("worktreeDir(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
}
