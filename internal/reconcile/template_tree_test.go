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
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	vpcontext "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// seedOverride writes data to the vault target for embeddedRel and plants a
// templates.lock entry recording baselineSHA as the embedded baseline. It
// simulates a vault that already tracks a resource — either a genuine user
// override (data differs from baselineSHA's bytes) or a legacy
// reconciler-owned mirror (data hashes to baselineSHA). Returns the absolute
// target path and the vault-relative lock key.
func seedOverride(t *testing.T, root, embeddedRel string, data []byte, baselineSHA string) (target, key string) {
	t.Helper()
	key = "Templates/" + embeddedRel
	target = filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := templates.ReadLock(root)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if lock.Entries == nil {
		lock.Entries = map[string]templates.LockEntry{}
	}
	lock.Entries[key] = templates.LockEntry{EmbeddedSHA: baselineSHA, WrittenAt: time.Now().UTC()}
	if err := templates.WriteLock(root, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	return target, key
}

// embeddedSHAFor returns the current embedded SHA for a templates-root
// relative path, honoring any test override of templates.EmbeddedSHA.
func embeddedSHAFor(t *testing.T, embeddedRel string) string {
	t.Helper()
	sha, ok := templates.EmbeddedSHA(embeddedRel)
	if !ok {
		t.Fatalf("no embedded SHA for %q", embeddedRel)
	}
	return sha
}

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
// == 0, Pruned == 0), no file may land under Templates/, and the lock must
// end empty.
func TestTemplateTree_MaterializeFreshVault(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Every action is a non-writing "served from embedded floor" row.
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

	// No file was materialized under Templates/.
	if entries, err := os.ReadDir(filepath.Join(root, "Templates")); err == nil {
		var files []string
		for _, e := range entries {
			if e.Name() != ".surface" {
				files = append(files, e.Name())
			}
		}
		if len(files) != 0 {
			t.Errorf("fresh vault materialized files under Templates/: %v", files)
		}
	}

	// Lock ends empty — no reconciler-owned mirror is tracked.
	lock, err := templates.ReadLock(root)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if len(lock.Entries) != 0 {
		t.Errorf("lock should be empty on a fresh override-only vault, got %d entries", len(lock.Entries))
	}

	// Gitignore still gets the canonical sidecar patterns (a per-Apply
	// side effect independent of materialization).
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), "*.bak") || !strings.Contains(string(data), "*.new") {
		t.Errorf("gitignore missing sidecar patterns:\n%s", data)
	}

	// Idempotent re-run: still all Unchanged, still zero writes.
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
	if rep2.Created != 0 || rep2.Updated != 0 || rep2.Pruned != 0 {
		t.Errorf("re-run should not create/update/prune: %+v", rep2)
	}

	// The embedded floor still resolves every resource (e.g. commands/wrap).
	content, source, err := vpcontext.NewResolver(root).Resolve("command:wrap", "")
	if err != nil {
		t.Fatalf("resolve command:wrap: %v", err)
	}
	if source != "embedded" {
		t.Errorf("command:wrap resolved from %q, want embedded", source)
	}
	if content == "" {
		t.Error("command:wrap resolved empty content")
	}
}

