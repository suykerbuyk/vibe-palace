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

// vdcFindings runs the audit over a synthetic tree and returns the
// vault-dir-creation symbols, without the floor sentinel (a fixture tree is far
// below the floor by design; the floor has its own row).
func vdcFindings(t *testing.T, tree map[string]string) []string {
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
		if f.Kind == KindVaultDirCreation && !strings.Contains(f.Symbol, "VACUOUS") {
			out = append(out, f.Symbol)
		}
	}
	slices.Sort(out)
	return out
}

// withVaultDirAllow swaps the allowlist for one test.
func withVaultDirAllow(t *testing.T, m map[string]dirAllow) {
	t.Helper()
	prev := vaultDirAllow
	vaultDirAllow = m
	t.Cleanup(func() { vaultDirAllow = prev })
}

const vdcHeader = `package storage

import (
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

var _ = filepath.Join
var _ = surface.GuardTestPathWrite
var _ = os.MkdirAll
`

func vdc(body string) map[string]string {
	return map[string]string{"internal/storage/x.go": vdcHeader + body}
}

func TestVaultDirCreation(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	cases := []struct {
		name string
		src  string
		want []string // symbols (without the "unbound-dir-creator " prefix), empty = clean
	}{
		{"unguarded raw MkdirAll is reported", `
func F(p string) error { return os.MkdirAll(p, 0o755) }
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(p)"}},

		{"same-identifier guard then sink is accepted", `
func F(p string) error {
	surface.GuardTestPathWrite(p)
	return os.MkdirAll(p, 0o755)
}
`, nil},

		{"same expression through the pure path helpers is accepted", `
func F(root string) error {
	surface.GuardTestPathWrite(filepath.Join(root, "a"))
	return os.MkdirAll(filepath.Join(root, "a"), 0o755)
}
`, nil},

		{"NEGATIVE: a guard on a different path (adjacent) is reported", `
func F(x, y string) error {
	surface.GuardTestPathWrite(x)
	return os.MkdirAll(y, 0o755)
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(y)"}},

		{"NEGATIVE: a first-statement guard on a root, sink on a plan target (the applyScaffold shape) is reported", `
type R struct{ vaultRoot string }

func (r *R) Apply(targets []string) error {
	surface.GuardTestPathWrite(r.vaultRoot)
	for _, t := range targets {
		if err := os.MkdirAll(t, 0o755); err != nil {
			return err
		}
	}
	return nil
}
`, []string{"unbound-dir-creator storage.R.Apply|os.MkdirAll(t)"}},

		{"NEGATIVE: a reassignment between the guard and the sink is reported", `
func F(x, other string) error {
	surface.GuardTestPathWrite(x)
	x = other
	return os.MkdirAll(x, 0o755)
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(x)"}},

		{"NEGATIVE: a redeclaration (shadowing) between guard and sink is reported", `
func F(x string) error {
	surface.GuardTestPathWrite(x)
	if true {
		x := "elsewhere"
		return os.MkdirAll(x, 0o755)
	}
	return nil
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(x)"}},

		{"NEGATIVE: an impure call in the guard expression cannot be compared and is reported", `
func next() string { return "" }

func F() error {
	surface.GuardTestPathWrite(next())
	return os.MkdirAll(next(), 0o755)
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(next())"}},

		{"NEGATIVE: a guard in a sibling branch does not cover a sink in another branch", `
func F(p string, c bool) error {
	if c {
		surface.GuardTestPathWrite(p)
	} else {
		return os.MkdirAll(p, 0o755)
	}
	return nil
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(p)"}},

		{"NEGATIVE: a guard AFTER the sink does not cover it", `
func F(p string) error {
	err := os.MkdirAll(p, 0o755)
	surface.GuardTestPathWrite(p)
	return err
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(p)"}},

		{"NEGATIVE: an aliased os import is still seen (import fs \"os\")", `
func F(p string) error { return fs.MkdirAll(p, 0o755) }
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(p)"}},

		{"a guard in an enclosing block covers a sink in a nested block", `
func F(p string, c bool) error {
	surface.GuardTestPathWrite(p)
	if c {
		return os.MkdirAll(p, 0o755)
	}
	return nil
}
`, nil},

		{"MkdirTemp and Mkdir are creators too", `
func F(p string) {
	_, _ = os.MkdirTemp(p, "x-")
	_ = os.Mkdir(p, 0o755)
}
`, []string{"unbound-dir-creator storage.F|os.Mkdir(p)", "unbound-dir-creator storage.F|os.MkdirTemp(p)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := c.src
			tree := vdc(src)
			if strings.Contains(c.name, "aliased") {
				tree = map[string]string{"internal/storage/x.go": strings.Replace(vdcHeader, "\t\"os\"\n", "\tfs \"os\"\n", 1) + src}
				tree["internal/storage/x.go"] = strings.Replace(tree["internal/storage/x.go"], "var _ = os.MkdirAll", "var _ = fs.MkdirAll", 1)
			}
			got := vdcFindings(t, tree)
			if !slices.Equal(got, c.want) {
				t.Errorf("findings = %q, want %q", got, c.want)
			}
		})
	}
}

// A package outside the scanned set is not reported.
func TestVaultDirCreationIgnoresUnscannedPackages(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	tree := map[string]string{"internal/cache/x.go": strings.Replace(vdcHeader, "package storage", "package cache", 1) + "\nfunc F(p string) error { return os.MkdirAll(p, 0o755) }\n"}
	if got := vdcFindings(t, tree); len(got) != 0 {
		t.Errorf("a package outside the scan set was reported: %q", got)
	}
}

// The scan set is the UNION with the old creating-project-owner rawSinkPackages:
// a raw creator in any of them is reported, so consolidating the two rules lost
// no package.
func TestVaultDirCreationScansTheOldRawSinkPackages(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	for _, pkg := range []string{"storage", "vaultfs", "archive", "reconcile", "migrate", "absorb", "tools", "memory", "hook", "commands", "palace"} {
		tree := map[string]string{"internal/" + pkg + "/x.go": strings.Replace(vdcHeader, "package storage", "package "+pkg, 1) + "\nfunc F(p string) error { return os.MkdirAll(p, 0o755) }\n"}
		want := []string{"unbound-dir-creator " + pkg + ".F|os.MkdirAll(p)"}
		if got := vdcFindings(t, tree); !slices.Equal(got, want) {
			t.Errorf("%s: findings = %q, want %q", pkg, got, want)
		}
	}
}

// The unqualified guard form inside package surface binds the same way.
func TestVaultDirCreationAcceptsTheUnqualifiedGuardInsideSurface(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	src := `package surface

import "os"

func GuardTestPathWrite(string) {}

func Bound(p string) error {
	GuardTestPathWrite(p)
	return os.MkdirAll(p, 0o755)
}

func Unbound(p string) error { return os.MkdirAll(p, 0o755) }
`
	got := vdcFindings(t, map[string]string{"internal/surface/x.go": src})
	want := []string{"unbound-dir-creator surface.Unbound|os.MkdirAll(p)"}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}

// The allowlist is per CALL SITE: a listed site is silent, a second raw call of
// the same text is reported, a second raw call of different text is reported as
// unbound, and a listed site that is gone is reported as stale.
func TestVaultDirCreationAllowlistIsPerCallSite(t *testing.T) {
	const listed = "storage.G|os.MkdirAll(p)"
	one := `
func G(p string) error { return os.MkdirAll(p, 0o755) }
`
	two := `
func G(p string) error {
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}
`
	other := `
func G(p, q string) error {
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(q, 0o755)
}
`
	withVaultDirAllow(t, map[string]dirAllow{listed: {1, "test reason"}})
	if got := vdcFindings(t, vdc(one)); len(got) != 0 {
		t.Errorf("a listed site was reported: %q", got)
	}
	if got := vdcFindings(t, vdc(two)); !slices.Equal(got, []string{"extra-dir-creator " + listed}) {
		t.Errorf("a second identical raw call = %q, want the extra-dir-creator finding", got)
	}
	if got := vdcFindings(t, vdc(other)); !slices.Equal(got, []string{"unbound-dir-creator storage.G|os.MkdirAll(q)"}) {
		t.Errorf("a second raw call of different text = %q, want it reported as unbound", got)
	}
	// stale: the listed function no longer creates a directory.
	if got := vdcFindings(t, vdc("\nfunc G(p string) error { return nil }\n")); !slices.Equal(got, []string{"sourceaudit.vaultDirCreation/STALE/" + listed}) {
		t.Errorf("a stale allowlist entry = %q", got)
	}
}

// The floor: a tree with too few creators is vacuous, not clean.
func TestVaultDirCreationFloor(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "storage", "x.go"), []byte(vdcHeader), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	vacuous := false
	for _, f := range findings {
		if f.Kind == KindVaultDirCreation && strings.Contains(f.Symbol, "VACUOUS") {
			vacuous = true
		}
	}
	if !vacuous {
		t.Error("a tree with no directory creators did not report the VACUOUS floor")
	}
}

// Pure helpers are RESOLVED, not recognised by spelling (code-review blocker).
func TestVaultDirCreationResolvesThePureHelpers(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	cases := []struct {
		name string
		path string
		src  string
		want []string
	}{
		{"MUST REPORT: an impure lockDirPath in another package (guard and sink both on lockDirPath(p))", "internal/storage/x.go", `package storage

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

var counter int

func lockDirPath(p string) string { counter++; return p + string(rune('a'+counter)) }

func F(p string) error {
	surface.GuardTestPathWrite(lockDirPath(p))
	return os.MkdirAll(lockDirPath(p), 0o755)
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(lockDirPath(p))"}},

		{"MUST REPORT: filepath is an alias of a package that is not path/filepath", "internal/storage/x.go", `package storage

import (
	"os"

	filepath "example.com/notfilepath"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func F(a, b string) error {
	surface.GuardTestPathWrite(filepath.Join(a, b))
	return os.MkdirAll(filepath.Join(a, b), 0o755)
}
`, []string{"unbound-dir-creator storage.F|os.MkdirAll(filepath.Join(a, b))"}},

		{"MUST REPORT: a local variable shadows lockDirPath even inside package vaultlock", "internal/vaultlock/x.go", `package vaultlock

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func lockDirPath(root string) string { return root + "/.vp-locks" }

func F(root string) error {
	lockDirPath := func(r string) string { return r + "/elsewhere" }
	surface.GuardTestPathWrite(lockDirPath(root))
	return os.MkdirAll(lockDirPath(root), 0o755)
}
`, []string{"unbound-dir-creator vaultlock.F|os.MkdirAll(lockDirPath(root))"}},

		{"MUST REPORT: lockDirPath with no top-level declaration in the package", "internal/vaultlock/x.go", `package vaultlock

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func F(root string) error {
	surface.GuardTestPathWrite(lockDirPath(root))
	return os.MkdirAll(lockDirPath(root), 0o755)
}
`, []string{"unbound-dir-creator vaultlock.F|os.MkdirAll(lockDirPath(root))"}},

		{"accepted: the real shape, lockDirPath resolving to vaultlock's own top-level func", "internal/vaultlock/x.go", `package vaultlock

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func lockDirPath(root string) string { return root + "/.vp-locks" }

func F(root string) error {
	surface.GuardTestPathWrite(lockDirPath(root))
	return os.MkdirAll(lockDirPath(root), 0o755)
}
`, nil},

		{"accepted: the real path/filepath under its default name", "internal/storage/x.go", `package storage

import (
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func F(a, b string) error {
	surface.GuardTestPathWrite(filepath.Join(a, b))
	return os.MkdirAll(filepath.Join(a, b), 0o755)
}
`, nil},

		{"accepted: the real path/filepath under an alias", "internal/storage/x.go", `package storage

import (
	"os"
	fp "path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func F(a, b string) error {
	surface.GuardTestPathWrite(fp.Join(a, b))
	return os.MkdirAll(fp.Join(a, b), 0o755)
}
`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := vdcFindings(t, map[string]string{c.path: c.src})
			if !slices.Equal(got, c.want) {
				t.Errorf("findings = %q, want %q", got, c.want)
			}
		})
	}
}

// Shadowing is resolved by go/types, so EVERY binding form defeats the helper,
// not just the ones a hand-written list knew (code-review blocker, round 2: a
// NAMED RESULT beat the parameter-and-local-declaration list). Each fixture is
// package vaultlock with a real top-level lockDirPath, a guard and a sink both on
// lockDirPath(root), and ONE binding form that shadows it: all must be reported.
func TestVaultDirCreationEveryBindingFormShadowsTheHelper(t *testing.T) {
	withVaultDirAllow(t, map[string]dirAllow{})
	const head = `package vaultlock

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func lockDirPath(root string) string { return root + "/.vp-locks" }

`
	cases := []struct{ name, fn string }{
		{"NAMED RESULT (codex's counterexample)", `
func F(root string) (lockDirPath func(string) string, err error) {
	lockDirPath = func(r string) string { return r + "/elsewhere" }
	surface.GuardTestPathWrite(lockDirPath(root))
	return lockDirPath, os.MkdirAll(lockDirPath(root), 0o755)
}
`},
		{"function-literal PARAMETER", `
func F(root string) error {
	g := func(lockDirPath func(string) string) error {
		surface.GuardTestPathWrite(lockDirPath(root))
		return os.MkdirAll(lockDirPath(root), 0o755)
	}
	return g(func(r string) string { return r + "/elsewhere" })
}
`},
		{"function-literal NAMED RESULT", `
func F(root string) error {
	g := func() (lockDirPath func(string) string) {
		lockDirPath = func(r string) string { return r + "/elsewhere" }
		surface.GuardTestPathWrite(lockDirPath(root))
		_ = os.MkdirAll(lockDirPath(root), 0o755)
		return
	}
	_ = g()
	return nil
}
`},
		{"RANGE variable", `
func F(root string, fns []func(string) string) error {
	for _, lockDirPath := range fns {
		surface.GuardTestPathWrite(lockDirPath(root))
		return os.MkdirAll(lockDirPath(root), 0o755)
	}
	return nil
}
`},
		{"RECEIVER", `
type R func(string) string

func (lockDirPath R) F(root string) error {
	surface.GuardTestPathWrite(lockDirPath(root))
	return os.MkdirAll(lockDirPath(root), 0o755)
}
`},
		{"TYPE-SWITCH binding", `
func F(root string, v any) error {
	switch lockDirPath := v.(type) {
	case func(string) string:
		surface.GuardTestPathWrite(lockDirPath(root))
		return os.MkdirAll(lockDirPath(root), 0o755)
	}
	return nil
}
`},
		{"var declaration in a nested block", `
func F(root string, c bool) error {
	if c {
		var lockDirPath = func(r string) string { return r + "/elsewhere" }
		surface.GuardTestPathWrite(lockDirPath(root))
		return os.MkdirAll(lockDirPath(root), 0o755)
	}
	return nil
}
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := vdcFindings(t, map[string]string{"internal/vaultlock/x.go": head + c.fn})
			if len(got) != 1 || !strings.HasPrefix(got[0], "unbound-dir-creator vaultlock.") {
				t.Errorf("a shadowed lockDirPath was accepted as the pure helper: findings = %q", got)
			}
		})
	}
	// control: the same file with no shadow is accepted, so a green row above is
	// the shadow's doing and not an unrelated rejection.
	ok := head + `
func F(root string) error {
	surface.GuardTestPathWrite(lockDirPath(root))
	return os.MkdirAll(lockDirPath(root), 0o755)
}
`
	if got := vdcFindings(t, map[string]string{"internal/vaultlock/x.go": ok}); len(got) != 0 {
		t.Errorf("control (no shadow) was reported: %q", got)
	}
}
