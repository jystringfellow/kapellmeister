package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoke(input string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

const fixturePath = "testdata/kit.yaml"

func copyFile(t *testing.T, source string) string {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	kitPath := filepath.Join(t.TempDir(), "kit")
	if err = os.WriteFile(kitPath, content, 0600); err != nil {
		t.Fatal(err)
	}
	return kitPath
}

func copyKit(t *testing.T) string {
	return copyFile(t, fixturePath)
}

func writeSums(t *testing.T, content string) string {
	t.Helper()
	sumsPath := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(sumsPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return sumsPath
}

func sums(tag, arm64Sum, amd64Sum string) string {
	return arm64Sum + "  km_" + tag + "_linux_arm64.tar.gz\n" + amd64Sum + " *km_" + tag + "_linux_amd64.tar.gz\n"
}

func TestSetChangesOnlyTheThreeDefaultLinesOfTheKitFile(t *testing.T) {
	kitPath := copyKit(t)
	before, _ := os.ReadFile(kitPath)
	tag := "v1.2.3"
	armChecksum := strings.Repeat("a", 64)
	amdChecksum := strings.Repeat("b", 64)
	sumsPath := writeSums(t, sums(tag, armChecksum, amdChecksum))
	code, stdout, stderr := invoke("", "set", kitPath, tag, sumsPath)
	if code != 0 {
		t.Fatalf("%s %s", stdout, stderr)
	}
	after, _ := os.ReadFile(kitPath)
	beforeLines, afterLines := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatal("line count changed")
	}
	var changed []string
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			changed = append(changed, afterLines[i])
		}
	}
	if strings.Join(changed, "|") != "    default: \""+tag+"\"|    default: \""+armChecksum+"\"|    default: \""+amdChecksum+"\"" {
		t.Fatalf("changed: %q", changed)
	}
	code, stdout, stderr = invoke("", "pinned", kitPath)
	if code != 0 || stdout != tag+"\n" {
		t.Fatalf("%d %q %s", code, stdout, stderr)
	}
	code, stdout, stderr = invoke("", "set", kitPath, tag, sumsPath)
	if code != 0 || stdout != "unchanged\n" {
		t.Fatalf("%d %q %s", code, stdout, stderr)
	}
}

func TestSetRefusesAndLeavesTheFileByteIdentical(t *testing.T) {
	armChecksum := strings.Repeat("a", 64)
	amdChecksum := strings.Repeat("b", 64)
	goodSums := sums("v1.2.3", armChecksum, amdChecksum)
	cases := []struct{ name, tag, sum, target, replacement string }{{"tag", "book/v1.2.3", goodSums, "", ""}, {"missing", "v1.2.3", armChecksum + "  km_v1.2.3_linux_arm64.tar.gz\n", "", ""}, {"uppercase", "v1.2.3", sums("v1.2.3", strings.Repeat("A", 64), amdChecksum), "", ""}, {"short", "v1.2.3", sums("v1.2.3", armChecksum[:63], amdChecksum), "", ""}, {"duplicate checksum", "v1.2.3", goodSums + strings.Repeat("c", 64) + "  km_v1.2.3_linux_arm64.tar.gz\n", "", ""}, {"missing arg", "v1.2.3", goodSums, "  kmSha256Arm64:", "  missing:"}, {"duplicate arg", "v1.2.3", goodSums, "  kmSha256Arm64:", "  kmSha256Arm64:\n    default: \"x\"\n  kmSha256Arm64:"}, {"missing default", "v1.2.3", goodSums, "    default: \"v0.0.1-rc.6\"", "    default: 'v0.0.1-rc.6'"}, {"duplicate default", "v1.2.3", goodSums, "  km:\n", "  km:\n    default: \"x\"\n"}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			kitPath := copyKit(t)
			data, _ := os.ReadFile(kitPath)
			if testCase.target != "" {
				data = []byte(strings.Replace(string(data), testCase.target, testCase.replacement, 1))
				os.WriteFile(kitPath, data, 0600)
			}
			code, _, _ := invoke("", "set", kitPath, testCase.tag, writeSums(t, testCase.sum))
			after, _ := os.ReadFile(kitPath)
			if code == 0 || !bytes.Equal(data, after) {
				t.Fatal("refusal changed file or succeeded")
			}
		})
	}
}

