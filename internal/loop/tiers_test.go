package loop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	claudeChair = Chair{Provider: "claude_code", Profile: "km-claude"}
	codexChair  = Chair{Provider: "codex", Profile: "km-codex"}
	agyChair    = Chair{Provider: "antigravity_cli", Profile: "km-agy"}
)

func defaultPolicy() *Policy {
	p := DefaultPolicy()
	return &p
}

func TestEachStepResolvesToItsTierModelAndEffortProfile(t *testing.T) {
	p := defaultPolicy()
	chair, a := p.Chair("implement", claudeChair, "", 0)
	if chair != (Chair{Provider: "claude_code", Profile: "km-claude-high", Model: "claude-sonnet-5-5"}) || a != (Assignment{Tier: "medium", Effort: "high", Source: SourceDefault}) {
		t.Fatalf("implement resolved to %+v %+v", chair, a)
	}
	chair, a = p.Chair("classify", codexChair, "large", 0)
	if chair.Model != "gpt-6-luna" || chair.Profile != "km-codex-medium" || a.Tier != "small" {
		t.Fatalf("classify is pinned to small, got %+v %+v", chair, a)
	}
	chair, a = p.Chair("gate", agyChair, "small", 0)
	if chair != (Chair{Provider: "antigravity_cli", Profile: "km-agy", Model: "gemini-3.8-flash-high"}) || a.Tier != "medium" || a.Effort != "" {
		t.Fatalf("gate never drops below medium and agy has no effort profile, got %+v %+v", chair, a)
	}
}

