// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// The Templates reconcile never writes a template, never prompts, and reads no
// host-local state: provenance is decided by the binary alone
// (templates.ClassifyVaultCopy). Its one file operation — the prune — removes
// only bytes that are provably vp's. These tests pin that at the reconciler;
// cmd/vp pins it through `vp config sync`.

// seedMirror writes the current embedded bytes for embeddedRel.
func seedMirror(t *testing.T, root, embeddedRel string) (target, key string) {
	t.Helper()
	return seedOverride(t, root, embeddedRel, embeddedBytesForRel(t, embeddedRel))
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

func materialize(root string, seed TemplateTreeSeed) *TemplateTreeReconciler {
	seed.Mode = TemplateModeMaterialize
	return NewTemplateTree(root, "Templates", seed)
}

// TestTemplateTree_PlanRows is one subtest per row of the provenance table
// (Plan §5.1), asserting Kind, Summary and every Detail.
func TestTemplateTree_PlanRows(t *testing.T) {
	wrap := embeddedBytesForRel(t, "commands/wrap.md")
	wrapKey := templates.ProvenanceKey(wrap)
	earlier := earlierRestart(t)
	crlf := bytes.ReplaceAll(wrap, []byte("\n"), []byte("\r\n"))
	const ref = "skills/code-digger/references/prompt-deep-dive.md"
	refBytes := embeddedBytesForRel(t, ref)

	type row struct {
		name    string
		rel     string // embedded relpath
		setup   func(t *testing.T, root string) TemplateTreeSeed
		kind    ActionKind
		summary func(root string) string
		details []string // nil = no Details expected
	}
	pending := func(key string, head []byte) func(t *testing.T, root string) TemplateTreeSeed {
		return func(t *testing.T, root string) TemplateTreeSeed {
			return TemplateTreeSeed{ExternalPrune: true, PendingRemovals: map[string][]byte{key: head}}
		}
	}
	put := func(rel string, body []byte) func(t *testing.T, root string) TemplateTreeSeed {
		return func(t *testing.T, root string) TemplateTreeSeed {
			seedOverride(t, root, rel, body)
			return TemplateTreeSeed{}
		}
	}
	none := func(t *testing.T, root string) TemplateTreeSeed { return TemplateTreeSeed{} }
	fixed := func(s string) func(string) string { return func(string) string { return s } }
	rows := []row{
		{
			name: "absent", rel: "commands/wrap.md", setup: none,
			kind:    ActionUnchanged,
			summary: fixed("Templates/commands/wrap.md served from embedded floor"),
		},
		{
			name: "absent root-level resource", rel: "workflow.md", setup: none,
			kind:    ActionUnchanged,
			summary: fixed("Templates/workflow.md served from embedded floor"),
		},
		{
			name: "pending, HEAD current", rel: "commands/wrap.md",
			setup: pending("Templates/commands/wrap.md", wrap),
			kind:  ActionDelete,
			summary: fixed("prune Templates/commands/wrap.md (removed from the worktree before this sync and not committed; " +
				"the committed copy is the current embedded copy)"),
			details: []string{"embedded_relpath=commands/wrap.md", "provenance=current", "vault_sha=", "key=" + wrapKey, "pending=true"},
		},
		{
			name: "pending, HEAD earlier", rel: "commands/restart.md",
			setup: pending("Templates/commands/restart.md", earlier),
			kind:  ActionDelete,
			summary: fixed("prune Templates/commands/restart.md (removed from the worktree before this sync and not committed; " +
				"the committed copy is an earlier shipped version of commands/restart.md)"),
			details: []string{"embedded_relpath=commands/restart.md", "provenance=earlier", "vault_sha=",
				"key=" + templates.ProvenanceKey(earlier), "pending=true"},
		},
		{
			name: "pending, HEAD operator, a command", rel: "commands/wrap.md",
			setup: pending("Templates/commands/wrap.md", []byte("# my wrap\n")),
			kind:  ActionUnchanged,
			summary: func(root string) string {
				return "Templates/commands/wrap.md removed from the worktree; the committed copy is operator content — " +
					"finish removing it with vp commands reset wrap (commits the removal; a backup an earlier reset " +
					"wrote stays beside it), or restore it with git -C " + root + " checkout HEAD -- Templates/commands/wrap.md"
			},
		},
		{
			name: "pending, HEAD operator, a skill reference", rel: ref,
			setup: pending("Templates/"+ref, []byte("# my reference\n")),
			kind:  ActionUnchanged,
			summary: func(root string) string {
				return "Templates/" + ref + " removed from the worktree; the committed copy is operator content — " +
					"finish removing it with vp skills reset code-digger/references/prompt-deep-dive.md (commits the " +
					"removal; a backup an earlier reset wrote stays beside it), or restore it with git -C " + root +
					" checkout HEAD -- Templates/" + ref
			},
		},
		{
			name: "pending, HEAD operator, root-level workflow.md", rel: "workflow.md",
			setup: pending("Templates/workflow.md", []byte("# my workflow\n")),
			kind:  ActionUnchanged,
			summary: func(root string) string {
				return "Templates/workflow.md removed from the worktree; the committed copy is operator content — " +
					"finish removing it with git -C " + root + " commit -m \"chore(templates): remove an override\" -- " +
					"Templates/workflow.md, or restore it with git -C " + root + " checkout HEAD -- Templates/workflow.md"
			},
		},
		{
			name: "current", rel: "commands/wrap.md",
			setup:   put("commands/wrap.md", wrap),
			kind:    ActionDelete,
			summary: fixed("prune Templates/commands/wrap.md (byte-identical, line endings aside, to the current embedded copy)"),
			details: []string{"embedded_relpath=commands/wrap.md", "provenance=current", "vault_sha=" + shaBytes(wrap), "key=" + wrapKey},
		},
		{
			name: "current skill reference", rel: ref,
			setup:   put(ref, refBytes),
			kind:    ActionDelete,
			summary: fixed("prune Templates/" + ref + " (byte-identical, line endings aside, to the current embedded copy)"),
			details: []string{"embedded_relpath=" + ref, "provenance=current", "vault_sha=" + shaBytes(refBytes), "key=" + templates.ProvenanceKey(refBytes)},
		},
		{
			name: "CRLF current", rel: "commands/wrap.md",
			setup:   put("commands/wrap.md", crlf),
			kind:    ActionDelete,
			summary: fixed("prune Templates/commands/wrap.md (byte-identical, line endings aside, to the current embedded copy)"),
			details: []string{"embedded_relpath=commands/wrap.md", "provenance=current", "vault_sha=" + shaBytes(crlf), "key=" + wrapKey},
		},
		{
			name: "earlier", rel: "commands/restart.md",
			setup: put("commands/restart.md", earlier),
			kind:  ActionDelete,
			summary: fixed("prune Templates/commands/restart.md (matched an earlier shipped version of commands/restart.md; " +
				"recoverable from vibe-palace history)"),
			details: []string{"embedded_relpath=commands/restart.md", "provenance=earlier", "vault_sha=" + shaBytes(earlier),
				"key=" + templates.ProvenanceKey(earlier)},
		},
		{
			name: "operator", rel: "commands/wrap.md",
			setup:   put("commands/wrap.md", []byte("# my wrap\n")),
			kind:    ActionUnchanged,
			summary: fixed("Templates/commands/wrap.md operator override of a built-in (kept)"),
		},
		{
			name: "operator root-level workflow.md", rel: "workflow.md",
			setup:   put("workflow.md", []byte("# my workflow\n")),
			kind:    ActionUnchanged,
			summary: fixed("Templates/workflow.md operator override of a built-in (kept)"),
		},
		{
			name: "a present copy wins over a pending entry", rel: "commands/wrap.md",
			setup: func(t *testing.T, root string) TemplateTreeSeed {
				seedOverride(t, root, "commands/wrap.md", []byte("# mine\n"))
				return TemplateTreeSeed{PendingRemovals: map[string][]byte{"Templates/commands/wrap.md": wrap}}
			},
			kind:    ActionUnchanged,
			summary: fixed("Templates/commands/wrap.md operator override of a built-in (kept)"),
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			r := materialize(root, tc.setup(t, root))
			plan, err := r.Plan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			a, ok := findAction(plan, filepath.Join("Templates", filepath.FromSlash(tc.rel)))
			if !ok {
				t.Fatalf("no action for %s", tc.rel)
			}
			if a.Kind != tc.kind {
				t.Errorf("kind = %s, want %s", a.Kind, tc.kind)
			}
			if want := tc.summary(root); a.Summary != want {
				t.Errorf("summary:\n got  %q\n want %q", a.Summary, want)
			}
			if strings.Join(a.Details, "|") != strings.Join(tc.details, "|") {
				t.Errorf("details:\n got  %q\n want %q", a.Details, tc.details)
			}
		})
	}

	t.Run("not reached directly", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need a privilege on Windows")
		}
		root := t.TempDir()
		elsewhere := filepath.Join(root, "Elsewhere")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(elsewhere, "wrap.md"), wrap, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "Templates"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(root, "Templates", "commands")); err != nil {
			t.Fatal(err)
		}
		plan, err := materialize(root, TemplateTreeSeed{}).Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		a, _ := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
		if a.Kind != ActionUnchanged || a.Summary != "Templates/commands/wrap.md is "+check.NotReachedDirectly {
			t.Errorf("action = %+v", a)
		}
		if reason := a.Detail("reason"); !strings.Contains(reason, "symlink (Templates/commands)") {
			t.Errorf("reason = %q", reason)
		}
		for _, x := range plan.Actions {
			if x.Kind == ActionDelete {
				t.Errorf("a Delete was planned through the link: %+v", x)
			}
		}
	})
}

