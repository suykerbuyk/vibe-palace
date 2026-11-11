// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// uvcFindings runs the audit over a synthetic tree and returns the
// unguarded-vault-commit symbols it reports, with the VACUOUS sentinel kept
// (its own row asserts on it).
func uvcFindings(t *testing.T, tree map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	for rel, src := range tree {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindUnguardedVaultCommit {
			out = append(out, f.Symbol)
		}
	}
	slices.Sort(out)
	return out
}

// uvcWithoutVacuous drops the floor sentinel, for the rows that are about which
// functions are reported rather than about the floor.
func uvcWithoutVacuous(syms []string) []string {
	var out []string
	for _, s := range syms {
		if !strings.Contains(s, "VACUOUS") {
			out = append(out, s)
		}
	}
	return out
}

// Six commits in one file, enough to clear the floor, in the three shapes the
// rule matches plus the two seat forms (plain func and method).
const uvcSixCommits = `package storage

import "os/exec"

func gitCmd(dir string, timeout int, args ...string) (string, error) { return "", nil }
func gitCmdStdin(dir string, timeout int, stdin string, args ...string) (string, int, error) {
	return "", 0, nil
}

type Vault struct{ Root string }

// seat 2 shape: a seated plain function.
func commitPathspec(vaultPath string, limit int, message string, paths []string) error {
	_, err := gitCmd(vaultPath, limit, "commit", "-m", message)
	return err
}

// seat 3 shape: a seated plain function using the bare exec form.
func GitCommitAllAt(root, message string) error {
	cmd := exec.Command("git", "-C", root, "commit", "-m", message)
	return cmd.Run()
}

// seat 6 shape: a seated METHOD, whose bindingScopes name carries its receiver.
func (v *Vault) ApplyAuthoredOnlyMigration(msg string) error {
	_, _, err := gitCmdStdin(v.Root, 10, msg, "commit", "-q", "-F", "-")
	return err
}

// seat 4 and 5 shapes, to clear the floor.
func healDerivedConflicts(vaultPath string) error {
	_, err := gitCmd(vaultPath, 10, "commit", "--no-edit")
	return err
}

func untrackDerivedAfterMerge(vaultPath string) error {
	_, err := gitCmd(vaultPath, 10, "commit", "-q", "-m", "untrack")
	return err
}

func readOnly(dir string) (string, error) { return gitCmd(dir, 10, "status", "--porcelain") }
`

// TestUnguardedVaultCommitExemptsSeatedFunctions is fixture 1: every commit is
// inside a seated function, so nothing is reported.
//
// 🔴 It includes a seated METHOD on purpose. bindingScopes names a method with
// its receiver ("Vault.ApplyAuthoredOnlyMigration"), so a whole-name exemption
// lookup misses it and reports seat 6 as unguarded. The rule's first run against
// the real tree did exactly that; this row is why it cannot regress.
func TestUnguardedVaultCommitExemptsSeatedFunctions(t *testing.T) {
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{"storage/git.go": uvcSixCommits}))
	if len(got) != 0 {
		t.Errorf("seated commits reported as unguarded: %q", got)
	}
}

// TestUnguardedVaultCommitReportsTheGitCmdShape is fixture 2.
func TestUnguardedVaultCommitReportsTheGitCmdShape(t *testing.T) {
	src := uvcSixCommits + `
func sneakyGitCmd(vaultPath string) error {
	_, err := gitCmd(vaultPath, 10, "commit", "-m", "sneaky")
	return err
}
`
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{"storage/git.go": src}))
	if !slices.Contains(got, "storage.sneakyGitCmd") {
		t.Errorf("findings = %q, want storage.sneakyGitCmd", got)
	}
}

// TestUnguardedVaultCommitReportsTheGitCmdStdinShape is fixture 3 — the shape a
// walk scoped to gitCmd alone would miss, and the real shape of
// authored_only_migration.go's commit.
func TestUnguardedVaultCommitReportsTheGitCmdStdinShape(t *testing.T) {
	src := uvcSixCommits + `
func sneakyStdin(vaultPath, msg string) error {
	_, _, err := gitCmdStdin(vaultPath, 10, msg, "commit", "-q", "-F", "-")
	return err
}
`
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{"storage/git.go": src}))
	if !slices.Contains(got, "storage.sneakyStdin") {
		t.Errorf("findings = %q, want storage.sneakyStdin", got)
	}
}

