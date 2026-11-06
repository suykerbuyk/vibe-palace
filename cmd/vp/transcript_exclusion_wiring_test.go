// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// WIRING guards for the transcript-exclusion flag and config path.
//
// Every guard in this file starts from an ARGV SLICE or an on-disk CONFIG FILE
// and ends at a DURABLE EFFECT — recSession rows in ledger.jsonl and
// Store.CountChunks(ArchiveOwner(sha)). None of them hands a component a value
// the test made up, because the defect they exist to catch is precisely a
// value that never arrives: the flag defined but never read, the struct field
// set but never copied into the options literal, the config key decoded into
// a layer nothing merges.
//
// 🔴 EVERY FIXTURE THAT EXERCISES THE CONFIG LAYERING SETS transcripts = true,
// the value OPPOSITE this project's default. A fixture set to false would
// agree with the default, so dropping the key from the allow-list — or losing
// the merge — would change nothing observable and the guard would stay green
// on its own mutation. Do not "simplify" it back.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// dispatchVP runs argv through the PRODUCTION command registry — the same
// cli.NewRegistry + registerAll that main() builds — so a flag that is not
// defined, not parsed, or not carried into the options literal shows up here
// as a different durable effect, not as a compile error in a hand-built args
// struct.
func dispatchVP(t *testing.T, argv ...string) int {
	t.Helper()
	info := cli.BuildInfo{}
	reg := cli.NewRegistry(info)
	registerAll(reg, info)
	reg.SetOutput(os.Stderr, os.Stderr)
	return reg.Dispatch(argv)
}

// ingestedArchives is the durable effect every guard here asserts on: the
// number of archive-owned chunks the store holds for sha, and whether the
// session has a live ledger row.
func ingestedArchives(t *testing.T, vault, project, sha string) (chunks int, live bool) {
	t.Helper()
	return ingestedArchivesOf(t, vault, project, "S", sha)
}

// ingestedArchivesOf is ingestedArchives for a named session. Two projects in
// one pass must use DIFFERENT session ids: drainArchiveOf derives the
// transcript body from the session id, so a shared id gives both archives the
// SAME source_sha256 — and a run's memo refuses an archive it has already
// attempted in that run, keyed on exactly that hash. With a shared id the
// second project is skipped by the memo whatever its config says, and the
// guard is inert.
func ingestedArchivesOf(t *testing.T, vault, project, session, sha string) (chunks int, live bool) {
	t.Helper()
	st, err := indexstore.ReadStore(storage.NewVault(vault), project)
	if err != nil {
		t.Fatal(err)
	}
	chunks = st.CountChunks(indexstore.ArchiveOwner(sha))
	if l := st.Ledger(); l != nil {
		if rec, ok := l.Session(session); ok && rec.State == indexstore.StateLive {
			live = true
		}
	}
	return chunks, live
}

// admissibleArchiveFixture is the Q3 PRECONDITION every guard in this file
// consumes: at least one archive on disk that ingest.decide would ADMIT absent
// the exclusion arm. It returns the vault root and the archive's
// source_sha256.
//
// 🔴 It ASSERTS the precondition rather than assuming it. Without an archive
// decide would take, "nothing was ingested" is true of a correct run and of a
// broken one alike, and every assertion below is vacuous. The assertion is
// empirical: the control run at the bottom of each guard (config true, no
// flag) must ingest it.
func admissibleArchiveFixture(t *testing.T, project string) (vault, sha string) {
	t.Helper()
	vault = setupTestVaultEnv(t)
	stubVaultEmbedder(t, embedder.NewMock(384))
	m := drainArchiveOf(t, vault, project, "S", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	if m.SourceSHA256 == "" {
		t.Fatal("precondition: the fixture archive has no source_sha256, so decide would refuse it on the hashless arm")
	}
	if m.SourceBytes <= 0 {
		t.Fatalf("precondition: the fixture archive reports %d source bytes", m.SourceBytes)
	}
	// Not already ledgered: no ingest has run in this fixture yet.
	if n, _ := ingestedArchives(t, vault, project, m.SourceSHA256); n != 0 {
		t.Fatalf("precondition: the fixture already holds %d archive-owned chunks", n)
	}
	return vault, m.SourceSHA256
}

// writeHostProjectTranscripts writes the HOST-LOCAL per-project config file
// (storage.HostProjectConfigPath) with [index] transcripts = on.
func writeHostProjectTranscripts(t *testing.T, project string, on bool) string {
	t.Helper()
	path, err := storage.HostProjectConfigPath(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[meta]\nversion_major = 1\nversion_minor = 0\nkind = \"project\"\n\n[index]\ntranscripts = " +
		map[bool]string{true: "true", false: "false"}[on] + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Q3: the file must EXIST and parse, or LoadConfig skips the host-local
	// layer entirely and neither the allow-list nor the pointer merge ever
	// runs — the mutation would then be invisible.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: the host-local project config was not written: %v", err)
	}
	cfg, err := storage.NewVault("").LoadConfig(project)
	if err != nil {
		t.Fatalf("precondition: the host-local project config does not parse: %v", err)
	}
	if cfg.IndexTranscripts != on {
		t.Fatalf("precondition: the host-local file set transcripts = %v and LoadConfig resolved %v; "+
			"the layer did not reach the resolved config at all, so nothing below this line is meaningful",
			on, cfg.IndexTranscripts)
	}
	return path
}

// W1 — WIRING. TestTheTranscriptsFlagBeatsTheConfigFromArgv drives all six of
// the plan's wiring edits from a full argv slice through the production
// registry, and asserts the DURABLE effect.
//
// Q1 PATH: wiring. cli.NewRegistry + registerAll + Dispatch(argv), with only
// newVaultEmbedder stubbed.
// Q2 VALUE: arms (a), (b) and (c) each pair a config value with a flag (or no
// flag) so that a broken link produces the OPPOSITE outcome. Arm (d) is a
// CONTROL — config false, no flag — and is NOT evidence of anything: it agrees
// with the default, so every broken wiring passes it.
// Q3 PRESENCE: admissibleArchiveFixture, asserted.
//
// Mutations, each its own run, each reddening the arm(s) named:
//   - drop the two cli.FlagDefs                  -> (a), (b) red (ParseFlags errors)
//   - drop indexRebuildArgs.transcripts from the Run body literal -> (a), (b)
//   - drop Transcripts: a.transcripts from the RebuildOptions literal -> (a), (b)
//   - drop the resolveTranscripts call in Rebuild -> (c)
//   - re-introduce an overwrite of o.Transcripts from d.Config -> (a)
//   - make the flag the only input (ignore the config) -> (c)
func TestTheTranscriptsFlagBeatsTheConfigFromArgv(t *testing.T) {
	for _, arm := range []struct {
		name     string
		config   bool
		flags    []string
		ingested bool
		control  bool
	}{
		{name: "a/config-false-flag-on", config: false, flags: []string{"--transcripts"}, ingested: true},
		{name: "b/config-true-flag-off", config: true, flags: []string{"--no-transcripts"}, ingested: false},
		{name: "c/config-true-no-flag", config: true, ingested: true},
		{name: "d/config-false-no-flag", config: false, ingested: false, control: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			const project = "p"
			vault, sha := admissibleArchiveFixture(t, project)
			setHostGlobalTranscripts(t, vault, arm.config)

			argv := append([]string{"index", "rebuild", project, "--vault-root", vault}, arm.flags...)
			if code := dispatchVP(t, argv...); code != cli.ExitOK {
				t.Fatalf("vp %v: exit %d", argv, code)
			}

			chunks, live := ingestedArchives(t, vault, project, sha)
			switch {
			case arm.ingested && chunks == 0:
				t.Fatalf("argv %v with [index] transcripts = %v ingested NOTHING (chunks=%d live=%v); "+
					"one of the six wiring edits dropped the flag or the config on the floor",
					argv, arm.config, chunks, live)
			case arm.ingested && !live:
				t.Fatalf("argv %v wrote %d archive chunks but no live ledger row", argv, chunks)
			case !arm.ingested && chunks != 0:
				msg := "the exclusion did not bind"
				if arm.control {
					msg = "the CONTROL arm ingested; the host default is not exclusion"
				}
				t.Fatalf("argv %v with [index] transcripts = %v ingested %d archive chunks: %s",
					argv, arm.config, chunks, msg)
			}
		})
	}
}

