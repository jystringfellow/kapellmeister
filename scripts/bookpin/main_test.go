package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoke(input string, args ...string) (int, string, string) {
	var out, err bytes.Buffer
	code := run(args, strings.NewReader(input), &out, &err)
	return code, out.String(), err.String()
}
func copyKit(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../../book/kits/kapellmeister/kapellmeister.yaml")
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "kit")
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func writeSums(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "SHA256SUMS")
	if e := os.WriteFile(p, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func sums(tag, a, b string) string {
	return a + "  km_" + tag + "_linux_arm64.tar.gz\n" + b + " *km_" + tag + "_linux_amd64.tar.gz\n"
}
func TestSetChangesOnlyTheThreeDefaultLinesOfTheKitFile(t *testing.T) {
	p := copyKit(t)
	before, _ := os.ReadFile(p)
	tag := "v1.2.3"
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	s := writeSums(t, sums(tag, a, b))
	code, out, err := invoke("", "set", p, tag, s)
	if code != 0 {
		t.Fatalf("%s %s", out, err)
	}
	after, _ := os.ReadFile(p)
	x, y := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(x) != len(y) {
		t.Fatal("line count changed")
	}
	var changed []string
	for i := range x {
		if x[i] != y[i] {
			changed = append(changed, y[i])
		}
	}
	if strings.Join(changed, "|") != "    default: \""+tag+"\"|    default: \""+a+"\"|    default: \""+b+"\"" {
		t.Fatalf("changed: %q", changed)
	}
	code, out, err = invoke("", "pinned", p)
	if code != 0 || out != tag+"\n" {
		t.Fatalf("%d %q %s", code, out, err)
	}
	code, out, err = invoke("", "set", p, tag, s)
	if code != 0 || out != "unchanged\n" {
		t.Fatalf("%d %q %s", code, out, err)
	}
}
func TestSetRefusesAndLeavesTheFileByteIdentical(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	good := sums("v1.2.3", a, b)
	cases := []struct{ name, tag, sum, old, new string }{{"tag", "book/v1.2.3", good, "", ""}, {"missing", "v1.2.3", a + "  km_v1.2.3_linux_arm64.tar.gz\n", "", ""}, {"uppercase", "v1.2.3", sums("v1.2.3", strings.Repeat("A", 64), b), "", ""}, {"short", "v1.2.3", sums("v1.2.3", a[:63], b), "", ""}, {"duplicate checksum", "v1.2.3", good + strings.Repeat("c", 64) + "  km_v1.2.3_linux_arm64.tar.gz\n", "", ""}, {"missing arg", "v1.2.3", good, "  kmSha256Arm64:", "  missing:"}, {"duplicate arg", "v1.2.3", good, "  kmSha256Arm64:", "  kmSha256Arm64:\n    default: \"x\"\n  kmSha256Arm64:"}, {"missing default", "v1.2.3", good, "    default: \"v0.0.1-rc.6\"", "    default: 'v0.0.1-rc.6'"}, {"duplicate default", "v1.2.3", good, "  km:\n", "  km:\n    default: \"x\"\n"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := copyKit(t)
			data, _ := os.ReadFile(p)
			if tc.old != "" {
				data = []byte(strings.Replace(string(data), tc.old, tc.new, 1))
				os.WriteFile(p, data, 0600)
			}
			code, _, _ := invoke("", "set", p, tc.tag, writeSums(t, tc.sum))
			after, _ := os.ReadFile(p)
			if code == 0 || !bytes.Equal(data, after) {
				t.Fatal("refusal changed file or succeeded")
			}
		})
	}
}
func TestSetKeepsCRLFLineEndingsAndAMissingFinalNewline(t *testing.T) {
	p := copyKit(t)
	b, _ := os.ReadFile(p)
	data := strings.TrimSuffix(strings.ReplaceAll(string(b), "\n", "\r\n"), "\r\n")
	os.WriteFile(p, []byte(data), 0600)
	a := strings.Repeat("a", 64)
	code, _, err := invoke("", "set", p, "v1.2.3", writeSums(t, sums("v1.2.3", a, a)))
	if code != 0 {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if strings.Contains(strings.ReplaceAll(string(after), "\r\n", ""), "\n") || strings.HasSuffix(string(after), "\n") {
		t.Fatal("line endings changed")
	}
}
func TestNextBookTagIncrementsThePatchOfTheHighestBookTag(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"book/v0.1.0\n", "book/v0.1.1\n"}, {"book/v0.2.0\nbook/v0.1.9\n", "book/v0.2.1\n"}, {"book/v0.1.9\nbook/v0.1.10\nrefs/tags/book/v9.9.9\nbook/v9.0.0-rc.1\n", "book/v0.1.11\n"}} {
		code, out, err := invoke(tc.in, "next-book-tag")
		if code != 0 || out != tc.want {
			t.Fatalf("%d %q %s", code, out, err)
		}
	}
	if code, _, _ := invoke("v1.0.0\n", "next-book-tag"); code == 0 {
		t.Fatal("missing tags accepted")
	}
}
func TestCheckComparesReleaseTagsBySemver(t *testing.T) {
	for _, tc := range []struct {
		pin, tags string
		fail      bool
	}{{"v0.0.1-rc.6", "v0.0.1", true}, {"v0.0.1-rc.9", "v0.0.1-rc.10", true}, {"v0.0.1", "v0.0.1", false}, {"v0.0.1", "book/v9.9.9\nv0.0.1-rc.9", false}, {"v0.0.1-rc.10", "v0.0.1-rc.9", false}, {"v0.0.1", "", false}} {
		p := copyKit(t)
		data, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(data), "v0.0.1-rc.6", tc.pin, 1)), 0600)
		code, _, err := invoke(tc.tags, "check", p)
		if (code != 0) != tc.fail || (tc.fail && (!strings.Contains(err, tc.pin) || !strings.Contains(err, tc.tags))) {
			t.Fatalf("%+v: %d %s", tc, code, err)
		}
	}
}
func TestTheCheckedInKitFileIsParsedByPinned(t *testing.T) {
	p := "../../book/kits/kapellmeister/kapellmeister.yaml"
	code, out, err := invoke("", "pinned", p)
	if code != 0 || !strings.HasPrefix(out, "v") {
		t.Fatalf("%d %q %s", code, out, err)
	}
	if code, _, err = invoke(out, "check", p); code != 0 {
		t.Fatal(err)
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
	for _, tc := range []struct{ lower, higher string }{
		{"v0.0.1-rc.9", "v0.0.1-rc.10"}, {"v0.0.1-rc.6", "v0.0.1"},
		{"v0.0.1-alpha", "v0.0.1-beta"}, {"v0.0.1-rc.1", "v0.0.1-rc.1.1"},
		{"v0.0.1-1", "v0.0.1-alpha"}, {"v0.0.9", "v0.1.0"},
	} {
		if compare(tc.lower, tc.higher) >= 0 || compare(tc.higher, tc.lower) <= 0 {
			t.Fatalf("%s must precede %s", tc.lower, tc.higher)
		}
	}
}

func TestPinnedRefusesAReformattedOrInvalidDefault(t *testing.T) {
	for _, replacement := range []string{"    default: 'v0.0.1-rc.6'", "    default: \"not-a-tag\""} {
		p := copyKit(t)
		data, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(data), "    default: \"v0.0.1-rc.6\"", replacement, 1)), 0600)
		if code, _, _ := invoke("", "pinned", p); code == 0 {
			t.Fatal(replacement)
		}
	}
}

func TestAnArgumentDefaultOutsideItsBlockIsRejected(t *testing.T) {
	p := copyKit(t)
	data, _ := os.ReadFile(p)
	data = []byte(strings.Replace(string(data), "  km:\n    default:", "  km:\n other:\n    default:", 1))
	os.WriteFile(p, data, 0600)
	if code, _, _ := invoke("", "pinned", p); code == 0 {
		t.Fatal("accepted default outside km block")
	}
}
