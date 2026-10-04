// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// updateGolden regenerates internal/palace/testdata/prepare_golden.json from
// IndexTranscript: go test ./internal/capture -run TestIndexTranscriptMatchesThePrepareGolden -update-golden
var updateGolden = flag.Bool("update-golden", false, "regenerate internal/palace/testdata/prepare_golden.json from IndexTranscript")

// goldenDate is the date the golden fixture is captured at (the pinned clock).
var goldenDate = time.Date(2026, 5, 13, 15, 4, 5, 0, time.UTC)

// GoldenRun is one IndexTranscript call's output, as the golden records it.
// The fixture is shared with internal/palace's Prepare parity test, which
// decodes the same shape.
type goldenRun struct {
	Project   string         `json:"project"`
	SourceRef string         `json:"source_ref"`
	Chunks    []goldenChunk  `json:"chunks"`
	Entities  []goldenEntity `json:"entities"`
	Triples   []goldenTriple `json:"triples"`
}

type goldenChunk struct {
	Wing       string `json:"wing"`
	Room       string `json:"room"`
	Hall       string `json:"hall"`
	ChunkIndex int    `json:"chunk_index"`
	Content    string `json:"content"`
	DrawerID   string `json:"drawer_id"`
	SourceRef  string `json:"source_ref"`
	FiledAt    string `json:"filed_at"`
}

type goldenEntity struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
}

type goldenTriple struct {
	Subject       string  `json:"subject"`
	Predicate     string  `json:"predicate"`
	Object        string  `json:"object"`
	ValidFrom     string  `json:"valid_from,omitempty"`
	ExtractedAt   string  `json:"extracted_at"`
	SourceSession string  `json:"source_session"`
	Confidence    float64 `json:"confidence"`
}

type golden struct {
	Date string      `json:"date"`
	Runs []goldenRun `json:"runs"`
}

// TestIndexTranscriptMatchesThePrepareGolden runs IndexTranscript on the
// golden fixture transcript, under the default config and the pinned clock,
// for two projects (so the same content lands in two wings), and compares
// what it wrote with internal/palace/testdata/prepare_golden.json. The golden
// is a parity pin between IndexTranscript and palace.Prepare: a change to
// either one's chunking, classification or extraction fails one of the two
// tests. With -update-golden it rewrites the file instead.
//
// Regeneration: while IndexTranscript exists, run this test with
// -update-golden. Once capture-and-backfill-write-host-local-index-only
// deletes IndexTranscript, this test goes with it and the golden becomes a
// regression pin of palace.Prepare alone, regenerated only by a commit that
// states the behaviour change (task importers-write-the-frozen-tracked-corpus,
// plan revision R-3).
func TestIndexTranscriptMatchesThePrepareGolden(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	old := indexerNow
	indexerNow = func() time.Time { return goldenDate }
	t.Cleanup(func() { indexerNow = old })

	text, err := os.ReadFile(filepath.Join("..", "palace", "testdata", "prepare_fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := golden{Date: goldenDate.Format(time.RFC3339)}
	for _, project := range []string{"alpha", "beta"} {
		v := testVault(t)
		// A current vault (data format 2), so the KG reads below pass the
		// format gate.
		if err := os.MkdirAll(filepath.Join(v.Root, ".vibe-palace"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v.Root, ".vibe-palace", "vault.toml"), []byte("format = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := v.LoadConfig("")
		if err != nil {
			t.Fatal(err)
		}
		idx := NewIndexer(v, nil, nil, cfg)
		if _, err := idx.IndexTranscript(context.Background(), "sess-golden", project, string(text)); err != nil {
			t.Fatal(err)
		}
		got.Runs = append(got.Runs, readGoldenRun(t, v, project, "sess-golden"))
	}
	if len(got.Runs[0].Chunks) < 2 || len(got.Runs[0].Entities) == 0 || len(got.Runs[0].Triples) == 0 {
		t.Fatalf("the fixture must give several chunks, entities and triples: %+v", got.Runs[0])
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("..", "palace", "testdata", "prepare_golden.json")
	if *updateGolden {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("IndexTranscript no longer matches the committed golden %s; if the change is intended, regenerate it with -update-golden and say why in the commit", path)
	}
}

// readGoldenRun reads back what IndexTranscript wrote for project.
func readGoldenRun(t *testing.T, v *storage.Vault, project, sourceRef string) goldenRun {
	t.Helper()
	run := goldenRun{Project: project, SourceRef: sourceRef}
	wings, err := v.ListWings(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wings {
		rooms, err := v.ListRooms(project, w)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rooms {
			ds, err := v.ListDrawers(project, w, r)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range ds {
				run.Chunks = append(run.Chunks, goldenChunk{Wing: w, Room: r, Hall: d.Hall, ChunkIndex: d.ChunkIndex,
					Content: d.Content, DrawerID: d.ID, SourceRef: d.SourceRef, FiledAt: d.FiledAt})
			}
		}
	}
	sort.Slice(run.Chunks, func(i, j int) bool { return run.Chunks[i].ChunkIndex < run.Chunks[j].ChunkIndex })
	ents, _, err := v.ListEntities(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		run.Entities = append(run.Entities, goldenEntity{ID: e.ID, Name: e.Name, Type: e.Type, CreatedAt: e.CreatedAt})
	}
	sort.Slice(run.Entities, func(i, j int) bool { return run.Entities[i].ID < run.Entities[j].ID })
	trs, err := v.ListTriples(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trs {
		run.Triples = append(run.Triples, goldenTriple{Subject: tr.Subject, Predicate: tr.Predicate, Object: tr.Object,
			ValidFrom: tr.ValidFrom, ExtractedAt: tr.ExtractedAt, SourceSession: tr.SourceSession, Confidence: tr.Confidence})
	}
	sort.Slice(run.Triples, func(i, j int) bool {
		a, b := run.Triples[i], run.Triples[j]
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if a.Predicate != b.Predicate {
			return a.Predicate < b.Predicate
		}
		return a.Object < b.Object
	})
	return run
}
