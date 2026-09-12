// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// ecWrite writes body at rel under root, creating parents.
func ecWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ecExists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// ecLegacyVector writes the mock embedder's vector for text at the pre-move
// path palace/<project>/.local/embed-cache/<id>.vec — what a binary from before
// the move left behind. The new code cannot write there.
func ecLegacyVector(t *testing.T, h *testHarness, project, id, text string) []byte {
	t.Helper()
	vec, err := h.Embedder.Embed(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(f))
	}
	ecWrite(t, h.Vault.Root, "palace/"+project+"/.local/embed-cache/"+id+".vec", string(data))
	return data
}

// ecNote writes a session note in the shape the note corpus indexes.
func ecNote(t *testing.T, root, project, stem, body string) {
	t.Helper()
	ecWrite(t, root, "Projects/"+project+"/sessions/"+stem+".md",
		"---\nsession_id: "+stem+"\nproject: "+project+"\ndate: 2026-09-10\ntitle: Notes\n---\n"+body+"\n")
}

// ecAuditArtifacts returns the NEW artifacts vaultaudit reports under dim.
func ecAuditArtifacts(t *testing.T, v *storage.Vault, dim string) []string {
	t.Helper()
	rep, err := vaultaudit.Run(v)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range rep.Dimensions {
		if d.Name != dim {
			continue
		}
		for _, f := range d.New {
			out = append(out, f.Artifact)
		}
	}
	return out
}

// TestIntegrationEmbedCacheLivesOutsideProjectTrees is mechanism 2 of the task,
// closed. A search of a notes-only project used to write its vectors under
// palace/<slug>/.local/embed-cache/ and so conjure a palace/<slug>/ — after which
// project-tree-coherence went silent on a real Direction-A project and
// cross-project search covered it only on hosts where someone had searched it
// first. Now the vector lands under palace/.local/ and nothing else moves.
func TestIntegrationEmbedCacheLivesOutsideProjectTrees(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	root := h.Vault.Root
	const stem = "2026-09-10-aaaa0000-01"
	ecNote(t, root, "notesonly", stem, "The gearbox rebuild replaced every bearing in the drivetrain.")

	// The MCP layer wraps a list result as {"items": [...]}.
	var sr struct {
		Items []search.SearchResult `json:"items"`
	}

	// Cross-project search FIRST, on a cold engine that has never searched the
	// notes-only project: that is the case it used to miss. It enumerates every
	// project now, so it covers the notes-only one on every host — not only
	// where someone searched it directly first.
	h.Seed(t, testinfra.WithDrawer("withstore", "general", "general", "unrelated widget inventory", "facts", "2026-09-10T10:00:00Z"))
	if h.Engine.HasIndex("notesonly") {
		t.Fatal("precondition: the engine must not have indexed notesonly yet")
	}
	raw := h.callTool(t, "vp_search_cross_project", map[string]any{"query": "gearbox bearing drivetrain"})
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if !slices.ContainsFunc(sr.Items, func(r search.SearchResult) bool { return r.Project == "notesonly" }) {
		t.Errorf("vp_search_cross_project missed the notes-only project: %+v", sr.Items)
	}

	sr.Items = nil
	raw = h.callTool(t, "vp_search", map[string]any{"project": "notesonly", "query": "gearbox bearing"})
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if len(sr.Items) == 0 {
		t.Fatal("vp_search returned nothing for the notes-only project")
	}

	vec := "palace/.local/embed-cache/notesonly/note.notesonly." + stem + ".c0.vec"
	if !ecExists(root, vec) {
		t.Errorf("vector not at %s", vec)
	}
	if ecExists(root, "palace/notesonly") {
		t.Error("vp_search created palace/notesonly — the cache must write only under palace/.local/")
	}
	if got := ecAuditArtifacts(t, h.Vault, vaultaudit.DimProjectTreeCoherence); !slices.Contains(got, "notesonly") {
		t.Errorf("project-tree-coherence must still report the notes-only project; got %v", got)
	}
	if r := check.CheckPalaceLocalOnly(h.Vault); r.Status != check.Pass {
		t.Errorf("palace-local-only = %+v, want Pass", r)
	}

}

// ecGit runs git hermetically in dir: vsGit, plus the identity a commit needs.
func ecGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return vsGit(t, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
}

