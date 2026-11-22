package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type repertoireEnsemble struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Score       string `json:"score"`
	Chairs      int    `json:"chairs"`
	Ready       bool   `json:"ready"`
	Reason      string `json:"reason,omitempty"`
	Description string `json:"description,omitempty"`
}
type repertoireScore struct {
	Name        string   `json:"name"`
	Source      string   `json:"source"`
	Steps       []string `json:"steps"`
	Description string   `json:"description,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func repertoireCommand(args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("repertoire", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", ".", "repository directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return &exitStatusError{code: 2, err: err}
	}
	if fs.NArg() != 0 {
		return &exitStatusError{code: 2, err: fmt.Errorf("usage: km repertoire [-worktree DIR] [-json]")}
	}
	home := loop.Home()
	settings, err := loop.LoadSettings(home, *worktree)
	if err != nil {
		return err
	}
	source := loop.ScoreSource{Home: home, Worktree: *worktree, AllowRepository: settings.RepositoryScores}
	entries, note := loop.ListScores(source)
	if note != nil {
		fmt.Fprintf(stderr, "note: repository scores not read: %v\n", note)
	}
	scores, byName := repertoireScores(entries)
	rows := repertoireEnsembles(home, *worktree, settings, source, byName)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Schema    int                  `json:"schema"`
			Ensembles []repertoireEnsemble `json:"ensembles"`
			Scores    []repertoireScore    `json:"scores"`
		}{1, rows, scores})
	}
	value := func(s string) string {
		if s == "" {
			return "-"
		}
		return plain(s)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no ensembles")
	} else {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSOURCE\tSCORE\tCHAIRS\tREADY\tDESCRIPTION")
		for _, r := range rows {
			ready := "yes"
			if !r.Ready {
				ready = "no (" + plain(r.Reason) + ")"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", r.Name, r.Source, value(r.Score), r.Chairs, ready, value(r.Description))
		}
		w.Flush()
	}
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSOURCE\tSTEPS\tDESCRIPTION")
	for _, s := range scores {
		description := s.Description
		if s.Error != "" {
			description = "error: " + s.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Source, value(strings.Join(s.Steps, ", ")), value(description))
	}
	return w.Flush()
}

func repertoireScores(entries []loop.ScoreEntry) ([]repertoireScore, map[string]loop.ScoreEntry) {
	scores := make([]repertoireScore, 0, len(entries))
	byName := make(map[string]loop.ScoreEntry, len(entries))
	for _, entry := range entries {
		row := repertoireScore{Name: entry.Name, Source: entry.Source, Steps: []string{}, Description: entry.Score.Description}
		if entry.Err != nil {
			row.Error = entry.Err.Error()
		}
		for _, step := range entry.Score.Steps {
			row.Steps = append(row.Steps, step.Name)
		}
		scores = append(scores, row)
		if current, ok := byName[entry.Name]; !ok || current.Source == "built-in" {
			byName[entry.Name] = entry
		}
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Name == scores[j].Name {
			return scores[i].Source < scores[j].Source
		}
		return scores[i].Name < scores[j].Name
	})
	return scores, byName
}

func repertoireEnsembles(home, worktree string, settings loop.Settings, source loop.ScoreSource, scores map[string]loop.ScoreEntry) []repertoireEnsemble {
	rows := []repertoireEnsemble{}
	for _, name := range ensemble.Names(home, worktree) {
		rows = append(rows, repertoireFileEnsemble(name, home, worktree, settings, source, scores))
	}
	for name := range settings.Ensembles {
		rows = append(rows, repertoireConfigEnsemble(name, home, worktree, settings))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name == rows[j].Name {
			return rows[i].Source < rows[j].Source
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func repertoireConflict(name, home, path string) string {
	return fmt.Sprintf("ensemble %s is defined both in [ensembles.%s] of %s and in %s; rename one", name, name, loop.UserConfigPath(home), path)
}

func repertoireFileEnsemble(name, home, worktree string, settings loop.Settings, source loop.ScoreSource, scores map[string]loop.ScoreEntry) repertoireEnsemble {
	path, repo, _ := ensemble.Locate(name, home, worktree)
	row := repertoireEnsemble{Name: name, Source: "user"}
	if repo {
		row.Source = "repository"
	}
	spec, _, err := ensemble.Find(name, home, worktree, settings.Defined)
	if err == nil {
		row.Score = spec.Score
		if row.Score == "" {
			row.Score = "default"
		}
		row.Description = spec.Description
		for _, section := range spec.Sections {
			row.Chairs += len(section.Chairs)
		}
	}
	if _, ok := settings.Ensembles[name]; ok {
		row.Reason = repertoireConflict(name, home, path)
		return row
	}
	if err != nil {
		row.Reason = err.Error()
		return row
	}
	score := loop.DefaultScore()
	if row.Score != "default" {
		entry, ok := scores[row.Score]
		if !ok {
			loaded, loadErr := loop.LoadScore(row.Score, source)
			entry.Score, entry.Err = loaded.Score, loadErr
		}
		if entry.Err != nil {
			row.Reason = entry.Err.Error()
			return row
		}
		score = entry.Score
	}
	if err := spec.Ready(score, settings.Defined, settings.Agents); err != nil {
		var unavailable *ensemble.UnavailableError
		if errors.As(err, &unavailable) {
			row.Reason = fmt.Sprintf("%s.%s: none of %s is available", unavailable.Section, unavailable.Chair, strings.Join(unavailable.Agents, ", "))
		} else {
			row.Reason = strings.TrimPrefix(err.Error(), spec.Path+": ")
		}
	}
	row.Ready = row.Reason == ""
	return row
}

func repertoireConfigEnsemble(name, home, worktree string, settings loop.Settings) repertoireEnsemble {
	row := repertoireEnsemble{Name: name, Source: "config", Score: "default"}
	selected, err := settings.WithEnsemble(name)
	if err != nil {
		row.Reason = err.Error()
		return row
	}
	chairs, err := resolveChairs(selected.Defined, selected.Chairs)
	if err == nil {
		for _, role := range loop.AllRoles() {
			row.Chairs += len(chairs.Of(role))
		}
	}
	if path, ok := ensemble.Path(name, home, worktree); ok {
		row.Reason = repertoireConflict(name, home, path)
		return row
	}
	if err != nil {
		row.Reason = err.Error()
		return row
	}
	var section ensemble.Section
	for _, role := range loop.AllRoles() {
		if selected.Chairs[role] == "" {
			continue
		}
		specs := loop.ChairList(selected.Chairs[role])
		for i, spec := range specs {
			label := role
			if role == "reviewer" {
				label = loop.ReviewerName(i, len(specs))
			}
			section.Chairs = append(section.Chairs, ensemble.ChairSpec{Name: label, Part: role, Agents: []string{spec}})
		}
	}
	_, err = (ensemble.Spec{Sections: []ensemble.Section{section}}).Bind(selected.Defined, selected.Agents)
	if err != nil {
		var unavailable *ensemble.UnavailableError
		if errors.As(err, &unavailable) {
			row.Reason = fmt.Sprintf("%s: none of %s is available", unavailable.Chair, strings.Join(unavailable.Agents, ", "))
		} else {
			row.Reason = err.Error()
		}
	}
	row.Ready = row.Reason == ""
	return row
}