// W2 — WIRING. TestTheLauncherArgvTurnsTranscriptsOffEndToEnd takes the argv
// the PRODUCTION cold-search launcher emits, verbatim, and feeds it back
// through Dispatch against a host config that says transcripts = true.
//
// Q1 PATH: wiring. tools.NewRebuildLauncher produces the argv; the production
// registry consumes it. G7 proves only that the flag is PRODUCED; this is what
// proves it is CONSUMED, and G7 alone may not be cited for this claim.
// Q2 VALUE: the host config is TRUE. With false the config and the flag agree
// and deleting the flag changes nothing.
// Q3 PRESENCE: two preconditions, both asserted — the launch seam fired
// exactly once (NewRebuildLauncher returns early on an empty project, a live
// holder record and an active run cooldown, and then there is no argv at all),
// and admissibleArchiveFixture's ingestible archive.
//
// Mutation: delete "--no-transcripts" from internal/tools/system_tools.go's
// NewRebuildLauncher argv. The captured argv then resolves the config's true
// and the archive is ingested.
func TestTheLauncherArgvTurnsTranscriptsOffEndToEnd(t *testing.T) {
	const project = "p"
	vault, sha := admissibleArchiveFixture(t, project)
	setHostGlobalTranscripts(t, vault, true)

	var captured [][]string
	launch := detachlaunch.LaunchFunc(func(_ string, args []string, _ string) (int, error) {
		captured = append(captured, append([]string(nil), args...))
		return 4321, nil
	})
	tools.NewRebuildLauncher(storage.NewVault(vault), launch)(project)

	// Q3: without a fired launch there is no argv to assert on, and every
	// assertion below would pass over an empty slice.
	if len(captured) != 1 {
		t.Fatalf("the launch seam fired %d times, want exactly 1 — the launcher returned early "+
			"(empty project, a live holder record, or an active run cooldown) and produced no argv",
			len(captured))
	}
	argv := captured[0]

	if code := dispatchVP(t, argv...); code != cli.ExitOK {
		t.Fatalf("vp %v: exit %d", argv, code)
	}
	if chunks, _ := ingestedArchives(t, vault, project, sha); chunks != 0 {
		t.Fatalf("the cold-search launcher's own argv %v ingested %d archive chunks against a host "+
			"config of transcripts = true. An AUTOMATIC trigger must be bounded by construction: "+
			"without --no-transcripts in that argv, killing a backlog build does not stop it, because "+
			"the next cold search spawns another", argv, chunks)
	}
}

// W2b — WIRING. The same for the drain's rebuild-want consumer, the second
// automatic spawn site. Same three answers as W2; mutation: delete
// "--no-transcripts" from spawnRebuildFor in cmd/vp/cmd_drain_archives.go.
func TestTheDrainSpawnerArgvTurnsTranscriptsOffEndToEnd(t *testing.T) {
	const project = "p"
	vault, sha := admissibleArchiveFixture(t, project)
	setHostGlobalTranscripts(t, vault, true)

	var captured [][]string
	prev := drainRebuildLaunch
	drainRebuildLaunch = func(_ string, args []string, _ string) (int, error) {
		captured = append(captured, append([]string(nil), args...))
		return 4321, nil
	}
	defer func() { drainRebuildLaunch = prev }()
	spawnRebuildFor(storage.NewVault(vault))(project)

	if len(captured) != 1 {
		t.Fatalf("the launch seam fired %d times, want exactly 1 — spawnRebuildFor produced no argv", len(captured))
	}
	argv := captured[0]

	if code := dispatchVP(t, argv...); code != cli.ExitOK {
		t.Fatalf("vp %v: exit %d", argv, code)
	}
	if chunks, _ := ingestedArchives(t, vault, project, sha); chunks != 0 {
		t.Fatalf("spawnRebuildFor's own argv %v ingested %d archive chunks against a host config of "+
			"transcripts = true; the second automatic spawn site is unbounded", argv, chunks)
	}
}