// TestTemplateTree_PrunesByteIdenticalStaleLock covers the old relock
// scenario under Design B: a vault file whose bytes equal the CURRENT
// embedded bytes but whose lock baseline is stale is a redundant
// reconciler-owned mirror. Rather than refresh the lock (old ActionRelock),
// Plan now prunes it (ActionDelete) so the embedded floor serves it. The
// pruned bytes are backed up to .bak, the lock entry is dropped, and a
// second plan is idempotent.
func TestTemplateTree_PrunesByteIdenticalStaleLock(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	// Seed a byte-identical mirror of commands/wrap.md with a STALE lock
	// baseline (all-zeros) so vaultSHA == embSHA but vaultSHA != baseline.
	embBytes := embeddedBytesForRel(t, "commands/wrap.md")
	target, key := seedOverride(t, root, "commands/wrap.md", embBytes, strings.Repeat("0", 64))

	// Check reports drift (byte-identical mirror pending prune).
	if got := checkSummaryFor(r.Check(context.Background()), r.Name()+":"+key); !strings.HasPrefix(got, "drift") {
		t.Fatalf("pre-prune check summary = %q, want a drift row", got)
	}

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan (stale lock): %v", err)
	}
	a, ok := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatalf("no action for wrap.md")
	}
	if a.Kind != ActionDelete {
		t.Fatalf("expected ActionDelete (prune), got %s", a.Kind)
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply (prune): %v", err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("Apply errors: %v", rep.Errors)
	}
	if rep.Pruned != 1 {
		t.Errorf("Pruned = %d, want 1", rep.Pruned)
	}

	// The mirror file is gone; its bytes were preserved to .bak.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("pruned file still present (err=%v)", err)
	}
	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatalf("prune should back up bytes to .bak: %v", err)
	}
	if string(bak) != string(embBytes) {
		t.Error(".bak != pruned bytes")
	}

	// Lock entry was dropped.
	after, err := templates.ReadLock(root)
	if err != nil {
		t.Fatalf("ReadLock (post-prune): %v", err)
	}
	if _, ok := after.Entries[key]; ok {
		t.Errorf("lock still lists pruned key %q", key)
	}

	// Resolution now falls through to the embedded floor.
	_, source, err := vpcontext.NewResolver(root).Resolve("command:wrap", "")
	if err != nil {
		t.Fatalf("resolve command:wrap after prune: %v", err)
	}
	if source != "embedded" {
		t.Errorf("post-prune command:wrap resolved from %q, want embedded", source)
	}

	// Idempotent: a second plan routes the (now absent) key to Unchanged.
	plan2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan (post-prune): %v", err)
	}
	a2, ok := findAction(plan2, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatalf("no action for wrap.md post-prune")
	}
	if a2.Kind != ActionUnchanged {
		t.Errorf("post-prune expected ActionUnchanged, got %s", a2.Kind)
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

// checkSummaryFor returns the Summary of the check.Result with the given
// Name, or "" if absent.
func checkSummaryFor(results []check.Result, name string) string {
	for _, res := range results {
		if res.Name == name {
			return res.Summary
		}
	}
	return ""
}

// TestTemplateTree_BinaryBumpedUserUntouched: under Design B an embedded
// bump on a reconciler-owned mirror the user never edited (old Row 3
// auto-Update) becomes a PRUNE — the mirror still equals the lock baseline,
// so it is reconciler-owned and the (now-bumped) embedded floor serves the
// new version directly. No in-vault upgrade write happens.
func TestTemplateTree_BinaryBumpedUserUntouched(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	// Seed a reconciler-owned mirror: bytes == embedded, baseline == the
	// real embedded SHA (so it is provably unedited).
	embBytes := embeddedBytesForRel(t, "commands/wrap.md")
	baseline := embeddedSHAFor(t, "commands/wrap.md")
	target, key := seedOverride(t, root, "commands/wrap.md", embBytes, baseline)

	// Simulate a binary bump: override EmbeddedSHA for wrap.md.
	defer restoreEmbeddedSHA(t)()
	orig := templates.EmbeddedSHA
	templates.EmbeddedSHA = func(rel string) (string, bool) {
		if rel == "commands/wrap.md" {
			return strings.Repeat("a", 64), true
		}
		return orig(rel)
	}

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatal("no action for wrap.md")
	}
	if a.Kind != ActionDelete {
		t.Errorf("expected Delete (prune), got %s", a.Kind)
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pruned != 1 {
		t.Errorf("Pruned = %d, want 1", rep.Pruned)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("mirror not pruned (err=%v)", err)
	}
	if _, err := os.Stat(target + ".bak"); err != nil {
		t.Errorf("prune .bak not written: %v", err)
	}

	// Lock entry dropped — embedded floor now owns the resource.
	lock, _ := templates.ReadLock(root)
	if _, ok := lock.Entries[key]; ok {
		t.Errorf("lock still lists pruned key %q", key)
	}
}

// TestTemplateTree_UserEditedBinaryStable: a genuine user override (vault
// bytes differ from the lock baseline) with embedded stable is KEPT (Case 4)
// — not pruned. The override file and its lock entry survive Apply.
func TestTemplateTree_UserEditedBinaryStable(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	// Seed a genuine override: distinct bytes, baseline == the real
	// embedded SHA (embedded stable, user edited).
	userBytes := []byte("user edit\n")
	baseline := embeddedSHAFor(t, "commands/wrap.md")
	target, key := seedOverride(t, root, "commands/wrap.md", userBytes, baseline)

	plan, _ := r.Plan(context.Background())
	a, ok := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok {
		t.Fatal("no action for wrap.md")
	}
	if a.Kind != ActionUnchanged {
		t.Errorf("expected Unchanged (kept), got %s", a.Kind)
	}
	if !strings.Contains(a.Summary, "user override (kept)") {
		t.Errorf("summary = %q, want 'user override (kept)'", a.Summary)
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pruned != 0 {
		t.Errorf("override must not be pruned, Pruned = %d", rep.Pruned)
	}
	// Override file survives byte-for-byte.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("override clobbered/removed: %v", err)
	}
	if string(got) != string(userBytes) {
		t.Errorf("override bytes changed: %q", got)
	}
	// Lock entry for the override survives.
	lock, _ := templates.ReadLock(root)
	if _, ok := lock.Entries[key]; !ok {
		t.Errorf("lock entry for override %q dropped", key)
	}
	// The override wins resolution over the embedded floor.
	content, source, err := vpcontext.NewResolver(root).Resolve("command:wrap", "")
	if err != nil {
		t.Fatalf("resolve command:wrap: %v", err)
	}
	if source != "vault" {
		t.Errorf("override should resolve from vault, got %q", source)
	}
	if content != string(userBytes) {
		t.Errorf("resolved content = %q, want the override bytes", content)
	}
}

// TestTemplateTree_BothDivergedPrompt: a tracked override that is both
// user-edited (vault != baseline) AND embedded-bumped (embedded != baseline)
// is ambiguous → Prompt (Case 5), and Apply must reject the raw Prompt.
func TestTemplateTree_BothDivergedPrompt(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	// Seed a tracked override with baseline == real embedded SHA.
	userBytes := []byte("user edit\n")
	baseline := embeddedSHAFor(t, "commands/wrap.md")
	target, _ := seedOverride(t, root, "commands/wrap.md", userBytes, baseline)
	_ = target

	// Binary bump: embedded now differs from the baseline too.
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

// TestTemplateTree_SilentAdoptOnPopulatedVault: a legacy full-corpus vault
// (every embedded file mirrored byte-identically, no lock) is now PRUNED
// under Design B. The silent-adopt pre-pass plants a lock entry per
// byte-identical file, which flows straight into the prune case: every
// mirror file is deleted (backed up to .bak) and the lock ends empty. No
// Prompt is emitted on this path.
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
	// No Prompts; every byte-identical mirror is a prune.
	prunes := 0
	for _, a := range plan.Actions {
		if a.Kind == ActionPrompt {
			t.Errorf("unexpected Prompt on silent-adopt path: %s", a.Target)
		}
		if a.Kind != ActionDelete {
			t.Errorf("expected Delete (prune), got %s for %s", a.Kind, a.Target)
			continue
		}
		prunes++
	}
	if prunes != len(resources) {
		t.Errorf("prune actions = %d, want %d", prunes, len(resources))
	}

	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pruned != len(resources) {
		t.Errorf("Pruned = %d, want %d", rep.Pruned, len(resources))
	}
	// Every mirror file is gone (with a .bak); the lock ends empty.
	for _, res := range resources {
		target := filepath.Join(root, "Templates", filepath.FromSlash(res.RelPath))
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("mirror %s not pruned (err=%v)", res.RelPath, err)
		}
		if _, err := os.Stat(target + ".bak"); err != nil {
			t.Errorf("prune .bak missing for %s: %v", res.RelPath, err)
		}
	}
	lock, _ := templates.ReadLock(root)
	if len(lock.Entries) != 0 {
		t.Errorf("lock entries = %d, want 0 after pruning byte-identical mirrors", len(lock.Entries))
	}
}

