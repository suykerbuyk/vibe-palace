// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// The Templates reconcile never writes a template, and its one file operation
// — the prune — removes only bytes that are provably vp's. These tests pin
// both halves at the reconciler; cmd/vp pins them through `vp config sync`.

// seedMirror writes the current embedded bytes for embeddedRel with a lock
// baseline at the current embedded SHA — the state an old binary's overwrite
// (or a reconciler-owned mirror) leaves, which plans a case-2 prune.
func seedMirror(t *testing.T, root, embeddedRel string) (target, key string) {
	t.Helper()
	return seedOverride(t, root, embeddedRel, embeddedBytesForRel(t, embeddedRel), embeddedSHAFor(t, embeddedRel))
}

func mustPlanDelete(t *testing.T, r *TemplateTreeReconciler, suffix string) Plan {
	t.Helper()
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a, ok := findAction(plan, suffix)
	if !ok || a.Kind != ActionDelete {
		t.Fatalf("fixture: %s should plan a Delete, got %+v", suffix, a)
	}
	return plan
}

// TestTemplateTree_ApplyRefusesCreateAndUpdate: an Update used to be what the
// `o` answer and --yes resolved a diverged override to, and it overwrote the
// operator's bytes. No Plan and no orchestrator answer produces either kind
// now, so one reaching Apply is a caller defect: reported, never executed.
func TestTemplateTree_ApplyRefusesCreateAndUpdate(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	mine := []byte("# my wrap override\n")
	target, _ := seedOverride(t, root, "commands/wrap.md", mine, strings.Repeat("e", 64))

	rep, err := r.Apply(context.Background(), Plan{Actions: []Action{
		{Kind: ActionCreate, Target: target, Summary: "create"},
		{Kind: ActionUpdate, Target: target, Summary: "overwrite"},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Errors) != 2 {
		t.Fatalf("Errors = %v, want one per refused action", rep.Errors)
	}
	for _, e := range rep.Errors {
		if !strings.Contains(e.Error(), "never writes a template") {
			t.Errorf("error does not say why: %v", e)
		}
	}
	if rep.Created != 0 || rep.Updated != 0 {
		t.Errorf("counters moved: %+v", rep)
	}
	if got, _ := os.ReadFile(target); string(got) != string(mine) {
		t.Errorf("override changed: %q", got)
	}
	for _, side := range []string{".bak", ".new"} {
		if _, err := os.Stat(target + side); !os.IsNotExist(err) {
			t.Errorf("%s written (err=%v)", side, err)
		}
	}
}

// TestTemplateTree_PruneLeavesExistingBakUntouched: a .bak beside a mirror is
// the one copy of the operator's bytes an earlier overwrite or upgrade reset
// left. The prune used to overwrite it with the mirror's (embedded) bytes.
func TestTemplateTree_PruneLeavesExistingBakUntouched(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	target, _ := seedMirror(t, root, "commands/wrap.md")
	operator := []byte("# the operator's override, kept by an earlier overwrite\n")
	if err := os.WriteFile(target+".bak", operator, 0o644); err != nil {
		t.Fatal(err)
	}

	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 1 {
		t.Fatalf("Pruned = %d, want 1", rep.Pruned)
	}
	if got, err := os.ReadFile(target + ".bak"); err != nil || string(got) != string(operator) {
		t.Errorf(".bak changed: %q (err=%v)", got, err)
	}
}

// TestTemplateTree_PruneKeepsFileChangedSincePlan: `vp config sync` can block
// on a prompt between Plan and Apply for as long as the operator likes. An
// edit made meanwhile must be kept — the plan-time hash is stale — and, with
// no prune .bak any more, removing it would lose it outright.
func TestTemplateTree_PruneKeepsFileChangedSincePlan(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	target, key := seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))

	edit := []byte("# operator edit made while the prompt waited\n")
	if err := os.WriteFile(target, edit, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 0 || rep.Skipped != 1 {
		t.Errorf("Pruned=%d Skipped=%d, want 0 and 1", rep.Pruned, rep.Skipped)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(edit) {
		t.Errorf("edit not kept: %q (err=%v)", got, err)
	}
	if len(rep.Notes) != 1 || rep.Notes[0] != key+" changed since plan; kept" {
		t.Errorf("Notes = %q, want the changed-since-plan line", rep.Notes)
	}
	lock, _ := templates.ReadLock(root)
	if _, ok := lock.Entries[key]; !ok {
		t.Errorf("lock entry for the kept file was dropped")
	}
}

// TestTemplateTree_PruneOfAlreadyGoneFileIsNotCounted: a concurrent sync (or a
// manual rm) that removed the mirror first leaves nothing for this run to
// remove. Its lock entry goes, and `pruned=N` does not count it — the summary
// counts only paths this run actually removed.
func TestTemplateTree_PruneOfAlreadyGoneFileIsNotCounted(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	target, key := seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 0 || rep.Unchanged == 0 {
		t.Errorf("Pruned=%d Unchanged=%d, want 0 and >0", rep.Pruned, rep.Unchanged)
	}
	lock, _ := templates.ReadLock(root)
	if _, ok := lock.Entries[key]; ok {
		t.Errorf("lock still lists %q", key)
	}
}

// TestTemplateTree_AbsentFileWithLockPlansADelete is case 1b: a lock entry
// whose file is gone is a prune whose removal may never have been committed
// (a git failure after the remove keeps the entry on purpose). It plans a
// Delete carrying the lock baseline, so the next sync on a git vault commits
// the removal or leaves it alone, instead of forgetting it.
func TestTemplateTree_AbsentFileWithLockPlansADelete(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	target, _ := seedMirror(t, root, "commands/wrap.md")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
	if !ok || a.Kind != ActionDelete || !strings.Contains(a.Summary, "already removed") {
		t.Fatalf("action = %+v", a)
	}
	if a.Detail("lock_sha") != embeddedSHAFor(t, "commands/wrap.md") {
		t.Errorf("lock_sha = %q", a.Detail("lock_sha"))
	}
}

// TestTemplateTree_ExternalPruneLeavesDeletesToTheCaller: on a git vault the
// reconciler neither removes a mirror nor drops its entry — the caller does,
// once it has checked HEAD — and ForgetPruned drops only the keys it is given.
func TestTemplateTree_ExternalPruneLeavesDeletesToTheCaller(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize, ExternalPrune: true})
	wrap, wrapKey := seedMirror(t, root, "commands/wrap.md")
	_, restartKey := seedMirror(t, root, "commands/restart.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 0 {
		t.Errorf("Pruned = %d", rep.Pruned)
	}
	if _, err := os.Stat(wrap); err != nil {
		t.Errorf("the reconciler removed a mirror it was told to leave: %v", err)
	}
	lock, _ := templates.ReadLock(root)
	if _, ok := lock.Entries[wrapKey]; !ok {
		t.Error("the entry was dropped before the outcome was known")
	}
	if err := r.ForgetPruned([]string{wrapKey}); err != nil {
		t.Fatal(err)
	}
	lock, _ = templates.ReadLock(root)
	if _, ok := lock.Entries[wrapKey]; ok {
		t.Error("ForgetPruned kept the entry")
	}
	if _, ok := lock.Entries[restartKey]; !ok {
		t.Error("ForgetPruned dropped an entry it was not given")
	}
	if err := r.ForgetPruned(nil); err != nil {
		t.Error(err)
	}
}

