package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func ensembleCommand(args []string, w io.Writer) error {
	const usage = "usage: km ensemble (list | down [-cao URL] SESSION)"
	if len(args) == 0 {
		return errors.New(usage)
	}
	dir := filepath.Join(loop.StateDir(), "ensembles")
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New(usage)
		}
		records, err := ensemble.ListStanding(dir)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			fmt.Fprintln(w, "no standing ensembles")
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SESSION\tENSEMBLE\tWORKTREE\tCHAIRS")
		for _, s := range records {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", s.Session, s.Ensemble, s.Worktree, len(s.Chairs))
		}
		return tw.Flush()
	case "down":
		fs := flag.NewFlagSet("ensemble down", flag.ContinueOnError)
		caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New(usage)
		}
		session := fs.Arg(0)
		if _, err := findStanding(dir, session); err != nil {
			return err
		}
		s, err := loop.LoadSettings(loop.Home(), "")
		if err != nil {
			return err
		}
		client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
		if err := client.DeleteSession(context.Background(), session); err != nil {
			var httpErr *cao.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
				return err
			}
			fmt.Fprintf(w, "session %s already gone\n", session)
		}
		if err := ensemble.RemoveStanding(dir, session); err != nil {
			return err
		}
		fmt.Fprintf(w, "took down %s\n", session)
		return nil
	default:
		return errors.New(usage)
	}
}

func findStanding(dir, session string) (ensemble.Standing, error) {
	records, err := ensemble.ListStanding(dir)
	if err != nil {
		return ensemble.Standing{}, err
	}
	var names []string
	for _, s := range records {
		if s.Session == session {
			return s, nil
		}
		names = append(names, s.Session)
	}
	recorded := "none recorded"
	if len(names) > 0 {
		recorded = "recorded: " + strings.Join(names, ", ")
	}
	return ensemble.Standing{}, fmt.Errorf("unknown session %q (%s)", session, recorded)
}
