package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTheReleaseWorkflowTriggersOnlyOnVTagsAndGrantsNoPermissionsAtTheTop(t *testing.T) {
	source := releaseWorkflow(t)
	for _, want := range []string{"tags: ['v*']", "permissions: {}", "git merge-base --is-ancestor HEAD origin/main", "fetch-depth: 0", "persist-credentials: false", "^v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.-]+)?$", "go test ./...", "contents: read", "contents: write", "id-token: write", "attestations: write"} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q", want)
		}
	}
	trigger := strings.Split(source, "permissions:")[0]
	if strings.Contains(trigger, "branches:") || strings.Contains(trigger, "pull_request:") {
		t.Fatal("release has a non-tag trigger")
	}
}

func TestTheReleaseWorkflowBuildsAllFourTargetsWithoutCgoAndInjectsTheVersionIntoMainVersion(t *testing.T) {
	source := releaseWorkflow(t)
	for _, want := range []string{"darwin/amd64 darwin/arm64 linux/amd64 linux/arm64", "CGO_ENABLED=0", "-trimpath", "-X main.version=${VERSION}", "km_${VERSION}_${goos}_${goarch}", "./cmd/km", "cp LICENSE README.md", "sha256sum *.tar.gz > SHA256SUMS", "sha256sum -c SHA256SUMS", "subject-path: dist/*.tar.gz"} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestEveryActionInTheWorkflowsIsPinnedToACommitSha(t *testing.T) {
	paths, err := filepath.Glob("../../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	action := regexp.MustCompile(`uses: ([^\s]+)`)
	pinned := regexp.MustCompile(`@[a-f0-9]{40}$`)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range action.FindAllStringSubmatch(string(data), -1) {
			if !pinned.MatchString(match[1]) {
				t.Errorf("%s: unpinned %s", path, match[1])
			}
		}
	}
	source := releaseWorkflow(t)
	data, err := os.ReadFile("../../.github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"actions/checkout@", "actions/setup-go@"} {
		re := regexp.MustCompile(regexp.QuoteMeta(name) + `[a-f0-9]{40}`)
		want := re.FindString(string(data))
		if want == "" || !strings.Contains(source, want) {
			t.Errorf("release differs from test: %s", want)
		}
	}
}

func TestTheReleaseWorkflowPublishesAPrereleaseForTagsWithASuffix(t *testing.T) {
	source := releaseWorkflow(t)
	for _, want := range []string{`if [[ "$TAG" == *-* ]]`, `flags+=(--prerelease)`, `gh release create "$TAG" --verify-tag --title "$TAG" --generate-notes "${flags[@]}" dist/*`} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q", want)
		}
	}
}
