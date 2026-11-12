package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestWithoutAVerifyCommandNoPromptClaimsAVerificationResultOrThatKmWillVerify(t *testing.T) {
	e, f := testerEngine(t)
	e.Config.VerifyCommand = ""
	e.Verify = func(_ context.Context, _ string) (VerifyResult, error) {
		t.Fatal("verification ran without a command")
		return VerifyResult{}, nil
	}
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	for _, name := range []string{"implement", "review", "gate", "test"} {
		steps := testerSteps(f, name)
		if len(steps) != 1 {
			t.Fatalf("%s steps = %d", name, len(steps))
		}
		p := steps[0].Prompt
		for _, forbidden := range []string{"authoritative", "Do not re-run the test suite", "exited", "verification passed", "runs the full verification"} {
			if strings.Contains(p, forbidden) {
				t.Errorf("%s claims %q", name, forbidden)
			}
		}
		if name != "implement" && !strings.Contains(p, "kapellmeister did not verify this change: no verify command is configured.") {
			t.Errorf("%s does not explain that km verified nothing", name)
		}
	}
}

func TestWithAVerifyCommandReviewersGateAndTesterAreToldNotToRerunTheSuite(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	for _, name := range []string{"review", "gate", "test"} {
		p := testerSteps(f, name)[0].Prompt
		for _, want := range []string{"kapellmeister ran `check`; it exited 0.", "Do not re-run the test suite; kapellmeister's verification result above is authoritative."} {
			if !strings.Contains(p, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
	if !strings.Contains(testerSteps(f, "gate")[0].Prompt, "the verification passed") {
		t.Error("gate does not require successful configured verification")
	}
}

func TestTheImplementerIsToldKmRunsTheFullVerificationOnlyWhenAVerifyCommandIsConfigured(t *testing.T) {
	for _, check := range []string{"", "scripts/check {args}"} {
		for _, verify := range []string{"", "scripts/verify"} {
			t.Run(fmt.Sprintf("check=%q/verify=%q", check, verify), func(t *testing.T) {
				e, f, _ := newEngine(t, passing)
				e.Config.CheckCommand, e.Config.VerifyCommand = check, verify
				run(t, e)
				p := testerSteps(f, "implement")[0].Prompt
				for _, phrase := range []string{"Do not run the full suite", "kapellmeister runs the full verification after you finish"} {
					if strings.Contains(p, phrase) != (verify != "") {
						t.Errorf("%q presence does not match verify configuration: %s", phrase, p)
					}
				}
				if !strings.Contains(p, "foreground") || !strings.Contains(p, "wait") {
					t.Error("tests must finish in the foreground")
				}
				if check != "" && !strings.Contains(p, "scripts/check <tests>") {
					t.Error("check usage missing")
				}
				if check == "" && !strings.Contains(p, "Do not start background tasks or timers") {
					t.Error("background task protection missing")
				}
			})
		}
	}
}

func TestTheImplementerIsNotToldTheCheckCommandPrintsOnlyFailures(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	e.Config.CheckCommand = "go test {args}"
	run(t, e)
	if strings.Contains(testerSteps(f, "implement")[0].Prompt, "prints only") {
		t.Error("km cannot know what the check command prints")
	}
}

func TestThePlanPromptAsksForTestsWithoutOrderingThem(t *testing.T) {
	p := planPrompt("task", "plan.md", false, "")
	if !strings.Contains(p, "the tests or facts to add or change, following the repository's development rules") {
		t.Error("test guidance must defer to repository rules without prescribing ordering")
	}
	for _, forbidden := range []string{"specs", "change first"} {
		if strings.Contains(p, forbidden) {
			t.Errorf("plan contains %q", forbidden)
		}
	}
}

func TestTheTesterPlanPromptNamesNoKmSpecificFailureModes(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	p := testerSteps(f, "test-plan")[0].Prompt
	if !strings.Contains(p, "including the failure paths this change could plausibly hit") {
		t.Error("tester needs change-specific failure paths")
	}
	for _, forbidden := range []string{"hostile", "missing, stale", "concurrent state", "external services misbehaving"} {
		if strings.Contains(p, forbidden) {
			t.Errorf("tester plan contains %q", forbidden)
		}
	}
}

func TestThePreambleSaysOnceThatEveryInstructionFileIsReadIncludingThoseTheToolDoesNotLoad(t *testing.T) {
	p := preamble("planner")
	if !strings.Contains(p, "every agent instruction file at the repository root") || !strings.Contains(p, "including those your tool does not load by itself") || strings.Contains(p, "whichever") {
		t.Error(p)
	}
}

func TestTheLessonsPromptOffersTesterOnlyWhenATesterIsSeated(t *testing.T) {
	for _, tester := range []bool{false, true} {
		t.Run(fmt.Sprint(tester), func(t *testing.T) {
			e, f, _ := lessonsEngine(t, "[]")
			want := "<planner|implementer|reviewer|gate|classifier>"
			if tester {
				e.Config.Chairs.Tester = &Chair{Provider: "p", Profile: "tester"}
				want = "<planner|implementer|reviewer|gate|classifier|tester>"
			}
			run(t, e)
			p := testerSteps(f, "lessons")[0].Prompt
			if !strings.Contains(p, `"part":"`+want+`"`) || (!tester && strings.Contains(p, "tester")) {
				t.Errorf("lessons part list: %s", p)
			}
		})
	}
}
