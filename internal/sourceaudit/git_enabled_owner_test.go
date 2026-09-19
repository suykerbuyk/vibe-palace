// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// The tests for the git_enabled one-owner rule. Its healthy steady state is
// zero findings, so a silent run proves nothing: each fixture below is one of
// the plan's named breaks (U1–U4), asserted to fire, beside the shapes that
// must stay clean.

// gitOwnerStorage declares every allow-listed reader's package half, so only
// the break under test produces a finding.
const gitOwnerStorage = `package storage

import "errors"

var ErrGitDisabled = errors.New("git is disabled")
var ErrGitConfigUnreadable = errors.New("unreadable")

func HostGitEnabled() (bool, error) { return true, nil }

// CLEAN: the refusal's one reader.
func RefuseIfGitDisabled(vaultPath, verb string) error {
	enabled, err := HostGitEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return ErrGitDisabled
	}
	return nil
}
`

const gitOwnerReconcile = `package reconcile

import "example.com/storage"

type VaultReconciler struct{}

// CLEAN: an allow-listed reporting reader.
func (r *VaultReconciler) gitEnabled() (bool, error) { return storage.HostGitEnabled() }
`

const gitOwnerToolsClean = `package tools

import (
	"errors"

	"example.com/storage"
)

// CLEAN: an allow-listed reporting reader.
func computeVaultDirt() string {
	enabled, _ := storage.HostGitEnabled()
	if enabled {
		return "on"
	}
	return "off"
}

// CLEAN: mapping the refusal is allowed everywhere.
func mapsTheRefusal(err error) bool {
	return errors.Is(err, storage.ErrGitDisabled) || errors.Is(err, storage.ErrGitConfigUnreadable)
}

// CLEAN: calling the gate is not reading the setting.
func preflight() error { return storage.RefuseIfGitDisabled("/v", "pull") }
`

func gitOwnerFindings(t *testing.T, pkgs map[string]string) []string {
	t.Helper()
	findings, err := Run(writeFixtureTree(t, pkgs))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindGitEnabledOwner {
			out = append(out, f.Symbol)
		}
	}
	return out
}

func gitOwnerTree(tools string) map[string]string {
	return map[string]string{
		"storage":   gitOwnerStorage,
		"reconcile": gitOwnerReconcile,
		"tools":     tools,
	}
}

// TestGitEnabledOwnerIsSilentOnTheSanctionedShapes is the negative control on a
// fixture: the one reader, the two reporting readers, errors.Is mapping and a
// preflight call produce nothing.
func TestGitEnabledOwnerIsSilentOnTheSanctionedShapes(t *testing.T) {
	if got := gitOwnerFindings(t, gitOwnerTree(gitOwnerToolsClean)); len(got) != 0 {
		t.Errorf("the sanctioned shapes produced findings: %v", got)
	}
}

// Break U1: a second reader outside storage.
func TestGitEnabledOwnerFlagsASecondReaderOutsideStorage(t *testing.T) {
	src := gitOwnerToolsClean + `
func handler() bool {
	enabled, _ := storage.HostGitEnabled()
	return enabled
}
`
	if got := gitOwnerFindings(t, gitOwnerTree(src)); !slices.Contains(got, "tools.handler -> HostGitEnabled") {
		t.Errorf("a surface reading git_enabled itself was not flagged: %v", got)
	}
}

// Break U2: a second read INSIDE storage, e.g. a core re-reading the config.
// That is the read-once-per-entry-call property.
func TestGitEnabledOwnerFlagsASecondReadInsideStorage(t *testing.T) {
	tree := gitOwnerTree(gitOwnerToolsClean)
	tree["storage"] = gitOwnerStorage + `
func pullCore() error {
	if enabled, _ := HostGitEnabled(); !enabled {
		return ErrGitDisabled
	}
	return nil
}
`
	got := gitOwnerFindings(t, tree)
	if !slices.Contains(got, "storage.pullCore -> HostGitEnabled") {
		t.Errorf("an inner storage function re-reading the config was not flagged: %v", got)
	}
	if slices.Contains(got, "storage.pullCore -> ErrGitDisabled") {
		t.Errorf("storage owns the sentinel and must be able to return it: %v", got)
	}
}

// Break U3: a surface forging the refusal by wrapping the sentinel itself.
func TestGitEnabledOwnerFlagsAForgedRefusal(t *testing.T) {
	src := strings.Replace(gitOwnerToolsClean, `import (
	"errors"
`, `import (
	"errors"
	"fmt"
`, 1) + `
func forged() error { return fmt.Errorf("nope: %w", storage.ErrGitDisabled) }
`
	got := gitOwnerFindings(t, gitOwnerTree(src))
	if !slices.Contains(got, "tools.forged -> ErrGitDisabled") {
		t.Errorf("a surface wrapping the sentinel itself was not flagged: %v", got)
	}
	if slices.Contains(got, "tools.mapsTheRefusal -> ErrGitDisabled") {
		t.Errorf("errors.Is mapping in the same file was flagged: %v", got)
	}
}