// TestTemplateTree_PlanErrors: a path that cannot be inspected or read fails
// the Plan rather than guessing.
func TestTemplateTree_PlanErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-vault")
	if _, err := materialize(missing, TemplateTreeSeed{}).Plan(context.Background()); err == nil {
		t.Error("an unresolvable vault root planned")
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("unreadable-file fixture needs a non-root POSIX user")
	}
	root := t.TempDir()
	target, _ := seedOverride(t, root, "commands/wrap.md", []byte("x\n"))
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o644) })
	if _, err := materialize(root, TemplateTreeSeed{}).Plan(context.Background()); err == nil {
		t.Error("an unreadable override planned")
	}
}

// TestTemplateTree_LegacyLockIsUnread: a retired templates.lock whose entry
// records the override's own SHA. The lock-era case 2 read that as "vp wrote
// these bytes" and pruned the override; nothing reads the lock now, so the
// override is kept, and Apply leaves the lock byte-identical.
func TestTemplateTree_LegacyLockIsUnread(t *testing.T) {
	root := t.TempDir()
	mine := []byte("# my wrap override\n")
	target, _ := seedOverride(t, root, "commands/wrap.md", mine)
	lockPath := filepath.Join(root, ".vibe-palace", "templates.lock")
	lock := "[entries]\n  [entries.\"Templates/commands/wrap.md\"]\n    embedded_sha = \"" + shaBytes(mine) +
		"\"\n    written_at = 2026-01-01T00:00:00Z\n"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	r := materialize(root, TemplateTreeSeed{})
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := findAction(plan, filepath.Join("Templates", "commands", "wrap.md"))
	if a.Kind != ActionUnchanged || !strings.Contains(a.Summary, "operator override of a built-in (kept)") {
		t.Fatalf("action = %+v", a)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 || rep.Pruned != 0 {
		t.Fatalf("Apply: %v %+v", err, rep)
	}
	if got, _ := os.ReadFile(target); string(got) != string(mine) {
		t.Errorf("override changed: %q", got)
	}
	if got, _ := os.ReadFile(lockPath); string(got) != lock {
		t.Errorf("the lock was rewritten:\n%s", got)
	}
}

// TestTemplateTree_NearMissesOfShippedBytesNeverPlanADelete (Review L4): a
// random-bytes property test is vacuous — random bytes never hit a sha256.
// Mutate real shipped bytes instead. Every near miss is operator content;
// only a line-ending swap of the whole file is still vp's. A mutation that is
// a no-op on a file (no final newline to remove) is skipped for that file, so
// it cannot pass as "current" by accident (round-2 L7).
func TestTemplateTree_NearMissesOfShippedBytesNeverPlanADelete(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	corpora := []map[string][]byte{{}, {"commands/restart.md": earlierRestart(t)}}
	for _, res := range resources {
		corpora[0][res.RelPath] = res.Bytes
	}
	mutations := []struct {
		name   string
		mutate func([]byte) []byte
		want   ActionKind
	}{
		{"byte flip", func(b []byte) []byte { c := bytes.Clone(b); c[len(c)/2] ^= 0x01; return c }, ActionUnchanged},
		{"truncated by one byte", func(b []byte) []byte { return bytes.Clone(b[:len(b)-1]) }, ActionUnchanged},
		{"final newline added", func(b []byte) []byte { return append(bytes.Clone(b), '\n') }, ActionUnchanged},
		{"final newline removed", func(b []byte) []byte { return bytes.TrimSuffix(bytes.Clone(b), []byte("\n")) }, ActionUnchanged},
		{"UTF-8 BOM", func(b []byte) []byte { return append([]byte("\xef\xbb\xbf"), b...) }, ActionUnchanged},
		{"lone CR", func(b []byte) []byte { return append([]byte("\r"), b...) }, ActionUnchanged},
		{`\n -> \r\r\n`, func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\r\n")) }, ActionUnchanged},
		{"LF -> CRLF", func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")) }, ActionDelete},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			exercised := 0
			for _, corpus := range corpora {
				root := t.TempDir()
				want := map[string]bool{}
				for rel, b := range corpus {
					mut := m.mutate(b)
					if bytes.Equal(mut, b) {
						continue
					}
					seedOverride(t, root, rel, mut)
					want[rel] = true
				}
				exercised += len(want)
				if len(want) == 0 {
					continue
				}
				plan, err := materialize(root, TemplateTreeSeed{}).Plan(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range plan.Actions {
					switch a.Kind {
					case ActionUnchanged, ActionDelete, ActionSkip:
					default:
						t.Errorf("%s: planned a %s", a.Target, a.Kind)
					}
					rel := strings.TrimPrefix(filepath.ToSlash(a.Target), filepath.ToSlash(root)+"/Templates/")
					if want[rel] && a.Kind != m.want {
						t.Errorf("%s: planned %s, want %s (%s)", rel, a.Kind, m.want, a.Summary)
					}
				}
			}
			if exercised == 0 {
				t.Fatal("the mutation changed no file")
			}
		})
	}
}

// TestTemplateTree_ApplyRefusesCreateAndUpdate: an Update used to be what the
// `o` answer and --yes resolved a diverged override to, and it overwrote the
// operator's bytes. No Plan produces either kind now, so one reaching Apply is
// a caller defect: reported, never executed.
func TestTemplateTree_ApplyRefusesCreateAndUpdate(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	mine := []byte("# my wrap override\n")
	target, _ := seedOverride(t, root, "commands/wrap.md", mine)

	rep, err := r.Apply(context.Background(), Plan{Actions: []Action{
		{Kind: ActionCreate, Target: target, Summary: "create"},
		{Kind: ActionUpdate, Target: target, Summary: "overwrite"},
		{Kind: ActionSkip, Target: target, Summary: "skip"},
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
	if rep.Created != 0 || rep.Updated != 0 || rep.Skipped != 1 {
		t.Errorf("counters: %+v", rep)
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
// the one copy of the operator's bytes an earlier overwrite or reset left.
func TestTemplateTree_PruneLeavesExistingBakUntouched(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
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

// TestTemplateTree_PruneKeepsFileChangedSincePlan: an edit made between Plan
// and Apply fails PruneAccepts on the fresh re-read and is kept.
func TestTemplateTree_PruneKeepsFileChangedSincePlan(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	target, key := seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))

	hookRan := false
	orig := pruneBeforeRemoveHook
	pruneBeforeRemoveHook = func(string) { hookRan = true }
	t.Cleanup(func() { pruneBeforeRemoveHook = orig })

	edit := []byte("# operator edit made between plan and apply\n")
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
	if hookRan {
		t.Error("the removal step was reached: PruneAccepts should have kept it")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(edit) {
		t.Errorf("edit not kept: %q (err=%v)", got, err)
	}
	if len(rep.Notes) != 1 || rep.Notes[0] != key+" changed since plan; kept" {
		t.Errorf("Notes = %q, want the changed-since-plan line", rep.Notes)
	}
	assertNoLock(t, root)
}

// TestTemplateTree_PruneRemovesThroughCompareAndSet (Review L5): the edit
// lands AFTER PruneAccepts passed — from pruneBeforeRemoveHook, the one seam
// between the re-read and the remove. vaultfs.Delete's compare-and-set on the
// bytes that were accepted is what keeps it.
func TestTemplateTree_PruneRemovesThroughCompareAndSet(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	target, key := seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))

	edit := []byte("# an edit that lands after the accept\n")
	var hooked string
	orig := pruneBeforeRemoveHook
	pruneBeforeRemoveHook = func(k string) {
		hooked = k
		if err := os.WriteFile(target, edit, 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { pruneBeforeRemoveHook = orig })

	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if hooked != key {
		t.Fatalf("hook saw %q: PruneAccepts, not the compare-and-set, decided", hooked)
	}
	if rep.Pruned != 0 || rep.Skipped != 1 || len(rep.Notes) != 1 || rep.Notes[0] != key+" changed since plan; kept" {
		t.Errorf("rep = %+v", rep)
	}
	if got, _ := os.ReadFile(target); string(got) != string(edit) {
		t.Errorf("the edit was lost: %q", got)
	}
}

// TestTemplateTree_PruneOfFileRemovedDuringRemoveIsNotCounted: the file goes
// between the accept and the remove — vaultfs.Delete reports it not found.
func TestTemplateTree_PruneOfFileRemovedDuringRemoveIsNotCounted(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	target, _ := seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))
	orig := pruneBeforeRemoveHook
	pruneBeforeRemoveHook = func(string) { _ = os.Remove(target) }
	t.Cleanup(func() { pruneBeforeRemoveHook = orig })
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 || rep.Pruned != 0 || rep.Unchanged == 0 {
		t.Errorf("rep = %+v err=%v", rep, err)
	}
}

// TestTemplateTree_PruneRefusesAPathSwappedToASymlinkBeforeApply: between Plan
// and Apply, Templates/commands became a link to a directory holding the same
// bytes. Removing through it would delete a file this tree does not own.
func TestTemplateTree_PruneRefusesAPathSwappedToASymlinkBeforeApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))

	commandsDir := filepath.Join(root, "Templates", "commands")
	elsewhere := filepath.Join(root, "Elsewhere")
	if err := os.Rename(commandsDir, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, commandsDir); err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 0 || rep.Skipped != 1 || len(rep.Notes) != 1 ||
		rep.Notes[0] != "Templates/commands/wrap.md is no longer reached directly; kept" {
		t.Errorf("rep = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "wrap.md")); err != nil {
		t.Errorf("the linked file was removed: %v", err)
	}
}

// TestTemplateTree_ApplyWritesNoLockAndNoGitignore: Apply's only file
// operation is the prune. It used to rewrite .gitignore raw (overriding an
// operator's `s` on the Vault reconciler's top-up) and persist templates.lock.
func TestTemplateTree_ApplyWritesNoLockAndNoGitignore(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	seedMirror(t, root, "commands/wrap.md")
	seedOverride(t, root, "commands/restart.md", []byte("# mine\n"))
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 || rep.Pruned != 1 {
		t.Fatalf("Apply: %v %+v", err, rep)
	}
	assertNoLock(t, root)
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf(".gitignore written (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".vibe-palace")); !os.IsNotExist(err) {
		t.Errorf(".vibe-palace/ created (err=%v)", err)
	}
}

// TestTemplateTree_EarlierPruneLeavesAnAuditNote: an earlier-version prune
// keeps no backup, so the outcome line is the record of what went.
func TestTemplateTree_EarlierPruneLeavesAnAuditNote(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	target, _ := seedOverride(t, root, "commands/restart.md", earlierRestart(t))
	seedMirror(t, root, "commands/wrap.md")
	plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "restart.md"))
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 || rep.Pruned != 2 {
		t.Fatalf("Apply: %v %+v", err, rep)
	}
	want := "pruned Templates/commands/restart.md (earlier shipped version of commands/restart.md; no backup)"
	if len(rep.Notes) != 1 || rep.Notes[0] != want {
		t.Errorf("Notes = %q, want only %q (a current mirror needs no note)", rep.Notes, want)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("not pruned: %v", err)
	}
	if matches, _ := filepath.Glob(target + "*"); len(matches) != 0 {
		t.Errorf("something was left beside the pruned file: %v", matches)
	}
}

