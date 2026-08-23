// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// mustEvalRoot returns a t.TempDir() whose symlinks are already resolved. On
// darwin /var is a symlink to /private/var, so an unresolved TempDir compared
// against a resolved candidate would make these tests pass or fail for the
// wrong reason.
func mustEvalRoot(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval tempdir: %v", err)
	}
	return d
}

func TestRefuseDestinationInsideVaultRefusesAnExistingVaultDocument(t *testing.T) {
	vault := mustEvalRoot(t)
	doc := filepath.Join(vault, "Projects", "p")
	if err := os.MkdirAll(doc, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dest := filepath.Join(doc, "resume.md")
	if err := os.WriteFile(dest, []byte("live document\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := RefuseDestinationInsideVault(vault, dest)
	if !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("want ErrDestinationInsideVault, got %v", err)
	}
}

// TestRefuseDestinationInsideVaultSymlinkedRoot is THE test that proves the
// predicate rather than the plumbing: a lexical HasPrefix comparison passes this
// case wrongly. The vault root is reached through a symlink, so the destination
// the operator types does not lexically sit under the realpath, and the realpath
// does not lexically sit under the typed root either.
func TestRefuseDestinationInsideVaultSymlinkedRoot(t *testing.T) {
	base := mustEvalRoot(t)
	real := filepath.Join(base, "real-vault")
	if err := os.MkdirAll(filepath.Join(real, "Projects"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	// Destination typed through the symlink, vault root given as the realpath.
	dest := filepath.Join(link, "Projects", "resume.md")
	if err := os.WriteFile(dest, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := RefuseDestinationInsideVault(real, dest); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("dest through symlink, root as realpath: want refusal, got %v", err)
	}

	// And the mirror image: vault root given as the symlink, destination as the
	// realpath. Both directions must refuse.
	realDest := filepath.Join(real, "Projects", "resume.md")
	if err := RefuseDestinationInsideVault(link, realDest); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("root as symlink, dest as realpath: want refusal, got %v", err)
	}
}

func TestRefuseDestinationInsideVaultDestDoesNotExistYet(t *testing.T) {
	vault := mustEvalRoot(t)
	if err := os.MkdirAll(filepath.Join(vault, "Projects"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Parent exists, leaf does not — resolution must fall to the parent.
	dest := filepath.Join(vault, "Projects", "not-created-yet.json")
	if err := RefuseDestinationInsideVault(vault, dest); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("want refusal via parent resolution, got %v", err)
	}
}

func TestRefuseDestinationInsideVaultParentDoesNotExistEither(t *testing.T) {
	vault := mustEvalRoot(t)
	// Neither the leaf nor its parent chain exists — the lexical Clean fallback
	// is the only rung left, and it must still refuse.
	dest := filepath.Join(vault, "no", "such", "dir", "report.json")
	if err := RefuseDestinationInsideVault(vault, dest); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("want refusal via lexical fallback, got %v", err)
	}
}

// TestRefuseDestinationInsideVaultSiblingPrefixIsAllowed pins the separator-safe
// comparison: "/x/vault-backup" must not count as inside "/x/vault".
func TestRefuseDestinationInsideVaultSiblingPrefixIsAllowed(t *testing.T) {
	base := mustEvalRoot(t)
	vault := filepath.Join(base, "vault")
	sibling := filepath.Join(base, "vault-backup")
	for _, d := range []string{vault, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	dest := filepath.Join(sibling, "report.json")
	if err := RefuseDestinationInsideVault(vault, dest); err != nil {
		t.Fatalf("sibling prefix must be allowed, got %v", err)
	}
}

func TestRefuseDestinationInsideVaultOutsideIsAllowed(t *testing.T) {
	base := mustEvalRoot(t)
	vault := filepath.Join(base, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(base, "elsewhere", "report.json")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := RefuseDestinationInsideVault(vault, outside); err != nil {
		t.Fatalf("destination outside the vault must be allowed, got %v", err)
	}
}

func TestRefuseDestinationInsideVaultRefusesTheVaultRootItself(t *testing.T) {
	vault := mustEvalRoot(t)
	if err := RefuseDestinationInsideVault(vault, vault); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("the vault root itself must be refused, got %v", err)
	}
}

// TestRefuseDestinationInsideVaultRelativeDest is the hole the plan missed: a
// relative destination survives EvalSymlinks as a relative path, and a relative
// candidate never prefix-matches an absolute root. Without filepath.Abs on both
// sides this FAILS OPEN and the export writes into the vault.
func TestRefuseDestinationInsideVaultRelativeDest(t *testing.T) {
	vault := mustEvalRoot(t)
	sub := filepath.Join(vault, "Projects", "p")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Run with the process cwd inside the vault, exactly as an operator would
	// when they type a vault-relative destination.
	t.Chdir(vault)

	if err := RefuseDestinationInsideVault(vault, filepath.Join("Projects", "p", "resume.md")); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("relative dest resolving inside the vault must be refused, got %v", err)
	}
	if err := RefuseDestinationInsideVault(vault, "report.json"); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("bare relative filename in the vault cwd must be refused, got %v", err)
	}
	if err := RefuseDestinationInsideVault(vault, "."); !errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("relative \".\" inside the vault must be refused, got %v", err)
	}
}

// TestRefuseDestinationInsideVaultUnresolvableRootFailsClosed pins ruling 3.
// An unresolvable vault root is exactly when we cannot tell whether the
// destination is inside it, so the predicate must error rather than return nil.
func TestRefuseDestinationInsideVaultUnresolvableRootFailsClosed(t *testing.T) {
	base := mustEvalRoot(t)
	missing := filepath.Join(base, "no-such-vault")
	outside := filepath.Join(base, "elsewhere.json")

	err := RefuseDestinationInsideVault(missing, outside)
	if err == nil {
		t.Fatal("unresolvable vault root must fail closed, got nil")
	}
	// It is NOT the inside-vault sentinel: callers distinguish the two to pick
	// ExitSystem over ExitUser.
	if errors.Is(err, ErrDestinationInsideVault) {
		t.Fatalf("unresolvable root must not report as inside-vault, got %v", err)
	}
}

// TestRefuseDestinationInsideVaultEmptyRootFailsClosed pins the hole that
// filepath.Abs("") opens: Abs succeeds on an empty string and yields the process
// working directory, so an empty vault root would silently compare the
// destination against the cwd and answer "allowed" for anything outside it.
// The refusal must not be the inside-vault sentinel — callers route this to a
// system-fault exit, and there is no override for it.
func TestRefuseDestinationInsideVaultEmptyRootFailsClosed(t *testing.T) {
	base := mustEvalRoot(t)
	t.Chdir(base)

	for _, dest := range []string{
		filepath.Join(base, "report.json"), // absolute, inside the cwd
		"report.json",                      // relative to the cwd
		"/tmp/somewhere-else.json",         // absolute, well outside
	} {
		err := RefuseDestinationInsideVault("", dest)
		if err == nil {
			t.Fatalf("empty vault root must fail closed for %q, got nil", dest)
		}
		if errors.Is(err, ErrDestinationInsideVault) {
			t.Fatalf("empty vault root must not report as inside-vault for %q, got %v", dest, err)
		}
	}
}
