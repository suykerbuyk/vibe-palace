// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/check"
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

// TestTemplateTree_CheckScaffold_ZeroLengthReadme seeds a crash-left,
// zero-length README directly (the file this task is about: a scaffold
// README observed at length zero because a writer never got to finish it)
// and asserts Check reports non-Pass (naming it empty, not missing) and
// Plan emits ActionUpdate — not the ActionUnchanged a stat-for-existence-only
// check used to emit — for it. This is the detection half of the fix.
func TestTemplateTree_CheckScaffold_ZeroLengthReadme(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, "Projects", "foo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(cmdDir, "README.md")
	if err := os.WriteFile(readme, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// skills/ is left absent so the only anomaly under test is the
	// zero-length commands/README.md; checkScaffold breaks on the first
	// non-Pass finding, so seed skills/ complete to isolate the assertion.
	skillsDir := filepath.Join(root, "Projects", "foo", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "README.md"), []byte(templates.RenderReadmeStub("skills")), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})

	results := r.Check(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate check row, got %d", len(results))
	}
	if results[0].Status == check.Pass {
		t.Errorf("expected non-Pass status for a zero-length README, got Pass: %s", results[0].Summary)
	}
	if !strings.Contains(results[0].Summary, "empty") {
		t.Errorf("expected summary naming the empty README, got: %s", results[0].Summary)
	}

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var gotUpdate bool
	for _, a := range plan.Actions {
		if a.Target != readme {
			continue
		}
		gotUpdate = true
		if a.Kind != ActionUpdate {
			t.Errorf("zero-length README plan action = %s, want Update: %+v", a.Kind, a)
		}
		if got := a.Detail("expected_sha256"); got != emptySHA256 {
			t.Errorf("expected_sha256 detail = %q, want %q", got, emptySHA256)
		}
	}
	if !gotUpdate {
		t.Fatalf("no plan action found for %s: %+v", readme, plan.Actions)
	}
}

// TestTemplateTree_ScaffoldRepairsZeroLengthReadme proves a crash-left
// zero-length README (this task's central scenario: a writer crashed, or was
// raced, between creating the file and writing its body) is detected as
// ActionUpdate and repaired — not left empty, not silently re-Created — on
// the next Apply, and that a further run is idempotent.
func TestTemplateTree_ScaffoldRepairsZeroLengthReadme(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, "Projects", "foo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(cmdDir, "README.md")
	if err := os.WriteFile(readme, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("apply errors: %v", rep.Errors)
	}
	if rep.Updated != 1 {
		t.Errorf("Updated = %d, want 1: %+v", rep.Updated, rep)
	}

	got, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	want := templates.RenderReadmeStub("commands")
	if string(got) != want {
		t.Errorf("repaired README content mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Second run: the repaired README is now canonical — idempotent, no
	// further Update.
	plan2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan2.Actions {
		if a.Target == readme && a.Kind != ActionUnchanged {
			t.Errorf("second plan action for repaired README = %s, want Unchanged: %+v", a.Kind, a)
		}
	}
	rep2, err := r.Apply(context.Background(), plan2)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Updated != 0 {
		t.Errorf("second run Updated = %d, want 0", rep2.Updated)
	}
}

// TestTemplateTree_ScaffoldRepairRefusedOnConflict proves the zero-length
// README repair is CAS-guarded against the empty-file sha256: content that
// landed in the window between Plan and Apply (no longer empty, and not the
// canonical stub either) is kept rather than clobbered, mirroring the
// existing prune's "changed since plan; kept" behaviour.
func TestTemplateTree_ScaffoldRepairRefusedOnConflict(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, "Projects", "foo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(cmdDir, "README.md")
	if err := os.WriteFile(readme, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// An operator (or another writer) lands real content in the window
	// between Plan and Apply — the exact race the CAS guard exists for.
	operatorContent := []byte("# my own notes, written between plan and apply\n")
	if err := os.WriteFile(readme, operatorContent, 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("apply errors: %v", rep.Errors)
	}
	if rep.Updated != 0 {
		t.Errorf("Updated = %d, want 0 (should be refused, not repaired)", rep.Updated)
	}
	if rep.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", rep.Skipped)
	}

	got, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(operatorContent) {
		t.Errorf("operator content clobbered: got %q, want %q", got, operatorContent)
	}
}

// TestTemplateTree_ScaffoldConcurrentApplyDoesNotDeadlock proves concurrent
// Apply calls racing to create/repair the same scaffold README complete
// rather than hang. ADR-003 ("RMW callers must not double-acquire") flags a
// same-path second vaultlock.Acquire in one process as a PERMANENT hang, not
// an error — an ordinary assertion cannot distinguish "slow" from
// "deadlocked", so this test bounds the race with a timeout: a genuine
// double-acquire blocks forever and fails the test via the timeout branch
// rather than a hang that never reports. It is a call-through proof that the
// new vaultfs.Create/vaultfs.Write call sites acquire the lock exactly once
// per call with no ambient lock held by the caller, not a deadlock
// simulation.
func TestTemplateTree_ScaffoldConcurrentApplyDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	const n = 8
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			r := NewTemplateTree(root, "Projects/race", TemplateTreeSeed{Mode: TemplateModeScaffold})
			plan, err := r.Plan(context.Background())
			if err != nil {
				done <- err
				return
			}
			_, err = r.Apply(context.Background(), plan)
			done <- err
		}()
	}

	timeout := time.After(10 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("concurrent Apply: %v", err)
			}
		case <-timeout:
			t.Fatalf("concurrent Apply did not complete within timeout — possible double lock-acquire hang")
		}
	}

	for _, kind := range []string{"commands", "skills"} {
		readme := filepath.Join(root, "Projects", "race", kind, "README.md")
		data, err := os.ReadFile(readme)
		if err != nil {
			t.Fatalf("missing README %s: %v", readme, err)
		}
		want := templates.RenderReadmeStub(kind)
		if string(data) != want {
			t.Errorf("README %s content mismatch after concurrent scaffold", readme)
		}
	}
}

