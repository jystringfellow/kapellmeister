package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTheBookJobSkipsThePullRequestOnUnchangedAndOlderAndBothBookWorkflowsSetATimeout(t *testing.T) {
	release, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	tagWorkflow, err := os.ReadFile("../.github/workflows/book-tag.yml")
	if err != nil {
		t.Fatal(err)
	}
	jobPattern := func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?ms)^  ` + name + `:\n(.*?)(?:^  [a-zA-Z][a-zA-Z0-9_-]*:\n|\z)`)
	}
	book := jobPattern("book").FindStringSubmatch(string(release))
	tag := jobPattern("tag").FindStringSubmatch(string(tagWorkflow))
	if len(book) != 2 || len(tag) != 2 {
		t.Fatal("book or tag job missing")
	}
	t.Run("timeouts", func(t *testing.T) {
		for name, job := range map[string]string{"book": book[1], "tag": tag[1]} {
			if !strings.Contains(job, "    timeout-minutes: 10\n") {
				t.Errorf("%s job missing timeout", name)
			}
		}
	})
	t.Run("skip", func(t *testing.T) {
		pin := strings.SplitN(book[1], "      - name: Open book bump pull request", 2)[0]
		if !strings.Contains(pin, `${result%% *}`) || !regexp.MustCompile(`(?m)unchanged\|older:\).*changed=false`).MatchString(pin) || strings.Contains(pin, `[ "$result" = unchanged ]`) {
			t.Errorf("pin step must match the first word and skip unchanged and older: %s", pin)
		}
		if !strings.Contains(book[1], "steps.pin.outputs.changed == 'true'") {
			t.Error("pull request is not gated on changed")
		}
	})
}
