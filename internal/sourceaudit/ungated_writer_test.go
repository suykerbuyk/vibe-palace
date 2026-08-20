// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"testing"
)

// ungatedFixture builds the shape the rule exists to catch: a command
// constructor whose Run closure reaches a stamped vault write, registered on a
// registry. register is spliced in verbatim so a caller can test the wrapped and
// unwrapped forms against an otherwise identical tree.
func ungatedFixture(register string) map[string]string {
	return map[string]string{
		"surface": `package surface

func StampForPath(vaultRoot, writePath string) error { return nil }
`,
		"atomicfile": `package atomicfile

import "example.com/fixture/surface"

func Write(vaultRoot, absPath string, data []byte) error {
	return surface.StampForPath(vaultRoot, absPath)
}
`,
		"templates": `package templates

import "example.com/fixture/atomicfile"

type Executor struct{}

func NewExecutor() *Executor { return &Executor{} }

// Write is a METHOD. Its receiver type is not recoverable without go/types, so
// this is the hop the local constructor inference has to carry.
func (e *Executor) Write(dst string, data []byte) error {
	return atomicfile.Write("/vault", dst, data)
}
`,
		"commands": `package commands

import "example.com/fixture/templates"

func Apply() error { return applyWithPolicy() }

func applyWithPolicy() error {
	exec := templates.NewExecutor()
	return exec.Write("/vault/Templates/x.md", nil)
}
`,
		"main": `package main

import "example.com/fixture/commands"

type Command struct {
	Name string
	Run  func() int
}

type Registry struct{}

func (r *Registry) Register(c *Command) {}

func mutates(c *Command) *Command { return c }

func cmdUpgrade() *Command {
	return &Command{
		Name: "upgrade",
		Run: func() int {
			if err := commands.Apply(); err != nil {
				return 1
			}
			return 0
		},
	}
}

func cmdList() *Command {
	return &Command{Name: "list", Run: func() int { return 0 }}
}

func registerAll(reg *Registry) {
` + register + `
	reg.Register(cmdList())
}
`,
	}
}

// TestUngatedVaultWriterIsFlagged is the rule's mutation proof, run on every
// `go test`. It was proven red before the fix by removing the real
// mutates(cmdCommandsUpgrade()) wrapper in cmd/vp/commands.go, at which point the
// audit reported main.cmdCommandsUpgrade; restoring the wrapper cleared it.
//
// Every hop below is one the resolver must carry, and the chain is the real one:
// same-package bare call -> package-qualified call -> METHOD call on a receiver
// assigned from a constructor in the same body -> package-qualified call -> sink.
// Break any of them and this goes green while the tree is unprotected, which is
// the exact failure shape this rule was written against.
func TestUngatedVaultWriterIsFlagged(t *testing.T) {
	root := writeMultiPkgFixture(t, ungatedFixture("\treg.Register(cmdUpgrade())"))

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); !slices.Contains(got, "ungated-vault-writer main.cmdUpgrade") {
		t.Fatalf("cmdUpgrade reaches surface.StampForPath and is registered WITHOUT mutates(), "+
			"and the rule did not flag it. A ratchet that cannot see the defect it was written "+
			"for is coverage in name only.\n  findings: %v", got)
	}
}

// TestGatedVaultWriterIsNotFlagged is the other half. 203's asymmetry applies
// here too: a gate that reddens a correctly-wrapped command teaches everyone to
// wave off its findings, and the wave-off is permanent.
func TestGatedVaultWriterIsNotFlagged(t *testing.T) {
	root := writeMultiPkgFixture(t, ungatedFixture("\treg.Register(mutates(cmdUpgrade()))"))

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); slices.Contains(got, "ungated-vault-writer main.cmdUpgrade") {
		t.Fatalf("cmdUpgrade IS wrapped in mutates() and was flagged anyway: %v", got)
	}
}

// TestNonWritingCommandIsNotFlagged pins the precision claim. cmdList reaches
// nothing; if it is flagged, the reachability walk is leaking through the
// registry rather than through the call graph.
func TestNonWritingCommandIsNotFlagged(t *testing.T) {
	root := writeMultiPkgFixture(t, ungatedFixture("\treg.Register(cmdUpgrade())"))

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); slices.Contains(got, "ungated-vault-writer main.cmdList") {
		t.Fatalf("cmdList writes nothing and was flagged: %v", got)
	}
}

