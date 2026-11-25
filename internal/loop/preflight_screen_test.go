package loop

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode"
	"unicode/utf8"
)

const copilotPane = " ❯ This is a readiness check. Write the single word ready to .kapellmeister/runs/preflight/2-copilot_cli.txt and do nothing else. Then reply with: ready     15:35\n ✗ \x1b[31mYour organization or enterprise has exceeded its Copilot budget. Contact your admin to resume usage.\x07 (Request ID: F399)\n%s\n /tmp/wt-km [detached] Session: 0 AIC used\n────────────────\n❯\n────────────────\n ← open sidebar · Autopilot · Allow All · / commands · tab next tab km-copilot"

func TestABlockedPreflightWithNoKnownPromptKeepsTheEndOfThePaneCleanedAndBounded(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		host.actions = map[string]func(Step){"preflight": func(Step) {}}
		host.screen = func(context.Context, string) (string, string, error) {
			return "", strings.Replace(copilotPane, "%s", strings.Repeat("é", 400), 1), nil
		}
		results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "copilot_cli"}}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r := results[0]
		if r.OK || r.Blocker != "" || !strings.Contains(r.Screen, "Your organization or enterprise has exceeded its Copilot budget") {
			t.Fatalf("result: %+v", r)
		}
		lines := strings.Split(r.Screen, "\n")
		if len(lines) > 6 {
			t.Fatalf("too many lines: %q", r.Screen)
		}
		for _, line := range lines {
			if !utf8.ValidString(line) || utf8.RuneCountInString(line) > 160 || strings.Trim(line, "─❯ ") == "" {
				t.Errorf("invalid line: %q", line)
			}
			for _, c := range line {
				if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
					t.Errorf("control character in %q", line)
				}
			}
		}
	})
}

func TestThePaneQuoteTakesTheLastSixWordedLinesOfTheLastTwelveNonBlankOnes(t *testing.T) {
	for _, tc := range []struct{ screen, want string }{
		{"old1\nold2\na\nb\n\nc\n─\nd\ne\nf\ng\nh\ni\nj\nk\n❯", "f\ng\nh\ni\nj\nk"},
		{"old\nword\n─\n─\n─\n─\n─\n─\n─\n─\n─\n─\n❯", "word"},
		{"─\n❯\n \n──", ""},
	} {
		if got := screenTail(tc.screen); got != tc.want {
			t.Errorf("screenTail(%q) = %q, want %q", tc.screen, got, tc.want)
		}
	}
}

func TestABlockedPreflightWithARecognisedPromptQuotesNoPane(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		host.actions = map[string]func(Step){"preflight": func(Step) {}}
		host.screen = func(context.Context, string) (string, string, error) {
			return "", "startup\nDo you trust the files in this folder?\n  1. Yes\n", nil
		}
		results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if r := results[0]; r.OK || r.Blocker == "" || r.Screen != "" {
			t.Fatalf("result: %+v", r)
		}
	})
}
