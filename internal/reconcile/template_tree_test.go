// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpcontext "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// seedOverride writes data to the vault Templates/ target for embeddedRel and
// nothing else: no host-local state records anything about it. Returns the
// absolute target path and the vault-relative key.
func seedOverride(t *testing.T, root, embeddedRel string, data []byte) (target, key string) {
	t.Helper()
	key = "Templates/" + embeddedRel
	target = filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return target, key
}

// earlierRestart is the committed earlier-version fixture: the version of
// commands/restart.md just before its current one (templates package,
// TestEarlierFixtureIsAShippedVersion pins it).
func earlierRestart(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "templates", "testdata", "earlier", "commands", "restart.md"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertNoLock fails if the retired templates.lock exists under root.
func assertNoLock(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".vibe-palace", "templates.lock")); !os.IsNotExist(err) {
		t.Errorf("a templates.lock was written (err=%v)", err)
	}
}

func shaBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// findAction returns the first Action whose Target has the given suffix.
func findAction(p Plan, suffix string) (Action, bool) {
	for _, a := range p.Actions {
		if strings.HasSuffix(a.Target, suffix) {
			return a, true
		}
	}
	return Action{}, false
}

func TestTemplateTree_Metadata(t *testing.T) {
	r := NewTemplateTree(t.TempDir(), "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	if r.Name() != "TemplateTree:Templates" {
		t.Errorf("Name = %q", r.Name())
	}
	if r.Tier() != TierVault {
		t.Errorf("Tier = %s", r.Tier())
	}

	p := NewTemplateTree(t.TempDir(), "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	if p.Name() != "TemplateTree:Projects/foo" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.Tier() != TierProject {
		t.Errorf("Tier = %s", p.Tier())
	}
}

// TestTemplateTree_MaterializeFreshVault pins the Design B (override-only)
// contract: a fresh vault gets NO Templates/ mirror — the embedded floor
// serves every resource directly over MCP. Plan must emit only "served from
// embedded floor" (ActionUnchanged) rows, Apply must write nothing (Created
// == 0, Pruned == 0): no file under Templates/, no lock, no .gitignore.
func TestTemplateTree_MaterializeFreshVault(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) == 0 {
		t.Fatal("expected served-from-embedded actions, got none")
	}
	for _, a := range plan.Actions {
		if a.Kind != ActionUnchanged {
			t.Errorf("fresh vault expected Unchanged (served from embedded), got %s for %s", a.Kind, a.Target)
		}
		if !strings.Contains(a.Summary, "served from embedded floor") {
			t.Errorf("summary = %q, want 'served from embedded floor'", a.Summary)
		}
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("Apply errors: %v", rep.Errors)
	}
	if rep.Created != 0 || rep.Updated != 0 || rep.Pruned != 0 {
		t.Errorf("fresh vault must produce zero writes/prunes, got %+v", rep)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Apply wrote into a fresh vault: %v", names)
	}
	assertNoLock(t, root)

	// The embedded floor still resolves every resource (e.g. commands/wrap).
	content, source, err := vpcontext.NewResolver(root).Resolve("command:wrap", "")
	if err != nil {
		t.Fatalf("resolve command:wrap: %v", err)
	}
	if source != "embedded" || content == "" {
		t.Errorf("command:wrap resolved from %q (%d bytes), want embedded", source, len(content))
	}
}

// embeddedBytesForRel returns the embedded bytes for a templates-root
// relative path, failing if absent.
func embeddedBytesForRel(t *testing.T, embeddedRel string) []byte {
	t.Helper()
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	for _, res := range resources {
		if res.RelPath == embeddedRel {
			return res.Bytes
		}
	}
	t.Fatalf("no embedded resource %q", embeddedRel)
	return nil
}

// TestTemplateTree_PruneMirrorKeepOverride is the focused Design B
// acceptance test, with no lock anywhere: in one vault holding a mirror, an
// earlier shipped version and a genuine override, Plan prunes the first two
// and keeps the override, and after Apply the pruned resources resolve from
// the embedded floor while the override still wins from the vault.
func TestTemplateTree_PruneMirrorKeepOverride(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	wrapTarget, _ := seedOverride(t, root, "commands/wrap.md", embeddedBytesForRel(t, "commands/wrap.md"))
	restartTarget, _ := seedOverride(t, root, "commands/restart.md", earlierRestart(t))
	captureBytes := []byte("# MY CUSTOM CAPTURE\n")
	captureTarget, _ := seedOverride(t, root, "commands/capture.md", captureBytes)

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for suffix, want := range map[string]ActionKind{
		filepath.Join("Templates", "commands", "wrap.md"):    ActionDelete,
		filepath.Join("Templates", "commands", "restart.md"): ActionDelete,
		filepath.Join("Templates", "commands", "capture.md"): ActionUnchanged,
	} {
		if a, _ := findAction(plan, suffix); a.Kind != want {
			t.Errorf("%s planned %s, want %s", suffix, a.Kind, want)
		}
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 2 {
		t.Errorf("Pruned = %d, want 2", rep.Pruned)
	}
	for _, gone := range []string{wrapTarget, restartTarget} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s not pruned (err=%v)", gone, err)
		}
		for _, side := range []string{".bak", ".new"} {
			if _, err := os.Stat(gone + side); !os.IsNotExist(err) {
				t.Errorf("%s%s written (err=%v)", gone, side, err)
			}
		}
	}
	if got, err := os.ReadFile(captureTarget); err != nil || string(got) != string(captureBytes) {
		t.Errorf("override not preserved: got=%q err=%v", got, err)
	}
	assertNoLock(t, root)

	res := vpcontext.NewResolver(root)
	for _, name := range []string{"command:wrap", "command:restart"} {
		if _, src, err := res.Resolve(name, ""); err != nil || src != "embedded" {
			t.Errorf("%s src=%q err=%v, want embedded", name, src, err)
		}
	}
	if content, src, err := res.Resolve("command:capture", ""); err != nil || src != "vault" || content != string(captureBytes) {
		t.Errorf("command:capture src=%q content=%q err=%v, want vault + override bytes", src, content, err)
	}
}

