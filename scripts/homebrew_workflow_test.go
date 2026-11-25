package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func releaseWorkflowJob(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?ms)^  ` + name + `:\n(.*?)(?:^  [a-zA-Z][a-zA-Z0-9_-]*:\n|\z)`)
	match := pattern.FindStringSubmatch(string(data))
	if len(match) != 2 {
		t.Fatalf("%s job missing", name)
	}
	return match[1]
}

func homebrewWorkflowSteps(t *testing.T) []string {
	t.Helper()
	job := releaseWorkflowJob(t, "homebrew")
	parts := strings.SplitN(job, "    steps:\n", 2)
	if len(parts) != 2 {
		t.Fatal("steps missing")
	}
	steps := regexp.MustCompile(`(?m)^      - `).Split(parts[1], -1)
	return steps[1:]
}

func homebrewWorkflowStep(t *testing.T, marker string) string {
	t.Helper()
	for _, step := range homebrewWorkflowSteps(t) {
		if strings.Contains(step, marker) {
			return step
		}
	}
	t.Fatalf("step %q missing", marker)
	return ""
}

func workflowHasLine(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

func TestTheReleaseWorkflowsHomebrewJobUsesNeitherTheTapSecretNorTheActionsBot(t *testing.T) {
	job := releaseWorkflowJob(t, "homebrew")
	data, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "HOMEBREW_TAP_TOKEN") {
		t.Error("workflow still uses tap secret")
	}
	if strings.Contains(job, "github-actions[bot]") {
		t.Error("homebrew job still uses actions bot")
	}
}

func TestTheReleaseWorkflowScopesTheTapTokenToTheTapWithContentsWriteOnly(t *testing.T) {
	job := releaseWorkflowJob(t, "homebrew")
	step := homebrewWorkflowStep(t, "id: app-token")
	for _, line := range []string{"if: steps.credentials.outputs.enabled == 'true'", "client-id: ${{ vars.PUBLISHER_APP_CLIENT_ID }}", "private-key: ${{ secrets.PUBLISHER_APP_PRIVATE_KEY }}", "owner: ${{ github.repository_owner }}", "repositories: homebrew-tap"} {
		if !workflowHasLine(step, line) {
			t.Errorf("missing line %s", line)
		}
	}
	lines := regexp.MustCompile(`(?m)^\s*permission-.*$`).FindAllString(job, -1)
	if len(lines) != 1 || strings.TrimSpace(lines[0]) != "permission-contents: write" {
		t.Errorf("unexpected permissions: %q", lines)
	}
}

func TestTheReleaseWorkflowPinsTheSameAppTokenActionForTheTapAndTheBook(t *testing.T) {
	pattern := regexp.MustCompile(`(?m)^.*actions/create-github-app-token@.*$`)
	tap := pattern.FindAllString(releaseWorkflowJob(t, "homebrew"), -1)
	book := pattern.FindAllString(releaseWorkflowJob(t, "book"), -1)
	if len(tap) != 1 || len(book) != 1 || tap[0] != book[0] {
		t.Errorf("action lines differ: tap %q, book %q", tap, book)
	}
}

func TestTheReleaseWorkflowSkipsEveryTapStepWithoutFailingWhenACredentialIsEmpty(t *testing.T) {
	steps := homebrewWorkflowSteps(t)
	if len(steps) < 4 {
		t.Fatalf("expected at least four steps, got %d", len(steps))
	}
	credentials := steps[0]
	for _, line := range []string{"name: Check credentials", "id: credentials", "PUBLISHER_APP_CLIENT_ID: ${{ vars.PUBLISHER_APP_CLIENT_ID }}", "PUBLISHER_APP_PRIVATE_KEY: ${{ secrets.PUBLISHER_APP_PRIVATE_KEY }}"} {
		if !workflowHasLine(credentials, line) {
			t.Errorf("credentials step missing %s", line)
		}
	}
	for _, step := range steps[1:] {
		if !workflowHasLine(step, "if: steps.credentials.outputs.enabled == 'true'") {
			t.Errorf("step is not gated: %s", step)
		}
	}
	parts := strings.SplitN(credentials, "        run: |\n", 2)
	if len(parts) != 2 {
		t.Fatal("credentials script missing")
	}
	var script strings.Builder
	for _, line := range strings.Split(parts[1], "\n") {
		script.WriteString(strings.TrimPrefix(line, "          ") + "\n")
	}
	for _, tc := range []struct{ name, id, key, enabled string }{{"both empty", "", "", "false"}, {"id empty", "", "key", "false"}, {"key empty", "id", "", "false"}, {"both set", "id", "key", "true"}} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			cmd := exec.Command("bash", "-c", script.String())
			cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output, "PUBLISHER_APP_CLIENT_ID="+tc.id, "PUBLISHER_APP_PRIVATE_KEY="+tc.key)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v: %s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "enabled="+tc.enabled+"\n" {
				t.Errorf("output = %q", data)
			}
			if tc.enabled == "false" && !strings.Contains(string(out), "PUBLISHER_APP_CLIENT_ID or PUBLISHER_APP_PRIVATE_KEY is empty; skipping publishing to the tap") {
				t.Errorf("missing skip message: %s", out)
			}
		})
	}
}

func TestTheReleaseWorkflowCommitsTheFormulaAsTheAppBotAndPushesWithTheHeader(t *testing.T) {
	step := homebrewWorkflowStep(t, "name: Publish formula to the tap")
	for _, line := range []string{"TOKEN: ${{ steps.app-token.outputs.token }}", "GH_TOKEN: ${{ steps.app-token.outputs.token }}", "APP_SLUG: ${{ steps.app-token.outputs.app-slug }}"} {
		if !workflowHasLine(step, line) {
			t.Errorf("missing %s", line)
		}
	}
	for _, text := range []string{`bot="${APP_SLUG}[bot]"`, `user.name="$bot"`, `user.email="${id}+${bot}@users.noreply.github.com"`, `commit --quiet -m "kapellmeister $TAG"`, `git -c "$header" push`} {
		if !strings.Contains(step, text) {
			t.Errorf("missing %s", text)
		}
	}
	unchanged, lookup := strings.Index(step, `echo "formula unchanged"`), strings.Index(step, `gh api "/users/${bot}"`)
	if unchanged < 0 || lookup < 0 || unchanged >= lookup {
		t.Error("bot lookup must follow unchanged exit")
	}
}
