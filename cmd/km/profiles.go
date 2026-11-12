package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/profiles"
)

const profilesUsage = "usage: km profiles install [-cao URL] [-replace] [AGENT ...]"

type baseProfile struct {
	agent          loop.Agent
	source         []byte
	action, reason string
}

func profilesCommand(args []string, w io.Writer) error {
	if len(args) == 0 || args[0] != "install" {
		return errors.New(profilesUsage)
	}
	fs := flag.NewFlagSet("profiles install", flag.ContinueOnError)
	fs.SetOutput(w)
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url)")
	replace := fs.Bool("replace", false, "replace differing base profiles authored by kapellmeister")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), "")
	if err != nil {
		return err
	}
	names := fs.Args()
	explicit := len(names) > 0
	if !explicit {
		names = s.Agents
	}
	if len(names) == 0 {
		return errors.New("no agents to install: name agents (km profiles install claude codex) or set [agents] available")
	}
	agents, err := s.Defined.Select(strings.Join(names, ","))
	if err != nil {
		return err
	}
	builtins := map[string]loop.Chair{}
	for _, agent := range loop.BuiltInAgents() {
		builtins[agent.Name] = agent.Chair
	}
	var selected []baseProfile
	seen := map[string]bool{}
	for _, agent := range agents {
		if seen[agent.Name] {
			continue
		}
		seen[agent.Name] = true
		builtin, ok := builtins[agent.Name]
		if !ok || builtin != agent.Chair {
			if explicit {
				selected = append(selected, baseProfile{agent: agent, action: "refused", reason: "defined in [agents.define]; its profile is yours"})
			}
			continue
		}
		source, err := profiles.Source(agent.Chair.Profile)
		if err != nil {
			return err
		}
		selected = append(selected, baseProfile{agent: agent, source: source})
	}
	if len(selected) == 0 {
		fmt.Fprintln(w, "nothing to install")
		return nil
	}
	ctx := context.Background()
	client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
	home, err := caoHome()
	if err != nil {
		return err
	}
	for _, p := range selected {
		if p.action == "refused" {
			continue
		}
		if err := proveCAOHome(ctx, client, home, p.agent.Chair.Profile); err != nil {
			return err
		}
		break
	}
	for i := range selected {
		p := &selected[i]
		if p.action == "refused" {
			continue
		}
		name := p.agent.Chair.Profile
		existing, err := os.ReadFile(filepath.Join(home, "agent-store", name+".md"))
		switch {
		case os.IsNotExist(err):
			p.action = "installed"
		case err != nil:
			return err
		case bytes.Equal(existing, p.source):
			p.action = "unchanged"
		default:
			existingProfile, err := client.ProfilePermissions(ctx, name)
			if err != nil || !kmAuthoredDescription(existingProfile.Description) {
				p.action, p.reason = "refused", "exists and was not authored by kapellmeister"
			} else if !*replace {
				p.action, p.reason = "refused", "content differs from this km's; -replace overwrites it; installed copy left untouched"
			} else {
				p.action = "replaced"
			}
		}
	}
	for _, p := range selected {
		if p.action != "installed" && p.action != "replaced" {
			continue
		}
		if err := guardProfile(home, p.agent.Chair.Profile, string(p.source)); err != nil {
			fmt.Fprintf(w, "refused %s: %s\n", p.agent.Chair.Profile, strings.Join(strings.Fields(err.Error()), " "))
			return err
		}
	}
	refused := 0
	for _, p := range selected {
		name := p.agent.Chair.Profile
		switch p.action {
		case "refused":
			if p.source == nil {
				name = p.agent.Name
			}
			fmt.Fprintf(w, "refused %s: %s\n", name, p.reason)
			refused++
		case "unchanged":
			fmt.Fprintf(w, "unchanged %s\n", name)
		default:
			if err := installBaseProfile(ctx, home, p); err != nil {
				fmt.Fprintf(w, "refused %s: %s\n", name, strings.Join(strings.Fields(err.Error()), " "))
				return err
			}
			fmt.Fprintf(w, "%s %s (%s)\n", p.action, name, p.agent.Chair.Provider)
		}
	}
	if refused > 0 {
		return fmt.Errorf("%d profile(s) refused", refused)
	}
	return nil
}

func kmAuthoredDescription(description string) bool {
	return strings.HasPrefix(description, "kapellmeister chair")
}

func caoInstall(ctx context.Context, path, provider string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "cao", "install", path, "--provider", provider).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("cao install %s: %w: %s", path, err, out)
	}
	return out, nil
}

func installBaseProfile(ctx context.Context, home string, p baseProfile) error {
	dir, err := os.MkdirTemp("", "km-profiles-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	name := p.agent.Chair.Profile
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, p.source, 0o600); err != nil {
		return err
	}
	out, err := caoInstall(ctx, path, p.agent.Chair.Provider)
	if err != nil {
		return err
	}
	installed, err := os.ReadFile(filepath.Join(home, "agent-store", name+".md"))
	if err != nil || !bytes.Equal(installed, p.source) {
		return fmt.Errorf("cao install %s exited 0 but the installed profile is not the one km sent (read back: %v): %s", name, err, out)
	}
	return nil
}
