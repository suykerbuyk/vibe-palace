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

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

func shaBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// restoreEmbeddedSHA returns a cleanup func that restores the original
// templates.EmbeddedSHA after a test override.
func restoreEmbeddedSHA(t *testing.T) func() {
	t.Helper()
	orig := templates.EmbeddedSHA
	return func() { templates.EmbeddedSHA = orig }
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
	if got := r.Requires(); len(got) != 1 || got[0] != "Vault" {
		t.Errorf("Requires = %v", got)
	}

	p := NewTemplateTree(t.TempDir(), "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	if p.Name() != "TemplateTree:Projects/foo" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.Tier() != TierProject {
		t.Errorf("Tier = %s", p.Tier())
	}
}

func TestTemplateTree_MaterializeFreshVault(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Every action should be Create on a fresh vault.
	if len(plan.Actions) == 0 {
		t.Fatal("expected Create actions, got none")
	}
	for _, a := range plan.Actions {
		if a.Kind != ActionCreate {
			t.Errorf("expected Create, got %s for %s", a.Kind, a.Target)
		}
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("Apply errors: %v", rep.Errors)
	}
	if rep.Created == 0 {
		t.Error("Created counter is zero")
	}

	// Lock file exists with entries.
	lock, err := templates.ReadLock(root)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if len(lock.Entries) == 0 {
		t.Error("lock entries empty after materialize")
	}
	// Gitignore has canonical patterns.
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), "*.bak") || !strings.Contains(string(data), "*.new") {
		t.Errorf("gitignore missing sidecar patterns:\n%s", data)
	}

	// Idempotent re-run: every action Unchanged.
	plan2, _ := r.Plan(context.Background())
	for _, a := range plan2.Actions {
		if a.Kind != ActionUnchanged {
			t.Errorf("re-run expected Unchanged, got %s for %s", a.Kind, a.Target)
		}
	}
	rep2, err := r.Apply(context.Background(), plan2)
	if err != nil {
		t.Fatalf("Apply re-run: %v", err)
	}
	if rep2.Created != 0 || rep2.Updated != 0 {
		t.Errorf("re-run should not create/update: %+v", rep2)
	}
}

func TestTemplateTree_BinaryBumpedUserUntouched(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	// Simulate a binary bump: override EmbeddedSHA for wrap.md.
	defer restoreEmbeddedSHA(t)()
	orig := templates.EmbeddedSHA
	templates.EmbeddedSHA = func(rel string) (string, bool) {
		if rel == "commands/wrap.md" {
			return strings.Repeat("a", 64), true
		}
		return orig(rel)
	}

	plan2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := findAction(plan2, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatal("no action for wrap.md")
	}
	if a.Kind != ActionUpdate {
		t.Errorf("expected Update, got %s", a.Kind)
	}

	target := filepath.Join(root, "Templates", "commands", "wrap.md")
	rep, err := r.Apply(context.Background(), plan2)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Updated == 0 {
		t.Error("Updated == 0")
	}
	if _, err := os.Stat(target + ".bak"); err != nil {
		t.Errorf(".bak not written: %v", err)
	}

	// Lock refreshed with the bumped SHA.
	lock, _ := templates.ReadLock(root)
	e, ok := lock.Entries["Templates/commands/wrap.md"]
	if !ok {
		t.Fatal("no lock entry after update")
	}
	if e.EmbeddedSHA != strings.Repeat("a", 64) {
		t.Errorf("lock SHA not refreshed: %s", e.EmbeddedSHA)
	}
}

