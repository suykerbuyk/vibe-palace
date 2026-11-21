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

// writeTree writes a synthetic tree: keys are slash paths of files, values their
// source. (writeFixtureTree allows one file per directory; the type-check tests
// need two files in one package.)
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// funnelSymbols returns every funnel-rule symbol, meta-findings included.
func funnelSymbols(t *testing.T, files map[string]string) []string {
	t.Helper()
	findings, err := Run(writeTree(t, files))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindVaultWriteOutsideFunnel {
			out = append(out, f.Symbol)
		}
	}
	slices.Sort(out)
	return out
}

func funnelFn(t *testing.T, files map[string]string) []string {
	t.Helper()
	var out []string
	for _, s := range funnelSymbols(t, files) {
		if !strings.HasPrefix(s, "sourceaudit.") {
			out = append(out, s)
		}
	}
	return out
}

func withTables(t *testing.T) {
	t.Helper()
	prevEdges, prevAcc, prevSrc := funnelAllowedEdges, typecheckAccounted, vaultSources
	t.Cleanup(func() { funnelAllowedEdges, typecheckAccounted, vaultSources = prevEdges, prevAcc, prevSrc })
}

func hasSym(syms []string, want string) bool { return slices.Contains(syms, want) }

// pre is a fixture file header: package, imports (os, filepath, and any extra),
// and uses that keep them referenced.
func pre(pkg string, extra ...string) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n")
	for _, e := range extra {
		b.WriteString("\t\"" + e + "\"\n")
	}
	b.WriteString(")\n\nvar _ = os.WriteFile\nvar _ = filepath.Join\n")
	return b.String()
}