func TestSetKeepsCRLFLineEndingsAndAMissingFinalNewline(t *testing.T) {
	kitPath := copyKit(t)
	content, _ := os.ReadFile(kitPath)
	data := strings.TrimSuffix(strings.ReplaceAll(string(content), "\n", "\r\n"), "\r\n")
	os.WriteFile(kitPath, []byte(data), 0600)
	armChecksum := strings.Repeat("a", 64)
	code, _, stderr := invoke("", "set", kitPath, "v1.2.3", writeSums(t, sums("v1.2.3", armChecksum, armChecksum)))
	if code != 0 {
		t.Fatal(stderr)
	}
	after, _ := os.ReadFile(kitPath)
	if strings.Contains(strings.ReplaceAll(string(after), "\r\n", ""), "\n") || strings.HasSuffix(string(after), "\n") {
		t.Fatal("line endings changed")
	}
}

func TestNextBookTagIncrementsThePatchOfTheHighestBookTag(t *testing.T) {
	for _, testCase := range []struct{ input, want string }{{"book/v0.1.0\n", "book/v0.1.1\n"}, {"book/v0.2.0\nbook/v0.1.9\n", "book/v0.2.1\n"}, {"book/v0.1.9\nbook/v0.1.10\nrefs/tags/book/v9.9.9\nbook/v9.0.0-rc.1\n", "book/v0.1.11\n"}} {
		code, stdout, stderr := invoke(testCase.input, "next-book-tag")
		if code != 0 || stdout != testCase.want {
			t.Fatalf("%d %q %s", code, stdout, stderr)
		}
	}
	if code, _, _ := invoke("v1.0.0\n", "next-book-tag"); code == 0 {
		t.Fatal("missing tags accepted")
	}
}

func TestCheckComparesReleaseTagsBySemver(t *testing.T) {
	for _, testCase := range []struct {
		pin, tags string
		fail      bool
	}{{"v0.0.1-rc.6", "v0.0.1", true}, {"v0.0.1-rc.9", "v0.0.1-rc.10", true}, {"v0.0.1", "v0.0.1", false}, {"v0.0.1", "book/v9.9.9\nv0.0.1-rc.9", false}, {"v0.0.1-rc.10", "v0.0.1-rc.9", false}, {"v0.0.1", "", false}} {
		kitPath := copyKit(t)
		data, _ := os.ReadFile(kitPath)
		os.WriteFile(kitPath, []byte(strings.Replace(string(data), "v0.0.1-rc.6", testCase.pin, 1)), 0600)
		code, _, stderr := invoke(testCase.tags, "check", kitPath)
		if (code != 0) != testCase.fail || (testCase.fail && (!strings.Contains(stderr, testCase.pin) || !strings.Contains(stderr, testCase.tags))) {
			t.Fatalf("%+v: %d %s", testCase, code, stderr)
		}
	}
}

