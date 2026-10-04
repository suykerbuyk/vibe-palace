// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The golden's shape, written by internal/capture's
// TestIndexTranscriptMatchesThePrepareGolden from IndexTranscript.
type goldenFile struct {
	Date string `json:"date"`
	Runs []struct {
		Project   string `json:"project"`
		SourceRef string `json:"source_ref"`
		Chunks    []struct {
			Wing       string `json:"wing"`
			Room       string `json:"room"`
			Hall       string `json:"hall"`
			ChunkIndex int    `json:"chunk_index"`
			Content    string `json:"content"`
			DrawerID   string `json:"drawer_id"`
			SourceRef  string `json:"source_ref"`
			FiledAt    string `json:"filed_at"`
		} `json:"chunks"`
		Entities []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Type      string `json:"type"`
			CreatedAt string `json:"created_at"`
		} `json:"entities"`
		Triples []struct {
			Subject       string  `json:"subject"`
			Predicate     string  `json:"predicate"`
			Object        string  `json:"object"`
			ValidFrom     string  `json:"valid_from"`
			ExtractedAt   string  `json:"extracted_at"`
			SourceSession string  `json:"source_session"`
			Confidence    float64 `json:"confidence"`
		} `json:"triples"`
	} `json:"runs"`
}

// TestPrepareMatchesTheGolden (5-S2) is the other half of the parity pin with
// IndexTranscript: on the fixture transcript, under the default config and
// the golden's date, Prepare yields the same wings, rooms, halls, chunk
// indexes, contents, dates, entities and triples IndexTranscript wrote. Ids
// are compared by content: each chunk's id is index.ChunkID(content), while the
// golden's drawer id for the same chunk is storage.DrawerID(wing, content). The
// fixture runs in two projects, so equal content sits in two wings: Prepare
// gives both one id, the golden two drawer ids.
func TestPrepareMatchesTheGolden(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	raw, err := os.ReadFile(filepath.Join("testdata", "prepare_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join("testdata", "prepare_fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	date, err := time.Parse(time.RFC3339, g.Date)
	if err != nil {
		t.Fatal(err)
	}
	v := storage.NewVault(t.TempDir())
	idsByRun := map[string][]string{}
	for _, run := range g.Runs {
		ix, err := ProjectIndexing(v, run.Project)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Prepare(ix, PrepareInput{Project: run.Project, SourceRef: run.SourceRef, Date: date, Text: string(text)})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Chunks) != len(run.Chunks) {
			t.Fatalf("%s: %d chunks, golden %d", run.Project, len(p.Chunks), len(run.Chunks))
		}
		for i, c := range p.Chunks {
			w := run.Chunks[i]
			if c.Wing != w.Wing || c.Room != w.Room || c.Hall != w.Hall || c.ChunkIndex != w.ChunkIndex ||
				c.Content != w.Content || c.SourceRef != w.SourceRef || c.FiledAt != w.FiledAt {
				t.Fatalf("%s chunk %d: %+v, golden %+v", run.Project, i, c, w)
			}
			if c.ID != index.ChunkID(c.Content) {
				t.Fatalf("%s chunk %d: id %s is not index.ChunkID(content)", run.Project, i, c.ID)
			}
			if w.DrawerID != storage.DrawerID(w.Wing, w.Content) {
				t.Fatalf("%s chunk %d: the golden's drawer id is not storage.DrawerID(wing, content)", run.Project, i)
			}
			idsByRun[run.Project] = append(idsByRun[run.Project], c.ID)
		}
		var ents []string
		for _, e := range p.Entities {
			ents = append(ents, e.ID+"|"+e.Name+"|"+e.Type+"|"+e.CreatedAt)
		}
		var wantEnts []string
		for _, e := range run.Entities {
			wantEnts = append(wantEnts, e.ID+"|"+e.Name+"|"+e.Type+"|"+e.CreatedAt)
		}
		sort.Strings(ents)
		if !equalStrings(ents, wantEnts) {
			t.Fatalf("%s entities:\n%v\ngolden:\n%v", run.Project, ents, wantEnts)
		}
		var trs, wantTrs []string
		for _, tr := range p.Triples {
			trs = append(trs, tripleKey(tr.Subject, tr.Predicate, tr.Object, tr.ValidFrom, tr.ExtractedAt, tr.SourceSession, tr.Confidence))
		}
		for _, tr := range run.Triples {
			wantTrs = append(wantTrs, tripleKey(tr.Subject, tr.Predicate, tr.Object, tr.ValidFrom, tr.ExtractedAt, tr.SourceSession, tr.Confidence))
		}
		sort.Strings(trs)
		sort.Strings(wantTrs)
		if !equalStrings(trs, wantTrs) {
			t.Fatalf("%s triples:\n%v\ngolden:\n%v", run.Project, trs, wantTrs)
		}
	}
	a, b := g.Runs[0], g.Runs[1]
	if a.Chunks[0].Wing == b.Chunks[0].Wing || a.Chunks[0].Content != b.Chunks[0].Content {
		t.Fatal("fixture: the two runs must put equal content in two wings")
	}
	if !equalStrings(idsByRun[a.Project], idsByRun[b.Project]) {
		t.Fatal("equal content in two wings must get one ChunkID: the id must hash the content alone")
	}
	if a.Chunks[0].DrawerID == b.Chunks[0].DrawerID {
		t.Fatal("fixture: the golden's drawer ids should differ across wings")
	}
}

func tripleKey(s, p, o, vf, ea, ss string, c float64) string {
	return s + "|" + p + "|" + o + "|" + vf + "|" + ea + "|" + ss + "|" + strconvF(c)
}

func strconvF(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPrepareDoesNoIO (importers-S3, behavioural half): a temp vault root,
// HOME and TMPDIR are byte-identical before and after a Prepare call. The
// sink half is the sourceaudit planner rule, which lists Prepare.
func TestPrepareDoesNoIO(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("TMPDIR", tmp)
	root := t.TempDir()
	v := storage.NewVault(root)
	ix, err := ProjectIndexing(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join("testdata", "prepare_fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	before := []string{treeHash(t, root), treeHash(t, home), treeHash(t, tmp)}
	if _, err := Prepare(ix, PrepareInput{Project: "proj", SourceRef: "s", Date: time.Unix(0, 0), Text: string(text)}); err != nil {
		t.Fatal(err)
	}
	after := []string{treeHash(t, root), treeHash(t, home), treeHash(t, tmp)}
	if !equalStrings(before, after) {
		t.Fatalf("Prepare changed the filesystem: %v -> %v", before, after)
	}
}

func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h.Write([]byte(rel + "\x00"))
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