// ecPull pulls origin into the harness vault through the production path and
// fails on any per-remote failure, which Pull reports in the result, not err.
func ecPull(t *testing.T, root string) {
	t.Helper()
	res, err := storage.Pull(root, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if rerr := res.RemoteResults["origin"]; rerr != nil {
		t.Fatalf("Pull origin: %v\n%s", rerr, res.RemoteOutput["origin"])
	}
}

// ecSweepBySearch runs one search on a FRESH engine. The harness engine's
// per-cache sweep Once may already have fired, so a sweep observed through it
// would prove nothing about the next process.
func ecSweepBySearch(t *testing.T, h *testHarness, project string) {
	t.Helper()
	eng := search.NewEngine(h.Embedder, h.Vault, h.Config)
	if _, err := eng.Search(context.Background(), "anything", search.SearchFilters{Project: project}); err != nil {
		t.Fatalf("search %s: %v", project, err)
	}
}

// TestIntegrationPulledDeletionLeavesNoHusk reproduces the incident end to end
// and proves both halves of the fix. Host B is the harness vault itself, turned
// into a git repository in place; host A is a plain clone of the shared remote,
// used only as a git working copy and never opened as a vault.
//
// Legacy leg: B holds a vector at the OLD path — a binary from before the move
// — and a pull of A's deletion leaves the husk, exactly as on 2026-09-09. The
// presence rule makes every enumerator and the audit ignore it and the check row
// report it; the first sweep of a fresh engine heals it and reaps its cache.
//
// New-layout leg: the vector lives under palace/.local/, so the same pulled
// deletion leaves nothing behind at all — git removed the last file under
// palace/<slug>/ and there was nothing ignored inside it.
func TestIntegrationPulledDeletionLeavesNoHusk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	h := newHarness(t, false)
	h.registerAllTools(t)
	root := h.Vault.Root

	// Host B: the harness root, with the operator's ignore shape.
	vsGit(t, root, "init", "-q", "-b", "main")
	ecWrite(t, root, ".gitignore", "palace/.local/\npalace/*/.local/\n.vibe-palace/\n")
	var stub storage.Drawer
	h.Seed(t, testinfra.WithDrawerOut("stub", "general", "general", "stub drawer about pumps", "facts", "2026-09-09T10:00:00Z", &stub))
	// A keeper, so the orphan reaper's zero-projects guard does not decline.
	ecWrite(t, root, "Projects/keep/resume.md", "# keep\n")
	ecGit(t, root, "add", "-A")
	ecGit(t, root, "commit", "-q", "-m", "seed")
	bare := t.TempDir()
	vsGit(t, bare, "init", "-q", "--bare", "-b", "main")
	vsGit(t, root, "remote", "add", "origin", bare)
	vsGit(t, root, "push", "-q", "-u", "origin", "main")

	// Host A: a plain clone.
	a := filepath.Join(t.TempDir(), "a")
	vsGit(t, filepath.Dir(a), "clone", "-q", bare, a)

	// --- Legacy leg ---------------------------------------------------------
	legacyRel := "palace/stub/.local/embed-cache/" + stub.ID + ".vec"
	ecLegacyVector(t, h, "stub", stub.ID, stub.Content)

	ecGit(t, a, "rm", "-r", "-q", "palace/stub")
	ecGit(t, a, "commit", "-q", "-m", "remove stub")
	ecGit(t, a, "push", "-q")
	ecPull(t, root)

	if !ecExists(root, legacyRel) {
		t.Fatal("precondition: the pull must leave the ignored legacy vector behind — the incident")
	}
	all, err := h.Vault.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(all, func(p storage.ProjectPresence) bool { return p.Slug == "stub" }) {
		t.Errorf("ListAllProjects counts the husk: %+v", all)
	}
	for _, dim := range []string{vaultaudit.DimProjectTreeCoherence, vaultaudit.DimPalaceStoreDrawers} {
		if got := ecAuditArtifacts(t, h.Vault, dim); slices.Contains(got, "stub") {
			t.Errorf("%s reports the husk: %v", dim, got)
		}
	}
	var out struct {
		Checks []struct {
			Status  string   `json:"status"`
			Details []string `json:"details"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_check", map[string]any{"checks": []string{"palace-local-only"}})), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Checks) != 1 || out.Checks[0].Status != "info" ||
		!strings.Contains(strings.Join(out.Checks[0].Details, "\n"), "palace/stub/ — .local/: embed-cache/ (1 file)") {
		t.Fatalf("palace-local-only must report the husk as Info: %+v", out.Checks)
	}

	// --- Heal ---------------------------------------------------------------
	ecSweepBySearch(t, h, "keep")
	if ecExists(root, "palace/stub") {
		t.Error("the first sweep must heal the husk")
	}
	if ecExists(root, "palace/.local/embed-cache/stub") {
		t.Error("stub is in neither tree, so its migrated cache must be reaped")
	}
	if r := check.CheckPalaceLocalOnly(h.Vault); r.Status != check.Pass {
		t.Errorf("palace-local-only after the heal = %+v, want Pass", r)
	}

	// --- New-layout leg -----------------------------------------------------
	var stub2 storage.Drawer
	h.Seed(t, testinfra.WithDrawerOut("stub2", "general", "general", "stub2 drawer about valves", "facts", "2026-09-10T10:00:00Z", &stub2))
	ecGit(t, root, "add", "-A")
	ecGit(t, root, "commit", "-q", "-m", "add stub2")
	ecGit(t, root, "push", "-q")
	ecSweepBySearch(t, h, "stub2")
	newRel := "palace/.local/embed-cache/stub2/" + stub2.ID + ".vec"
	if !ecExists(root, newRel) {
		t.Fatalf("indexing stub2 must cache its vector at %s", newRel)
	}

	ecGit(t, a, "pull", "-q")
	ecGit(t, a, "rm", "-r", "-q", "palace/stub2")
	ecGit(t, a, "commit", "-q", "-m", "remove stub2")
	ecGit(t, a, "push", "-q")
	ecPull(t, root)

	if ecExists(root, "palace/stub2") {
		t.Error("with the cache outside palace/stub2/, the pulled deletion must leave no directory behind")
	}
	ecSweepBySearch(t, h, "keep")
	if ecExists(root, "palace/.local/embed-cache/stub2") {
		t.Error("the next sweep must reap stub2's orphaned cache")
	}
}

// TestIntegrationConcurrentSweepsConverge: four engines — as four processes on
// one host would be — each with its own sweep Once, search at the same moment
// over a vault still in the legacy layout. Every search succeeds; every vector
// of a known project ends at the new path with its bytes; no legacy cache
// survives; husks are healed; real stores keep their drawers.
func TestIntegrationConcurrentSweepsConverge(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	want := map[string][]byte{}
	for _, p := range []string{"real1", "real2"} {
		var d storage.Drawer
		h.Seed(t, testinfra.WithDrawerOut(p, "general", "general", p+" drawer about compressors", "facts", "2026-09-10T10:00:00Z", &d))
		h.seedProject(t, p)
		want["palace/.local/embed-cache/"+p+"/"+d.ID+".vec"] = ecLegacyVector(t, h, p, d.ID, d.Content)
	}
	// husk1 is mechanism 2's phantom: a notes-only project whose vectors an old
	// binary cached under palace/husk1/.local/.
	const stem = "2026-09-10-bbbb0000-01"
	body := "Notes about the compressor overhaul."
	ecNote(t, root, "husk1", stem, body)
	id := "note.husk1." + stem + ".c0"
	want["palace/.local/embed-cache/husk1/"+id+".vec"] = ecLegacyVector(t, h, "husk1", id, body)
	// husk2 is the incident's shape: in neither tree, only a cached vector left.
	ecLegacyVector(t, h, "husk2", "7a31b05d", "gone")
	// A crash between the rename and the removals.
	if err := os.MkdirAll(filepath.Join(root, "palace", "crash", ".local"), 0o755); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	filters := []search.SearchFilters{{Project: "real1"}, {Project: "real2"}, {Project: "husk1"}, {}}
	errs := make([]error, len(filters))
	for i, f := range filters {
		eng := search.NewEngine(h.Embedder, h.Vault, h.Config)
		wg.Go(func() {
			<-start
			_, errs[i] = eng.Search(context.Background(), "compressor", f)
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("search %d (%+v): %v", i, filters[i], err)
		}
	}

	for rel, b := range want {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !slices.Equal(got, b) {
			t.Errorf("%s changed bytes in the migration", rel)
		}
	}
	legacy, err := filepath.Glob(filepath.Join(root, "palace", "*", ".local", "embed-cache"))
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 0 {
		t.Errorf("legacy caches survived: %v", legacy)
	}
	for _, s := range []string{"husk1", "husk2", "crash"} {
		if ecExists(root, "palace/"+s) {
			t.Errorf("palace/%s must be healed away", s)
		}
	}
	for _, s := range []string{"real1", "real2"} {
		if !ecExists(root, "palace/"+s+"/drawers/general/general/drawers.jsonl") {
			t.Errorf("palace/%s lost its drawers", s)
		}
	}
}
