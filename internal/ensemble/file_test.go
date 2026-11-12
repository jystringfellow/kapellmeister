package ensemble

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

var everyAgent = []string{"claude", "codex", "copilot", "agy"}

func writeEnsemble(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name+".toml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func repoEnsemble(t *testing.T, name, body string) (home, worktree string) {
	t.Helper()
	home, worktree = t.TempDir(), t.TempDir()
	writeEnsemble(t, filepath.Join(worktree, RepoDir), name, body)
	return home, worktree
}

func find(t *testing.T, name, home, worktree string) Spec {
	t.Helper()
	spec, found, err := Find(name, home, worktree, loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("ensemble %s not found", name)
	}
	return spec
}

func bind(t *testing.T, spec Spec, available []string) []Member {
	t.Helper()
	members, err := spec.Bind(loop.BuiltInAgents(), available)
	if err != nil {
		t.Fatal(err)
	}
	return members
}

const thorough = `
gate_when = "all"

[sections.leads.chairs.planner]
part = "planner"
agents = ["claude@large"]

[sections.leads.chairs.implementer]
part = "implementer"
agents = ["codex"]

[sections.review.chairs.security]
part = "reviewer"
agents = ["claude"]

[sections.review.chairs.style]
part = "reviewer"
agents = ["copilot"]

[sections.leads.chairs.gate]
part = "gate"
agents = ["claude"]

[sections.leads.chairs.classifier]
part = "classifier"
agents = ["claude"]
`

func TestARepositoryEnsembleFileSeatsItsChairsInSectionsInFileOrder(t *testing.T) {
	home, wt := repoEnsemble(t, "thorough", thorough)
	spec := find(t, "thorough", home, wt)
	if spec.Path != filepath.Join(wt, RepoDir, "thorough.toml") || spec.GateWhen != "all" {
		t.Fatalf("spec = %+v", spec)
	}
	var got []string
	for _, m := range bind(t, spec, everyAgent) {
		got = append(got, m.Address("thorough")+":"+m.Part+":"+m.Chair.String()+"@"+m.Chair.Tier)
	}
	want := []string{
		"leads.planner@thorough:planner:claude_code:km-claude@large",
		"leads.implementer@thorough:implementer:codex:km-codex@",
		"leads.gate@thorough:gate:claude_code:km-claude@",
		"leads.classifier@thorough:classifier:claude_code:km-claude@",
		"review.security@thorough:reviewer:claude_code:km-claude@",
		"review.style@thorough:reviewer:copilot_cli:km-copilot@",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members\n got: %v\nwant: %v", got, want)
	}
}

func TestAChairPlaysTheFirstOfItsAgentsThatTheUserHas(t *testing.T) {
	home, wt := repoEnsemble(t, "solo", `
[sections.all.chairs.implementer]
part = "implementer"
agents = ["codex", "agy", "claude@small"]
`)
	members := bind(t, find(t, "solo", home, wt), []string{"copilot", "claude"})
	if c := members[0].Chair; c.Profile != "km-claude" || c.Tier != "small" {
		t.Fatalf("implementer plays %s@%s, want claude@small", c, c.Tier)
	}
}

func TestAChairWithNoneOfItsAgentsAvailableIsRejectedNamingTheChairAndItsAgents(t *testing.T) {
	home, wt := repoEnsemble(t, "solo", `
[sections.all.chairs.implementer]
part = "implementer"
agents = ["codex", "agy"]
`)
	_, err := find(t, "solo", home, wt).Bind(loop.BuiltInAgents(), []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "all.implementer@solo: none of codex, agy is in [agents] available (claude)") {
		t.Fatalf("err = %v", err)
	}
}

