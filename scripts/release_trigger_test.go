package scripts

import (
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestTheReleaseWorkflowIsTriggeredOnlyByVTagsSoBookTagsCannotStartIt(t *testing.T) {
	b, e := os.ReadFile("../.github/workflows/release.yml")
	if e != nil {
		t.Fatal(e)
	}
	s := strings.SplitN(strings.SplitN(string(b), "on:\n", 2)[1], "permissions:", 2)[0]
	if strings.Count(s, "tags:") != 1 || !strings.Contains(s, "    tags: ['v*']\n") || strings.Contains(s, "tags-ignore:") || strings.Contains(s, "branches:") || strings.Contains(s, "pull_request:") {
		t.Fatalf("unexpected trigger: %s", s)
	}
	for _, tc := range []struct {
		s    string
		want bool
	}{{"book/v0.1.1", false}, {"v0.0.1-rc.6", true}} {
		got, e := path.Match("v*", tc.s)
		if e != nil || got != tc.want {
			t.Fatalf("glob %s: %v %v", tc.s, got, e)
		}
	}
}
func TestTheValidateTagStepRejectsBookTagsAndAcceptsAPrerelease(t *testing.T) {
	b, e := os.ReadFile("../.github/workflows/release.yml")
	if e != nil {
		t.Fatal(e)
	}
	m := regexp.MustCompile(`(?m)\[\[ "\$VERSION" =~ ([^ ]+) \]\]`).FindStringSubmatch(string(b))
	if len(m) != 2 {
		t.Fatal("missing validation pattern")
	}
	r, e := regexp.Compile(m[1])
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		s    string
		want bool
	}{{"book/v0.1.1", false}, {"v0.0.1-rc.6", true}, {"v0.0.1", true}, {"v0.0.1/x", false}} {
		if r.MatchString(tc.s) != tc.want {
			t.Fatal(tc.s)
		}
	}
}