func TestTemplateTree_UserEditedBinaryStable(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	// User edits wrap.md.
	target := filepath.Join(root, "Templates", "commands", "wrap.md")
	if err := os.WriteFile(target, []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan2, _ := r.Plan(context.Background())
	a, ok := findAction(plan2, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatal("no action for wrap.md")
	}
	if a.Kind != ActionUnchanged {
		t.Errorf("expected Unchanged, got %s", a.Kind)
	}
}

func TestTemplateTree_BothDivergedPrompt(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	// User edit.
	target := filepath.Join(root, "Templates", "commands", "wrap.md")
	userBytes := []byte("user edit\n")
	if err := os.WriteFile(target, userBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// Binary bump.
	defer restoreEmbeddedSHA(t)()
	orig := templates.EmbeddedSHA
	bumped := strings.Repeat("b", 64)
	templates.EmbeddedSHA = func(rel string) (string, bool) {
		if rel == "commands/wrap.md" {
			return bumped, true
		}
		return orig(rel)
	}

	plan2, _ := r.Plan(context.Background())
	a, ok := findAction(plan2, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatal("missing action")
	}
	if a.Kind != ActionPrompt {
		t.Fatalf("expected Prompt, got %s", a.Kind)
	}
	// Details carry all three SHAs.
	want := map[string]string{
		"embedded_sha": bumped,
		"vault_sha":    shaBytes(userBytes),
	}
	got := map[string]string{}
	for _, d := range a.Details {
		k, v, ok := strings.Cut(d, "=")
		if !ok {
			t.Errorf("malformed detail %q", d)
		}
		got[k] = v
	}
	if got["embedded_sha"] != want["embedded_sha"] {
		t.Errorf("embedded_sha mismatch: got %q want %q", got["embedded_sha"], want["embedded_sha"])
	}
	if got["vault_sha"] != want["vault_sha"] {
		t.Errorf("vault_sha mismatch: got %q want %q", got["vault_sha"], want["vault_sha"])
	}
	if got["lock_sha"] == "" {
		t.Error("lock_sha should be non-empty for this row")
	}

	// Apply MUST reject Prompt.
	if _, err := r.Apply(context.Background(), plan2); err == nil {
		t.Error("Apply should reject ActionPrompt")
	}
}

func TestTemplateTree_SilentAdoptOnPopulatedVault(t *testing.T) {
	root := t.TempDir()
	// Pre-populate Templates/ manually with the embedded bytes, but no
	// lock file.
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range resources {
		target := filepath.Join(root, "Templates", filepath.FromSlash(res.RelPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, res.Bytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// No Prompts; every resource Unchanged.
	for _, a := range plan.Actions {
		if a.Kind == ActionPrompt {
			t.Errorf("unexpected Prompt on silent-adopt path: %s", a.Target)
		}
		if a.Kind != ActionUnchanged {
			t.Errorf("expected Unchanged, got %s for %s", a.Kind, a.Target)
		}
	}
	// Apply writes the lock.
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	lock, _ := templates.ReadLock(root)
	if len(lock.Entries) != len(resources) {
		t.Errorf("lock entries = %d, want %d", len(lock.Entries), len(resources))
	}
}

func TestTemplateTree_LockAbsentNonEmbeddedBytesPrompt(t *testing.T) {
	root := t.TempDir()
	// Pre-populate with NON-embedded bytes.
	resources, _ := templates.WalkEmbedded()
	target := filepath.Join(root, "Templates", filepath.FromSlash(resources[0].RelPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("not the embedded bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	plan, _ := r.Plan(context.Background())
	a, ok := findAction(plan, filepath.FromSlash(resources[0].RelPath))
	if !ok {
		t.Fatal("missing action")
	}
	if a.Kind != ActionPrompt {
		t.Errorf("expected Prompt, got %s", a.Kind)
	}
	// lock_sha should be empty.
	var lockSHA string
	for _, d := range a.Details {
		if strings.HasPrefix(d, "lock_sha=") {
			lockSHA = strings.TrimPrefix(d, "lock_sha=")
		}
	}
	if lockSHA != "" {
		t.Errorf("lock_sha should be empty, got %q", lockSHA)
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
		if filepath.Base(p) == "README.md" {
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

// TestTemplateTree_BakRotationReplacesStale exercises the edge case
// where a .bak file already exists before an auto-Update (Row 3 in the
// decision table). The reconciler must replace the stale .bak with the
// pre-update bytes — not preserve an older backup that predates this
// upgrade — so .bak is always a meaningful single-step undo.
func TestTemplateTree_BakRotationReplacesStale(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	plan, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	target := filepath.Join(root, "Templates", "commands", "wrap.md")
	priorVaultBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	// Plant a stale .bak with bytes that must NOT survive the Update.
	stale := []byte("STALE BACKUP CONTENT — must be replaced\n")
	if err := os.WriteFile(target+".bak", stale, 0o644); err != nil {
		t.Fatalf("write stale bak: %v", err)
	}

	// Trigger an auto-Update via an EmbeddedSHA bump for wrap.md.
	defer restoreEmbeddedSHA(t)()
	orig := templates.EmbeddedSHA
	templates.EmbeddedSHA = func(rel string) (string, bool) {
		if rel == "commands/wrap.md" {
			return strings.Repeat("c", 64), true
		}
		return orig(rel)
	}

	plan2, _ := r.Plan(context.Background())
	if _, err := r.Apply(context.Background(), plan2); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Target now has embedded bytes.
	newTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated target: %v", err)
	}
	if string(newTarget) != string(priorVaultBytes) {
		// Expected — the embedded bytes overwrote the prior vault bytes.
		// (We compare to the embedded corpus below.)
	}
	// Locate embedded bytes for wrap.md.
	var embedded []byte
	rs, _ := templates.WalkEmbedded()
	for _, res := range rs {
		if res.RelPath == "commands/wrap.md" {
			embedded = res.Bytes
			break
		}
	}
	if embedded == nil {
		t.Fatal("could not find commands/wrap.md in embedded corpus")
	}
	if string(newTarget) != string(embedded) {
		t.Errorf("target not written with embedded bytes")
	}

	// .bak must hold the PRIOR vault bytes, not the stale content.
	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatalf("read bak: %v", err)
	}
	if string(bak) == string(stale) {
		t.Error(".bak still holds stale pre-existing content; should have been replaced")
	}
	if string(bak) != string(priorVaultBytes) {
		t.Errorf(".bak != prior vault bytes")
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
	// Fresh vault: all Info (drift).
	for _, res := range results {
		if !strings.HasPrefix(res.Name, "TemplateTree:Templates:") {
			t.Errorf("name prefix: %s", res.Name)
		}
	}

	// After Apply, all Pass.
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
