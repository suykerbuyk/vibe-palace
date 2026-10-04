// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"testing"
)

// The creating-project owner rule. Its healthy steady state is zero findings
// outside the baseline, so each break below is asserted to fire beside a tree
// of the sanctioned shapes that must stay clean.

const cpAtomicfile = `package atomicfile

type Option func()

func CreatingProject() Option { return func() {} }
`

const cpVaultfs = `package vaultfs

import "example.com/atomicfile"

type CreateOption func()

func CreatingProject() CreateOption { return func() {} }

// CLEAN: the forwarding owner.
func Create(path string, opts ...CreateOption) { _ = atomicfile.CreatingProject() }

func RenameNoLock(a, b string) error { return nil }
`

const cpReconcile = `package reconcile

import "example.com/vaultfs"

type TemplateTreeReconciler struct{}

// CLEAN: the init scaffold.
func (r *TemplateTreeReconciler) applyScaffold() { vaultfs.Create("x", vaultfs.CreatingProject()) }
`

const cpStorage = `package storage

import "example.com/atomicfile"

// CLEAN: the lifecycle copy's destination writes.
func CopyProjectTreeEntry() { _ = atomicfile.CreatingProject() }

func EnsureDir(p string) error { return nil }
`

func cpFindings(t *testing.T, extra map[string]string) []string {
	t.Helper()
	pkgs := map[string]string{"atomicfile": cpAtomicfile, "vaultfs": cpVaultfs, "reconcile": cpReconcile, "storage": cpStorage}
	for k, v := range extra {
		pkgs[k] = v
	}
	findings, err := Run(writeFixtureTree(t, pkgs))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindCreatingProjectOwner {
			out = append(out, f.Symbol)
		}
	}
	return out
}

func TestCreatingProjectOwnerIsSilentOnTheSanctionedShapes(t *testing.T) {
	if got := cpFindings(t, nil); len(got) != 0 {
		t.Fatalf("findings on the sanctioned tree: %q", got)
	}
}

func TestCreatingProjectOwnerReportsEachBypass(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, src string
		want           []string
	}{
		{"a new caller of the option", "tools", `package tools

import "example.com/vaultfs"

func writeIt() { vaultfs.Create("x", vaultfs.CreatingProject()) }
`, []string{"tools.writeIt -> vaultfs.CreatingProject"}},
		{"under an alias, in a package-level var", "tools", `package tools

import af "example.com/atomicfile"

var opt = af.CreatingProject()
`, []string{"tools.opt -> atomicfile.CreatingProject"}},
		{"under a dot import", "tools", `package tools

import . "example.com/atomicfile"

func writeIt() { _ = CreatingProject() }
`, []string{"tools.writeIt -> CreatingProject"}},
		{"a new RenameNoLock caller", "tools", `package tools

import "example.com/vaultfs"

func moveIt() { _ = vaultfs.RenameNoLock("a", "b") }
`, []string{"raw-sink tools.moveIt"}},
		{"a new os.MkdirAll in a vault writer", "archive", `package archive

import "os"

func mk() { _ = os.MkdirAll("x", 0o755) }
`, []string{"raw-sink archive.mk"}},
		{"a new os.Mkdir in a vault writer", "hook", `package hook

import "os"

func mk() { _ = os.Mkdir("x", 0o755) }
`, []string{"raw-sink hook.mk"}},
		{"EnsureDir inside storage", "storage", cpStorage + `
func writeIt() { _ = EnsureDir("x") }
`, []string{"storage-ensuredir storage.writeIt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cpFindings(t, map[string]string{tc.pkg: tc.src}); !slices.Equal(got, tc.want) {
				t.Fatalf("findings = %q, want %q", got, tc.want)
			}
		})
	}
}

// os.MkdirAll outside the vault-writing packages is not this rule's business.
func TestCreatingProjectOwnerIgnoresMkdirAllOutsideVaultWriters(t *testing.T) {
	got := cpFindings(t, map[string]string{"cache": `package cache

import "os"

func mk() { _ = os.MkdirAll("x", 0o755) }
`})
	if len(got) != 0 {
		t.Fatalf("findings = %q, want none", got)
	}
}

func TestCreatingProjectOwnerAnchors(t *testing.T) {
	got := cpFindings(t, map[string]string{"reconcile": "package reconcile\n", "storage": "package storage\n"})
	for _, want := range []string{
		"sourceaudit.creatingProjectOwner/ABSENT/reconcile.TemplateTreeReconciler.applyScaffold",
		"sourceaudit.creatingProjectOwner/ABSENT/storage.CopyProjectTreeEntry",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing anchor %q in %q", want, got)
		}
	}
}