// TestPruneBasis is the one accept set: both SHAs, the embedded copy naming a
// shared SHA, and nothing for an empty value.
func TestPruneBasis(t *testing.T) {
	a := Action{Details: []string{"embedded_sha=e", "lock_sha=l"}}
	if b := PruneBasis(a); b["e"] != "current embedded copy" || b["l"] != "lock-recorded embedded version" || len(b) != 2 {
		t.Errorf("basis = %v", b)
	}
	if b := PruneBasis(Action{Details: []string{"embedded_sha=x", "lock_sha=x"}}); b["x"] != "current embedded copy" || len(b) != 1 {
		t.Errorf("shared SHA basis = %v", b)
	}
	if b := PruneBasis(Action{Details: []string{"embedded_sha=e", "lock_sha="}}); len(b) != 1 {
		t.Errorf("empty lock_sha contributed: %v", b)
	}
}

// TestTemplateTree_PruneWithoutPlanSHAIsRefused: a Delete that carries no SHA
// to re-hash against cannot prove the bytes are vp's, so it is not made.
func TestTemplateTree_PruneWithoutPlanSHAIsRefused(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
	target, _ := seedMirror(t, root, "commands/wrap.md")
	rep, err := r.Apply(context.Background(), Plan{Actions: []Action{{Kind: ActionDelete, Target: target, Summary: "prune"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "no SHA") {
		t.Errorf("Errors = %v, want the no-SHA refusal", rep.Errors)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("file removed without proof: %v", err)
	}
}

// TestTemplateTree_OperatorBytesNeverPlanADelete covers every embedded
// resource — root-level workflow.md, doctrine.md, resume.md and enrichment.md
// take the same code path as commands and skills. Operator bytes with no lock
// entry prompt; with a lock entry at the embedded SHA they are kept. Neither
// ever plans a Delete, and neither summary calls them anything but an override.
func TestTemplateTree_OperatorBytesNeverPlanADelete(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var sawRootLevel bool
	for _, withLock := range []bool{false, true} {
		root := t.TempDir()
		for _, res := range resources {
			body := []byte("# operator override of " + res.RelPath + "\n")
			if withLock {
				seedOverride(t, root, res.RelPath, body, embeddedSHAFor(t, res.RelPath))
				continue
			}
			target := filepath.Join(root, "Templates", filepath.FromSlash(res.RelPath))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})
		plan, err := r.Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := ActionPrompt
		if withLock {
			want = ActionUnchanged
		}
		for _, a := range plan.Actions {
			if a.Kind != want {
				t.Errorf("withLock=%v: %s planned %s, want %s", withLock, a.Target, a.Kind, want)
			}
			if !strings.Contains(a.Target, string(filepath.Separator)+"commands"+string(filepath.Separator)) &&
				!strings.Contains(a.Target, string(filepath.Separator)+"skills"+string(filepath.Separator)) {
				sawRootLevel = true
			}
		}
	}
	if !sawRootLevel {
		t.Error("fixture: no root-level resource was exercised")
	}
}