// TestTemplateTree_PruneMirrorKeepOverride is the focused Design B
// acceptance test: in one vault holding both a reconciler-owned mirror and a
// genuine override, Plan prunes the mirror and keeps the override, and after
// Apply the pruned resource resolves from the embedded floor while the
// override still wins from the vault.
func TestTemplateTree_PruneMirrorKeepOverride(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	// Mirror: commands/wrap.md byte-identical to embedded, baseline == its
	// real embedded SHA → reconciler-owned → prune.
	wrapEmb := embeddedBytesForRel(t, "commands/wrap.md")
	wrapBaseline := embeddedSHAFor(t, "commands/wrap.md")
	wrapTarget, wrapKey := seedOverride(t, root, "commands/wrap.md", wrapEmb, wrapBaseline)

	// Override: commands/restart.md with distinct bytes, baseline == its
	// real embedded SHA → genuine override → keep.
	restartBaseline := embeddedSHAFor(t, "commands/restart.md")
	restartBytes := []byte("# MY CUSTOM RESTART\n")
	restartTarget, restartKey := seedOverride(t, root, "commands/restart.md", restartBytes, restartBaseline)

	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	wrapAct, _ := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
	if wrapAct.Kind != ActionDelete {
		t.Errorf("mirror expected Delete, got %s", wrapAct.Kind)
	}
	restartAct, _ := findAction(plan, filepath.Join("Templates", "commands", "restart.md"))
	if restartAct.Kind != ActionUnchanged {
		t.Errorf("override expected Unchanged, got %s", restartAct.Kind)
	}

	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Mirror pruned; override survives.
	if _, err := os.Stat(wrapTarget); !os.IsNotExist(err) {
		t.Errorf("mirror not pruned (err=%v)", err)
	}
	if got, err := os.ReadFile(restartTarget); err != nil || string(got) != string(restartBytes) {
		t.Errorf("override not preserved: got=%q err=%v", got, err)
	}

	// Lock lists only the override.
	lock, _ := templates.ReadLock(root)
	if _, ok := lock.Entries[wrapKey]; ok {
		t.Errorf("lock still lists pruned mirror %q", wrapKey)
	}
	if _, ok := lock.Entries[restartKey]; !ok {
		t.Errorf("lock dropped override %q", restartKey)
	}

	// Resolution: pruned wrap → embedded; override restart → vault.
	res := vpcontext.NewResolver(root)
	if _, src, err := res.Resolve("command:wrap", ""); err != nil || src != "embedded" {
		t.Errorf("command:wrap src=%q err=%v, want embedded", src, err)
	}
	if content, src, err := res.Resolve("command:restart", ""); err != nil || src != "vault" || content != string(restartBytes) {
		t.Errorf("command:restart src=%q content=%q err=%v, want vault + override bytes", src, content, err)
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
		if after, ok0 := strings.CutPrefix(d, "lock_sha="); ok0 {
			lockSHA = after
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

// TestTemplateTree_BakRotationReplacesStale exercises the edge case where a
// .bak already exists before an ActionUpdate overwrite (the path the Prompt
// resolver routes a diverged override to when the user picks "overwrite").
// The reconciler must replace the stale .bak with the pre-update bytes — not
// preserve an older backup that predates this upgrade — so .bak is always a
// meaningful single-step undo. Under Design B the reconciler no longer emits
// ActionUpdate on its own, so we drive the Apply path with a hand-built
// ActionUpdate plan (equivalent to a resolved "overwrite" prompt).
func TestTemplateTree_BakRotationReplacesStale(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	// Pre-create a diverged override with prior bytes + a stale .bak.
	priorVaultBytes := []byte("# prior user override\n")
	target, _ := seedOverride(t, root, "commands/wrap.md", priorVaultBytes, strings.Repeat("d", 64))
	stale := []byte("STALE BACKUP CONTENT — must be replaced\n")
	if err := os.WriteFile(target+".bak", stale, 0o644); err != nil {
		t.Fatalf("write stale bak: %v", err)
	}

	// Apply an overwrite (ActionUpdate) directly — the resolved-prompt path.
	updatePlan := Plan{Actions: []Action{{Kind: ActionUpdate, Target: target, Summary: "overwrite"}}}
	rep, err := r.Apply(context.Background(), updatePlan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Updated != 1 {
		t.Errorf("Updated = %d, want 1", rep.Updated)
	}

	// Target now holds the embedded bytes.
	newTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated target: %v", err)
	}
	embedded := embeddedBytesForRel(t, "commands/wrap.md")
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
