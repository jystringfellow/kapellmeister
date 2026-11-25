package loop

import (
	"strings"
	"testing"
)

func TestABlockedReasonIsTheBlockerElseTheJoinedScreenElseTheErrorPrintableAndAtMost300Runes(t *testing.T) {
	cases := []struct {
		result PreflightResult
		want   string
	}{
		{PreflightResult{Blocker: "sign in\x1b[31m now", Screen: "other", Error: "error"}, "sign in [31m now"},
		{PreflightResult{Screen: "first\nsecond"}, "first | second"},
		{PreflightResult{Error: "failed"}, "failed"},
		{PreflightResult{Blocker: "\r\u202e", Screen: "\n first\r \n\u202e\nsecond\n"}, "first | second"},
		{PreflightResult{Screen: "\n\r\u202e", Error: "failed"}, "failed"},
	}
	for _, c := range cases {
		if got := c.result.Reason(); got != c.want {
			t.Errorf("Reason() = %q, want %q", got, c.want)
		}
	}
	if got := (PreflightResult{Error: strings.Repeat("é", 400)}).Reason(); len([]rune(got)) != 300 {
		t.Errorf("reason length = %d", len([]rune(got)))
	}
}

func TestSeatFallbackIsOnByDefaultAndEitherConfigCanTurnItOff(t *testing.T) {
	if !DefaultSettings().SeatFallback {
		t.Error("seat fallback should default on")
	}
	home := writeUserConfig(t, "[run]\nseat_fallback = false\n")
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.SeatFallback {
		t.Error("user config should turn fallback off")
	}
	repo := t.TempDir()
	writeConfig(t, repo, "[run]\nseat_fallback = true\n")
	s, err = LoadSettings(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !s.SeatFallback {
		t.Error("repository config should override user config")
	}
}

func TestRepositoryConfigAloneCanTurnSeatFallbackOff(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	writeConfig(t, repo, "[run]\nseat_fallback = false\n")
	s, err := LoadSettings(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if s.SeatFallback {
		t.Error("repository config should turn fallback off from its default")
	}
}