func TestTemplateTree_ScaffoldFresh(t *testing.T) {
	root := t.TempDir()
	// Vault root must exist; Projects/foo need not.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Should contain Create for two dirs + two READMEs = 4 Create actions.
	creates := 0
	for _, a := range plan.Actions {
		if a.Kind == ActionCreate {
			creates++
		}
	}
	if creates != 4 {
		t.Errorf("expected 4 Create actions, got %d: %+v", creates, plan.Actions)
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("apply errors: %v", rep.Errors)
	}

	for _, kind := range []string{"commands", "skills"} {
		dir := filepath.Join(root, "Projects", "foo", kind)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("missing dir %s: %v", dir, err)
		}
		readme := filepath.Join(dir, "README.md")
		data, err := os.ReadFile(readme)
		if err != nil {
			t.Errorf("missing README %s: %v", readme, err)
		}
		if len(data) == 0 {
			t.Errorf("empty README %s", readme)
		}
	}

	// No stray files.
	var others []string
	_ = filepath.Walk(filepath.Join(root, "Projects", "foo"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// README stubs and the managed .surface stamp sidecar are expected.
		if base := filepath.Base(p); base == "README.md" || base == ".surface" {
			return nil
		}
		others = append(others, p)
		return nil
	})
	if len(others) != 0 {
		t.Errorf("unexpected non-README files: %v", others)
	}
}

func TestTemplateTree_ScaffoldExistingOverrides(t *testing.T) {
	root := t.TempDir()
	// Pre-populate with a user override file.
	cmdDir := filepath.Join(root, "Projects", "foo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(cmdDir, "myoverride.md")
	userBytes := []byte("# mine\n")
	if err := os.WriteFile(userFile, userBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// Also an existing README the user crafted.
	userReadme := filepath.Join(cmdDir, "README.md")
	if err := os.WriteFile(userReadme, []byte("my readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	// User files untouched.
	data, _ := os.ReadFile(userFile)
	if string(data) != string(userBytes) {
		t.Errorf("user override clobbered: %s", data)
	}
	rm, _ := os.ReadFile(userReadme)
	if string(rm) != "my readme" {
		t.Errorf("user README clobbered: %s", rm)
	}
	// skills/ was missing, got created + stub.
	skillsReadme := filepath.Join(root, "Projects", "foo", "skills", "README.md")
	if _, err := os.Stat(skillsReadme); err != nil {
		t.Errorf("skills README not created: %v", err)
	}

	// Second run: all Unchanged.
	plan2, _ := r.Plan(context.Background())
	for _, a := range plan2.Actions {
		if a.Kind == ActionCreate {
			t.Errorf("second run should not Create, got %+v", a)
		}
	}
}

// TestTemplateTree_ScaffoldIdempotent runs scaffold twice back-to-back
// and asserts the second run emits only Unchanged actions (no Create
// spam). Paired with the existing ScaffoldExistingOverrides test this
// pins down scaffold-mode idempotence on both fresh and populated
// layouts.
func TestTemplateTree_ScaffoldIdempotent(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})

	plan1, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan1); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	plan2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	for _, a := range plan2.Actions {
		if a.Kind == ActionCreate {
			t.Errorf("second run should not Create %s (%s)", a.Target, a.Summary)
		}
	}
	rep, err := r.Apply(context.Background(), plan2)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if rep.Created != 0 {
		t.Errorf("second run Created = %d, want 0", rep.Created)
	}
}

// TestTemplateTree_ScaffoldLeavesUserReadmeUntouched pre-populates an
// existing README.md under the scaffold path and asserts scaffold-mode
// leaves it byte-identical (existence = present = Unchanged).
func TestTemplateTree_ScaffoldLeavesUserReadmeUntouched(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, "Projects", "foo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userReadme := []byte("# my hand-written README\n\nnot the stub\n")
	readmePath := filepath.Join(cmdDir, "README.md")
	if err := os.WriteFile(readmePath, userReadme, 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read user readme: %v", err)
	}
	if string(got) != string(userReadme) {
		t.Errorf("user README clobbered:\n got: %q\nwant: %q", got, userReadme)
	}
}

func TestTemplateTree_CheckMaterialize(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	results := r.Check(context.Background())
	if len(results) == 0 {
		t.Fatal("no check results")
	}
	// Fresh vault: all Pass (served from embedded floor) — no mirror is the
	// healthy override-only state. We only assert the row naming here.
	for _, res := range results {
		if !strings.HasPrefix(res.Name, "TemplateTree:Templates:") {
			t.Errorf("name prefix: %s", res.Name)
		}
	}

	// After Apply (a no-op on a fresh vault), all Pass.
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	results = r.Check(context.Background())
	for _, res := range results {
		if res.Status != 0 { // check.Pass == 0
			t.Errorf("expected Pass, got %v for %s: %s", res.Status, res.Name, res.Summary)
		}
	}
}

func TestTemplateTree_CheckScaffold(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Projects/bar", TemplateTreeSeed{Mode: TemplateModeScaffold})
	results := r.Check(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate check row, got %d", len(results))
	}
	// Create then check.
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	results = r.Check(context.Background())
	if results[0].Status != 0 {
		t.Errorf("expected Pass after Apply, got %v: %s", results[0].Status, results[0].Summary)
	}
}
