package ensemble

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestAChairMayStateItsFocusInUserAndRepositoryEnsembles(t *testing.T) {
	for _, portable := range []bool{false, true} {
		for _, part := range []string{"reviewer", "classifier", "gate"} {
			body := fmt.Sprintf("score = \"review\"\n[sections.review.chairs.security]\npart = %q\nagents = [\"claude\"]\nfocus = \"  see {reply} %%s  \"\n", part)
			path := writeEnsemble(t, t.TempDir(), "sample", body)
			spec, err := load("sample", path, portable, loop.BuiltInAgents())
			if err != nil {
				t.Fatal(err)
			}
			members := bind(t, spec, everyAgent)
			if spec.Sections[0].Chairs[0].Focus != "  see {reply} %s  " || members[0].Chair.Focus != "  see {reply} %s  " {
				t.Fatalf("focus was not preserved: %+v %+v", spec, members)
			}
		}
	}
}

func TestInvalidFocusIsRejectedNamingTheFileAndChair(t *testing.T) {
	for _, tc := range []struct{ focus, reason string }{{"", "empty or whitespace-only"}, {"  \n\t", "empty or whitespace-only"}, {strings.Repeat("é", 513), "1026 bytes"}} {
		body := fmt.Sprintf("[sections.review.chairs.security]\npart = \"reviewer\"\nagents = [\"claude\"]\nfocus = %q\n", tc.focus)
		path := writeEnsemble(t, t.TempDir(), "sample", body)
		_, err := load("sample", path, false, loop.BuiltInAgents())
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "sections.review.chairs.security.focus") || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("error = %v", err)
		}
	}
	path := writeEnsemble(t, t.TempDir(), "sample", fmt.Sprintf("[sections.review.chairs.security]\npart = \"reviewer\"\nagents = [\"claude\"]\nfocus = %q\n", strings.Repeat("é", 512)))
	if _, err := load("sample", path, false, loop.BuiltInAgents()); err != nil {
		t.Fatal(err)
	}
}

func TestARehearsalBriefsAChairWithItsFocus(t *testing.T) {
	m := Member{Section: "review", Name: "security", Part: "reviewer"}
	original := briefing("band", "task", "km", "id", m, nil, "queue")
	want := "Cue id from km: a rehearsal of ensemble band. You are chair review.security@band (part reviewer).\nSection-mates: none\nTask:\ntask\nThis is a briefing: read and understand the task, but do not change any files yet. When you have, run this shell command exactly, then stop:\n'km' cue done -queue 'queue' id"
	if original != want {
		t.Fatalf("unfocused briefing = %q, want %q", original, want)
	}
	m.Chair.Focus = "security {reply}"
	got := briefing("band", "task", "km", "id", m, nil, "queue")
	if !strings.Contains(got, "Your focus in this ensemble: security {reply}\nOther chairs playing the same part may cover other angles; concentrate on yours.\n") || strings.Contains(original, "Your focus") {
		t.Fatalf("briefing = %s", got)
	}
}