// W4 — WIRING. TestTheHostLocalConfigFileReachesTheIngestDecision writes real
// TOML into storage.HostProjectConfigPath(project) and drives a no-flag
// rebuild from it.
//
// Q1 PATH: wiring. A FILE on disk, LoadConfig's host-local layer, Dispatch,
// and a durable ingest. G1/G1c hand the engine a Config literal and prove the
// predicate; they prove nothing about whether a config file ever reaches it.
// Q2 VALUE: the file says transcripts = TRUE, the value opposite the default,
// and the guard asserts the OPPOSITE outcome (archives ingested, state not
// authored). With false it would agree with the default and the mutation would
// be invisible — this is exactly how revision 3's version of this guard stayed
// green on its own mutation.
// Q3 PRESENCE: the file must exist and parse (asserted inside
// writeHostProjectTranscripts, which also asserts the value RESOLVED), plus an
// admissible archive.
//
// Mutation: remove the Index field from storage.hostProjectLayer. "THE TYPE IS
// THE ALLOW-LIST", so the key is then silently ignored, the resolved value
// falls back to the default false, nothing is ingested — and the precondition
// assertion inside writeHostProjectTranscripts reddens first, naming it.
func TestTheHostLocalConfigFileReachesTheIngestDecision(t *testing.T) {
	const project = "p"
	vault, sha := admissibleArchiveFixture(t, project)
	setHostGlobalTranscripts(t, vault, false) // the layer BELOW says exclude
	writeHostProjectTranscripts(t, project, true)

	argv := []string{"index", "rebuild", project, "--vault-root", vault}
	if code := dispatchVP(t, argv...); code != cli.ExitOK {
		t.Fatalf("vp %v: exit %d", argv, code)
	}
	chunks, live := ingestedArchives(t, vault, project, sha)
	if chunks == 0 || !live {
		t.Fatalf("a host-local [index] transcripts = true did NOT reach the ingest decision: "+
			"chunks=%d live=%v. The host-local layer is the per-project opt-in; if the key is not in "+
			"hostProjectLayer's allow-list it is silently ignored and only a warning line shows for it",
			chunks, live)
	}
}

