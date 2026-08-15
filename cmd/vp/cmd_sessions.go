// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var sessionsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--last", Short: "-n", Arg: "N", Help: "Number of sessions to show", Default: "10"},
	{Name: "--host", Arg: "HOST", Help: "Only sessions attributed to HOST ('unknown' = looked and could not tell, 'none' = no claim recorded)"},
	{Name: "--hosts", Help: "Report the host mix instead of the session list"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdSessions() *cli.Command {
	return &cli.Command{
		Name:        "sessions",
		Synopsis:    "vp sessions [--project P] [--last N] [--json]",
		Description: "List recent sessions for a project. Sessions are captured work units with summaries, tags, and friction scores.",
		Flags:       sessionsFlags,
		Examples: []cli.Example{
			{Cmd: "vp sessions", Comment: "List recent sessions for the current project"},
			{Cmd: "vp sessions -p myapp --last 5 --json", Comment: "Show last 5 sessions as JSON"},
			{Cmd: "vp sessions --hosts", Comment: "Which hosts produced this project's sessions"},
			{Cmd: "vp sessions --host grok --last 20", Comment: "Only the sessions Grok captured"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(sessionsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp sessions: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp sessions: could not detect project (use --project)")
				return cli.ExitUser
			}

			limit := fv.Int("--last")
			if limit == 0 {
				limit = 10
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp sessions: %v\n", err)
				return cli.ExitUser
			}

			return runSessions(vault, proj, sessionsQuery{
				limit:   limit,
				host:    fv.Get("--host"),
				hostMix: fv.Bool("--hosts"),
				asJSON:  fv.Bool("--json"),
			}, os.Stdout)
		},
	}
}

// sessionsQuery carries what the caller asked for. It exists so the host filter
// and the host report could be added without giving runSessions a fifth
// positional argument nobody can read at the call site.
type sessionsQuery struct {
	limit   int
	host    string
	hostMix bool
	asJSON  bool
}

// hostNoClaim is how a session with NO host key is named on screen and in the
// --host filter.
//
// 🔴 It is a THIRD state, not a synonym for "unknown", and keeping the two apart
// is the whole reason this reader exists. "unknown" means a writer looked for a
// host and could not establish one. "none" means no claim was recorded at all —
// the note predates the field, or came from a writer that makes no host claim,
// or was written by a build older than the hook attribution change, and all
// three are live in the vault. Collapsing them would let a reader infer a host
// from an absence, which is precisely the rule this reader was built to replace.
const hostNoClaim = "none"

// hostOf names a session's host for display and filtering, mapping the empty
// (no-claim) case onto hostNoClaim.
func hostOf(s storage.SessionMeta) string {
	if h := strings.TrimSpace(s.Host); h != "" {
		return h
	}
	return hostNoClaim
}

// entrypointOf names a session's entrypoint for display, mapping the empty case
// onto hostNoClaim for the same reason as hostOf — and note that a RECORDED
// "unknown" is a different thing again: the writer read the environment and
// found nothing in it.
func entrypointOf(s storage.SessionMeta) string {
	if e := strings.TrimSpace(s.Entrypoint); e != "" {
		return e
	}
	return hostNoClaim
}

// hostCount is one row of the --hosts report.
type hostCount struct {
	Host        string         `json:"host"`
	Sessions    int            `json:"sessions"`
	Entrypoints map[string]int `json:"entrypoints,omitempty"`
}

// hostMix tallies sessions by host, most frequent first, ties broken by name so
// the report is stable across runs.
func hostMix(sessions []storage.SessionMeta) []hostCount {
	byHost := map[string]*hostCount{}
	for _, s := range sessions {
		h := hostOf(s)
		row, ok := byHost[h]
		if !ok {
			row = &hostCount{Host: h, Entrypoints: map[string]int{}}
			byHost[h] = row
		}
		row.Sessions++
		row.Entrypoints[entrypointOf(s)]++
	}

	rows := make([]hostCount, 0, len(byHost))
	for _, row := range byHost {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sessions != rows[j].Sessions {
			return rows[i].Sessions > rows[j].Sessions
		}
		return rows[i].Host < rows[j].Host
	})
	return rows
}

// formatEntrypoints renders one row's entrypoint tally compactly, in a stable
// order.
func formatEntrypoints(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

// runSessionHosts answers "which hosts produced this project's sessions" — the
// measurement host-parity is named for, and which nothing in the tree could
// report until now. The host field had exactly one consumer (an auto-inline
// decision on the MCP path) and no reader at all, so "distinguishable in the
// vault by inspection" meant opening frontmatter by hand.
//
// It lives here rather than as a `vp check` row on purpose: check reports
// HEALTH and stays silent when healthy, and a host mix is never unhealthy.
func runSessionHosts(sessions []storage.SessionMeta, asJSON bool, out io.Writer) int {
	rows := hostMix(sessions)

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(rows)
		return cli.ExitOK
	}

	if len(rows) == 0 {
		fmt.Fprintln(out, "No sessions found.")
		return cli.ExitOK
	}

	// Size the host column to the data. A fixed width looks fine against the
	// hook's closed vocabulary and breaks the moment the MCP path contributes a
	// real one: clientInfo.name is vendor-chosen, and the live vault already
	// holds "grok-shell-vibe-palace" at 22 characters.
	width := len("HOST")
	for _, row := range rows {
		if len(row.Host) > width {
			width = len(row.Host)
		}
	}

	fmt.Fprintf(out, "%-*s %-9s %s\n", width, "HOST", "SESSIONS", "ENTRYPOINTS")
	sawNoClaim := false
	for _, row := range rows {
		if row.Host == hostNoClaim {
			sawNoClaim = true
		}
		fmt.Fprintf(out, "%-*s %-9d %s\n", width, row.Host, row.Sessions, formatEntrypoints(row.Entrypoints))
	}

	// Say out loud what an absence does and does not mean, every time one is
	// counted. A number in a table invites inference, and this is the exact
	// inference that had to be withdrawn from the plan.
	if sawNoClaim {
		fmt.Fprintf(out, "\n%q is not a host: those notes recorded no claim (they predate the field or\n"+
			"came from a writer that makes none). It is not evidence about which host ran.\n", hostNoClaim)
	}
	return cli.ExitOK
}

func runSessions(vault *storage.Vault, proj string, q sessionsQuery, out io.Writer) int {
	sessions, err := vault.ListSessions(proj, "", "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp sessions: %v\n", err)
		return cli.ExitSystem
	}

	// The host filter runs BEFORE the --last window, so "the last 10 Grok
	// sessions" means what it says rather than "whichever of the last 10
	// sessions happened to be Grok".
	if q.host != "" {
		want := strings.ToLower(strings.TrimSpace(q.host))
		kept := make([]storage.SessionMeta, 0, len(sessions))
		for _, s := range sessions {
			if strings.EqualFold(hostOf(s), want) {
				kept = append(kept, s)
			}
		}
		sessions = kept
	}

	if q.hostMix {
		return runSessionHosts(sessions, q.asJSON, out)
	}

	limit, asJSON := q.limit, q.asJSON

	// Take last N sessions.
	if len(sessions) > limit {
		sessions = sessions[len(sessions)-limit:]
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(sessions)
		return cli.ExitOK
	}

	if len(sessions) == 0 {
		if q.host != "" {
			fmt.Fprintf(out, "No sessions attributed to %q. Try 'vp sessions --hosts' for the mix.\n", q.host)
			return cli.ExitOK
		}
		fmt.Fprintln(out, "No sessions found.")
		return cli.ExitOK
	}

	fmt.Fprintf(out, "%-12s %-4s %-10s %-5s %-12s %s\n", "DATE", "ITER", "TAG", "FRIC", "HOST", "TITLE")
	for _, s := range sessions {
		title := s.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		tag := s.Tag
		if tag == "" {
			tag = "-"
		}
		fric := "-"
		if s.FrictionScore > 0 {
			fric = fmt.Sprintf("%d", s.FrictionScore)
		}
		fmt.Fprintf(out, "%-12s %-4d %-10s %-5s %-12s %s\n", s.Date, s.Iteration, tag, fric, hostOf(s), title)
	}
	return cli.ExitOK
}