// TestTemplateTree_ScaffoldSkipsNonPortableSlug pins the fix for a Projects/
// directory whose name is not portable across filesystems (e.g. contains ':',
// illegal on NTFS/exFAT). Before the fix, planScaffold still planned the
// directory Create and README Create for such a slug; applyScaffold's raw
// os.MkdirAll for the directory succeeded, then the vaultfs.Create README
// write was refused (ValidateRelPath rejects the segment), landing in
// rep.Errors and leaving an empty commands/skills/ dir behind. Now: one Skip
// per kind, no directory Create, exit clean.
func TestTemplateTree_ScaffoldSkipsNonPortableSlug(t *testing.T) {
	root := t.TempDir()
	// Mirrors the filed repro: a directory hand-created outside vp (a raw
	// mkdir bypasses slug.Validate entirely), not one vp itself produced.
	projDir := filepath.Join(root, "Projects", "a:b")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/a:b", TemplateTreeSeed{Mode: TemplateModeScaffold})
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("expected 2 actions (one Skip per kind), got %d: %+v", len(plan.Actions), plan.Actions)
	}
	for _, a := range plan.Actions {
		if a.Kind != ActionSkip {
			t.Errorf("action for %s: Kind = %s, want Skip", a.Target, a.Kind)
		}
		if !strings.Contains(a.Summary, "not portable") {
			t.Errorf("action for %s: Summary = %q, want it to mention \"not portable\"", a.Target, a.Summary)
		}
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("apply errors: %v", rep.Errors)
	}
	if rep.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", rep.Skipped)
	}
	if rep.Created != 0 {
		t.Errorf("Created = %d, want 0", rep.Created)
	}

	for _, kind := range []string{"commands", "skills"} {
		dir := filepath.Join(projDir, kind)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s should not have been created (err=%v)", dir, err)
		}
	}
}

// TestTemplateTree_ScaffoldSkipsSymlinkedDir pins the fix's other guard:
// vaultfs.CheckDirectPath catches a kind directory reached only through a
// symlink (planScaffold otherwise never checked this in scaffold mode, unlike
// materialize mode). The symlinked kind is Skipped and nothing is written
// through it; an unaffected sibling kind scaffolds normally.
func TestTemplateTree_ScaffoldSkipsSymlinkedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	projDir := filepath.Join(root, "Projects", "foo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(root, "Elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(projDir, "commands")); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	a, ok := findAction(plan, "commands")
	if !ok {
		t.Fatalf("no action for the symlinked commands dir: %+v", plan.Actions)
	}
	if a.Kind != ActionSkip {
		t.Errorf("commands action Kind = %s, want Skip", a.Kind)
	}
	if !strings.Contains(a.Summary, "not scaffolded") {
		t.Errorf("commands action Summary = %q, want it to mention \"not scaffolded\"", a.Summary)
	}
	// skills is unaffected: its normal 2 Create actions (dir + README) plus
	// the 1 Skip for commands = 3 actions total.
	if len(plan.Actions) != 3 {
		t.Errorf("expected 3 actions total, got %d: %+v", len(plan.Actions), plan.Actions)
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("apply errors: %v", rep.Errors)
	}
	if rep.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", rep.Skipped)
	}
	if rep.Created != 2 {
		t.Errorf("Created = %d, want 2 (skills dir + README)", rep.Created)
	}

	// Nothing was written through the symlink.
	entries, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("symlink target should be untouched, got entries: %v", entries)
	}

	// skills scaffolded normally.
	skillsReadme := filepath.Join(projDir, "skills", "README.md")
	if _, err := os.Stat(skillsReadme); err != nil {
		t.Errorf("skills README not created: %v", err)
	}
}
