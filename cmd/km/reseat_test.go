package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type reseatCAO struct {
	mu       sync.Mutex
	calls    []string
	cues     []string
	query    url.Values
	profiles []string
	ignores  bool
}

func (f *reseatCAO) serve(t *testing.T, session string) *httptest.Server {
	t.Helper()
	closed := regexp.MustCompile(`'([^']+)' cue done -queue '([^']+)' (\S+)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents/profiles":
			var profiles []map[string]any
			for _, name := range f.profiles {
				profiles = append(profiles, map[string]any{"name": name, "loadable": true})
			}
			json.NewEncoder(w).Encode(profiles)
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/"+session+"/terminals":
			f.query = r.URL.Query()
			fmt.Fprintf(w, `{"id":"new-terminal","session_name":%q}`, session)
		case r.Method == http.MethodPatch:
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/output"):
			fmt.Fprint(w, `{"output":""}`)
		case r.Method == http.MethodPost && r.URL.Path == "/terminals/new-terminal/inbox/messages":
			text := r.URL.Query().Get("message")
			f.cues = append(f.cues, text)
			match := closed.FindStringSubmatch(text)
			if match == nil {
				t.Errorf("briefing lacks closure command: %s", text)
			} else if !f.ignores {
				if err := ensemble.Close(match[2], match[3], "new-terminal", ""); err != nil {
					t.Error(err)
				}
			}
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/terminals/"):
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected CAO call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReseatOfAnUnknownSessionFailsListingTheRecordedSessions(t *testing.T) {
	standingState(t)
	err := reseatCommand([]string{"missing", "chair"})
	if err == nil || err.Error() != `unknown session "missing" (recorded: alpha, beta)` {
		t.Fatalf("error = %v", err)
	}
	t.Setenv(loop.StateEnv, t.TempDir())
	err = reseatCommand([]string{"missing", "chair"})
	if err == nil || err.Error() != `unknown session "missing" (none recorded)` {
		t.Fatalf("error = %v", err)
	}
}

func TestReseatOfAnUnknownAddressFailsListingTheRecordedAddresses(t *testing.T) {
	s := playStanding(t)
	err := reseatCommand([]string{s.Session, "missing", "-cao", unreachableCAO})
	if err == nil || !strings.Contains(err.Error(), `unknown chair "missing" in session `+s.Session) {
		t.Fatalf("error = %v", err)
	}
	for _, c := range s.Chairs {
		if !strings.Contains(err.Error(), c.Address) {
			t.Errorf("error %v lacks %s", err, c.Address)
		}
	}
}

func TestReseatNeedsExactlyASessionAndAnAddress(t *testing.T) {
	for _, args := range [][]string{nil, {"one"}, {"one", "two", "three"}} {
		if err := reseatCommand(args); err == nil || !strings.Contains(err.Error(), "usage: km reseat SESSION ADDRESS") {
			t.Errorf("args %v: %v", args, err)
		}
	}
}

func TestReseatAcceptsFlagsBeforeAndAfterThePositionals(t *testing.T) {
	for _, position := range []string{"before", "between", "after"} {
		t.Run(position, func(t *testing.T) {
			s := playStanding(t)
			fake := &reseatCAO{}
			srv := fake.serve(t, s.Session)
			flags := []string{"-cao", srv.URL, "-timeout", "1s"}
			args := append(slices.Clone(flags), s.Session, s.Chairs[0].Address)
			if position == "between" {
				args = append(append([]string{s.Session}, flags...), s.Chairs[0].Address)
			}
			if position == "after" {
				args = append([]string{s.Session, s.Chairs[0].Address}, flags...)
			}
			if err := reseatCommand(args); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReseatWithAgentRecordsTheNewBindingForThatChairOnly(t *testing.T) {
	s := playStanding(t)
	fake := &reseatCAO{}
	srv := fake.serve(t, s.Session)
	if err := reseatCommand([]string{s.Session, s.Chairs[2].Address, "-agent", "codex:km-other:model-x", "-cao", srv.URL}); err != nil {
		t.Fatal(err)
	}
	got, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), s.Session)
	if err != nil {
		t.Fatal(err)
	}
	want := s
	want.Chairs = slices.Clone(s.Chairs)
	c := &want.Chairs[2]
	c.Binding = loop.Chair{Address: c.Address, Provider: "codex", Profile: "km-other", Model: "model-x"}
	c.Chair, c.TerminalID, c.Briefed = c.Binding.String(), "new-terminal", true
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	if fake.query.Get("provider") != "codex" || fake.query.Get("agent_profile") != "km-other" || fake.query.Get("model") != "model-x" {
		t.Errorf("launch = %v", fake.query)
	}
}

func TestReseatWithAnAgentTierThatTheTiersDoNotCoverFailsBeforeAnyCAOCall(t *testing.T) {
	for _, spec := range []string{"myprov:myprof@large", "myprov:myprof@small"} {
		s := playStanding(t)
		writeFile(t, loop.UserConfigPath(loop.Home()), "[tiers.myprov.medium]\nmodel = 'medium-model'\n")
		err := reseatCommand([]string{s.Session, s.Chairs[2].Address, "-agent", spec, "-cao", unreachableCAO})
		if err == nil || !strings.Contains(err.Error(), "tier to pin against") || !strings.Contains(err.Error(), s.Chairs[2].Address) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestReseatWithoutAgentKeepsTheRecordedBinding(t *testing.T) {
	s := playStanding(t)
	s.Chairs[0].Binding.Model = ""
	s.Chairs[0].Binding.Tier = "large"
	s.Chairs[0].Binding.Profile = "recorded-profile"
	savePlayStanding(t, s)
	fake := &reseatCAO{}
	srv := fake.serve(t, s.Session)
	if err := reseatCommand([]string{s.Session, s.Chairs[0].Address, "-cao", srv.URL}); err != nil {
		t.Fatal(err)
	}
	got, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), s.Session)
	if err != nil {
		t.Fatal(err)
	}
	if got.Chairs[0].Binding != s.Chairs[0].Binding {
		t.Fatalf("binding = %+v, want %+v", got.Chairs[0].Binding, s.Chairs[0].Binding)
	}
	if fake.query.Get("model") != "" || fake.query.Get("agent_profile") != "recorded-profile" {
		t.Errorf("launch = %v", fake.query)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "profiles") {
			t.Errorf("unexpected profile preparation: %s", call)
		}
	}
}

func TestReseatWithAnAgentAppliesSizingAndEnsuresEffortProfilesBeforeBooting(t *testing.T) {
	s := playStanding(t)
	writeFile(t, loop.UserConfigPath(loop.Home()), "[tiers.codex.large]\nmodel = 'chosen-model'\neffort = 'xhigh'\n")
	fake := &reseatCAO{profiles: []string{"km-codex-medium", "km-codex-high", "km-codex-xhigh"}}
	srv := fake.serve(t, s.Session)
	if err := reseatCommand([]string{s.Session, s.Chairs[0].Address, "-agent", "codex@large", "-cao", srv.URL}); err != nil {
		t.Fatal(err)
	}
	got, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), s.Session)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Chairs[0]
	if c.Binding.Profile != "km-codex-xhigh" || c.Binding.Model != "chosen-model" || c.Binding.Address != c.Address || c.Chair != c.Binding.String() {
		t.Fatalf("chair = %+v", c)
	}
	if len(fake.calls) == 0 || fake.calls[0] != "GET /agents/profiles" {
		t.Fatalf("calls = %v", fake.calls)
	}
	if fake.query.Get("model") != "chosen-model" || fake.query.Get("agent_profile") != "km-codex-xhigh" {
		t.Errorf("launch = %v", fake.query)
	}
}

func TestReseatRereadsTheTaskFileWhenOneWasRecorded(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			s := playStanding(t)
			s.TaskFile = filepath.Join(t.TempDir(), "task.md")
			savePlayStanding(t, s)
			if !missing {
				writeFile(t, s.TaskFile, "updated task from file")
			}
			fake := &reseatCAO{}
			srv := fake.serve(t, s.Session)
			err := reseatCommand([]string{s.Session, s.Chairs[0].Address, "-cao", srv.URL})
			if missing {
				if err == nil || !strings.Contains(err.Error(), s.TaskFile) || len(fake.calls) != 0 {
					t.Fatalf("error = %v, calls = %v", err, fake.calls)
				}
			} else if err != nil || len(fake.cues) != 1 || !strings.Contains(fake.cues[0], "updated task from file") || strings.Contains(fake.cues[0], s.Task) {
				t.Fatalf("error = %v, cues = %v", err, fake.cues)
			}
		})
	}
}

func TestReseatUsesTheConfiguredBriefingTimeout(t *testing.T) {
	s := playStanding(t)
	writeFile(t, loop.UserConfigPath(loop.Home()), "[run]\npreflight_timeout = '1ms'\n")
	fake := &reseatCAO{ignores: true}
	srv := fake.serve(t, s.Session)
	err := reseatCommand([]string{s.Session, s.Chairs[0].Address, "-cao", srv.URL})
	if err == nil || !strings.Contains(err.Error(), "cue not closed within 1ms") {
		t.Fatalf("error = %v", err)
	}
}
