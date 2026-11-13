package tune

import (
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
)

func TestSameCommandsIgnoresCommentsBlankLinesAndStepLabels(t *testing.T) {
	a, _ := initrepo.RenderVerify("sh tests.sh")
	b := strings.Replace(a, "run 'verify'", "# fast checks\n\nrun 'unit tests'", 1)
	comparison, same := SameCommands(a, b)
	if !same || comparison.ProvedDigest == "" || comparison.ProvedDigest != comparison.CandidateDigest {
		t.Fatal(comparison, same)
	}
}

func TestSameCommandsProvesAgainWheneverCommandMeaningCouldDiffer(t *testing.T) {
	a, _ := initrepo.RenderVerify("sh tests.sh")
	for _, pair := range [][2]string{
		{a, strings.Replace(a, "sh tests.sh", "sh other.sh", 1)},
		{a, a + "false\n"},
		{a, strings.Replace(a, "sh -c", "echo", 1)},
		{"echo '$# a#b # text'\n", "echo '$# a#b # changed'\n"},
		{"echo '${#x}'\n", "echo '${#y}'\n"},
		{"echo 'first\n# text\nlast'\n", "echo 'first\n# changed\nlast'\n"},
		{"cat <<END\nx\nEND\n", "cat <<END\nx\nEND\n"},
		{"echo 'open", "echo 'open"},
		{"echo ok\r\n", "echo ok\r\n"},
		{"echo foo\\\n# comment\nbar\n", "echo foo\\\nbar\n"},
		{a, strings.Replace(a, "run 'verify'", "run \"$LABEL\"", 1)},
		{"run() { sh -c \"$1\"; }\nrun 'true' 'false'\n", "run() { sh -c \"$1\"; }\nrun 'false' 'false'\n"},
	} {
		if _, same := SameCommands(pair[0], pair[1]); same {
			t.Errorf("unsafe equality: %q vs %q", pair[0], pair[1])
		}
	}
}

func TestSameCommandsPreservesQuotedHashesAndMultilineCommandsWhileIgnoringDiagnostics(t *testing.T) {
	for _, command := range []string{"printf '%s\\n' '$# ${#x} a#b # text'", "printf 'first\n# not a comment\nlast'", "echo x; echo y"} {
		a, _ := initrepo.RenderVerify(command)
		b := strings.Replace(a, "run 'verify'", "run \"unit tests\"", 1)
		b = strings.Replace(b, "run \"unit tests\"", "# stable checks\nrun \"unit tests\"", 1)
		if _, same := SameCommands(a, b); !same {
			t.Errorf("lost literal command: %s", command)
		}
	}
}

func TestSameCommandsRequiresProofForAnUnrecognisedWrapper(t *testing.T) {
	a := "#!/bin/sh\nprintf '%s' $'first\\'\n# content\nlast'\n"
	b := strings.Replace(a, "# content", "# different content", 1)
	if _, same := SameCommands(a, b); same {
		t.Fatal("unrecognised quoting was normalized")
	}
}