// The M6 shape and its relatives: every route a vault path can take to a raw write
// must be reported, whatever the shape of the producer.
func TestTaintReportsEveryRouteToARawWrite(t *testing.T) {
	cases := []struct {
		name, want string
		files      map[string]string
	}{
		{"multi-value producer (the M6 shape)", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func pair(vaultRoot, name string) (string, string) { return filepath.Join(vaultRoot, "a"), filepath.Join(vaultRoot, name) }
func Repair(vaultRoot string) error {
	ap, mp := pair(vaultRoot, "x")
	_ = ap
	return os.WriteFile(mp, nil, 0o644)
}
`}},
		{"single-value helper producer", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func markerPath(vaultRoot string) string { return filepath.Join(vaultRoot, "m") }
func Repair(vaultRoot string) error { p := markerPath(vaultRoot); return os.WriteFile(p, nil, 0o644) }
`}},
		{"named result", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func named(vaultRoot string) (path string, err error) {
	path = filepath.Join(vaultRoot, "m")
	return
}
func Repair(vaultRoot string) error { p, _ := named(vaultRoot); return os.WriteFile(p, nil, 0o644) }
`}},
		{"var spec with a multi-value call", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func pair(vaultRoot string) (string, error) { return filepath.Join(vaultRoot, "a"), nil }
func Repair(vaultRoot string) error {
	var p, err = pair(vaultRoot)
	if err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o644)
}
`}},
		{"a tuple forwarded by return f()", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func inner(vaultRoot string) (string, error) { return filepath.Join(vaultRoot, "a"), nil }
func outer(vaultRoot string) (string, error) { return inner(vaultRoot) }
func Repair(vaultRoot string) error { p, _ := outer(vaultRoot); return os.WriteFile(p, nil, 0o644) }
`}},
		{"cross-package producer (resolved by full import path)", "migrate.Write", map[string]string{
			"internal/storage/x.go": pre("storage") + `
func Marker(vaultRoot string) (string, error) { return filepath.Join(vaultRoot, "m"), nil }
`,
			"internal/migrate/x.go": pre("migrate", "github.com/suykerbuyk/vibe-palace/internal/storage") + `
func Write(vaultRoot string) error {
	p, err := storage.Marker(vaultRoot)
	if err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o644)
}
`}},
		{"wrapper", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeRaw(p string, b []byte) error { return os.WriteFile(p, b, 0o644) }
func Repair(vaultRoot string) error { return writeRaw(filepath.Join(vaultRoot, "m"), nil) }
`}},
		{"wrapper of a wrapper", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeRaw(p string, b []byte) error { return os.WriteFile(p, b, 0o644) }
func writeTwice(q string) error { return writeRaw(q, nil) }
func Repair(vaultRoot string) error { return writeTwice(filepath.Join(vaultRoot, "m")) }
`}},
		{"wrapper deriving a .tmp sibling in a local (the reviewer's repro)", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeTmp(path string, b []byte) error {
	q := path + ".tmp"
	return os.WriteFile(q, b, 0o644)
}
func Repair(vaultRoot string) error { return writeTmp(filepath.Join(vaultRoot, "m"), nil) }
`}},
		{"wrapper deriving the path in a var declaration", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeVar(path string) error {
	var q = path + ".tmp"
	return os.Remove(q)
}
func Repair(vaultRoot string) error { return writeVar(filepath.Join(vaultRoot, "m")) }
`}},
		{"wrapper with a two-hop local chain", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeChain(path string) error {
	a := path
	b := a + ".x"
	return os.Remove(b)
}
func Repair(vaultRoot string) error { return writeChain(filepath.Join(vaultRoot, "m")) }
`}},
		{"wrapper joining the parameter into a local", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeUnder(dir string, b []byte) error {
	q := filepath.Join(dir, "x")
	return os.WriteFile(q, b, 0o644)
}
func Repair(vaultRoot string) error { return writeUnder(vaultRoot, nil) }
`}},
		{"a derived-local wrapper called through another wrapper", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeTmp(path string) error {
	q := path + ".tmp"
	return os.WriteFile(q, nil, 0o644)
}
func outer(p string) error { r := filepath.Clean(p); return writeTmp(r) }
func Repair(vaultRoot string) error { return outer(filepath.Join(vaultRoot, "m")) }
`}},
		{"import alias of os", "archive.Repair", map[string]string{"internal/archive/x.go": `package archive
import (
	o "os"
	"path/filepath"
)
func Repair(vaultRoot string) error { return o.WriteFile(filepath.Join(vaultRoot, "m"), nil, 0o644) }
`}},
		{"local variable holding os.WriteFile", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
func Repair(vaultRoot string) error { w := os.WriteFile; return w(filepath.Join(vaultRoot, "m"), nil, 0o644) }
`}},
		{"package-level seam var holding os.Remove", "archive.Repair", map[string]string{"internal/archive/x.go": pre("archive") + `
var seam = os.Remove
func Repair(vaultRoot string) error { return seam(filepath.Join(vaultRoot, "m")) }
`}},
		{"real storage.Vault.Root field", "storage.Repair", map[string]string{"internal/storage/x.go": pre("storage") + `
type Vault struct{ Root string }
func Repair(v *Vault) error { return os.WriteFile(filepath.Join(v.Root, "m"), nil, 0o644) }
`}},
		{"designated root function", "surface.ReadFormat", map[string]string{"internal/surface/x.go": pre("surface") + `
func ReadFormat(root string) error { return os.Remove(filepath.Join(root, "m")) }
`}},
		{"designated (function, parameter) row", "storage.finishClone", map[string]string{"internal/storage/x.go": pre("storage") + `
func finishClone(path string) error { return os.Remove(filepath.Join(path, ".git", "m")) }
`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := funnelFn(t, c.files)
			if !hasSym(got, c.want) {
				t.Errorf("a raw write of a vault path through this route was not reported (%s); got %v", c.want, got)
			}
		})
	}
}

// The other direction: nothing here is a vault write, and flagging it is how a gate
// gets disabled.
func TestTaintDoesNotReportCleanCode(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"a local root := /tmp is not a vault root", map[string]string{"internal/archive/x.go": pre("archive") + `
func F() error { root := "/tmp"; return os.WriteFile(root, nil, 0o644) }
`}},
		{"a local NAMED vaultRoot is not a seed", map[string]string{"internal/archive/x.go": pre("archive") + `
func F() error { vaultRoot := "/tmp"; return os.WriteFile(vaultRoot, nil, 0o644) }
`}},
		{"a vault-named parameter in a non-vault package", map[string]string{"internal/zzz/x.go": pre("zzz") + `
func F(vaultRoot string) error { return os.WriteFile(vaultRoot, nil, 0o644) }
`}},
		{"a root parameter that is not designated", map[string]string{"internal/archive/x.go": pre("archive") + `
func F(root string) error { return os.WriteFile(filepath.Join(root, "m"), nil, 0o644) }
`}},
		{"an unrelated Config.Root", map[string]string{"internal/archive/x.go": pre("archive") + `
type Config struct{ Root string }
func F(c Config) error { return os.WriteFile(filepath.Join(c.Root, "m"), nil, 0o644) }
`}},
		{"a producer called with a non-vault argument", map[string]string{"internal/archive/x.go": pre("archive") + `
func pair(base string) (string, string) { return filepath.Join(base, "a"), filepath.Join(base, "b") }
func F(home string) error { _, mp := pair(home); return os.WriteFile(mp, nil, 0o644) }
`}},
		{"a name shadowed by an unrelated local", map[string]string{"internal/archive/x.go": pre("archive") + `
func F(vaultRoot string) error {
	_ = vaultRoot
	{
		vaultRoot := "/tmp"
		return os.WriteFile(vaultRoot, nil, 0o644)
	}
}
`}},
		{"filepath.Rel is not a path producer", map[string]string{"internal/archive/x.go": pre("archive") + `
func F(vaultRoot, p string) error { rel, _ := filepath.Rel(vaultRoot, p); return os.WriteFile(rel, nil, 0o644) }
`}},
		{"a blessed sink called with a vault path", map[string]string{
			"internal/atomicfile/x.go": pre("atomicfile") + `
func Write(root, path string, b []byte) error { return os.WriteFile(path, b, 0o644) }
`,
			"internal/archive/x.go": pre("archive", "github.com/suykerbuyk/vibe-palace/internal/atomicfile") + `
func F(vaultRoot string) error { return atomicfile.Write(vaultRoot, filepath.Join(vaultRoot, "m"), nil) }
`}},
		{"a wrapper whose local path is unrelated to its parameter", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeElsewhere(path string) error {
	q := "/tmp/fixed" + ".tmp"
	_ = path
	return os.WriteFile(q, nil, 0o644)
}
func F(vaultRoot string) error { return writeElsewhere(filepath.Join(vaultRoot, "m")) }
`}},
		{"a wrapper called with a non-vault path", map[string]string{"internal/archive/x.go": pre("archive") + `
func writeRaw(p string) error { return os.WriteFile(p, nil, 0o644) }
func F(home string) error { return writeRaw(filepath.Join(home, "m")) }
`}},
		{"an O_RDWR-only open is not a write", map[string]string{"internal/archive/x.go": pre("archive") + `
func F(vaultRoot string) error { _, err := os.OpenFile(filepath.Join(vaultRoot, "m"), os.O_RDWR, 0); return err }
`}},
		{"an unresolved cross-package producer taints nothing (fail open, guarded by the floors)", map[string]string{"internal/archive/x.go": pre("archive", "example.com/unknown") + `
func F(vaultRoot string) error { p := unknown.Path(vaultRoot); return os.WriteFile(p, nil, 0o644) }
`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := funnelFn(t, c.files); len(got) != 0 {
				t.Errorf("correct code was reported: %v", got)
			}
		})
	}
}

