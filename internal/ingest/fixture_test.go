// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// countingEmbedder counts the texts it embeds, and can block on a gate.
type countingEmbedder struct {
	embedder.Embedder
	texts atomic.Int64
	calls atomic.Int64

	mu      sync.Mutex
	gate    chan struct{} // when set, EmbedBatch blocks until it is closed
	entered chan struct{} // closed on the first EmbedBatch
}

func newCountingEmbedder() *countingEmbedder {
	return &countingEmbedder{Embedder: embedder.NewMock(384)}
}

func (c *countingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	c.mu.Lock()
	gate, entered := c.gate, c.entered
	c.entered = nil
	c.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate
	}
	c.calls.Add(1)
	c.texts.Add(int64(len(texts)))
	return c.Embedder.EmbedBatch(ctx, texts)
}

// fixture is a git vault with project alpha (and beta), an engine over a
// counting embedder, and helpers that write real archives.
type fixture struct {
	t   *testing.T
	v   *storage.Vault
	eng *search.Engine
	emb *countingEmbedder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, "Projects", p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte("# "+p+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("palace/.local/\n.vp-locks/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, v: storage.NewVault(root), emb: newCountingEmbedder()}
	f.eng = search.NewEngine(f.emb, f.v, storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"})
	t.Cleanup(func() { f.eng.Close() })
	return f
}

func (f *fixture) deps() Deps {
	return Deps{Vault: f.v, Engine: f.eng, Embedder: f.emb}
}

// git runs git in the vault with a fixed identity.
func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.v.Root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// commitAll makes the vault a git repository with everything committed.
func (f *fixture) commitAll() {
	f.t.Helper()
	if _, err := os.Stat(filepath.Join(f.v.Root, ".git")); err != nil {
		f.git("init", "-q", "-b", "main")
	}
	f.git("add", "-A")
	f.git("commit", "-q", "--allow-empty", "-m", "fixture")
}

// transcript is a Claude-shaped JSONL transcript whose first timestamped
// record is at start, with n turns of distinct, chunkable text tagged by tag.
func transcript(start time.Time, tag string, n int) string {
	var b strings.Builder
	b.WriteString(`{"type":"custom-title","title":"x"}` + "\n")
	for i := range n {
		ts := start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		text := fmt.Sprintf("Turn %d of %s. We decided to refactor the indexer module %d and discussed the ledger design, the chunk store, and the embed cache in detail so the archive ingests cleanly. ", i, tag, i)
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`+"\n", ts, strings.Repeat(text, 3))
	}
	return b.String()
}

// archiveOf writes a Claude archive of session for project, captured at
// captured, and returns its listed entry.
func (f *fixture) archiveOf(project, session, body string, captured time.Time) *archive.Entry {
	f.t.Helper()
	src := filepath.Join(f.t.TempDir(), session+".jsonl")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	res, err := archive.Create(archive.CreateOptions{
		Adapter: archive.ClaudeCodeAdapterName, SessionID: session, SourcePath: src,
		VaultRoot: f.v.Root, ProjectSlug: project, Now: captured,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return f.entry(project, res.Manifest.SourceSHA256)
}

// entry finds the listed entry with this source_sha256.
func (f *fixture) entry(project, sha string) *archive.Entry {
	f.t.Helper()
	es, err := archive.ListEntries(f.v.Root, project)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, e := range es {
		if e.Manifest.SourceSHA256 == sha {
			return e
		}
	}
	f.t.Fatalf("no listed archive with sha %s", sha)
	return nil
}

// ensureLedger creates project's ledger with an empty baseline set: every
// archive listed now is new, not backlog.
func (f *fixture) ensureLedger(project string) { f.makeLedger(project, nil) }

// ensureLedgerWithBaseline creates project's ledger with every archive listed
// now in the baseline set, as a fresh host's first run does.
func (f *fixture) ensureLedgerWithBaseline(project string) { f.makeLedger(project, []string{}) }

func (f *fixture) makeLedger(project string, exclude []string) {
	f.t.Helper()
	tx, err := indexstore.Lock(context.Background(), f.v, project, indexstore.NoTimeout)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Release()
	if exclude == nil {
		exclude = f.allSHAs(project)
	}
	if _, err := tx.EnsureLedger(exclude); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

// allSHAs is every listed archive's source_sha256.
func (f *fixture) allSHAs(project string) []string {
	f.t.Helper()
	es, _ := archive.ListEntries(f.v.Root, project)
	out := []string{}
	for _, e := range es {
		out = append(out, e.Manifest.SourceSHA256)
	}
	return out
}

// store reads project's store.
func (f *fixture) store(project string) *indexstore.Store {
	f.t.Helper()
	st, err := indexstore.ReadStore(f.v, project)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// ledgerLines returns project's raw ledger lines.
func (f *fixture) ledgerLines(project string) []string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.v.Root, "palace", ".local", "index", project, "ledger.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// sessionLines counts raw session records for session in project's ledger.
func (f *fixture) sessionLines(project, session string) int {
	n := 0
	for _, l := range f.ledgerLines(project) {
		if strings.Contains(l, `"kind":"session"`) && strings.Contains(l, `"session_id":"`+session+`"`) {
			n++
		}
	}
	return n
}
