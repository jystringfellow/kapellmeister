package ensemble

import (
	"errors"
	"fmt"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"strings"
	"testing"
)

func TestAMessageChairSkipsOtherProviders(t *testing.T) {
	spec, err := Check("solo", "solo.toml", `[sections.all.chairs.reviewer]
part="reviewer"
agents=["codex", "claude"]
reply="message"`, loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	members, err := spec.Bind(loop.BuiltInAgents(), everyAgent)
	if err != nil {
		t.Fatal(err)
	}
	if members[0].Chair.Provider != "claude_code" || members[0].Chair.Reply != "message" {
		t.Fatalf("chair %+v", members[0].Chair)
	}
}
func TestAnInvalidReplyValueNamesItsFileAndChair(t *testing.T) {
	_, err := Check("solo", "solo.toml", `[sections.all.chairs.reviewer]
part="reviewer"
agents=["claude"]
reply="other"`, loop.BuiltInAgents())
	if err == nil || !strings.Contains(err.Error(), "solo.toml") || !strings.Contains(err.Error(), "all.reviewer@solo") {
		t.Fatalf("error %v", err)
	}
}
func TestAMessageChairCannotPlayAWorktreeChangingProduceStep(t *testing.T) {
	spec, err := Check("solo", "solo.toml", strings.Replace(strings.ReplaceAll(thorough, `agents = ["codex"]`, `agents = ["claude"]`), "part = \"implementer\"", "part = \"implementer\"\nreply = \"message\"", 1), loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"Fits":  func() error { return spec.Fits(loop.DefaultScore()) },
		"Ready": func() error { return spec.Ready(loop.DefaultScore(), loop.BuiltInAgents(), everyAgent) },
	} {
		t.Run(name, func(t *testing.T) {
			err := check()
			if err == nil || !strings.Contains(err.Error(), "solo.toml") || !strings.Contains(err.Error(), "leads.implementer@solo") {
				t.Fatalf("error %v", err)
			}
		})
	}
	members, err := spec.Bind(loop.BuiltInAgents(), everyAgent)
	if err != nil {
		t.Fatal(err)
	}
	for i := range members {
		members[i].Chair.Address = members[i].Address(spec.Name)
	}
	_, err = ChairsFor(loop.DefaultScore(), members)
	if err == nil || !strings.Contains(err.Error(), "leads.implementer@solo") || !strings.Contains(err.Error(), "worktree-changing") {
		t.Fatalf("error %v", err)
	}
}

func TestReplyMessageAndFileAreAcceptedInRepositoryAndUserEnsembles(t *testing.T) {
	for _, portable := range []bool{false, true} {
		for _, reply := range []string{"file", "message"} {
			t.Run(fmt.Sprintf("%v-%s", portable, reply), func(t *testing.T) {
				body := fmt.Sprintf("[sections.all.chairs.reviewer]\npart=\"reviewer\"\nagents=[\"claude\"]\nreply=%q", reply)
				var spec Spec
				var err error
				if portable {
					spec, err = CheckPortable("solo", "solo.toml", body, loop.BuiltInAgents())
				} else {
					spec, err = Check("solo", "solo.toml", body, loop.BuiltInAgents())
				}
				if err != nil {
					t.Fatal(err)
				}
				m, err := spec.Bind(loop.BuiltInAgents(), everyAgent)
				if err != nil {
					t.Fatal(err)
				}
				want := reply
				if want == "file" {
					want = ""
				}
				if m[0].Chair.Reply != want {
					t.Fatalf("reply %q", m[0].Chair.Reply)
				}
			})
		}
	}
}
func TestMessageChairsRejectASeatOnAnUnprovenProvider(t *testing.T) {
	spec, err := Check("solo", "solo.toml", `[sections.all.chairs.reviewer]
part="reviewer"
agents=["codex", "claude"]
reply="message"`, loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	_, err = spec.Seat(loop.BuiltInAgents(), "all", "reviewer", "codex")
	if err == nil || !strings.Contains(err.Error(), "proven live") {
		t.Fatalf("error %v", err)
	}
	chosen, _ := spec.Listed("all", "reviewer", everyAgent, loop.BuiltInAgents())
	if chosen != "claude" {
		t.Fatalf("chosen %q", chosen)
	}
}

func TestMessageChairsCanPlayReadOnlyAndReplyOnlySteps(t *testing.T) {
	for _, tc := range []struct {
		kind   loop.StepKind
		writes string
	}{{loop.KindReview, ""}, {loop.KindDecide, ""}, {loop.KindTest, ""}, {loop.KindProduce, "reply"}} {
		t.Run(string(tc.kind)+tc.writes, func(t *testing.T) {
			score := loop.Score{Name: "default", Parts: []loop.Part{{Name: "reader"}}, Steps: []loop.ScoreStep{{Name: "read", Part: "reader", Kind: tc.kind, Writes: tc.writes}}}
			_, err := ChairsFor(score, []Member{{Section: "all", Name: "one", Part: "reader", Chair: loop.Chair{Reply: loop.ReplyMessage}}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBindingAMessageChairWithoutAClaudeAgentExplainsTheUnprovenProviders(t *testing.T) {
	spec, err := Check("solo", "solo.toml", `[sections.all.chairs.reviewer]
part="reviewer"
agents=["codex"]
reply="message"`, loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	_, err = spec.Bind(loop.BuiltInAgents(), everyAgent)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error %v", err)
	}
	for _, want := range []string{"solo.toml", "all.reviewer@solo", "only on claude_code", "other providers are refused until proven live"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
}

func TestAMessageChairWithAMissingClaudeAgentGivesTheUnavailableMessage(t *testing.T) {
	for _, list := range []string{`"claude"`, `"codex", "claude"`} {
		spec, err := Check("solo", "solo.toml", `[sections.all.chairs.reviewer]
part="reviewer"
agents=[`+list+`]
reply="message"`, loop.BuiltInAgents())
		if err != nil {
			t.Fatal(err)
		}
		_, err = spec.Bind(loop.BuiltInAgents(), []string{"codex"})
		if err == nil || !strings.Contains(err.Error(), "none of claude is in [agents] available (codex)") || strings.Contains(err.Error(), "proven live") {
			t.Errorf("%s: %v", list, err)
		}
	}
}