// TestExternalTestPackageDoesNotSeverTheCallGraph pins the bug that cost this
// rule its own target chain during development.
//
// The directory→package map is built from parsed files. An external test package
// (package commands_test in internal/commands/) would overwrite the directory's
// real package name, so every import of that directory resolved to "commands_test"
// — a package nothing declares — and the walk died at that hop. The rule reported
// four findings and looked healthy while MISSING the defect it was written for.
// That is the silent-false-negative shape sourceaudit's own history warns about
// twice, so it gets a pin rather than a comment.
func TestExternalTestPackageDoesNotSeverTheCallGraph(t *testing.T) {
	src := ungatedFixture("\treg.Register(cmdUpgrade())")
	root := writeMultiPkgFixture(t, src)
	writeExtraFile(t, root, "commands", "commands_ext_test.go", `package commands_test

func helper() {}
`)

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); !slices.Contains(got, "ungated-vault-writer main.cmdUpgrade") {
		t.Fatalf("an external test package in internal/commands severed the call graph: the "+
			"directory→package map must be built from NON-TEST files only.\n  findings: %v", got)
	}
}

// TestNonStampingMutatorIsFlagged pins the class the first version of this rule
// could not see at all.
//
// internal/vaultfs/write.go:210-213 and :268-271 state it outright: Delete uses
// os.Remove and Move uses os.Rename, so neither routes through atomicfile and
// neither triggers surface stamping. Anchored only at the stamp, `vp vault
// delete` and `vp vault move` were structurally invisible — unwrapping their
// mutates() left the gate green. Removing "vaultfs.Delete" from
// vaultMutationSinks must turn this red.
func TestNonStampingMutatorIsFlagged(t *testing.T) {
	root := writeMultiPkgFixture(t, map[string]string{
		"vaultfs": `package vaultfs

import "os"

// Delete removes content rather than writing it, so it never stamps.
func Delete(vaultPath, relPath string) error { return os.Remove(relPath) }
`,
		"main": `package main

import "example.com/fixture/vaultfs"

type Command struct {
	Name string
	Run  func() int
}

type Registry struct{}

func (r *Registry) Register(c *Command) {}

func mutates(c *Command) *Command { return c }

func cmdVaultDelete() *Command {
	return &Command{
		Name: "vault delete",
		Run: func() int {
			if err := vaultfs.Delete("/vault", "x.md"); err != nil {
				return 1
			}
			return 0
		},
	}
}

func registerAll(reg *Registry) {
	reg.Register(cmdVaultDelete())
}
`,
	})

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); !slices.Contains(got, "ungated-vault-writer main.cmdVaultDelete") {
		t.Fatalf("a non-stamping vault mutation (vaultfs.Delete) reached from an unwrapped "+
			"command was not flagged. Deletion and rename mutate the vault WITHOUT stamping "+
			"(vaultfs/write.go:210,268), so anchoring only at surface.StampForPath cannot see "+
			"them by construction.\n  findings: %v", got)
	}
}

// TestDeclaredParameterReceiverResolves pins the hop that made `vp tasks edit`
// invisible: runTasksEdit(vault *storage.Vault, ...) then vault.OverwriteTaskFile(...).
// The receiver is a PARAMETER, not a same-body constructor result. Passing a
// vault into a helper is the dominant idiom in this tree, so losing this arm
// silently blinds the rule to most of cmd/vp.
func TestDeclaredParameterReceiverResolves(t *testing.T) {
	root := writeMultiPkgFixture(t, map[string]string{
		"surface": `package surface

func StampForPath(vaultRoot, writePath string) error { return nil }
`,
		"storage": `package storage

import "example.com/fixture/surface"

type Vault struct{ Root string }

func (v *Vault) OverwriteTaskFile(slug, body string) error {
	return surface.StampForPath(v.Root, slug)
}
`,
		"main": `package main

import "example.com/fixture/storage"

type Command struct {
	Name string
	Run  func() int
}

type Registry struct{}

func (r *Registry) Register(c *Command) {}

func mutates(c *Command) *Command { return c }

func runTasksEdit(vault *storage.Vault, slug string) int {
	if err := vault.OverwriteTaskFile(slug, "body"); err != nil {
		return 1
	}
	return 0
}

func cmdTasksEdit() *Command {
	return &Command{
		Name: "tasks edit",
		Run: func() int {
			var v *storage.Vault
			return runTasksEdit(v, "slug")
		},
	}
}

func registerAll(reg *Registry) {
	reg.Register(cmdTasksEdit())
}
`,
	})

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ids(findings); !slices.Contains(got, "ungated-vault-writer main.cmdTasksEdit") {
		t.Fatalf("a receiver whose package comes from its DECLARED parameter type did not "+
			"resolve, so the walk stopped at the helper. This is the `vp tasks edit` "+
			"shape.\n  findings: %v", got)
	}
}
