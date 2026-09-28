// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// splitHostMany is splitHost for several slugs: the default vault records
// each as moved to the quantum vault, which holds them all.
func splitHostMany(t *testing.T, home string, slugs ...string) (global, quantum, cfg string) {
	t.Helper()
	global, quantum = splitHost(t, home, slugs[0])
	for _, s := range slugs[1:] {
		rebindWrite(t, filepath.Join(quantum, "Projects", s, "resume.md"), "# resume\n")
		if _, err := NewVault(global).RecordDeparture(s, departure.MovedToVault, splitLabel); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	return global, quantum, cfg
}

func batchReq(slugs ...string) BindVaultsRequest {
	return BindVaultsRequest{Slugs: slugs, VaultPath: "~/quantum-vault", Mode: BindMoved}
}

// Several slugs, ONE write: config.toml.bak is the pre-bind config, so a
// restore undoes the whole bind.
func TestBindProjectVaultsWritesEverySlugInOneWrite(t *testing.T) {
	home := rebindEnv(t)
	_, _, cfg := splitHostMany(t, home, "qa", "orch")
	pre := bindRead(t, cfg)

	rep, err := BindProjectVaults(batchReq("qa", "orch"))
	if err != nil {
		t.Fatal(err)
	}
	after := bindRead(t, cfg)
	for _, s := range []string{"qa", "orch"} {
		if !strings.Contains(after, s+" = \"~/quantum-vault\"") {
			t.Errorf("%s is not bound:\n%s", s, after)
		}
	}
	if got := bindRead(t, rep.BackupPath); got != pre {
		t.Fatalf("config.toml.bak is not the pre-bind config:\n%s\nwant:\n%s", got, pre)
	}
}

// All or nothing: one slug that cannot bind refuses the whole bind, and the
// config is untouched.
func TestBindProjectVaultsIsAllOrNothing(t *testing.T) {
	home := rebindEnv(t)
	_, quantum, cfg := splitHostMany(t, home, "qa")
	rebindWrite(t, filepath.Join(quantum, "Projects", "stray", "resume.md"), "# never left\n")
	pre := bindRead(t, cfg)
	if _, err := BindProjectVaults(batchReq("qa", "stray")); err == nil || !strings.Contains(err.Error(), `"stray"`) {
		t.Fatalf("err = %v, want stray refused", err)
	}
	if bindRead(t, cfg) != pre {
		t.Fatal("a refused batch wrote the config")
	}
}

// The compare-and-set: a writer that changes the config between the read and
// the write makes the bind refuse, and nothing of the bind is written.
func TestBindProjectVaultsRefusesAConcurrentWrite(t *testing.T) {
	home := rebindEnv(t)
	_, _, cfg := splitHostMany(t, home, "qa", "orch")
	theirs := bindRead(t, cfg) + "# another writer\n"
	bindBeforeWrite = func() { rebindWrite(t, cfg, theirs) }
	t.Cleanup(func() { bindBeforeWrite = func() {} })

	if _, err := BindProjectVaults(batchReq("qa", "orch")); err == nil || !strings.Contains(err.Error(), "changed while the write was being prepared") {
		t.Fatalf("err = %v, want the compare-and-set refusal", err)
	}
	if got := bindRead(t, cfg); got != theirs {
		t.Fatalf("the config holds %q, want the other writer's bytes untouched", got)
	}
}

// Rule 1 with the one fixture today's departure.Find lets through: the target
// holds a departure record for the slug AND real Projects/<slug> content.
func TestBindRefusesATargetWithARecordOverRealContent(t *testing.T) {
	home := rebindEnv(t)
	_, quantum, cfg := splitHostMany(t, home, "qa")
	// Real content stays; the target also records qa as departed.
	if _, _, err := NewVault(quantum).RecordDepartureForPurge("qa", departure.MovedToVault, "git@example.com:elsewhere/vault.git"); err != nil {
		t.Fatal(err)
	}
	if _, found := departure.Find(quantum, "qa"); found {
		t.Fatal("fixture: Find must NOT call this slug departed (the directory wins), or the test proves nothing")
	}
	pre := bindRead(t, cfg)
	if _, err := BindProjectVaults(batchReq("qa")); err == nil || !strings.Contains(err.Error(), "holds a departure record for it") {
		t.Fatalf("err = %v, want rule 1's refusal", err)
	}
	if bindRead(t, cfg) != pre {
		t.Fatal("the refused bind wrote the config")
	}
}

// vaultPath and checkRoot are apart: a dry run may check a scratch tree for a
// path that does not exist yet, and a real bind refuses unless they are one.
func TestBindProjectVaultsCheckRootIsApartFromVaultPath(t *testing.T) {
	home := rebindEnv(t)
	_, quantum, cfg := splitHostMany(t, home, "qa")
	pre := bindRead(t, cfg)
	final := filepath.Join(home, "not-yet-cloned")
	req := BindVaultsRequest{Slugs: []string{"qa"}, VaultPath: final, CheckRoot: quantum, Mode: BindMoved, DryRun: true}
	rep, err := BindProjectVaults(req)
	if err != nil {
		t.Fatalf("dry run over a check root: %v", err)
	}
	if !strings.Contains(rep.Change, final) {
		t.Fatalf("the planned line does not name the final path: %q", rep.Change)
	}
	req.DryRun = false
	if _, err := BindProjectVaults(req); err == nil || !strings.Contains(err.Error(), "checks the vault it writes") {
		t.Fatalf("a real bind with a separate check root: err = %v", err)
	}
	if bindRead(t, cfg) != pre {
		t.Fatal("the config was written")
	}
}

// A checkout is verified against the slug its own marker names.
func TestBindProjectVaultsVerifiesEachCheckoutsOwnSlug(t *testing.T) {
	home := rebindEnv(t)
	splitHostMany(t, home, "qa", "orch")
	coQA := bindCheckout(t, home, "qa", "qa")
	coOrch := bindCheckout(t, home, "orch", "orch")
	req := batchReq("qa", "orch")
	req.Checkouts = []string{coQA, coOrch}
	rep, err := BindProjectVaults(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Checkouts) != 2 || !strings.HasSuffix(rep.Checkouts[0].Source, "#qa") || !strings.HasSuffix(rep.Checkouts[1].Source, "#orch") {
		t.Fatalf("checkouts = %+v", rep.Checkouts)
	}
	stray := bindCheckout(t, home, "other", "other")
	req = batchReq("qa")
	req.Checkouts = []string{stray}
	if _, err := BindProjectVaults(req); err == nil || !strings.Contains(err.Error(), "which this bind does not name") {
		t.Fatalf("a checkout of another project: err = %v", err)
	}
}

// staleHost binds qa to the quantum vault and returns what a check needs.
func staleHost(t *testing.T) (global, quantum, checkout string) {
	t.Helper()
	home := rebindEnv(t)
	global, quantum, _ = splitHostMany(t, home, "qa")
	checkout = bindCheckout(t, home, "qa", "qa")
	req := batchReq("qa")
	req.Checkouts = []string{checkout}
	if _, err := BindProjectVaults(req); err != nil {
		t.Fatal(err)
	}
	return global, quantum, checkout
}

// stale binding: the move is undone — B's copy reverted — while the host is
// still bound to B. Resolution refuses, and a running server's check reports
// it as a StaleBindingError.
func TestResolverRefusesAStaleBinding(t *testing.T) {
	_, quantum, checkout := staleHost(t)
	if _, err := ResolveVaultBinding(checkout); err != nil {
		t.Fatalf("a live binding refused: %v", err)
	}

	// B's copy is reverted: B holds neither tree; the default vault still
	// records qa's departure.
	if err := os.RemoveAll(filepath.Join(quantum, "Projects", "qa")); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveVaultBinding(checkout)
	if !errors.Is(err, ErrStaleProjectBinding) || !errors.Is(err, ErrVaultBindingRejected) {
		t.Fatalf("err = %v, want a stale-binding refusal that is also a binding rejection", err)
	}
	var sbe *StaleBindingError
	if cerr := CheckVaultBinding(quantum, checkout); !errors.As(cerr, &sbe) || sbe.Reason == "" {
		t.Fatalf("CheckVaultBinding = %v, want a *StaleBindingError carrying the reason", cerr)
	}
}

// stale binding, the other half of an undo: A's delete reverted, so the
// default vault holds the trees again and no record.
func TestResolverRefusesABindingWhenTheDefaultVaultHoldsTheProject(t *testing.T) {
	global, quantum, checkout := staleHost(t)
	if err := os.RemoveAll(filepath.Join(quantum, "Projects", "qa")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(global, filepath.FromSlash(departure.RelPath("qa")))); err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(global, "Projects", "qa", "resume.md"), "# restored\n")
	rebindGit(t, global, "init", "-q")
	rebindGit(t, global, "add", "-A")
	rebindGit(t, global, "commit", "-q", "-m", "the delete reverted")
	_, err := ResolveVaultBinding(checkout)
	if !errors.Is(err, ErrStaleProjectBinding) || !strings.Contains(err.Error(), "disagree about where") {
		t.Fatalf("err = %v, want the stale-binding refusal naming the disagreement", err)
	}
	if strings.Contains(err.Error(), "outlived the move") {
		t.Fatalf("tracked content in the default vault is not an outlived move: %v", err)
	}
}