// TestTemplateTree_PruneOfAlreadyGoneFileIsNotCounted: a concurrent sync (or a
// manual rm) that removed the mirror first leaves nothing for this run to
// remove; `pruned=N` counts only paths this run actually removed.
func TestTemplateTree_PruneOfAlreadyGoneFileIsNotCounted(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	target, _ := seedMirror(t, root, "commands/wrap.md")
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
}

// TestTemplateTree_PruneApplyErrors: a file that cannot be read at Apply, or a
// vault root that no longer resolves, is an error, never a removal.
func TestTemplateTree_PruneApplyErrors(t *testing.T) {
	t.Run("vault root gone", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "vault")
		r := materialize(root, TemplateTreeSeed{})
		seedMirror(t, root, "commands/wrap.md")
		plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		rep, err := r.Apply(context.Background(), plan)
		if err != nil || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "prune inspect") {
			t.Errorf("rep = %+v err=%v", rep, err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("unreadable-file fixture needs a non-root POSIX user")
		}
		root := t.TempDir()
		r := materialize(root, TemplateTreeSeed{})
		target, _ := seedMirror(t, root, "commands/wrap.md")
		plan := mustPlanDelete(t, r, filepath.Join("Templates", "commands", "wrap.md"))
		if err := os.Chmod(target, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(target, 0o644) })
		rep, err := r.Apply(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "prune read") || rep.Pruned != 0 {
			t.Errorf("rep = %+v", rep)
		}
		if _, err := os.Lstat(target); err != nil {
			t.Errorf("removed: %v", err)
		}
	})
}