// Break U4: an allow-listed reader renamed without updating the allow-list.
func TestGitEnabledOwnerAnchorFiresWhenAReaderIsMissing(t *testing.T) {
	tree := gitOwnerTree(gitOwnerToolsClean)
	tree["storage"] = strings.Replace(gitOwnerStorage, "func RefuseIfGitDisabled(", "func RefuseIfGitOff(", 1)
	got := gitOwnerFindings(t, tree)
	if !slices.Contains(got, "sourceaudit.gitEnabledOwner/ABSENT/storage.RefuseIfGitDisabled") {
		t.Errorf("a renamed owner left the allow-list guarding nothing, silently: %v", got)
	}
}

// TestGitEnabledOwnerIsSilentOnTheLiveTree is the negative control against the
// real corpus: one owner today, and every anchor resolves.
func TestGitEnabledOwnerIsSilentOnTheLiveTree(t *testing.T) {
	findings, err := Run(repoRoots...)
	if err != nil {
		t.Fatalf("Run over the live tree: %v", err)
	}
	for _, f := range findings {
		if f.Kind == KindGitEnabledOwner {
			t.Errorf("finding on the live tree: %s\n  %s", f.Symbol, f.Detail)
		}
	}
}

// Break U5: the refusal forged in a package-level var, with no function body
// to walk. It wraps the real sentinel, so the parity test's errors.Is holds and
// only this rule can catch it.
func TestGitEnabledOwnerFlagsAForgedRefusalInAPackageVar(t *testing.T) {
	src := strings.Replace(gitOwnerToolsClean, `import (
	"errors"
`, `import (
	"errors"
	"fmt"
`, 1) + `
var errForged = fmt.Errorf("nope: %w", storage.ErrGitDisabled)

func forgedViaVar() error { return errForged }
`
	if got := gitOwnerFindings(t, gitOwnerTree(src)); !slices.Contains(got, "tools.errForged -> ErrGitDisabled") {
		t.Errorf("a package var wrapping the sentinel was not flagged: %v", got)
	}
}

// Break U5b: the sentinel aliased into a package var. The alias then reaches
// fmt.Errorf or a return anywhere in the package, out of this rule's sight.
func TestGitEnabledOwnerFlagsAnAliasedSentinel(t *testing.T) {
	src := gitOwnerToolsClean + `
var errOff = storage.ErrGitDisabled
`
	if got := gitOwnerFindings(t, gitOwnerTree(src)); !slices.Contains(got, "tools.errOff -> ErrGitDisabled") {
		t.Errorf("a package var aliasing the sentinel was not flagged: %v", got)
	}
}

// Break U6: HostGitEnabled taken as a value rather than called — through a
// package var, and through a local :=. Neither is a call, so a rule that
// matches callee names only sees nothing.
func TestGitEnabledOwnerFlagsHostGitEnabledTakenAsAValue(t *testing.T) {
	src := gitOwnerToolsClean + `
var hge = storage.HostGitEnabled

func readsViaPackageVar() bool { ok, _ := hge(); return ok }

func readsViaLocalValue() bool {
	f := storage.HostGitEnabled
	ok, _ := f()
	return ok
}
`
	got := gitOwnerFindings(t, gitOwnerTree(src))
	for _, want := range []string{"tools.hge -> HostGitEnabled", "tools.readsViaLocalValue -> HostGitEnabled"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s missing: a function value reading git_enabled was not flagged: %v", want, got)
		}
	}
}

// An import alias must not hide the owner: the reference is resolved through
// the file's import of the storage package, not by the spelling "storage".
func TestGitEnabledOwnerResolvesAnImportAlias(t *testing.T) {
	src := `package tools

import (
	"fmt"

	st "example.com/storage"
)

func aliased() error {
	if ok, _ := st.HostGitEnabled(); ok {
		return nil
	}
	return fmt.Errorf("nope: %w", st.ErrGitDisabled)
}
`
	got := gitOwnerFindings(t, gitOwnerTree(src))
	for _, want := range []string{"tools.aliased -> HostGitEnabled", "tools.aliased -> ErrGitDisabled"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s missing under an import alias: %v", want, got)
		}
	}
}

// CLEAN: a package's OWN unrelated error of the same name is not storage's.
// The rule resolves by package identity, so this must not be flagged.
func TestGitEnabledOwnerIsSilentOnAPackageLocalNameClash(t *testing.T) {
	src := gitOwnerToolsClean + `
var ErrGitDisabled = errors.New("this package's own, unrelated error")

func returnsItsOwn() error { return ErrGitDisabled }
`
	for _, f := range gitOwnerFindings(t, gitOwnerTree(src)) {
		if strings.HasPrefix(f, "tools.returnsItsOwn") || f == "tools.ErrGitDisabled -> ErrGitDisabled" {
			t.Errorf("a package-local error of the same name was flagged as storage's: %v", f)
		}
	}
}
