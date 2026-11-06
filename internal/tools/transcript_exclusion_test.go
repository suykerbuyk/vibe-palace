// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// Guards for how the `authored` state surfaces through the bootstrap
// instrument, and for the two argv-producing launcher sites.
//
// G3, G4, G7 and G7c are COMPONENT guards: they drive one function with inputs
// this test supplies. G7 in particular proves only that the flag is PRODUCED;
// what proves it is CONSUMED is cmd/vp's
// TestTheLauncherArgvTurnsTranscriptsOffEndToEnd, which feeds the captured
// argv back through the production registry. G7 may not be cited for that.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// G3 — COMPONENT. TestAnAuthoredProjectRaisesNoBootstrapAlert.
//
// The alert switch is deliberately narrow — silent when there is nothing to
// say — and fires only on `stale` and `absent`, the two states that mean
// search is broken or has nothing. `authored` is a HEALTHY, CHOSEN state, like
// legacy / unbuilt / notes / partial.
//
// Q1 PATH: component. Q2 VALUE: "" vs a non-empty line.
// Q3 PRESENCE: the fixture's state really IS CoverageAuthored, which is the
// thing the mutation (adding a `case search.CoverageAuthored:` arm) acts on.
//
// Mutation: add a `case search.CoverageAuthored:` arm with a message to
// indexCoverageAlert's switch.
func TestAnAuthoredProjectRaisesNoBootstrapAlert(t *testing.T) {
	cov := search.Coverage{State: search.CoverageAuthored, Project: "p"}
	if cov.State != search.CoverageAuthored {
		t.Fatal("PRESENCE: the fixture is not in the authored state")
	}
	if msg := indexCoverageAlert(cov); msg != "" {
		t.Fatalf("the authored state raised the bootstrap alert %q. It is a deliberately chosen, "+
			"healthy state: alerting on it would nag at every session start about a configuration "+
			"the operator set on purpose", msg)
	}
	// The control: the switch still fires where it must, so a guard that
	// silenced everything would not pass here.
	if indexCoverageAlert(search.Coverage{State: search.CoverageStale, Project: "p"}) == "" {
		t.Fatal("CONTROL: stale no longer alerts; the switch was silenced wholesale")
	}
}

// G4 — COMPONENT. TestAnAuthoredProjectIsNotTalliedAsAProblem.
//
// The bootstrap's other-projects tally counts every state but `current`. Left
// alone, a host that excludes transcripts everywhere reports
// `others: {authored: 29}` at every session start, for ever.
//
// Q1 PATH: component — it drives the tally predicate over a fixture list.
// Q2 VALUE: the authored project must be ABSENT from the map while the stale
// one is present.
// Q3 PRESENCE: 🔴 at least one OTHER project in `authored`, asserted. With
// every other project `current` the map is empty either way and removing the
// authored exclusion is invisible.
//
// Mutation: remove `&& oc.State != search.CoverageAuthored` from the tally in
// coverageForProject's caller (buildIndexCoverageReport).
func TestAnAuthoredProjectIsNotTalliedAsAProblem(t *testing.T) {
	sandboxHostEnv(t)
	vault := storage.NewVault(t.TempDir())

	// The bootstrapped project, and an OTHER project seeded into `authored`.
	seedNote(t, vault, "boot")
	seedAuthoredProject(t, vault, "other")

	// Q3: the other project really IS authored. With every other project
	// `current` the tally map is empty either way and removing the authored
	// exclusion changes nothing observable.
	oc, err := coverageForProject(vault, "other")
	if err != nil {
		t.Fatal(err)
	}
	if oc.State != search.CoverageAuthored {
		t.Fatalf("PRESENCE: project \"other\" reads %q, want %q. Without an authored project in the "+
			"vault this guard cannot see the tally predicate at all: %s", oc.State, search.CoverageAuthored, oc.Reason)
	}

	rep := computeIndexCoverage(vault, "boot")
	if rep == nil {
		t.Fatal("computeIndexCoverage returned nil")
	}
	if n := rep.Others[string(search.CoverageAuthored)]; n != 0 {
		t.Fatalf("the bootstrap tally counted %d authored project(s) as a problem: %v. A host that "+
			"excludes transcripts everywhere would report others: {authored: N} at every session "+
			"start, for ever", n, rep.Others)
	}
}