// R1: after a reverted copy, ignored *.bak files keep B's Projects/<p>
// directory alive. The binding is still stale: judge by what git would carry.
func TestResolverRefusesAStaleBindingKeptAliveByIgnoredBak(t *testing.T) {
	_, quantum, checkout := staleHost(t)
	rebindWrite(t, filepath.Join(quantum, ".gitignore"), "*.bak\n")
	if err := os.Remove(filepath.Join(quantum, "Projects", "qa", "resume.md")); err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(quantum, "Projects", "qa", "resume.md.bak"), "# old\n")
	if _, err := ResolveVaultBinding(checkout); !errors.Is(err, ErrStaleProjectBinding) || !strings.Contains(err.Error(), "outlived the move") {
		t.Fatalf("err = %v, want the stale-binding refusal", err)
	}
}

// R1: the same with only machine-local residue, palace/<p>/.local/.
func TestResolverRefusesAStaleBindingKeptAliveByMachineLocalResidue(t *testing.T) {
	_, quantum, checkout := staleHost(t)
	if err := os.RemoveAll(filepath.Join(quantum, "Projects", "qa")); err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(quantum, "palace", "qa", ".local", "imported-sessions.jsonl"), "{}\n")
	if _, err := ResolveVaultBinding(checkout); !errors.Is(err, ErrStaleProjectBinding) {
		t.Fatalf("err = %v, want the stale-binding refusal", err)
	}
}

