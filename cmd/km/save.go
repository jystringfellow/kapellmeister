package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

type saveResult struct {
	Schema   int      `json:"schema"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Replaced bool     `json:"replaced"`
	Ready    bool     `json:"ready"`
	Reason   string   `json:"reason"`
	Errors   []string `json:"errors"`
}

func saveCommand(args []string, in io.Reader, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("ensemble save", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "ensemble name")
	wt := fs.String("worktree", ".", "worktree directory")
	runID := fs.String("run", "", "run id")
	repo := fs.Bool("repository", false, "save in repository")
	replace := fs.Bool("replace", false, "replace existing file")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return &exitStatusError{code: 2, err: err}
	}
	runSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "run" {
			runSet = true
		}
	})
	if *name == "" || (runSet && (*runID == "" || fs.NArg() != 0)) || (!runSet && fs.NArg() != 1) {
		return &exitStatusError{code: 2, err: errors.New("usage: km ensemble save -name NAME [-worktree DIR] [-repository] [-replace] [-json] (FILE | - | -run RUN)")}
	}
	result := saveResult{Schema: 1, Name: *name, Errors: []string{}}
	data, label, err := saveSource(fs.Args(), *runID, runSet, in, stderr)
	if err == nil {
		_, result.Reason, err = checkEnsemble(*name, label, string(data), *wt, stderr, true)
	}
	var settings loop.Settings
	if err == nil {
		settings, err = loop.LoadSettings(loop.Home(), *wt)
	}
	if err == nil && *repo {
		_, err = ensemble.CheckPortable(*name, label, string(data), settings.Defined)
	}
	if err == nil && *repo {
		var info os.FileInfo
		info, err = os.Stat(*wt)
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("%s: worktree is not a directory", *wt)
		}
	}
	if err == nil {
		result.Path, result.Replaced, err = saveEnsemble(*name, *wt, data, *repo, *replace, settings)
	}
	if err != nil {
		result.Path = ""
		result.Replaced = false
		result.Reason = ""
		result.Errors = append(result.Errors, err.Error())
	} else {
		result.Ready = result.Reason == ""
	}
	if *asJSON {
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return e
		}
	} else if err != nil {
		fmt.Fprintln(out, plain(err.Error()))
	} else {
		fmt.Fprintf(out, "saved %s to %s\n", plain(*name), plain(result.Path))
		if result.Reason != "" {
			fmt.Fprintf(out, "not ready here: %s\n", plain(result.Reason))
		}
	}
	if err != nil {
		return &exitStatusError{code: 1}
	}
	return nil
}

func saveSource(args []string, id string, runSet bool, in io.Reader, stderr io.Writer) ([]byte, string, error) {
	if !runSet {
		label := args[0]
		if label == "-" {
			label = "<stdin>"
		}
		b, err := readTextFile("ensemble", label, args[0], in)
		return b, label, err
	}
	run, _, err := findWaitRun(id, nil, 64, 1000000, stderr)
	if err != nil {
		return nil, "", errors.New(strings.Replace(err.Error(), "wait: ", "ensemble save: ", 1))
	}
	label := filepath.Join(run.Path, "ensemble.toml")
	dir, err := os.OpenFile(run.Path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, label, err
	}
	defer dir.Close()
	data, err := readReportFile(dir, "ensemble.toml", false)
	if errors.Is(err, os.ErrNotExist) {
		if run.Ensemble != "" {
			source := ""
			if run.EnsembleSource != "" {
				source = " (" + run.EnsembleSource + ")"
			}
			return nil, label, fmt.Errorf("run %s played ensemble %q%s; only a run that played an ensemble from text keeps one", run.RunID, run.Ensemble, source)
		}
		return nil, label, fmt.Errorf("run %s seated its chairs from [chairs] and played no named ensemble; it has no ensemble text", run.RunID)
	}
	if err != nil {
		return nil, label, fmt.Errorf("%s: %w", label, err)
	}
	if len(data) > loop.ScoreSizeLimit {
		err = fmt.Errorf("%s: ensemble file is %d bytes, over the %d-byte limit", label, len(data), loop.ScoreSizeLimit)
	}
	return data, label, err
}

func saveEnsemble(name, wt string, data []byte, repo, replace bool, settings loop.Settings) (string, bool, error) {
	home := loop.Home()
	user := filepath.Join(home, ensemble.UserDir, name+".toml")
	repository := filepath.Join(wt, ensemble.RepoDir, name+".toml")
	dest, other := user, repository
	if repo {
		dest, other = repository, user
	}
	if _, ok := settings.Ensembles[name]; ok {
		return "", false, fmt.Errorf("ensemble name %q is defined by [ensembles.%s] in %s; rename or remove the table", name, name, loop.UserConfigPath(home))
	}
	if repo {
		for _, dir := range []string{filepath.Dir(filepath.Dir(repository)), filepath.Dir(repository)} {
			if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
				return "", false, fmt.Errorf("%s is a link or not a directory; a repository save writes only inside the worktree", dir)
			}
		}
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", false, err
	}
	lock, err := os.OpenFile(home, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return "", false, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return "", false, err
	}
	info, err := os.Lstat(dest)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if exists && !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s is a link or not a regular file; remove it", dest)
	}
	_, err = os.Lstat(other)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err == nil {
		if repo {
			return "", false, fmt.Errorf("%s would shadow this file; remove it or save to the user directory", other)
		}
		if !replace {
			return "", false, fmt.Errorf("saving here would shadow %s; use -replace to shadow it or -repository to change it", other)
		}
	}
	if exists && !replace {
		return "", false, fmt.Errorf("%s already exists; use -replace to replace it", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", false, err
	}
	if err := writeAtomic(dest, data, 0o644); err != nil {
		return "", false, err
	}
	return dest, exists, nil
}