// initPublish's shape: held.Root() is a typed source that lives in ANOTHER package,
// reachable only through the importer stub built from the vaultlock declarations.
func TestTaintResolvesATypedSourceAcrossAPackageBoundary(t *testing.T) {
	files := map[string]string{
		"internal/vaultlock/x.go": `package vaultlock
type Held struct{ root string }
func (h *Held) Root() string { return h.root }
`,
		"internal/storage/x.go": pre("storage", "github.com/suykerbuyk/vibe-palace/internal/vaultlock") + `
func initPublish(held *vaultlock.Held) {
	path := held.Root()
	_ = os.RemoveAll(path)
}
`}
	if got := funnelFn(t, files); !hasSym(got, "storage.initPublish") {
		t.Fatalf("held.Root() -> os.RemoveAll through a stubbed cross-package receiver was not reported: %v", got)
	}

	// STUBS ARE KEYED BY FULL IMPORT PATH: a package that merely ends in
	// internal/vaultlock is not the vault's lock package and must not be stubbed.
	lookalike := map[string]string{
		"internal/vaultlock/x.go": files["internal/vaultlock/x.go"], // the REAL package is in the tree
		"internal/storage/x.go": pre("storage", "example.com/other/internal/vaultlock") + `
func initPublish(held *vaultlock.Held) {
	path := held.Root()
	_ = os.RemoveAll(path)
}
`}
	if got := funnelFn(t, lookalike); len(got) != 0 {
		t.Errorf("a look-alike import path (suffix match) was treated as the vault's lock package: %v", got)
	}

	// A typed Vault accessor through a stub (the main.initLoggingFor shape).
	acc := map[string]string{
		"internal/storage/x.go": `package storage
type Vault struct{ Root string }
func (v *Vault) VaultLocalDir() string { return v.Root }
`,
		"cmd/vp/x.go": pre("main", "github.com/suykerbuyk/vibe-palace/internal/storage") + `
func logTo(v *storage.Vault) { _ = os.Remove(v.VaultLocalDir() + "/vp.log") }
`}
	if got := funnelFn(t, acc); !hasSym(got, "main.logTo") {
		t.Errorf("a Vault accessor used from another package was not reported: %v", got)
	}
}