func TestAChairsTimeoutWinsOverItsSectionsWhichWinsOverTheEnsembles(t *testing.T) {
	home, wt := repoEnsemble(t, "paced", `
timeout = "30m"

[sections.hard]
timeout = "2h"

[sections.hard.chairs.implementer]
part = "implementer"
agents = ["codex"]

[sections.hard.chairs.planner]
part = "planner"
agents = ["claude"]
timeout = "10m"

[sections.easy.chairs.gate]
part = "gate"
agents = ["claude"]
`)
	got := map[string]time.Duration{}
	for _, m := range bind(t, find(t, "paced", home, wt), everyAgent) {
		got[m.Name] = m.Chair.Timeout
	}
	want := map[string]time.Duration{"implementer": 2 * time.Hour, "planner": 10 * time.Minute, "gate": 30 * time.Minute}
	for name, limit := range want {
		if got[name] != limit {
			t.Errorf("%s timeout = %s, want %s", name, got[name], limit)
		}
	}
}

func TestARepositoryEnsembleNamesOnlyAgentsWhileAUserEnsembleMayNameAnyChair(t *testing.T) {
	body := `
[sections.all.chairs.implementer]
part = "implementer"
agents = ["codex:my-codex:gpt-6-astra"]
`
	home, wt := repoEnsemble(t, "mine", body)
	_, _, err := Find("mine", home, wt, loop.BuiltInAgents())
	if err == nil || !strings.Contains(err.Error(), "sections.all.chairs.implementer.agents: \"codex:my-codex:gpt-6-astra\" is not an agent (claude, codex, copilot, agy); a repository ensemble names agents, with optional @small|@medium|@large") {
		t.Fatalf("err = %v", err)
	}
	home = t.TempDir()
	writeEnsemble(t, filepath.Join(home, UserDir), "mine", body)
	members := bind(t, find(t, "mine", home, t.TempDir()), []string{"claude"})
	if c := members[0].Chair; c.Profile != "my-codex" || c.Model != "gpt-6-astra" {
		t.Fatalf("implementer plays %s, want the user's own chair", c)
	}
}

func TestAUserEnsembleFileShadowsTheRepositorysOfTheSameName(t *testing.T) {
	home, wt := repoEnsemble(t, "thorough", thorough)
	path := writeEnsemble(t, filepath.Join(home, UserDir), "thorough", `
[sections.all.chairs.implementer]
part = "implementer"
agents = ["claude"]
`)
	if spec := find(t, "thorough", home, wt); spec.Path != path {
		t.Fatalf("found %s, want the user's %s", spec.Path, path)
	}
	if names := Names(home, wt); !slices.Equal(names, []string{"thorough"}) {
		t.Fatalf("names = %v, want thorough once", names)
	}
}

func TestAnEnsembleThatIsNotDefinedIsNotFound(t *testing.T) {
	home, wt := repoEnsemble(t, "thorough", thorough)
	if _, found, err := Find("quick", home, wt, loop.BuiltInAgents()); err != nil || found {
		t.Fatalf("found = %v, err = %v", found, err)
	}
	if names := Names(home, ""); len(names) != 0 {
		t.Fatalf("names without a worktree = %v, want none", names)
	}
}

func TestAMalformedEnsembleFileIsRejectedNamingTheFileAndKey(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"[sections.all.chairs.x]\npart = \"conductor\"\nagents = [\"claude\"]\n", "sections.all.chairs.x.part: unknown part \"conductor\" (want planner, implementer, reviewer, gate, classifier)"},
		{"[sections.all.chairs.x]\npart = \"gate\"\n", "sections.all.chairs.x.agents: want at least one agent"},
		{"[sections.all.chairs.x]\npart = \"gate\"\nagents = [\"claude\"]\nmodel = \"m\"\n", "unknown keys"},
		{"[sections.all.chairs.x]\npart = \"gate\"\nagents = [\"claude\"]\ntimeout = \"soon\"\n", "sections.all.chairs.x.timeout: \"soon\" is not a duration such as \"40m\""},
		{"[sections.All.chairs.x]\npart = \"gate\"\nagents = [\"claude\"]\n", "sections.All: want a lowercase name of letters, digits and dashes"},
		{"[sections.all.chairs.\"x.y\"]\npart = \"gate\"\nagents = [\"claude\"]\n", "sections.all.chairs.x.y: want a lowercase name of letters, digits and dashes"},
		{"gate_when = \"most\"\n", "gate_when: \"most\" is not any, all or always"},
		{"[sections.all]\n", "sections.all: want at least one chair"},
	} {
		home, wt := repoEnsemble(t, "bad", tc.body)
		_, _, err := Find("bad", home, wt, loop.BuiltInAgents())
		if err == nil || !strings.Contains(err.Error(), filepath.Join(wt, RepoDir, "bad.toml")) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.body, err, tc.want)
		}
	}
}