// W4b — WIRING. TestAPerProjectOptInDoesNotLeakToAnotherProject: a second
// project with no host-local file stays excluded under the same host global.
//
// Q3 PRESENCE: the second project must itself hold an admissible archive, or
// "stays excluded" is unobservable; asserted by ingesting it in a control run
// at the end.
//
// Mutation: make applyHostProjectLayer write into a tomlConfig shared across
// LoadConfig calls (e.g. memoize the decoded layer by nothing but the binary),
// so project q inherits p's override.
func TestAPerProjectOptInDoesNotLeakToAnotherProject(t *testing.T) {
	vault := setupTestVaultEnv(t)
	stubVaultEmbedder(t, embedder.NewMock(384))
	setHostGlobalTranscripts(t, vault, false)

	mp := drainArchiveOf(t, vault, "p", "S", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	mq := drainArchiveOf(t, vault, "q", "S", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	writeHostProjectTranscripts(t, "p", true)

	// q has NO host-local file, so it resolves the global's false.
	cfgQ, err := storage.NewVault(vault).LoadConfig("q")
	if err != nil {
		t.Fatal(err)
	}
	if cfgQ.IndexTranscripts {
		t.Fatal("project q has no host-local file and resolved transcripts = true: " +
			"p's per-project override leaked into another project's resolved config")
	}

	for _, p := range []string{"p", "q"} {
		argv := []string{"index", "rebuild", p, "--vault-root", vault}
		if code := dispatchVP(t, argv...); code != cli.ExitOK {
			t.Fatalf("vp %v: exit %d", argv, code)
		}
	}
	if n, _ := ingestedArchives(t, vault, "p", mp.SourceSHA256); n == 0 {
		t.Fatal("the opted-in project p ingested nothing; the fixture proves nothing about q")
	}
	if n, _ := ingestedArchives(t, vault, "q", mq.SourceSHA256); n != 0 {
		t.Fatalf("project q, with no host-local file, ingested %d archive chunks under a host global "+
			"of transcripts = false: the per-project opt-in leaked", n)
	}
}

// twoProjectVault builds a vault with projects p and q, each holding ONE
// admissible archive, and a host GLOBAL config of transcripts = false. It
// returns the vault root and the two archives' source_sha256s.
func twoProjectVault(t *testing.T) (vault, shaP, shaQ string) {
	t.Helper()
	vault = setupTestVaultEnv(t)
	stubVaultEmbedder(t, embedder.NewMock(384))
	setHostGlobalTranscripts(t, vault, false)
	mp := drainArchiveOf(t, vault, "p", "SP", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	mq := drainArchiveOf(t, vault, "q", "SQ", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	if mp.SourceSHA256 == "" || mq.SourceSHA256 == "" {
		t.Fatal("precondition: a fixture archive has no source_sha256, so decide would refuse it " +
			"on the hashless arm whatever the transcript setting says")
	}
	if mp.SourceSHA256 == mq.SourceSHA256 {
		t.Fatal("precondition: p's and q's archives hash the same, so a run's memo refuses the " +
			"second as already attempted and no config answer is observable for it")
	}
	return vault, mp.SourceSHA256, mq.SourceSHA256
}

// G-4 — WIRING. TestAPassOverTwoProjectsResolvesEachProjectsOwnConfig.
//
// A pass SPANS PROJECTS — `vp index rebuild --all` walks every target, and a
// drain visits the triggering project and then the vault's others — while
// storage.hostProjectLayer is a PER-PROJECT, host-local config layer a project
// may use to opt in. Resolving the config once for the whole pass therefore
// carries one project's answer onto every other project it visits. Under
// --all it is worse still: the caller loads the config for the EMPTY project,
// which has no host-local layer at all, so a single resolution there ignores
// every per-project override in the vault.
//
// 🔴 The ARGV FLAG half is NOT per project and must not become so. When
// --transcripts or --no-transcripts was passed, Deps.transcriptsFor returns it
// without reading any config, for every project. The operator said so for this
// invocation. Only the CONFIG FALLBACK is per project.
//
// Q1 PATH: wiring. Two real host-local config FILES, the production registry's
// Dispatch over a full argv slice, and the durable effect (archive-owned
// chunks and live ledger rows) asserted per project.
//
// Q2 VALUE: 🔴 the two projects DISAGREE — p's host-local file says `true`,
// the value OPPOSITE the default, and q has no file so it resolves the
// global's `false`. With both projects on the same value the mutation changes
// nothing and this guard is inert.
//
// Q3 PRESENCE: 🔴 q must hold an archive the pass WOULD ingest, or "q ingested
// nothing" is true of a correct run and of a broken one alike. The control
// sub-test proves it empirically: with q opted in too, q ingests.
//
// Mutation: restore the single per-pass resolution — hoist
// `d.transcriptsFor(o.Transcripts, p)` back out of Rebuild's target loop into
// one `resolveTranscripts(o.Transcripts, d.Config)` above it.
func TestAPassOverTwoProjectsResolvesEachProjectsOwnConfig(t *testing.T) {
	t.Run("rebuild --all: the projects disagree and each gets its own answer", func(t *testing.T) {
		vault, shaP, shaQ := twoProjectVault(t)
		writeHostProjectTranscripts(t, "p", true) // OPPOSITE the global's false

		argv := []string{"index", "rebuild", "--all", "--vault-root", vault}
		if code := dispatchVP(t, argv...); code != cli.ExitOK {
			t.Fatalf("vp %v: exit %d", argv, code)
		}

		nP, liveP := ingestedArchivesOf(t, vault, "p", "SP", shaP)
		if nP == 0 || !liveP {
			t.Fatalf("project p, whose host-local config says transcripts = true, ingested NOTHING "+
				"(chunks=%d live=%v). Under --all the caller resolves the config of the EMPTY "+
				"project, which carries no host-local layer, so one resolution for the whole pass "+
				"ignores every per-project override in the vault", nP, liveP)
		}
		if nQ, _ := ingestedArchivesOf(t, vault, "q", "SQ", shaQ); nQ != 0 {
			t.Fatalf("project q, which has NO host-local file and resolves the global's "+
				"transcripts = false, ingested %d archive chunks in the same pass as p. One "+
				"project's opt-in leaked onto another: the pass resolved one config for every "+
				"project it visited", nQ)
		}
	})

	// Q3 PRESENCE, empirical: the SAME fixture shape with q opted in too must
	// ingest q. Without this, "q ingested nothing" above could be an
	// inadmissible archive rather than a respected config.
	t.Run("control: q opted in too, so q's archive IS admissible", func(t *testing.T) {
		vault, shaP, shaQ := twoProjectVault(t)
		writeHostProjectTranscripts(t, "p", true)
		writeHostProjectTranscripts(t, "q", true)

		argv := []string{"index", "rebuild", "--all", "--vault-root", vault}
		if code := dispatchVP(t, argv...); code != cli.ExitOK {
			t.Fatalf("vp %v: exit %d", argv, code)
		}
		if n, _ := ingestedArchivesOf(t, vault, "p", "SP", shaP); n == 0 {
			t.Fatal("CONTROL: p ingested nothing even when opted in")
		}
		if n, _ := ingestedArchivesOf(t, vault, "q", "SQ", shaQ); n == 0 {
			t.Fatal("PRESENCE: q's archive is not admissible even when q is opted in, so the " +
				"'q ingested nothing' assertion above proves nothing about the config")
		}
	})

	// 🔴 The FLAG half, unchanged by this fix: an explicit --no-transcripts
	// binds EVERY project, including the one whose host-local config opted in.
	// A per-project flag would be R1 reopened.
	t.Run("the argv flag is NOT per project: --no-transcripts binds both", func(t *testing.T) {
		vault, shaP, shaQ := twoProjectVault(t)
		writeHostProjectTranscripts(t, "p", true)

		argv := []string{"index", "rebuild", "--all", "--vault-root", vault, "--no-transcripts"}
		if code := dispatchVP(t, argv...); code != cli.ExitOK {
			t.Fatalf("vp %v: exit %d", argv, code)
		}
		if n, _ := ingestedArchivesOf(t, vault, "p", "SP", shaP); n != 0 {
			t.Fatalf("an explicit --no-transcripts did not bind project p (%d chunks), whose "+
				"host-local config says true. The flag is the operator's word for THIS "+
				"invocation and must beat every project's config", n)
		}
		if n, _ := ingestedArchivesOf(t, vault, "q", "SQ", shaQ); n != 0 {
			t.Fatalf("--no-transcripts did not bind project q (%d chunks)", n)
		}
	})
}

// G-4b — WIRING, the DRAIN pass. `vp drain archives --project p` visits p and
// then the vault's OTHER projects (projectOrder), so the same leak lives on
// run.go's per-project loops. runPending resolves each project for itself.
//
// Q3 PRESENCE: q's ledger is created BEFORE its archive exists, so that
// archive is PENDING rather than part of q's baseline set — otherwise decide
// refuses it on the baseline arm and the guard is inert whatever the config
// says. The control sub-test proves the fixture admissible empirically.
//
// Mutation: hoist the resolution out of runPending back into Run/RunHeld.
func TestADrainPassResolvesEachProjectsOwnConfig(t *testing.T) {
	run := func(t *testing.T, qOptIn bool) (nP, nQ int) {
		t.Helper()
		vault := setupTestVaultEnv(t)
		stubVaultEmbedder(t, embedder.NewMock(384))
		setHostGlobalTranscripts(t, vault, false)

		// q's ledger first, with no archive on disk: its archive is then
		// PENDING, not baseline, so decide can admit it.
		if err := os.MkdirAll(filepath.Join(vault, "Projects", "q"), 0o755); err != nil {
			t.Fatal(err)
		}
		emptyLedger(t, vault, "q")
		mq := drainArchiveOf(t, vault, "q", "SQ", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
		mp := drainArchiveOf(t, vault, "p", "SP", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))

		writeHostProjectTranscripts(t, "p", true)
		if qOptIn {
			writeHostProjectTranscripts(t, "q", true)
		}

		if code := runDrainArchives(context.Background(), vault, "p", mp.SourceSHA256, os.Stderr); code != cli.ExitOK {
			t.Fatalf("vp drain archives: exit %d", code)
		}
		nP, _ = ingestedArchivesOf(t, vault, "p", "SP", mp.SourceSHA256)
		nQ, _ = ingestedArchivesOf(t, vault, "q", "SQ", mq.SourceSHA256)
		return nP, nQ
	}

	// Control first: with q opted in, the drain DOES take q's archive, so the
	// fixture's q is admissible and the assertion below is not vacuous.
	if nP, nQ := run(t, true); nP == 0 || nQ == 0 {
		t.Fatalf("PRESENCE: with both projects opted in the drain ingested p=%d q=%d chunks; "+
			"without an admissible archive in q the leak assertion below proves nothing", nP, nQ)
	}

	nP, nQ := run(t, false)
	if nP == 0 {
		t.Fatal("project p, opted in by its host-local config, ingested nothing in the drain pass")
	}
	if nQ != 0 {
		t.Fatalf("project q, which resolves the global's transcripts = false, ingested %d archive "+
			"chunks in the same drain pass as the opted-in p: the pass resolved one config for "+
			"every project it visited", nQ)
	}
}

// emptyLedger creates a project's ledger with an empty baseline set, the way a
// host that ran an ingest pass before any archive arrived would have.
func emptyLedger(t *testing.T, vault, project string) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), storage.NewVault(vault), project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Release()
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// liveOwnedArchiveChunks counts the archive-owned chunks Store.Chunks(true)
// yields for project — exactly the set the engine's exclusion belt acts on,
// and exactly what search would serve.
func liveOwnedArchiveChunks(t *testing.T, vault, project string) int {
	t.Helper()
	st, err := indexstore.ReadStore(storage.NewVault(vault), project)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range st.Chunks(true) {
		if c.Selected != nil && c.Selected.Kind == indexstore.OwnerArchive {
			n++
		}
	}
	return n
}

func completenessOf(t *testing.T, vault, project string) indexstore.Completeness {
	t.Helper()
	rec, err := indexstore.ReadCompleteness(storage.NewVault(vault), project)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// C1 — WIRING. TestTheENGINEIndexesEachProjectUnderItsOwnConfig.
//
// 🔴 THIS IS THE HALF THE G-4 GUARD DID NOT COVER.
// TestAPassOverTwoProjectsResolvesEachProjectsOwnConfig asserts per-project
// INGESTION — ledger rows and stored chunks — and passes with a single
// pass-level ENGINE config, because ingest and the engine are two separate
// resolutions of the same setting. The engine decides, per project, which
// archive-owned chunks the BUILT INDEX serves: walkCorpus's exclusion belt,
// writeCompletenessLocked's ExcludedTiers record and CoverageState's
// `authored` predicate all read it. One engine config for a whole pass
// therefore judges every project under one project's setting, in both
// directions:
//
//   - global true, project q locally false  -> q's stored archive chunks are
//     indexed anyway;
//   - global false, project p locally true  -> p's archive chunks are skipped
//     from the built index.
//
// Q1 PATH: wiring. Real host-local config FILES, the production registry's
// Dispatch over a full argv slice, and the DURABLE record the production
// writer left — `completeness.json`'s ExcludedTiers and Tiers — read per
// project. Nothing here is a value the test handed a component.
//
// Q2 VALUE: 🔴 the two PROJECTS disagree, and the guard runs BOTH global
// polarities. Under --all the caller loads the EMPTY project's config, so a
// single pass-level resolution lands on the global: with the global false the
// p assertion reds, with it true the q assertion reds. Running one polarity
// only would leave half the mutation invisible.
//
// Q3 PRESENCE: 🔴 both projects must hold LIVE-OWNED ARCHIVE CHUNKS before the
// pass, asserted. The belt only acts while VISITING such a chunk inside
// st.Chunks(true); with none, q's ExcludedTiers is set trivially (archiveOwned
// is 0 because there was nothing to count) and p's absence of archive chunks
// would read the same under either config. The fixture ingests them through a
// real `vp index rebuild <project> --transcripts` first.
//
// Mutation: revert the engine to the single pass-level config — drop
// `search.WithConfigFor(v.LoadConfig)` from cmd_index_rebuild.go's NewEngine,
// or make Engine.indexTranscripts return e.config.IndexTranscripts.
func TestTheENGINEIndexesEachProjectUnderItsOwnConfig(t *testing.T) {
	for _, arm := range []struct {
		name   string
		global bool
		localP *bool // nil: no host-local file for p
		localQ *bool
	}{
		{name: "global off, p opted in locally", global: false, localP: ptrTo(true)},
		{name: "global on, q opted out locally", global: true, localQ: ptrTo(false)},
	} {
		t.Run(arm.name, func(t *testing.T) {
			vault, shaP, shaQ := twoProjectVault(t)

			// Warm-up through PRODUCTION: both projects really ingest their
			// archive, so both stores hold live-owned archive chunks.
			//
			// 🔴 --no-embed, so those chunks have NO CACHED VECTOR. Since the
			// 2026-11-06 ruling the belt skips only a VECTORLESS archive
			// chunk — a vectored one is loaded whatever the setting says, so a
			// warm-with-vectors fixture would make this guard inert.
			//
			// 🔴 ONE --all run, not one run per project. A per-project run
			// makes the OTHER project a step-7 rescan target, and that
			// rescan's automatic-scope repair pass re-embeds a --no-embed
			// leftover's missing vectors (rebuild.go says so in as many
			// words). Warming them separately therefore left p vectored and
			// this guard inert; --all makes both of them targets, which the
			// rescan excludes.
			warm := []string{"index", "rebuild", "--all", "--vault-root", vault, "--transcripts", "--no-embed"}
			if code := dispatchVP(t, warm...); code != cli.ExitOK {
				t.Fatalf("warm-up vp %v: exit %d", warm, code)
			}
			if n, _ := ingestedArchivesOf(t, vault, "p", "SP", shaP); n == 0 {
				t.Fatal("warm-up: p ingested nothing")
			}
			if n, _ := ingestedArchivesOf(t, vault, "q", "SQ", shaQ); n == 0 {
				t.Fatal("warm-up: q ingested nothing")
			}
			// Q3, asserted: the belt's input exists in BOTH stores — a
			// live-owned archive chunk with NO vector, which is what the belt
			// now acts on.
			for _, p := range []string{"p", "q"} {
				if n := liveOwnedArchiveChunks(t, vault, p); n == 0 {
					t.Fatalf("PRESENCE: project %s holds no live-owned archive chunk, so the "+
						"engine's exclusion belt is never reached for it and this guard would "+
						"pass under either config", p)
				}
				if n := vectoredArchiveChunks(t, vault, p); n != 0 {
					t.Fatalf("PRESENCE: project %s's %d archive-owned chunk(s) already have cached "+
						"vectors, so the belt loads them whatever the config says and this guard "+
						"is inert", p, n)
				}
			}

			// Now make the two projects DISAGREE.
			setHostGlobalTranscripts(t, vault, arm.global)
			if arm.localP != nil {
				writeHostProjectTranscripts(t, "p", *arm.localP)
			}
			if arm.localQ != nil {
				writeHostProjectTranscripts(t, "q", *arm.localQ)
			}
			if got := resolvedTranscripts(t, vault, "p"); !got {
				t.Fatalf("PRESENCE: project p resolves transcripts = %v, want true", got)
			}
			if got := resolvedTranscripts(t, vault, "q"); got {
				t.Fatalf("PRESENCE: project q resolves transcripts = %v, want false — the two "+
					"projects must DISAGREE or the mutation changes nothing", got)
			}

			argv := []string{"index", "rebuild", "--all", "--vault-root", vault}
			if code := dispatchVP(t, argv...); code != cli.ExitOK {
				t.Fatalf("vp %v: exit %d", argv, code)
			}

			// The ENGINE's own record of what each project's walk LOADED.
			recP, recQ := completenessOf(t, vault, "p"), completenessOf(t, vault, "q")

			// 🔴 THE OBSERVABLE IS Tiers["local"], NOT ExcludedTiers.
			// writeCompletenessLocked sets ExcludedTiers only when
			// archiveOwned == 0, and archiveOwned is counted BEFORE the belt
			// skips (that is G1d's whole point) — so with archive chunks
			// present, as this fixture requires, BOTH projects leave
			// ExcludedTiers empty. What differs is whether the walk LOADED
			// them: a loaded archive chunk is a local-tier hit and records the
			// `local` tier; a skipped one records nothing.
			if _, ok := recP.Tiers["local"]; !ok {
				t.Fatalf("project p, whose config says transcripts = true, recorded NO local tier "+
					"although its store holds archive-owned chunks: tiers=%v. The engine skipped "+
					"them, so it judged p under another project's config — ingest resolving per "+
					"project is only half the fix", recP.Tiers)
			}
			if _, ok := recQ.Tiers["local"]; ok {
				t.Fatalf("project q, whose config says transcripts = false, recorded a local tier "+
					"(tiers=%v) although every local-tier chunk it has is archive-owned. The "+
					"engine INDEXED q's transcripts under another project's setting", recQ.Tiers)
			}
			// q is the in-between state: excluded by config, chunks still
			// live-owned on disk. ExcludedTiers must stay EMPTY there, or the
			// `authored` state would claim an index that still holds them.
			if slices.Contains(recQ.ExcludedTiers, "transcripts") {
				t.Fatalf("project q recorded ExcludedTiers=%v while %d archive-owned chunks are "+
					"still live-owned in its store: archiveOwned must be counted BEFORE the belt "+
					"skips, or `authored` becomes reachable over an index that still holds them",
					recQ.ExcludedTiers, liveOwnedArchiveChunks(t, vault, "q"))
			}
			// And the chunks are still THERE — excluded from the index, not
			// deleted. The purge is what reclaims them.
			if n := liveOwnedArchiveChunks(t, vault, "q"); n == 0 {
				t.Fatal("q's archive-owned chunks were removed, not merely left out of the index")
			}
		})
	}
}

// ptrTo is the tri-state helper these fixtures need: nil means "no
// host-local file for this project".
func ptrTo(b bool) *bool { return &b }

// resolvedTranscripts is what storage.LoadConfig resolves for project on this
// host — the value both ingest and the engine must each read per project.
func resolvedTranscripts(t *testing.T, vault, project string) bool {
	t.Helper()
	cfg, err := storage.NewVault(vault).LoadConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.IndexTranscripts
}

// D1 — WIRING. TestTheTranscriptsFlagReachesTheSearchEngine.
//
// 🔴 R1's PRECEDENCE RULE APPLIES TO THE ENGINE TOO. The argv flag is
// authoritative and the config is only the default when no flag was passed —
// and `rebuildProject` skips the archive INGEST pass for `--no-transcripts`
// and then still runs the ENGINE rebuild. If the engine re-reads config there,
// the flag never reaches the half that decides what the built index serves:
//
//   - warm store + config ON + `--no-transcripts` -> the engine RELOADS the
//     archive-owned chunks the flag said to leave out;
//   - config OFF + `--transcripts` -> ingest admits the archives and the
//     engine then SKIPS them, so the run that was asked for them builds an
//     index without them.
//
// Q1 PATH: wiring. A full argv slice through the production registry's
// Dispatch, against a real host config file, asserted on the durable record
// the PRODUCTION writer left (`completeness.json`).
//
// Q2 VALUE: both polarities, and in each the FLAG is the OPPOSITE of the
// config — which is the only arrangement under which the two can be told
// apart at all.
//
// Q3 PRESENCE: 🔴 A WARM STORE IS LOAD-BEARING. The `--no-transcripts` arm
// asserts the local tier is absent; with no pre-existing archive-owned chunk
// that is trivially true, because the skipped ingest pass leaves nothing for
// the walk to load either way. Both arms ingest through a real
// `vp index rebuild <p> --transcripts` first and assert the chunks are present
// in `Store.Chunks(true)` before the measured run.
//
// Mutation: revert `Engine.indexTranscripts` to return `cfg.IndexTranscripts`,
// ignoring the pass's flag. Both arms redden.
//
// 🔴 WHAT THIS GUARD ASSERTED BEFORE THE 2026-11-06 RULING, AND WHAT IT
// ASSERTS NOW. Before: the `--no-transcripts` arm warmed the store WITH
// vectors and asserted the local tier was ABSENT afterwards — i.e. that the
// flag made the engine DROP already-stored, already-embedded archive chunks
// from the index. The operator has ruled that behaviour out ("don't ingest,
// but don't discard"): an explicit opt-in run's work must survive the next
// automatic rebuild. That assertion is therefore not weakened, it is WRONG,
// and `TestAnAutomaticRebuildDoesNotDiscardAnOptInRunsWork` now asserts its
// opposite as the contract.
//
// What is unchanged is this guard's CLAIM — the pass's flag reaches the
// ENGINE, not only ingest. Only the observable moves, to the one place the
// flag still acts on the engine: a stored archive chunk with NO vector.
//
// 🔴 WHICH ARM IS THE DISCRIMINATOR, AND WHICH IS A CONTROL. Only the
// `--no-transcripts` arm discriminates. In the `--transcripts` arm the
// INGEST side has the flag too, and its repair pass embeds the vectorless
// chunk before the engine walks — after which the engine loads it whatever
// its own setting says, because that is exactly the ruling. So that arm stays
// green under the engine-side mutation; it is a CONTROL that the opt-in path
// works end to end, and it is NOT evidence that the flag reached the engine.
// Verified, not assumed: the mutation below was run and reddened the
// `--no-transcripts` arm alone.
func TestTheTranscriptsFlagReachesTheSearchEngine(t *testing.T) {
	for _, arm := range []struct {
		name      string
		config    bool
		flag      string
		wantLocal bool
	}{
		{name: "config ON, --no-transcripts: the flag must win", config: true, flag: "--no-transcripts", wantLocal: false},
		// 🔴 A CONTROL, not a discriminator — see the note above.
		{name: "config OFF, --transcripts: the opt-in path works end to end", config: false, flag: "--transcripts", wantLocal: true},
		// Both arms run over a store whose archive chunks have NO vector: that
		// is where the flag still changes what the engine does.
	} {
		t.Run(arm.name, func(t *testing.T) {
			vault := setupTestVaultEnv(t)
			stubVaultEmbedder(t, embedder.NewMock(384))
			setHostGlobalTranscripts(t, vault, true)
			m := drainArchiveOf(t, vault, "p", "SP", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))

			// The WARM STORE, built through production, with --no-embed so its
			// archive chunks are stored but NOT embedded.
			warm := []string{"index", "rebuild", "p", "--vault-root", vault, "--transcripts", "--no-embed"}
			if code := dispatchVP(t, warm...); code != cli.ExitOK {
				t.Fatalf("warm-up vp %v: exit %d", warm, code)
			}
			if n, _ := ingestedArchivesOf(t, vault, "p", "SP", m.SourceSHA256); n == 0 {
				t.Fatal("warm-up ingested nothing")
			}
			before := liveOwnedArchiveChunks(t, vault, "p")
			if before == 0 {
				t.Fatal("PRESENCE: the store holds no live-owned archive chunk, so neither arm " +
					"reaches the belt and both are vacuous")
			}
			if n := vectoredArchiveChunks(t, vault, "p"); n != 0 {
				t.Fatalf("PRESENCE: %d archive-owned chunk(s) already have cached vectors. Since "+
					"the 2026-11-06 ruling the belt acts only on a VECTORLESS chunk, so a vectored "+
					"fixture makes both arms follow the same path and the guard inert", n)
			}

			// The config now DISAGREES with the flag about to be passed.
			setHostGlobalTranscripts(t, vault, arm.config)

			argv := []string{"index", "rebuild", "p", "--vault-root", vault, arm.flag}
			if code := dispatchVP(t, argv...); code != cli.ExitOK {
				t.Fatalf("vp %v: exit %d", argv, code)
			}

			rec := completenessOf(t, vault, "p")
			_, gotLocal := rec.Tiers["local"]
			switch {
			case arm.wantLocal && !gotLocal:
				t.Fatalf("%s: the build recorded NO local tier (tiers=%v) although %q was passed "+
					"over a store holding %d archive-owned chunks. ingest admitted them and the "+
					"ENGINE then skipped them: the argv flag never reached the half that decides "+
					"what the built index serves", arm.name, rec.Tiers, arm.flag, before)
			case !arm.wantLocal && gotLocal:
				t.Fatalf("%s: %s still EMBEDDED the vectorless archive chunks: tiers=%v. The flag "+
					"is authoritative over the config (R1), and the engine re-read the config "+
					"instead. Embedding is the days-long cost the exclusion exists to avoid",
					arm.name, arm.flag, rec.Tiers)
			}
			// And an excluded run must not have made the project STALE: a
			// skipped vectorless archive chunk is not a miss.
			if !arm.wantLocal && rec.LocalMisses != 0 {
				t.Fatalf("%s: the excluded run counted %d local misses over archive chunks it did "+
					"not serve, which sets StaleMissingVectors and demands the very rebuild the "+
					"exclusion exists to avoid", arm.name, rec.LocalMisses)
			}
			// Either way the chunks are still on disk: a flag changes what the
			// INDEX serves for this run, never what the store holds.
			if n := liveOwnedArchiveChunks(t, vault, "p"); n != before {
				t.Fatalf("%s changed the STORE (%d -> %d archive-owned chunks); only the purge may",
					arm.flag, before, n)
			}
		})
	}
}

// vectoredArchiveChunks counts the live-owned archive chunks of project that
// already have a cached vector — the ones the belt LOADS whatever the
// transcript setting says, since the 2026-11-06 ruling.
func vectoredArchiveChunks(t *testing.T, vault, project string) int {
	t.Helper()
	v := storage.NewVault(vault)
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := v.EmbedCacheDir(project)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range st.Chunks(true) {
		if c.Selected == nil || c.Selected.Kind != indexstore.OwnerArchive {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, c.ID+".vec")); err == nil {
			n++
		}
	}
	return n
}

// E1 — WIRING. TestAnAutomaticRebuildDoesNotDiscardAnOptInRunsWork.
//
// 🔴 THE OPERATOR RULING OF 2026-11-06, as its own guard: "don't ingest, but
// don't discard". The automatic bound is unchanged — the two spawn sites still
// append `--no-transcripts` unconditionally and a cold search can never
// trigger the multi-day build — but an explicit opt-in run's work must SURVIVE
// the next automatic rebuild. The transcript decision governs INGESTION, not
// what the engine loads from what is already stored.
//
// Before the ruling this exact sequence silently undid the opt-in: the second
// run skipped every archive-owned chunk, the built index lost the transcript
// tier, and the next opt-in run had to find and re-embed it all. That churn is
// what the ruling removes.
//
// Q1 PATH: wiring. Two full argv slices through the production registry's
// Dispatch — the second being VERBATIM the argv the automatic spawn sites emit
// (`tools.NewRebuildLauncher`, `main.spawnRebuildFor`) — asserted on the
// durable record the production writer left.
//
// Q2 VALUE: the host config is `false`, the DEFAULT, so the surviving tier
// cannot be explained by the config happening to agree. Only the already-done
// work explains it.
//
// Q3 PRESENCE: 🔴 the opt-in run must actually have EMBEDDED the archive
// chunks, asserted two ways before the automatic run — a `local` tier in the
// completeness record, and a cached vector on disk for every archive-owned
// chunk. Without embedded chunks there is no work to survive and the guard is
// vacuous.
//
// Mutation: make the belt drop archive-owned chunks whatever their vector —
// `if archiveOwned && !indexTranscripts { continue }`, the pre-ruling shape.
func TestAnAutomaticRebuildDoesNotDiscardAnOptInRunsWork(t *testing.T) {
	vault := setupTestVaultEnv(t)
	stubVaultEmbedder(t, embedder.NewMock(384))
	// The host DEFAULT: transcripts excluded. Nothing below relies on the
	// config agreeing with the opt-in.
	setHostGlobalTranscripts(t, vault, false)
	m := drainArchiveOf(t, vault, "p", "SP", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))

	// 1. The operator's explicit opt-in run, which does the expensive work.
	optIn := []string{"index", "rebuild", "p", "--vault-root", vault, "--transcripts"}
	if code := dispatchVP(t, optIn...); code != cli.ExitOK {
		t.Fatalf("vp %v: exit %d", optIn, code)
	}
	if n, _ := ingestedArchivesOf(t, vault, "p", "SP", m.SourceSHA256); n == 0 {
		t.Fatal("PRESENCE: the opt-in run ingested nothing")
	}
	chunks := liveOwnedArchiveChunks(t, vault, "p")
	if chunks == 0 {
		t.Fatal("PRESENCE: no live-owned archive chunk after the opt-in run")
	}
	if got := vectoredArchiveChunks(t, vault, "p"); got != chunks {
		t.Fatalf("PRESENCE: the opt-in run embedded %d of %d archive-owned chunks. With no "+
			"embedded work there is nothing for the automatic run to preserve and this guard "+
			"is vacuous", got, chunks)
	}
	if _, ok := completenessOf(t, vault, "p").Tiers["local"]; !ok {
		t.Fatal("PRESENCE: the opt-in run recorded no local tier, so its survival below would " +
			"say nothing")
	}

	// 2. The AUTOMATIC run, with the argv the spawn sites actually emit.
	auto := []string{"index", "rebuild", "p", "--vault-root", vault, "--no-transcripts"}
	if code := dispatchVP(t, auto...); code != cli.ExitOK {
		t.Fatalf("vp %v: exit %d", auto, code)
	}

	rec := completenessOf(t, vault, "p")
	if _, ok := rec.Tiers["local"]; !ok {
		t.Fatalf("an automatic `%s` run DISCARDED the opt-in run's work: the archive tier is gone "+
			"from the built index (tiers=%v) although %d archive-owned chunks with cached vectors "+
			"are still in the store. Operator ruling 2026-11-06: the transcript decision governs "+
			"INGESTION, not what the engine loads from what is already stored — an opt-in run's "+
			"work must survive", strings.Join(auto[len(auto)-1:], " "), rec.Tiers, chunks)
	}
	if rec.LocalMisses != 0 {
		t.Fatalf("the automatic run counted %d local misses, which sets StaleMissingVectors and "+
			"demands the very rebuild the exclusion exists to avoid", rec.LocalMisses)
	}
	if n := liveOwnedArchiveChunks(t, vault, "p"); n != chunks {
		t.Fatalf("the automatic run changed the STORE (%d -> %d archive-owned chunks); only the "+
			"purge may", chunks, n)
	}
	// The bound itself is untouched: the automatic run ingested no NEW archive.
	// A second archive arrives and must stay out.
	m2 := drainArchiveOf(t, vault, "p", "SQ", time.Date(2026, 5, 14, 2, 0, 0, 0, time.UTC))
	if code := dispatchVP(t, auto...); code != cli.ExitOK {
		t.Fatalf("vp %v: exit %d", auto, code)
	}
	if n, _ := ingestedArchivesOf(t, vault, "p", "SQ", m2.SourceSHA256); n != 0 {
		t.Fatalf("the automatic run INGESTED a new archive (%d chunks). The bound is unchanged: "+
			"automatic runs must never ingest transcripts", n)
	}
}
