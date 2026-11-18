// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// khStatusFixture builds the `authored` coverage state through the PRODUCTION
// writers: a session note, an archive on disk (m > 0), and a real
// RebuildExplicit under the default host config, which EXCLUDES transcripts.
func khStatusFixture(t *testing.T, proj string) (*storage.Vault, storage.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	vault := storage.NewVault(t.TempDir())
	testutil.InitProject(t, vault.Root, proj)
	sdir, err := vault.SessionDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{"---", "session_id: 2026-05-13-aaaa0000-01", "project: " + proj,
		"date: 2026-05-13", "title: a note", "---", strings.Repeat("Authored content about the ledger. ", 20)}, "\n")
	if err := os.WriteFile(filepath.Join(sdir, "2026-05-13-aaaa0000-01.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Create(archive.CreateOptions{Adapter: archive.InlineAdapterName, SessionID: "s1",
		SourceContent: []byte(`{"type":"user","text":"x"}` + "\n"), VaultRoot: vault.Root, ProjectSlug: proj}); err != nil {
		t.Fatal(err)
	}
	cfg, err := vault.LoadConfig(proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IndexTranscripts {
		t.Fatal("PRESENCE: the default config indexes transcripts")
	}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	defer eng.Close()
	if _, err := eng.RebuildExplicit(context.Background(), proj); err != nil {
		t.Fatal(err)
	}
	return vault, cfg
}

// `vp index status` says the knowledge graph is excluded, in text and in JSON.
//
// Q1: the production indexCoverage + printCoverage the command runs. Q2: the
// default (excluded) config. Q3: the state is asserted authored.
//
// Mutation: delete the printCoverage line / the KGStatus assignment.
func TestIndexStatusSaysTheKGIsExcluded(t *testing.T) {
	const proj = "kgh-proj"
	vault, cfg := khStatusFixture(t, proj)
	cov, err := indexCoverage(vault, cfg, proj)
	if err != nil {
		t.Fatal(err)
	}
	if cov.State != search.CoverageAuthored {
		t.Fatalf("PRESENCE: the fixture reads %q, not authored: %s", cov.State, cov.Reason)
	}
	var out bytes.Buffer
	printCoverage(&out, cov)
	if !strings.Contains(out.String(), "knowledge graph: tier_excluded") || !strings.Contains(out.String(), "archive-extracted") {
		t.Errorf("`vp index status` text does not say the KG is excluded:\n%s", out.String())
	}
	raw, _ := json.Marshal(cov)
	if !strings.Contains(string(raw), `"kg_status":"tier_excluded"`) {
		t.Errorf("`vp index status --json` carries no kg_status: %s", raw)
	}
}

// The control: no knowledge-graph line when the tier is not excluded.
func TestIndexStatusDoesNotClaimAnExcludedKGOnAnIncludedHost(t *testing.T) {
	const proj = "kgh-proj"
	vault, cfg := khStatusFixture(t, proj)
	hp, err := storage.HostProjectConfigPath(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hp, []byte("[index]\ntranscripts = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = vault.LoadConfig(proj)
	if err != nil {
		t.Fatal(err)
	}
	cov, err := indexCoverage(vault, cfg, proj)
	if err != nil {
		t.Fatal(err)
	}
	if cov.State == search.CoverageAuthored {
		t.Fatal("PRESENCE: opting in left the project authored; the control is inert")
	}
	var out bytes.Buffer
	printCoverage(&out, cov)
	if strings.Contains(out.String(), "knowledge graph") {
		t.Errorf("an included-tier host prints a knowledge-graph line:\n%s", out.String())
	}
}