// TestTemplateTree_ExternalPruneLeavesDeletesToTheCaller: on a git vault the
// reconciler does not remove a mirror — the caller does, once it has checked
// HEAD and the remotes — and records nothing about it.
func TestTemplateTree_ExternalPruneLeavesDeletesToTheCaller(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{ExternalPrune: true})
	wrap, _ := seedMirror(t, root, "commands/wrap.md")
	restart, _ := seedOverride(t, root, "commands/restart.md", earlierRestart(t))
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: %v %v", err, rep.Errors)
	}
	if rep.Pruned != 0 || len(rep.Notes) != 0 {
		t.Errorf("rep = %+v", rep)
	}
	for _, p := range []string{wrap, restart} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the reconciler removed %s, which it was told to leave: %v", p, err)
		}
	}
	assertNoLock(t, root)
}

// TestPruneAccepts is the one accept rule: vp-shipped bytes for the built-in
// the Delete names, and nothing for a Delete that names none.
func TestPruneAccepts(t *testing.T) {
	prune := func(rel string) Action {
		return Action{Kind: ActionDelete, Details: []string{"embedded_relpath=" + rel}}
	}
	wrap := embeddedBytesForRel(t, "commands/wrap.md")
	cases := []struct {
		name    string
		a       Action
		content []byte
		want    bool
	}{
		{"current", prune("commands/wrap.md"), wrap, true},
		{"CRLF current", prune("commands/wrap.md"), bytes.ReplaceAll(wrap, []byte("\n"), []byte("\r\n")), true},
		{"earlier", prune("commands/restart.md"), earlierRestart(t), true},
		{"operator", prune("commands/wrap.md"), []byte("# mine\n"), false},
		{"another built-in's bytes", prune("commands/restart.md"), wrap, false},
		{"no embedded_relpath", Action{Kind: ActionDelete, Details: []string{"provenance=current"}}, wrap, false},
		{"the retired-lock removal", Action{Kind: ActionDelete, Details: []string{"retired_lock=true", "vault_sha=" + shaBytes(wrap)}}, wrap, false},
	}
	for _, tc := range cases {
		if got := PruneAccepts(tc.a, tc.content); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTemplateTree_PruneWithoutEmbeddedRelpathIsRefused: a Delete that names
// no built-in cannot prove the bytes are vp's, so it is not made.
func TestTemplateTree_PruneWithoutEmbeddedRelpathIsRefused(t *testing.T) {
	root := t.TempDir()
	r := materialize(root, TemplateTreeSeed{})
	target, _ := seedMirror(t, root, "commands/wrap.md")
	rep, err := r.Apply(context.Background(), Plan{Actions: []Action{{Kind: ActionDelete, Target: target, Summary: "prune"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0].Error(), "no embedded_relpath") {
		t.Errorf("Errors = %v, want the no-embedded_relpath refusal", rep.Errors)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("file removed without proof: %v", err)
	}
}

// TestTemplateTree_OperatorBytesNeverPlanADelete covers every embedded
// resource — root-level workflow.md, doctrine.md, resume.md and enrichment.md
// take the same code path as commands and skills. Operator bytes are kept
// (Unchanged): never pruned, never prompted.
func TestTemplateTree_OperatorBytesNeverPlanADelete(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var sawRootLevel bool
	root := t.TempDir()
	for _, res := range resources {
		seedOverride(t, root, res.RelPath, []byte("# operator override of "+res.RelPath+"\n"))
	}
	plan, err := materialize(root, TemplateTreeSeed{}).Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Actions {
		if a.Kind != ActionUnchanged || !strings.HasSuffix(a.Summary, "operator override of a built-in (kept)") {
			t.Errorf("%s planned %s: %s", a.Target, a.Kind, a.Summary)
		}
		if !strings.Contains(a.Target, string(filepath.Separator)+"commands"+string(filepath.Separator)) &&
			!strings.Contains(a.Target, string(filepath.Separator)+"skills"+string(filepath.Separator)) {
			sawRootLevel = true
		}
	}
	if !sawRootLevel {
		t.Error("fixture: no root-level resource was exercised")
	}
}
