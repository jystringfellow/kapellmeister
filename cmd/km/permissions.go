package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func samePermissions(a, b cao.ProfilePermissions) bool {
	return a.PermissionMode == b.PermissionMode && a.CodexProfile == b.CodexProfile && (a.AllowedTools == nil) == (b.AllowedTools == nil) && slices.Equal(a.AllowedTools, b.AllowedTools)
}

func chairPermissions(ctx context.Context, client *cao.Client, chair loop.Chair) (string, bool, error) {
	p, err := client.ProfilePermissions(ctx, chair.Profile)
	if err != nil {
		reason := "CAO request failed"
		var httpErr *cao.HTTPError
		if errors.As(err, &httpErr) {
			reason = fmt.Sprintf("HTTP %d", httpErr.Status)
		} else if errors.Is(err, context.DeadlineExceeded) {
			reason = "CAO request timed out"
		}
		return "unavailable: " + reason, false, err
	}
	label, unguarded := loop.PermissionLabel(chair.Provider, loop.Permissions{PermissionMode: p.PermissionMode, AllowedTools: p.AllowedTools, CodexProfile: p.CodexProfile})
	return label, unguarded, nil
}

func fillChairPermissions(ctx context.Context, client *cao.Client, report *ensemble.Report, members []ensemble.Member) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	chairs := map[string]loop.Chair{}
	for _, member := range members {
		chairs[member.Address(report.Ensemble)] = member.Chair
	}
	for i := range report.Chairs {
		chair := &report.Chairs[i]
		chair.Permissions, chair.Unguarded, _ = chairPermissions(ctx, client, chairs[chair.Address])
	}
}

func printConfigPermissions(ctx context.Context, w io.Writer, client *cao.Client, s loop.Settings) (int, error) {
	chairs, err := resolveChairs(s.Defined, s.Chairs)
	if err != nil {
		return 0, err
	}
	unguarded := 0
	reviewerIndex := 0
	for _, member := range ensemble.MembersOf(chairs) {
		resolved, assignment := s.Policy.Chair(roleSteps[member.Part], member.Chair, "", 0)
		if assignment.Effort != "" && assignment.Effort != loop.InheritEffort {
			resolved = member.Chair
		}
		label, off, _ := chairPermissions(ctx, client, resolved)
		spec := s.Chairs[member.Part]
		if member.Part == "reviewer" {
			spec = loop.ChairList(spec)[reviewerIndex]
			reviewerIndex++
		}
		fmt.Fprintf(w, "  %s  %s  permissions %s\n", member.Name, spec, label)
		if off {
			unguarded++
		}
	}
	return unguarded, nil
}

func printPermissionNote(w io.Writer, unguarded int) {
	if unguarded > 0 {
		fmt.Fprintf(w, "  note: %d chair(s) run with permission prompts off; see README \"Permissions\"\n", unguarded)
	}
}