// TestUnguardedVaultCommitReportsTheBareExecShape is fixture 4 — the shape a
// two-helper walk would miss, and the real shape of git.go's own commit, which
// is seat 3. A rule blind to its own seat is worse than no rule.
func TestUnguardedVaultCommitReportsTheBareExecShape(t *testing.T) {
	src := uvcSixCommits + `
func sneakyExec(root, message string) error {
	cmd := exec.Command("git", "-C", root, "commit", "-m", message)
	return cmd.Run()
}
`
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{"storage/git.go": src}))
	if !slices.Contains(got, "storage.sneakyExec") {
		t.Errorf("findings = %q, want storage.sneakyExec", got)
	}
}

// TestUnguardedVaultCommitFloorTripsWhenTheWalkSeesNothing is fixture 5: with no
// commit literal anywhere, a clean verdict would be vacuous, so the floor must
// say so rather than letting the rule pass for the wrong reason.
func TestUnguardedVaultCommitFloorTripsWhenTheWalkSeesNothing(t *testing.T) {
	src := `package storage

func gitCmd(dir string, timeout int, args ...string) (string, error) { return "", nil }

func readOnly(dir string) (string, error) { return gitCmd(dir, 10, "status", "--porcelain") }
`
	got := uvcFindings(t, map[string]string{"storage/git.go": src})
	var vacuous bool
	for _, s := range got {
		if strings.Contains(s, "VACUOUS") {
			vacuous = true
		}
	}
	if !vacuous {
		t.Errorf("findings = %q, want the VACUOUS floor entry — a walk that sees no commits must not report clean", got)
	}
}

// TestUnguardedVaultCommitIgnoresAReadVerb is the non-vacuity control for the
// matcher: it must key on the commit verb, not on "a git call happened".
func TestUnguardedVaultCommitIgnoresAReadVerb(t *testing.T) {
	src := uvcSixCommits + `
func justReads(vaultPath string) (string, error) {
	return gitCmd(vaultPath, 10, "status", "--porcelain")
}
`
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{"storage/git.go": src}))
	if slices.Contains(got, "storage.justReads") {
		t.Errorf("a read-only git call was reported as an unguarded commit: %q", got)
	}
}

// TestUnguardedVaultCommitDoesNotExemptASameNamedMethodOnAnotherType is the
// negative fixture for the exemption's precision.
//
// 🔴 A BARE-NAME EXEMPTION WOULD LET ANY COMMITTER EVADE THE RATCHET BY SHARING
// A SEAT'S NAME, which destroys the closed-set property the rule exists for. An
// earlier version stripped every dotted prefix before the lookup, so
// `(*Impostor).ApplyAuthoredOnlyMigration` and even a different package's
// `commitPathspec` were silently exempt. The key is the full binding scope —
// package, receiver and name — and this row is what holds it there.
func TestUnguardedVaultCommitDoesNotExemptASameNamedMethodOnAnotherType(t *testing.T) {
	src := uvcSixCommits + `
type Impostor struct{ Root string }

// Same method name as seat 6, different receiver type: NOT a seat.
func (i *Impostor) ApplyAuthoredOnlyMigration(msg string) error {
	_, _, err := gitCmdStdin(i.Root, 10, msg, "commit", "-q", "-F", "-")
	return err
}
`
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{"storage/git.go": src}))
	if !slices.Contains(got, "storage.Impostor.ApplyAuthoredOnlyMigration") {
		t.Errorf("findings = %q, want storage.Impostor.ApplyAuthoredOnlyMigration — a same-named method on another type is exempt", got)
	}
}

// TestUnguardedVaultCommitDoesNotExemptASeatNameInAnotherPackage is the package
// half of the same precision: a seat's name in a different package is not a seat.
func TestUnguardedVaultCommitDoesNotExemptASeatNameInAnotherPackage(t *testing.T) {
	other := `package elsewhere

func gitCmd(dir string, timeout int, args ...string) (string, error) { return "", nil }

// Same name as seat 2, different package: NOT a seat.
func commitPathspec(vaultPath string, limit int, message string, paths []string) error {
	_, err := gitCmd(vaultPath, limit, "commit", "-m", message)
	return err
}
`
	got := uvcWithoutVacuous(uvcFindings(t, map[string]string{
		"storage/git.go":      uvcSixCommits,
		"elsewhere/commit.go": other,
	}))
	if !slices.Contains(got, "elsewhere.commitPathspec") {
		t.Errorf("findings = %q, want elsewhere.commitPathspec — a seat's name in another package is exempt", got)
	}
}
