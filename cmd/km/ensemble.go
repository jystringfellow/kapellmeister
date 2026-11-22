package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func lineUp(s loop.Settings, worktree, name string, roles map[string]*string) (loop.Settings, loop.Chairs, []ensemble.Member, error) {
	spec, found, err := ensemble.Find(name, loop.Home(), worktree, s.Defined)
	if err != nil {
		return s, loop.Chairs{}, nil, err
	}
	_, inConfig := s.Ensembles[name]
	if !found {
		if name != "" && !inConfig {
			return s, loop.Chairs{}, nil, unknownEnsemble(s, worktree, name)
		}
		s, err = loadSelectedScore(s, worktree, s.ScoreName)
		if err != nil {
			return s, loop.Chairs{}, nil, err
		}
		if s.Score.Score.Source == "built-in" {
			for _, part := range loop.AllRoles() {
				if flag := roles[part]; flag != nil && *flag != "" {
					if _, ok := s.Score.Score.Part(part); !ok {
						return s, loop.Chairs{}, nil, fmt.Errorf("-%s: score %s has no %s part", part, s.Score.Score.Name, part)
					}
				}
			}
		}
		s, chairs, err := runChairs(s, name, roles)
		if err != nil {
			return s, chairs, nil, err
		}
		if err := checkPins(s.Policy, chairSpecs(s, roles), chairs); err != nil {
			return s, chairs, nil, err
		}
		members := ensemble.MembersOf(chairs)
		if s.Score.Score.Source == "built-in" {
			members = slices.DeleteFunc(members, func(m ensemble.Member) bool { _, ok := s.Score.Score.Part(m.Part); return !ok })
		}
		if _, err := ensemble.ChairsFor(s.Score.Score, members); err != nil {
			return s, chairs, nil, err
		}
		if s.Score.Score.Source != "" {
			s.GateWhen = ""
			if s.Score.Score.Source == "built-in" {
				s.GateWhen = s.Ensembles[name].GateWhen
			}
		}
		return s, chairs, members, nil
	}
	if s.ScoreName != "" {
		return s, loop.Chairs{}, nil, fmt.Errorf("-score cannot override ensemble %s from %s; edit its score key", name, spec.Path)
	}
	s, err = loadSelectedScore(s, worktree, spec.Score)
	if err != nil {
		return s, loop.Chairs{}, nil, err
	}
	if inConfig {
		return s, loop.Chairs{}, nil, fmt.Errorf("ensemble %s is defined both in [ensembles.%s] of %s and in %s; rename one", name, name, loop.UserConfigPath(loop.Home()), spec.Path)
	}
	for _, role := range loop.AllRoles() {
		if p := roles[role]; p != nil && *p != "" {
			return s, loop.Chairs{}, nil, fmt.Errorf("-%s cannot override ensemble %s from %s; edit it, or copy it to %s and edit that", role, name, spec.Path, loop.Home()+"/"+ensemble.UserDir)
		}
	}
	members, err := spec.Bind(s.Defined, s.Agents)
	if err != nil {
		return s, loop.Chairs{}, nil, err
	}
	for _, m := range members {
		if err := checkPin(s.Policy, m.Part, m.Address(name), m.Chair.String()+"@"+m.Chair.Tier, m.Chair); err != nil {
			return s, loop.Chairs{}, nil, err
		}
	}
	chairs, err := ensemble.ChairsFor(s.Score.Score, members)
	if err != nil {
		return s, loop.Chairs{}, nil, fmt.Errorf("%s: %w", spec.Path, err)
	}
	s.GateWhen = spec.GateWhen
	if s.Score.Score.Source == "" {
		s.GateWhen = firstNonEmpty(spec.GateWhen, loop.GateWhenAny)
	}
	return s, chairs, members, nil
}

func unknownEnsemble(s loop.Settings, worktree, name string) error {
	names := ensemble.Names(loop.Home(), worktree)
	for configName := range s.Ensembles {
		if !slices.Contains(names, configName) {
			names = append(names, configName)
		}
	}
	slices.Sort(names)
	defined := "no ensembles defined"
	if len(names) > 0 {
		defined = "defined: " + strings.Join(names, ", ")
	}
	return fmt.Errorf("unknown ensemble %q (%s)", name, defined)
}

func printEnsembleFiles(w io.Writer, home, worktree string) {
	for _, name := range ensemble.Names(home, worktree) {
		path, _ := ensemble.Path(name, home, worktree)
		fmt.Fprintf(w, "ensemble %s  %s\n", name, path)
	}
}

func worktreeDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func sizeMembers(policy loop.Policy, members []ensemble.Member) {
	for i, m := range members {
		members[i] = sizeMember(policy, m)
	}
}

func sizeMember(policy loop.Policy, m ensemble.Member) ensemble.Member {
	m.Chair, _ = policy.Chair(policy.PartPolicyKey(m.Part), m.Chair, "", 0)
	return m
}

func prepareMember(ctx context.Context, client *cao.Client, policy loop.Policy, agents loop.Agents, spec string, m ensemble.Member) (ensemble.Member, error) {
	chair, err := agents.ResolveChair(spec)
	if err != nil {
		return m, err
	}
	if err := checkPin(policy, m.Part, m.Chair.Address, spec, chair); err != nil {
		return m, err
	}
	chair.Address = m.Chair.Address
	chair.Focus = m.Chair.Focus
	m.Chair = chair
	m = sizeMember(policy, m)
	if err := ensureProfiles(ctx, client, &policy, map[string][]loop.Chair{policy.PartPolicyKey(m.Part): {chair}}); err != nil {
		return m, err
	}
	return m, nil
}

func failedChairs(report ensemble.Report) []string {
	var failed []string
	for _, c := range report.Chairs {
		if !c.OK {
			failed = append(failed, c.Address)
		}
	}
	return failed
}

func addressedChairs(worktree, name string, chairs loop.Chairs, members []ensemble.Member, score loop.Score) (loop.Chairs, error) {
	if _, found := ensemble.Path(name, loop.Home(), worktree); !found {
		return chairs, nil
	}
	seated := slices.Clone(members)
	for i, m := range seated {
		seated[i].Chair.Address = m.Address(name)
	}
	return ensemble.ChairsFor(score, seated)
}

func printScoreFiles(w io.Writer, files []loop.ScoreFile) {
	for _, file := range files {
		if file.Source == "repository" {
			fmt.Fprintf(w, "%s  %s  repository  %s  %.12s\n", file.Kind, file.Name, file.Path, file.SHA256)
		}
	}
}
func loadSelectedScore(s loop.Settings, worktree, name string) (loop.Settings, error) {
	loaded := s.Score
	if loaded.Score.Name != "" {
		selected := firstNonEmpty(name, "default")
		if selected != loaded.Score.Name {
			return s, fmt.Errorf("score changed from %s to %s after loading", loaded.Score.Name, selected)
		}
	} else {
		var err error
		loaded, err = loop.LoadScore(name, loop.ScoreSource{Home: loop.Home(), Worktree: worktree, AllowRepository: s.RepositoryScores})
		if err != nil {
			return s, err
		}
	}
	var err error
	s.Policy, err = s.Policy.ForScore(loaded.Score)
	if err != nil {
		return s, err
	}
	s.Score = loaded
	if loaded.Score.Rounds > 0 && !s.RoundsConfigured {
		s.Rounds = loaded.Score.Rounds
	}
	return s, nil
}

func ensembleScoreName(name, worktree string) (string, error) {
	return ensemble.ScoreName(name, loop.Home(), worktree)
}