// Reverse-set check: a new root-like member on a listed source type must be allowed
// or explicitly rejected; a stub that names a member the code no longer has fails.
func TestVaultSourcesReverseSetAndDrift(t *testing.T) {
	withTables(t)
	vaultSources = []vaultSourceType{{ImportPath: pathStorage, Type: "Vault",
		Allow:  []string{"Root", "MemoryFile", "GoneFile"},
		Reject: map[string]string{"SessionRelPath": "relative, never a destination"}}}
	files := map[string]string{"internal/storage/x.go": `package storage
type Vault struct {
	Root      string
	ExtraRoot string
	Name      string
}
func (v *Vault) MemoryFile() string      { return "" }
func (v *Vault) NewThingDir() string     { return "" }
func (v *Vault) SessionRelPath() string  { return "" }
func (v *Vault) DocFile() (string, error) { return "", nil }
func (v *Vault) Count() int              { return 0 }
`}
	got := funnelSymbols(t, files)
	id := pathStorage + ".Vault."
	for _, want := range []string{
		"sourceaudit.vaultSources/UNLISTED " + id + "ExtraRoot",
		"sourceaudit.vaultSources/UNLISTED " + id + "NewThingDir",
		"sourceaudit.vaultSources/UNLISTED " + id + "DocFile",
		"sourceaudit.vaultSources/STALE " + id + "GoneFile",
	} {
		if !hasSym(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{id + "Root", id + "MemoryFile", id + "SessionRelPath", id + "Name", id + "Count"} {
		for _, s := range got {
			if strings.HasSuffix(s, unwanted) && strings.HasPrefix(s, "sourceaudit.vaultSources/") {
				t.Errorf("%s must not be reported (allowed, rejected, or not root-like): %v", unwanted, got)
			}
		}
	}
}

// The fingerprint format is PINNED: category | file base name | symbol, built from
// the AST at the error's position, with none of go/types' wording, line or column.
func TestTypeIssueFingerprintFormat(t *testing.T) {
	files := map[string]string{
		"internal/zzz/a_unix.go":    "package zzz\nfunc f() {}\n",
		"internal/zzz/a_windows.go": "package zzz\nfunc f() {}\n",
		"internal/zzz/b.go": `package zzz
import "os"
type T struct{}
var _ = T{}.Missing
func g() { undefinedThing() }
var _ = os.NoSuchMember
`}
	tree := typeCheckTree(mustLoad(t, writeTree(t, files)))
	var got []string
	for _, p := range tree.pkgs {
		if p.name == "zzz" {
			got = p.sortedIssues()
			if p.expected != 1 {
				t.Errorf("os.NoSuchMember is a member of a fake import and must be counted as expected, got %d expected", p.expected)
			}
		}
	}
	want := []string{
		"declaration-conflict|a_unix.go|f",
		"declaration-conflict|a_windows.go|f",
		"no-member|b.go|Missing",
		"undefined-name|b.go|undefinedThing",
	}
	if !slices.Equal(got, want) {
		t.Errorf("fingerprints = %v\nwant           %v", got, want)
	}
	for _, g := range got {
		if strings.Contains(g, "redeclared") || strings.Contains(g, "undefined:") || strings.Contains(g, ":") {
			t.Errorf("fingerprint %q carries go/types wording or a position", g)
		}
	}
}

func mustLoad(t *testing.T, root string) []file {
	t.Helper()
	fs, err := loadPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// typecheckAccounted compares fingerprints, not counts: swapping one expected
// diagnostic for a different unexpected one at the SAME count is reported both ways.
func TestTypeIssueAccountingIsByFingerprintNotCount(t *testing.T) {
	withTables(t)
	files := map[string]string{
		"internal/zzz/a_unix.go":    "package zzz\nfunc f() {}\n",
		"internal/zzz/a_windows.go": "package zzz\nfunc g() {}\nfunc f() {}\n",
	}
	pkg := modulePath + "/internal/zzz"
	// exactly what the tree produces: accounted, so no typecheck finding.
	typecheckAccounted = map[string]accountedIssues{pkg: {reason: "twins", fingerprints: []string{
		"declaration-conflict|a_unix.go|f", "declaration-conflict|a_windows.go|f"}}}
	for _, s := range funnelSymbols(t, files) {
		if strings.HasPrefix(s, "sourceaudit.typecheck/") {
			t.Errorf("an accounted diagnostic set was reported: %s", s)
		}
	}
	// same count (2), one fingerprint substituted: NEW and STALE, never silent.
	typecheckAccounted = map[string]accountedIssues{pkg: {reason: "twins", fingerprints: []string{
		"declaration-conflict|a_unix.go|f", "declaration-conflict|a_windows.go|g"}}}
	got := funnelSymbols(t, files)
	if !hasSym(got, "sourceaudit.typecheck/NEW "+pkg+" declaration-conflict|a_windows.go|f") ||
		!hasSym(got, "sourceaudit.typecheck/STALE "+pkg+" declaration-conflict|a_windows.go|g") {
		t.Errorf("a same-count substitution was not reported both ways: %v", got)
	}
}

// Exemptions are ENUMERATED call edges with counts: a new caller of the same callee,
// or a second call from the allowed caller, is a finding; an edge with no call is STALE.
func TestFunnelAllowedEdgesAreEnumeratedNotGlobal(t *testing.T) {
	withTables(t)
	funnelAllowedEdges = map[string]allowedEdge{"main.initLoggingFor|" + pathVplog + ".Init": {1, "vp.log, host-local"}}
	vplog := `package vplog
import "os"
func Init(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
`
	base := map[string]string{"internal/vplog/x.go": vplog}
	with := func(body string) map[string]string {
		m := map[string]string{"internal/vplog/x.go": vplog,
			"cmd/vp/x.go": "package main\nimport \"" + pathVplog + "\"\n" + body}
		return m
	}
	_ = base
	allowed := `func initLoggingFor(vaultRoot string) { _ = vplog.Init(vaultRoot + "/vp.log") }`
	got := funnelSymbols(t, with(allowed))
	if hasSym(got, "main.initLoggingFor") || hasSym(got, "sourceaudit.funnelEdge/STALE main.initLoggingFor|"+pathVplog+".Init") {
		t.Errorf("the enumerated edge was not accepted cleanly: %v", got)
	}
	got = funnelSymbols(t, with(allowed+`
func newCaller(vaultRoot string) { _ = vplog.Init(vaultRoot + "/resume.md") }`))
	if !hasSym(got, "main.newCaller") {
		t.Errorf("a NEW caller of vplog.Init with a vault path was not reported (global callee exemption): %v", got)
	}
	got = funnelSymbols(t, with(`func initLoggingFor(vaultRoot string) {
	_ = vplog.Init(vaultRoot + "/vp.log")
	_ = vplog.Init(vaultRoot + "/other.log")
}`))
	if !hasSym(got, "main.initLoggingFor") {
		t.Errorf("a second call from the allowed caller exceeds the edge's count and must be reported: %v", got)
	}
	got = funnelSymbols(t, with(`func initLoggingFor(home string) { _ = vplog.Init(home + "/vp.log") }`))
	if !hasSym(got, "sourceaudit.funnelEdge/STALE main.initLoggingFor|"+pathVplog+".Init") {
		t.Errorf("an edge whose call is no longer vault-derived must be STALE: %v", got)
	}
}
