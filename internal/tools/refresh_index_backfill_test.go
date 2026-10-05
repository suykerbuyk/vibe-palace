// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// recLauncher is a detachlaunch.LaunchFunc that records its calls instead of
// spawning a process, so a test can prove vp_refresh_index spawns `vp index
// rebuild` exactly once, with which arguments, and never when it refuses.
type recLauncher struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (r *recLauncher) launch(_ string, args []string, _ string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return 0, r.err
	}
	r.calls = append(r.calls, slices.Clone(args))
	return 4242, nil
}

func (r *recLauncher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recLauncher) lastArgs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

// archivedProjectFixture builds a vault holding one project that has a
// transcript archive and no palace store — not a truly-empty project, so
// vp_refresh_index starts a rebuild for it rather than refusing.
func archivedProjectFixture(t *testing.T, project, transcript string) (*storage.Vault, *search.Engine) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	testutil.InitProject(t, vault.Root, project)
	if _, err := archive.Create(archive.CreateOptions{
		Adapter:       archive.InlineAdapterName,
		SessionID:     "backfill-fixture-session",
		SourceContent: []byte(transcript),
		VaultRoot:     vault.Root,
		ProjectSlug:   project,
	}); err != nil {
		t.Fatalf("create fixture archive: %v", err)
	}
	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { eng.Close() })
	return vault, eng
}

// writeHolder writes an advisory run-lock holder record naming pid.
func writeHolder(t *testing.T, vault *storage.Vault, pid int, kind indexstore.RunKind, project string) {
	t.Helper()
	if err := os.MkdirAll(vault.IndexLocksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(indexstore.Holder{PID: pid, Kind: kind, Project: project, StartTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vault.IndexRunHolderPath(), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRefreshIndexStartsADetachedRebuild: a project with content and no live
// holder makes the tool spawn `vp index rebuild <project> --vault-root <root>
// --max-archives N` exactly once and return {started:true, pid, log}.
func TestRefreshIndexStartsADetachedRebuild(t *testing.T) {
	const project = "backfill-proj"
	vault, eng := archivedProjectFixture(t, project, `{"type":"user","text":"hello"}`+"\n")
	rec := &recLauncher{}
	tool := RefreshIndexTool(eng, vault, rec.launch)
	params, _ := json.Marshal(refreshIndexParams{Project: project, MaxArchives: 2})

	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler refused a project with content: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["started"] != true {
		t.Fatalf("result = %v, want {started:true}", res)
	}
	if rec.count() != 1 {
		t.Fatalf("launches = %d, want exactly 1", rec.count())
	}
	want := []string{"index", "rebuild", project, "--vault-root", vault.Root, "--max-archives", "2"}
	if got := rec.lastArgs(); !slices.Equal(got, want) {
		t.Fatalf("launch args = %v, want %v", got, want)
	}
}

// TestRefreshIndexRefusesOnALiveHolder: a holder record naming a live process
// makes the tool refuse naming the holder, with zero launches and without
// taking the run lock; a holder naming a dead pid makes it spawn.
func TestRefreshIndexRefusesOnALiveHolder(t *testing.T) {
	const project = "holder-proj"
	vault, eng := archivedProjectFixture(t, project, `{"type":"user","text":"hi"}`+"\n")

	var tries int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	defer restore()

	// A live holder (this test process): refuse, naming it, no launch, no lock.
	writeHolder(t, vault, os.Getpid(), indexstore.KindIngest, "other-proj")
	rec := &recLauncher{}
	tool := RefreshIndexTool(eng, vault, rec.launch)
	params, _ := json.Marshal(refreshIndexParams{Project: project})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("handler did not refuse while a live holder held the lock")
	}
	for _, want := range []string{"ingest", "other-proj"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("launches = %d on a live-holder refusal, want 0", rec.count())
	}
	if tries != 0 {
		t.Fatalf("the probe made %d run-lock tries, want 0", tries)
	}

	// A dead holder: spawn.
	writeHolder(t, vault, 2147483646, indexstore.KindRebuild, project)
	rec2 := &recLauncher{}
	tool2 := RefreshIndexTool(eng, vault, rec2.launch)
	if _, err := tool2.Handler(context.Background(), params); err != nil {
		t.Fatalf("handler refused on a dead holder: %v", err)
	}
	if rec2.count() != 1 {
		t.Fatalf("launches = %d on a dead-holder spawn, want 1", rec2.count())
	}
}

// TestRefreshIndexFailedSpawn: when the launcher fails, the tool returns an
// error and reports nothing started.
func TestRefreshIndexFailedSpawn(t *testing.T) {
	const project = "spawn-fail"
	vault, eng := archivedProjectFixture(t, project, `{"type":"user","text":"x"}`+"\n")
	rec := &recLauncher{err: errors.New("launch boom")}
	tool := RefreshIndexTool(eng, vault, rec.launch)
	params, _ := json.Marshal(refreshIndexParams{Project: project})
	res, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatalf("handler reported success on a failed launch: %v", res)
	}
	if !strings.Contains(err.Error(), "launch") {
		t.Errorf("error %q does not name the launch failure", err)
	}
}

// TestRefreshIndexDryRunIsInProcess: dry_run returns the preflight report in
// process, launching nothing and writing no holder record.
func TestRefreshIndexDryRunIsInProcess(t *testing.T) {
	const project = "dry-proj"
	vault, eng := archivedProjectFixture(t, project, `{"type":"user","text":"y"}`+"\n")
	rec := &recLauncher{}
	tool := RefreshIndexTool(eng, vault, rec.launch)
	params, _ := json.Marshal(refreshIndexParams{Project: project, DryRun: true})

	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["dry_run"] != true {
		t.Fatalf("result = %v, want a dry-run report", res)
	}
	if rec.count() != 0 {
		t.Fatalf("dry run launched %d rebuilds, want 0", rec.count())
	}
	if _, err := os.Stat(vault.IndexRunHolderPath()); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote a holder record: %v", err)
	}
}

// TestRefreshIndexIsRegisteredMutating: the tool stays declared mutating (its
// start path launches a process that writes host-local index state).
func TestRefreshIndexIsRegisteredMutating(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	eng := search.NewEngine(embedder.NewMock(384), vault, storage.Config{})
	t.Cleanup(func() { eng.Close() })
	tool := RefreshIndexTool(eng, vault, func(string, []string, string) (int, error) { return 0, nil })
	if !tool.Mutating {
		t.Fatal("vp_refresh_index must stay Mutating: it launches a host-local-index writer")
	}
}