func TestTheCheckedInKitHasOneDefaultForEachPinnedArgument(t *testing.T) {
	kitPath := copyFile(t, "../../book/kits/kapellmeister/kapellmeister.yaml")
	code, stdout, stderr := invoke("", "pinned", kitPath)
	if code != 0 || !tagPattern.MatchString(strings.TrimSpace(stdout)) {
		t.Fatalf("%d %q %s", code, stdout, stderr)
	}
	before, err := os.ReadFile(kitPath)
	if err != nil {
		t.Fatal(err)
	}
	tag := "v999.0.0"
	armChecksum := strings.Repeat("a", 64)
	amdChecksum := strings.Repeat("b", 64)
	sumsPath := writeSums(t, sums(tag, armChecksum, amdChecksum))
	code, stdout, stderr = invoke("", "set", kitPath, tag, sumsPath)
	if code != 0 || stdout != "set "+tag+"\n" {
		t.Fatalf("%d %q %s", code, stdout, stderr)
	}
	after, err := os.ReadFile(kitPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeLines, afterLines := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatal("line count changed")
	}
	var changed []string
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			changed = append(changed, afterLines[i])
		}
	}
	if strings.Join(changed, "|") != "    default: \""+tag+"\"|    default: \""+armChecksum+"\"|    default: \""+amdChecksum+"\"" {
		t.Fatalf("changed: %q", changed)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"set"}, {"pinned"}, {"check"}, {"next-book-tag", "extra"}} {
		if code, _, _ := invoke("", args...); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

func TestPreReleaseIdentifiersCompareAsSemverSays(t *testing.T) {
	for _, testCase := range []struct{ lower, higher string }{
		{"v0.0.1-rc.9", "v0.0.1-rc.10"}, {"v0.0.1-rc.6", "v0.0.1"},
		{"v0.0.1-alpha", "v0.0.1-beta"}, {"v0.0.1-rc.1", "v0.0.1-rc.1.1"},
		{"v0.0.1-1", "v0.0.1-alpha"}, {"v0.0.9", "v0.1.0"},
	} {
		if compare(testCase.lower, testCase.higher) >= 0 || compare(testCase.higher, testCase.lower) <= 0 {
			t.Fatalf("%s must precede %s", testCase.lower, testCase.higher)
		}
	}
}

func TestPinnedRefusesAReformattedOrInvalidDefault(t *testing.T) {
	for _, replacement := range []string{"    default: 'v0.0.1-rc.6'", "    default: \"not-a-tag\""} {
		kitPath := copyKit(t)
		data, _ := os.ReadFile(kitPath)
		os.WriteFile(kitPath, []byte(strings.Replace(string(data), "    default: \"v0.0.1-rc.6\"", replacement, 1)), 0600)
		if code, _, _ := invoke("", "pinned", kitPath); code == 0 {
			t.Fatal(replacement)
		}
	}
}

func TestAnArgumentDefaultOutsideItsBlockIsRejected(t *testing.T) {
	kitPath := copyKit(t)
	data, _ := os.ReadFile(kitPath)
	data = []byte(strings.Replace(string(data), "  km:\n    default:", "  km:\n other:\n    default:", 1))
	os.WriteFile(kitPath, data, 0600)
	if code, _, _ := invoke("", "pinned", kitPath); code == 0 {
		t.Fatal("accepted default outside km block")
	}
}

func TestSetWithAReleaseBelowThePinPrintsOlderAndLeavesTheFileByteIdentical(t *testing.T) {
	for _, testCase := range []struct{ tag, pinned string }{{"v0.0.1-rc.5", "v0.0.1-rc.6"}, {"v0.0.1", "v0.1.0"}} {
		t.Run(testCase.tag, func(t *testing.T) {
			kitPath := copyKit(t)
			content, err := os.ReadFile(kitPath)
			if err != nil {
				t.Fatal(err)
			}
			before := []byte(strings.Replace(string(content), "v0.0.1-rc.6", testCase.pinned, 1))
			if err := os.WriteFile(kitPath, before, 0600); err != nil {
				t.Fatal(err)
			}
			sumsPath := writeSums(t, sums(testCase.tag, strings.Repeat("a", 64), strings.Repeat("b", 64)))
			code, output, stderr := invoke("", "set", kitPath, testCase.tag, sumsPath)
			after, err := os.ReadFile(kitPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "older: " + testCase.tag + " is below the pinned " + testCase.pinned + "\n"
			if code != 0 || output != want || !bytes.Equal(before, after) {
				t.Errorf("code %d, output %q, stderr %q, file unchanged %t", code, output, stderr, bytes.Equal(before, after))
			}
		})
	}
}

func TestSetWithThePinnedTagAndTheSameChecksumsPrintsUnchanged(t *testing.T) {
	kitPath := copyKit(t)
	tag := "v1.2.3"
	sumsPath := writeSums(t, sums(tag, strings.Repeat("a", 64), strings.Repeat("b", 64)))
	if code, output, stderr := invoke("", "set", kitPath, tag, sumsPath); code != 0 {
		t.Fatalf("%d %q %q", code, output, stderr)
	}
	before, err := os.ReadFile(kitPath)
	if err != nil {
		t.Fatal(err)
	}
	code, output, stderr := invoke("", "set", kitPath, tag, sumsPath)
	after, err := os.ReadFile(kitPath)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || output != "unchanged\n" || !bytes.Equal(before, after) {
		t.Fatalf("%d %q %q, file unchanged %t", code, output, stderr, bytes.Equal(before, after))
	}
}

func TestSetWithThePinnedTagAndDifferentChecksumsIsRefusedNamingTheTag(t *testing.T) {
	kitPath := copyKit(t)
	tag := "v1.2.3"
	sumsPath := writeSums(t, sums(tag, strings.Repeat("a", 64), strings.Repeat("b", 64)))
	if code, output, stderr := invoke("", "set", kitPath, tag, sumsPath); code != 0 {
		t.Fatalf("%d %q %q", code, output, stderr)
	}
	before, err := os.ReadFile(kitPath)
	if err != nil {
		t.Fatal(err)
	}
	changedSumsPath := writeSums(t, sums(tag, strings.Repeat("c", 64), strings.Repeat("d", 64)))
	code, output, stderr := invoke("", "set", kitPath, tag, changedSumsPath)
	after, err := os.ReadFile(kitPath)
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 || output != "" || !strings.Contains(stderr, tag) || !bytes.Equal(before, after) {
		t.Fatalf("%d %q %q, file unchanged %t", code, output, stderr, bytes.Equal(before, after))
	}
}