func TestExplicitModelsAndUntieredProvidersAreLeftAlone(t *testing.T) {
	p := defaultPolicy()
	explicit := Chair{Provider: "claude_code", Profile: "km-claude", Model: "claude-fable-5-1"}
	if chair, a := p.Chair("implement", explicit, "large", 3); chair != explicit || a.Source != SourceExplicit {
		t.Fatalf("explicit chair changed: %+v %+v", chair, a)
	}
	other := Chair{Provider: "kiro_cli", Profile: "dev"}
	if chair, a := p.Chair("implement", other, "large", 0); chair != other || a != (Assignment{}) {
		t.Fatalf("untiered chair changed: %+v %+v", chair, a)
	}
	var none *Policy
	if chair, _ := none.Chair("implement", claudeChair, "", 0); chair != claudeChair {
		t.Fatalf("nil policy changed the chair: %+v", chair)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".kapellmeister"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepoConfigOverridesTierFieldsStepBoundsAndSizing(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[tiers.claude_code]
large = { model = "claude-fable-5-1" }
small = { effort = "inherit" }

[steps.implement]
min = "medium"

[sizing]
mode = "on"
escalate_after = 0
`)
	settings, err := LoadSettings(t.TempDir(), dir)
	if err != nil {
		t.Fatal(err)
	}
	p := settings.Policy
	if got := p.Tiers["claude_code"]["large"]; got != (TierSpec{Model: "claude-fable-5-1", Effort: "xhigh"}) {
		t.Fatalf("large tier = %+v, want the model replaced and the default effort kept", got)
	}
	if chair, a := p.Chair("classify", claudeChair, "", 0); chair.Profile != "km-claude" || a.Effort != "" {
		t.Fatalf("inherit effort should keep the base profile, got %+v %+v", chair, a)
	}
	if _, a := p.Chair("implement", claudeChair, "small", 0); a.Tier != "medium" {
		t.Fatalf("implement min is medium, got %+v", a)
	}
	if p.Sizing.Mode != SizingOn || p.Sizing.Decider != DeciderPlanner || p.EscalateAfter() != 0 {
		t.Fatalf("sizing = %+v", p.Sizing)
	}
}

func TestPolicyRejectsSettingsCAOCannotApply(t *testing.T) {
	cases := map[string]string{
		"effort for copilot":    "[tiers.copilot_cli]\nlarge = { effort = \"high\" }\n",
		"unknown effort":        "[tiers.claude_code]\nlarge = { effort = \"ultra\" }\n",
		"inverted bounds":       "[steps.review]\nmin = \"large\"\n",
		"unknown step":          "[steps.deploy]\ntier = \"large\"\nmin = \"large\"\nmax = \"large\"\n",
		"unknown tier":          "[tiers.codex]\nhuge = { model = \"x\" }\n",
		"new provider no model": "[tiers.kiro_cli]\nsmall = { effort = \"low\" }\n",
		"bad mode":              "[sizing]\nmode = \"maybe\"\n",
		"command without one":   "[sizing]\ndecider = \"command\"\n",
		"display-name model":    "[tiers.antigravity_cli]\nlarge = { model = \"Gemini 4 Argon (High)\" }\n",
		"unknown key":           "[sizing]\nmodel = \"x\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, body)
			if _, err := LoadSettings(t.TempDir(), dir); err == nil {
				t.Fatalf("accepted:\n%s", body)
			}
		})
	}
}

func TestPlannerSizeReadsTheLastSizeLine(t *testing.T) {
	cases := map[string][2]string{
		"Size: large — touches the scheduler":       {"large", "touches the scheduler"},
		"**Size:** Medium - one package plus tests": {"medium", "one package plus tests"},
		"- size: small": {"small", ""},
		"Size: small — draft\n\nSize: large — on reflection": {"large", "on reflection"},
	}
	for plan, want := range cases {
		size, reason, err := PlannerSize("# Plan\nsteps\n" + plan + "\n")
		if err != nil || size != want[0] || reason != want[1] {
			t.Fatalf("%q: got %q %q %v, want %v", plan, size, reason, err, want)
		}
	}
	if _, _, err := PlannerSize("# Plan\nThe size of the change is large.\n"); err == nil {
		t.Fatal("prose without a Size line should not be read as a size")
	}
}

func tieredEngine(t *testing.T, mode string) (*Engine, *fakeStepper, *bytes.Buffer) {
	eng, fake, records := newEngine(t, passing)
	p := defaultPolicy()
	p.Sizing.Mode = mode
	eng.Config.Policy = p
	eng.Config.Chairs = Chairs{Planner: claudeChair, Implementer: codexChair, Reviewers: []Chair{claudeChair}, Gate: claudeChair, Classifier: claudeChair}
	return eng, fake, records
}

func planSays(fake *fakeStepper, line string) {
	fake.actions["plan"] = func(s Step) { fake.write(s.ReplyPath, "# Plan\n"+line+"\n") }
}

func stepNamed(t *testing.T, fake *fakeStepper, name string, round int) Step {
	t.Helper()
	for _, s := range fake.steps {
		if s.Name == name && s.Round == round {
			return s
		}
	}
	t.Fatalf("no %s[%d] step in %v", name, round, fake.names())
	return Step{}
}

func recordsOf(t *testing.T, buf *bytes.Buffer, step string) []Record {
	t.Helper()
	var out []Record
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Step == step {
			out = append(out, r)
		}
	}
	return out
}

func TestShadowSizingRecordsThePlannersSizeWithoutApplyingIt(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingShadow)
	planSays(fake, "Size: large — rewrites the locking")
	run(t, eng)
	if !strings.Contains(fake.steps[0].Prompt, "Size: small|medium|large") {
		t.Fatalf("planner was not asked for a size:\n%s", fake.steps[0].Prompt)
	}
	if impl := stepNamed(t, fake, "implement", 1); impl.Chair.Model != "gpt-6.1-sol" || impl.Assignment.Source != SourceDefault {
		t.Fatalf("shadow mode changed the implementer: %+v %+v", impl.Chair, impl.Assignment)
	}
	sized := recordsOf(t, records, "size")
	if len(sized) != 1 || sized[0].Decision == nil || *sized[0].Decision != (SizeDecision{Decider: DeciderPlanner, Mode: SizingShadow, Size: "large", Reason: "rewrites the locking"}) {
		t.Fatalf("size record = %+v", sized)
	}
	impl := recordsOf(t, records, "implement")[0]
	if impl.Tier != "medium" || impl.Effort != "high" || impl.TierSource != SourceDefault || impl.Chair.Profile != "km-codex-high" {
		t.Fatalf("implement record = %+v", impl)
	}
}

func TestOnSizingAppliesTheSizeWithinEachStepsBounds(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOn)
	planSays(fake, "Size: small — fixes a typo")
	run(t, eng)
	if s := stepNamed(t, fake, "plan", 0); s.Chair.Model != "claude-sonnet-5-5" {
		t.Fatalf("the plan runs before sizing, at its default tier: %+v", s.Chair)
	}
	if s := stepNamed(t, fake, "implement", 1); s.Chair.Model != "gpt-6-luna" || s.Assignment.Source != SourceSized {
		t.Fatalf("implement = %+v %+v", s.Chair, s.Assignment)
	}
	if s := stepNamed(t, fake, "review", 1); s.Chair.Model != "claude-haiku-5-5" {
		t.Fatalf("review = %+v", s.Chair)
	}
	if s := stepNamed(t, fake, "gate", 1); s.Chair.Model != "claude-sonnet-5-5" || s.Assignment.Tier != "medium" {
		t.Fatalf("gate min is medium, got %+v %+v", s.Chair, s.Assignment)
	}
	if d := recordsOf(t, records, "size")[0].Decision; !d.Applied {
		t.Fatalf("size decision not applied: %+v", d)
	}
}

func TestMissingSizeFallsBackToEachStepsDefault(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOn)
	planSays(fake, "No size here.")
	res := run(t, eng)
	if res.Outcome != Approved {
		t.Fatalf("got %+v", res)
	}
	if s := stepNamed(t, fake, "implement", 1); s.Assignment != (Assignment{Tier: "medium", Effort: "high", Source: SourceDefault}) {
		t.Fatalf("implement = %+v", s.Assignment)
	}
	if d := recordsOf(t, records, "size")[0].Decision; d.Applied || d.Error == "" {
		t.Fatalf("size decision = %+v", d)
	}
}

func TestSizingOffNeitherAsksNorRecords(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOff)
	run(t, eng)
	if strings.Contains(fake.steps[0].Prompt, "Size:") {
		t.Fatalf("planner was asked for a size with sizing off")
	}
	if len(recordsOf(t, records, "size")) != 0 {
		t.Fatal("sizing off wrote a size record")
	}
}

func TestImplementerMovesUpATierInAFreshTerminalAfterRepeatedRejections(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOff)
	fake.verdicts["review"] = []string{"CHANGES", "CHANGES"}
	res := run(t, eng)
	if res.Outcome != Approved || res.Rounds != 3 {
		t.Fatalf("got %+v", res)
	}
	first, second, third := stepNamed(t, fake, "implement", 1), stepNamed(t, fake, "implement", 2), stepNamed(t, fake, "implement", 3)
	if second.ReuseTerminal == "" || second.Chair != first.Chair {
		t.Fatalf("round 2 should reuse the medium implementer: %+v", second)
	}
	if third.ReuseTerminal != "" || third.Chair.Model != "gpt-6-astra" || third.Assignment.Source != SourceEscalated {
		t.Fatalf("round 3 should start a large implementer: %+v", third)
	}
	if !strings.Contains(third.Prompt, "still uncommitted in this worktree") {
		t.Fatalf("a fresh implementer must be told the earlier attempt is in the worktree:\n%s", third.Prompt)
	}
	released := strings.Join(fake.released, " ")
	if !strings.Contains(released, second.ReuseTerminal) {
		t.Fatalf("the medium implementer %s was not released: %v", second.ReuseTerminal, fake.released)
	}
	esc := recordsOf(t, records, "escalate")
	if len(esc) != 1 || esc[0].Round != 2 || esc[0].Tier != "large" || !strings.Contains(esc[0].Detail, "medium → large") {
		t.Fatalf("escalate records = %+v", esc)
	}
}

func TestEscalationStopsAtTheStepsMax(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOff)
	eng.Config.Policy.Steps["implement"] = StepPolicy{Tier: "large", Min: "small", Max: "large"}
	fake.verdicts["review"] = []string{"CHANGES", "CHANGES"}
	run(t, eng)
	if third := stepNamed(t, fake, "implement", 3); third.ReuseTerminal == "" {
		t.Fatalf("an implementer already at max should keep its terminal: %+v", third)
	}
	if len(recordsOf(t, records, "escalate")) != 0 {
		t.Fatal("recorded an escalation that changed nothing")
	}
}

func TestCommandDeciderReceivesTaskAndPlanAndItsAnswerIsApplied(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOn)
	stdin := filepath.Join(t.TempDir(), "stdin.json")
	eng.Config.Policy.Sizing.Decider = DeciderCommand
	eng.Config.Policy.Sizing.Command = `cat > ` + stdin + ` && echo '{"size":"Large","reason":"security review"}'`
	planSays(fake, "touch auth")
	run(t, eng)
	if strings.Contains(fake.steps[0].Prompt, "Size:") {
		t.Fatal("the planner should not be asked for a size when a command decides")
	}
	var req SizeRequest
	data, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &req); err != nil || req.Task != "do the thing" || !strings.Contains(req.Plan, "touch auth") || len(req.Tiers) != 3 {
		t.Fatalf("decider got %s (%v)", data, err)
	}
	if s := stepNamed(t, fake, "implement", 1); s.Chair.Model != "gpt-6-astra" {
		t.Fatalf("implement = %+v", s.Chair)
	}
	if d := recordsOf(t, records, "size")[0].Decision; d.Size != "large" || d.Reason != "security review" || d.Decider != DeciderCommand {
		t.Fatalf("decision = %+v", d)
	}
}

func TestCommandDeciderFailuresFallBack(t *testing.T) {
	for name, command := range map[string]string{
		"exit status":  "echo nope >&2; exit 3",
		"not json":     "echo large",
		"unknown size": `echo '{"size":"huge"}'`,
	} {
		t.Run(name, func(t *testing.T) {
			eng, fake, records := tieredEngine(t, SizingOn)
			eng.Config.Policy.Sizing.Decider = DeciderCommand
			eng.Config.Policy.Sizing.Command = command
			run(t, eng)
			if s := stepNamed(t, fake, "implement", 1); s.Assignment.Source != SourceDefault {
				t.Fatalf("implement = %+v", s.Assignment)
			}
			if d := recordsOf(t, records, "size")[0].Decision; d.Error == "" || d.Applied {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
}

func TestEffortVariantsCoverEveryTierAStepCanReach(t *testing.T) {
	p := defaultPolicy()
	got := p.EffortVariants(map[string][]Chair{
		"implement": {claudeChair, agyChair, {Provider: "codex", Profile: "km-codex", Model: "pinned"}},
		"classify":  {codexChair},
	})
	var names []string
	for _, v := range got {
		names = append(names, v.Name())
	}
	if strings.Join(names, " ") != "km-codex-medium km-claude-medium km-claude-high km-claude-xhigh" {
		t.Fatalf("variants = %v", names)
	}
}

func TestAnEffortProfileIsGeneratedFromAnyBaseProfile(t *testing.T) {
	src, err := EffortProfileSource("codex", "km-codex", "xhigh", Permissions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: km-codex-xhigh\n", "provider: codex\n", "codexConfig:\n  model_reasoning_effort: xhigh\n", chairInstructions} {
		if !strings.Contains(src, want) {
			t.Fatalf("profile lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "\nmodel:") {
		t.Fatalf("a generated profile must not pin a model, or agy-style providers ignore the step's model:\n%s", src)
	}
	src, err = EffortProfileSource("claude_code", "my-reviewer", "high", Permissions{PermissionMode: "default", AllowedTools: []string{"Read"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: my-reviewer-high\n", "provider: claude_code\n", "permissionMode: default\n", `allowedTools: ["Read"]`, "claudeConfig:\n  effort: high\n"} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing %q: %s", want, src)
		}
	}
	if strings.Contains(src, "\nmodel:") {
		t.Fatalf("pinned model: %s", src)
	}
	if _, err := EffortProfileSource("copilot_cli", "mine", "high", Permissions{}); err == nil {
		t.Fatal("unsupported effort accepted")
	}
}

func TestAChairSpecCanPinATier(t *testing.T) {
	for _, tier := range TierNames {
		spec := "codex:km-codex@" + tier
		chair, err := ParseChair(spec)
		if err != nil || chair != (Chair{Provider: "codex", Profile: "km-codex", Tier: tier}) {
			t.Fatalf("%s: %+v, %v", spec, chair, err)
		}
	}
}

func TestAnExplicitModelCannotAlsoPinATier(t *testing.T) {
	for _, spec := range []string{"codex:km-codex:gpt-6-astra@large", "codex:km-codex:vendor/model:version@small"} {
		if _, err := ParseChair(spec); err == nil || !strings.Contains(err.Error(), "explicit model") || !strings.Contains(err.Error(), spec) {
			t.Fatalf("%s: %v", spec, err)
		}
	}
}

func TestAnUnknownPinnedTierNamesTheValidTiers(t *testing.T) {
	for _, spec := range []string{"codex:km-codex@huge", "codex:km-codex@", "codex:km-codex@Large"} {
		if _, err := ParseChair(spec); err == nil || !strings.Contains(err.Error(), "small, medium, large") || !strings.Contains(err.Error(), spec) {
			t.Fatalf("%s: %v", spec, err)
		}
	}
}

func TestAPinnedTierReplacesTheStepDefaultAndSizingDoesNotMoveIt(t *testing.T) {
	p := defaultPolicy()
	pinned := claudeChair
	pinned.Tier = "large"
	for _, want := range []string{"", "small", "medium", "large"} {
		chair, a := p.Chair("implement", pinned, want, 0)
		if chair != (Chair{Provider: "claude_code", Profile: "km-claude-xhigh", Model: "claude-opus-5-5"}) || a != (Assignment{Tier: "large", Effort: "xhigh", Source: "pinned"}) {
			t.Fatalf("size %q: %+v %+v", want, chair, a)
		}
	}
	eng, fake, _ := tieredEngine(t, SizingOn)
	eng.Config.Chairs.Reviewers = []Chair{pinned}
	planSays(fake, "Size: small — one fix")
	run(t, eng)
	if s := stepNamed(t, fake, "review", 1); s.Chair.Model != "claude-opus-5-5" || s.Assignment.Source != "pinned" {
		t.Fatalf("review = %+v", s)
	}
	if s := stepNamed(t, fake, "implement", 1); s.Chair.Model != "gpt-6-luna" || s.Assignment.Source != SourceSized {
		t.Fatalf("implement = %+v", s)
	}
}

func TestAPinnedTierIsClampedToTheStepsBounds(t *testing.T) {
	repo := t.TempDir()
	writeConfig(t, repo, "[steps.implement]\nmin = \"medium\"\n")
	s, err := LoadSettings(t.TempDir(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ step, pin, want string }{
		{"gate", "small", "medium"},
		{"classify", "large", "small"},
		{"implement", "small", "medium"},
	} {
		pinned := claudeChair
		pinned.Tier = tc.pin
		chair, a := s.Policy.Chair(tc.step, pinned, "", 0)
		if a.Tier != tc.want || a.Source != "pinned" || chair.Tier != "" {
			t.Fatalf("%s pinned %s: %+v %+v", tc.step, tc.pin, chair, a)
		}
	}
}

func TestAPinnedChairIsRecordedWithTierSourcePinned(t *testing.T) {
	eng, _, records := tieredEngine(t, SizingOff)
	eng.Config.Chairs.Gate.Tier = "large"
	var progress bytes.Buffer
	eng.Progress = &progress
	run(t, eng)
	got := recordsOf(t, records, "gate")
	if len(got) != 1 || got[0].TierSource != "pinned" || got[0].Tier != "large" || got[0].Chair.Tier != "" {
		t.Fatalf("gate records = %+v", got)
	}
	if !strings.Contains(progress.String(), "(large, pinned)") {
		t.Fatalf("progress lacks pinned source:\n%s", progress.String())
	}
}

func TestAPinnedImplementerEscalatesWithinItsBounds(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOff)
	eng.Config.Chairs.Implementer.Tier = "small"
	eng.Config.Policy.Steps["implement"] = StepPolicy{Tier: "medium", Min: "small", Max: "medium"}
	eng.Config.MaxRounds = 5
	fake.verdicts["review"] = []string{"CHANGES", "CHANGES", "CHANGES", "CHANGES"}
	run(t, eng)
	first, second, third := stepNamed(t, fake, "implement", 1), stepNamed(t, fake, "implement", 2), stepNamed(t, fake, "implement", 3)
	if first.Chair.Model != "gpt-6-luna" || first.Assignment.Source != "pinned" || second.Chair != first.Chair || second.ReuseTerminal == "" {
		t.Fatalf("first two rounds = %+v %+v", first, second)
	}
	if third.ReuseTerminal != "" || third.Chair.Model != "gpt-6.1-sol" || third.Assignment.Source != SourceEscalated {
		t.Fatalf("third round = %+v", third)
	}
	if !strings.Contains(strings.Join(fake.released, " "), second.ReuseTerminal) {
		t.Fatalf("old terminal not released: %v", fake.released)
	}
	if fifth := stepNamed(t, fake, "implement", 5); fifth.Chair != third.Chair || fifth.ReuseTerminal == "" {
		t.Fatalf("escalation exceeded the maximum: %+v", fifth)
	}
	esc := recordsOf(t, records, "escalate")
	if len(esc) != 1 || esc[0].Round != 2 || esc[0].Tier != "medium" || !strings.Contains(esc[0].Detail, "small → medium") {
		t.Fatalf("escalate records = %+v", esc)
	}
}

func TestAnEnsembleWithoutEscalationKeepsThePinnedImplementerOnItsTier(t *testing.T) {
	home := writeUserConfig(t, "[ensembles.steady]\nimplementer = \"codex:km-codex@small\"\nescalate = false\n")
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.WithEnsemble("steady")
	if err != nil {
		t.Fatal(err)
	}
	eng, fake, records := tieredEngine(t, SizingOff)
	eng.Config.Policy = &s.Policy
	eng.Config.Chairs.Implementer, err = ParseChair(s.Chairs["implementer"])
	if err != nil {
		t.Fatal(err)
	}
	fake.verdicts["review"] = []string{"CHANGES", "CHANGES"}
	run(t, eng)
	for round := 1; round <= 3; round++ {
		s := stepNamed(t, fake, "implement", round)
		if s.Chair.Model != "gpt-6-luna" || s.Assignment.Source != "pinned" || (round > 1 && s.ReuseTerminal == "") {
			t.Fatalf("implement[%d] = %+v", round, s)
		}
	}
	if got := recordsOf(t, records, "escalate"); len(got) != 0 {
		t.Fatalf("escalation disabled, got %+v", got)
	}
}