// R1 control: a palace-only project in B is real content and still resolves.
func TestResolverAcceptsAPalaceOnlyProjectInTheTarget(t *testing.T) {
	_, quantum, checkout := staleHost(t)
	if err := os.RemoveAll(filepath.Join(quantum, "Projects", "qa")); err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(quantum, "palace", "qa", "kg", "entities.jsonl"), "{}\n")
	if _, err := ResolveVaultBinding(checkout); err != nil {
		t.Fatalf("a palace-only project refused: %v", err)
	}
}

// R2: an empty palace/<p> directory, or ignored residue, appearing in the
// default vault never refuses a --new binding.
func TestResolverIgnoresResidueInTheDefaultVaultForANewBinding(t *testing.T) {
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	rebindGit(t, global, "init", "-q")
	rebindWrite(t, filepath.Join(global, ".gitignore"), "*.bak\n")
	other := rebindVault(t, filepath.Join(home, "other-vault"))
	cfg, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n")
	checkout := bindCheckout(t, home, "chimera", "chimera")
	if _, err := BindProjectVaults(BindVaultsRequest{Slugs: []string{"chimera"}, VaultPath: other, Mode: BindNew, Checkouts: []string{checkout}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(global, "palace", "chimera"), 0o755); err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(global, "Projects", "chimera", "stray.md.bak"), "residue\n")
	if _, err := ResolveVaultBinding(checkout); err != nil {
		t.Fatalf("residue in the default vault refused a --new binding: %v", err)
	}
}

// A brand-new project bound with --new, whose vault holds nothing yet, is
// never refused.
func TestResolverNeverRefusesANewProjectWithNoFilesYet(t *testing.T) {
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	other := rebindVault(t, filepath.Join(home, "other-vault"))
	cfg, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n")
	checkout := bindCheckout(t, home, "chimera", "chimera")
	req := BindVaultsRequest{Slugs: []string{"chimera"}, VaultPath: other, Mode: BindNew, Checkouts: []string{checkout}}
	if _, err := BindProjectVaults(req); err != nil {
		t.Fatal(err)
	}
	res, err := ResolveVaultBinding(checkout)
	if err != nil || !sameVaultRoot(res.Path, other) {
		t.Fatalf("a fresh --new project: %+v, %v", res, err)
	}
}