func TestTheScoreNeedsOneChairForEachPartAndAtLeastOneReviewer(t *testing.T) {
	home, wt := repoEnsemble(t, "thorough", thorough)
	members := bind(t, find(t, "thorough", home, wt), everyAgent)
	chairs, err := ChairsOf(members)
	if err != nil {
		t.Fatal(err)
	}
	if chairs.Planner.Tier != "large" || chairs.Implementer.Profile != "km-codex" || len(chairs.Reviewers) != 2 || chairs.Reviewers[1].Profile != "km-copilot" {
		t.Fatalf("chairs = %+v", chairs)
	}
	for _, tc := range []struct {
		drop, want string
	}{
		{"planner", "ensemble seats no planner"},
		{"security,style", "ensemble seats no reviewer"},
	} {
		var some []Member
		for _, m := range members {
			if !slices.Contains(strings.Split(tc.drop, ","), m.Name) {
				some = append(some, m)
			}
		}
		if _, err := ChairsOf(some); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("without %s: err = %v, want %q", tc.drop, err, tc.want)
		}
	}
	twoGates := append(slices.Clone(members), Member{Section: "extra", Name: "gate-2", Part: "gate"})
	if _, err := ChairsOf(twoGates); err == nil || !strings.Contains(err.Error(), "ensemble seats 2 gates (leads.gate, extra.gate-2); the score has one") {
		t.Errorf("two gates: err = %v", err)
	}
}

func TestChairsOfKeepsEveryLoopPartFromMembersOf(t *testing.T) {
	c := loop.Chair{Provider: "claude_code", Profile: "km-claude"}
	in := loop.Chairs{Planner: c, Implementer: c, Reviewers: []loop.Chair{c, c}, Gate: c, Classifier: c}
	out, err := ChairsOf(MembersOf(in))
	if err != nil || len(out.Reviewers) != 2 {
		t.Fatalf("chairs = %+v, err = %v", out, err)
	}
}

func TestARepositoryEnsembleMayNameAUserDefinedAgent(t *testing.T) {
	home, wt := repoEnsemble(t, "mine", "[sections.all.chairs.reviewer]\npart = 'reviewer'\nagents = ['mine@large']\n")
	agents, err := loop.BuiltInAgents().With(map[string]string{"mine": "other:custom"})
	if err != nil {
		t.Fatal(err)
	}
	spec, found, err := Find("mine", home, wt, agents)
	if err != nil || !found {
		t.Fatalf("ensemble: %v", err)
	}
	members, err := spec.Bind(agents, []string{"mine"})
	if err != nil || len(members) != 1 || members[0].Chair.Provider != "other" || members[0].Chair.Profile != "custom" || members[0].Chair.Tier != "large" {
		t.Fatalf("members = %v, %v", members, err)
	}
}

func TestARepositoryEnsembleNamingAnAgentTheUserHasNotDefinedIsRejected(t *testing.T) {
	home, wt := repoEnsemble(t, "mine", "[sections.all.chairs.reviewer]\npart = 'reviewer'\nagents = ['missing', 'claude']\n")
	_, _, err := Find("mine", home, wt, loop.BuiltInAgents())
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "claude, codex, copilot, agy") {
		t.Fatalf("error = %v", err)
	}
}