// seedAuthoredProject builds the `authored` state for project through the
// PRODUCTION writers: an authored corpus, a real RebuildExplicit (which writes
// Tiers and, because this host excludes the tier and the walk loaded nothing
// archive-owned, ExcludedTiers), and an archive on disk so m > 0.
func seedAuthoredProject(t *testing.T, vault *storage.Vault, project string) {
	t.Helper()
	testutil.InitProject(t, vault.Root, project)
	dir, err := vault.SessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		"---", "session_id: 2026-05-13-aaaa0000-01", "project: " + project,
		"date: 2026-05-13", "title: an authored note", "---",
		strings.Repeat("Authored content about the ledger, the chunk store and the embed cache. ", 20),
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "2026-05-13-aaaa0000-01.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Create(archive.CreateOptions{
		Adapter: archive.InlineAdapterName, SessionID: "authored-fixture-session",
		SourceContent: []byte(`{"type":"user","text":"a transcript nobody indexes"}` + "\n"),
		VaultRoot:     vault.Root, ProjectSlug: project,
	}); err != nil {
		t.Fatalf("create fixture archive: %v", err)
	}
	// The host default: this config EXCLUDES the transcript tier.
	cfg, err := vault.LoadConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IndexTranscripts {
		t.Fatal("PRESENCE: this host's resolved config INDEXES transcripts, so no project can be authored")
	}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	defer eng.Close()
	if _, err := eng.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatalf("seed build: %v", err)
	}
}

// G7 — COMPONENT. TestTheColdSearchLauncherArgvCarriesTheFlag: the argv the
// cold-search launcher hands the spawn seam carries --no-transcripts.
//
// Q1 PATH: component. It proves the flag is PRODUCED. cmd/vp's
// TestTheLauncherArgvTurnsTranscriptsOffEndToEnd proves it is CONSUMED, and
// this guard is not evidence for that.
// Q2 VALUE: the flag present vs absent in a slice this test did not build.
// Q3 PRESENCE: 🔴 the launch seam must have FIRED EXACTLY ONCE — the launcher
// returns early on an empty project, on a live run-lock holder record and
// during the inter-run cooldown, and in any of those there is no argv to
// assert on at all.
//
// Mutation: delete "--no-transcripts" from NewRebuildLauncher's argv.
func TestTheColdSearchLauncherArgvCarriesTheFlag(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	var got [][]string
	launch := detachlaunch.LaunchFunc(func(_ string, args []string, _ string) (int, error) {
		got = append(got, append([]string(nil), args...))
		return 99, nil
	})
	NewRebuildLauncher(vault, launch)("proj")

	if len(got) != 1 {
		t.Fatalf("PRESENCE: the launch seam fired %d times, want 1. The launcher returned early "+
			"(empty project, a live holder record, or an active run cooldown) and produced no argv",
			len(got))
	}
	if !slices.Contains(got[0], "--no-transcripts") {
		t.Fatalf("the cold-search launcher's argv is %v and carries no --no-transcripts. An "+
			"AUTOMATIC trigger must be bounded by construction, not by configuration: on a host "+
			"that opted in, every cold search would re-arm the historical backlog build", got[0])
	}
}

// G7c — COMPONENT. TestRefreshIndexDoesNotForceTranscriptsOff: the EXPLICIT
// spawn site must NOT carry the flag, or the opt-in is unreachable over MCP.
//
// Q1 PATH: component. Q2 VALUE: absent vs present. Q3 PRESENCE: the same
// launch-fired assertion.
//
// Mutation: add "--no-transcripts" to vp_refresh_index's argv in
// refreshIndexHandler.
func TestRefreshIndexDoesNotForceTranscriptsOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
		not  []string
	}{
		{name: "default: neither flag, so the host config decides", not: []string{"--no-transcripts", "--transcripts"}},
		{name: "transcripts: true opts this one run in", want: []string{"--transcripts"}, not: []string{"--no-transcripts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := refreshIndexArgvFor(t, len(tc.want) > 0)
			for _, w := range tc.want {
				if !slices.Contains(argv, w) {
					t.Fatalf("argv %v does not carry %q", argv, w)
				}
			}
			for _, n := range tc.not {
				if slices.Contains(argv, n) {
					t.Fatalf("argv %v carries %q. vp_refresh_index is the EXPLICIT site: forcing the "+
						"tier off here makes the opt-in unreachable over MCP, and forcing it on makes "+
						"an ordinary refresh a days-long backlog build", argv, n)
				}
			}
		})
	}
}

// refreshIndexArgvFor drives the PRODUCTION vp_refresh_index handler and
// returns the argv it handed the spawn seam.
//
// Q3: the handler refuses a truly-empty project and refuses while a live
// holder record exists, and in either case there is no argv. The launch count
// is asserted below, so neither refusal can be mistaken for an absent flag.
func refreshIndexArgvFor(t *testing.T, transcripts bool) []string {
	t.Helper()
	const project = "refresh-proj"
	vault, eng := archivedProjectFixture(t, project, `{"type":"user","text":"hello"}`+"\n")
	rec := &recLauncher{}
	tool := RefreshIndexTool(eng, vault, rec.launch)
	params, err := json.Marshal(refreshIndexParams{Project: project, Transcripts: transcripts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("vp_refresh_index refused the fixture: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("PRESENCE: the launch seam fired %d times, want 1; there is no argv to assert on",
			rec.count())
	}
	return rec.lastArgs()
}
