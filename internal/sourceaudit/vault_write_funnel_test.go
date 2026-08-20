// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// funnelFixture is one synthetic package exercising the shapes the rule must
// separate. Every function here is a claim about the rule, and each is asserted
// below in BOTH directions — a rule that only ever proves it can fire is half a
// rule, and the half it is missing is the one that keeps it enabled.
const funnelFixture = `package fixture

import (
	"os"
	"path/filepath"

	"example.com/atomicfile"
)

type Vault struct{ Root string }

func (v *Vault) MemoryFile(rel string) string { return filepath.Join(v.Root, "memory", rel) }

// BYPASS: a raw removal of a path from a Vault accessor.
func RawRemove(v *Vault, rel string) error {
	abs := v.MemoryFile(rel)
	return os.Remove(abs)
}

// BYPASS: a raw write to a path joined onto a vault root PARAMETER — the
// cross-package idiom that neither deleted pin could see.
func RawWriteFromParam(vaultRoot string) error {
	p := filepath.Join(vaultRoot, "Audits", "baseline.json")
	return os.WriteFile(p, nil, 0o644)
}

// BYPASS: the .bak sibling of a vault path. Suffix concatenation is how three of
// the census's real bypasses actually write.
func RawWriteBackup(vaultRoot string) error {
	dst := filepath.Join(vaultRoot, "Templates", "x.md")
	return os.WriteFile(dst+".bak", nil, 0o644)
}

// BYPASS: an append-mode open on a vault path.
func RawAppend(vaultRoot string) error {
	p := filepath.Join(vaultRoot, "Projects", "p", "iterations.md")
	_, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	return err
}

// BYPASS: atomicfile.Write with a defeated vaultRoot.
func DefeatedRoot(v *Vault, abs string) error {
	return atomicfile.Write("", abs, nil)
}

// CLEAN: the sanctioned whole-file write.
func Sanctioned(v *Vault, abs string) error {
	return atomicfile.Write(v.Root, abs, nil)
}

// CLEAN: the sanctioned write with the root arriving as a parameter.
func SanctionedFromParam(vaultRoot, abs string) error {
	return atomicfile.Write(vaultRoot, abs, nil)
}

// CLEAN: a raw write to a NON-vault path. Flagging this is the failure mode that
// gets a gate disabled.
func RawWriteHostPath(home string) error {
	return os.WriteFile(filepath.Join(home, ".config", "x.toml"), nil, 0o644)
}

// CLEAN: a bare O_RDWR handle on a vault path that is only ever READ. This is
// the internal/storage/drawers.go:164 specimen — the anchor set's own comment
// called it a mutation for months, and it is not one.
func ReadHandle(vaultRoot string) error {
	p := filepath.Join(vaultRoot, "palace", "p", "r", "drawers.jsonl")
	_, err := os.OpenFile(p, os.O_RDWR, 0o644)
	return err
}

// CLEAN: directory creation. Deliberately not a content verb — see the rule doc.
func MakeDir(vaultRoot string) error {
	return os.MkdirAll(filepath.Join(vaultRoot, "Projects", "p"), 0o755)
}
`

func funnelIDs(t *testing.T) []string {
	t.Helper()
	findings, err := Run(writeFixture(t, funnelFixture))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindVaultWriteOutsideFunnel {
			out = append(out, f.Symbol)
		}
	}
	return out
}

// TestFunnelFlagsBypasses is the rule's mutation proof. Each shape below is a
// distinct resolver hop, and the two parameter-rooted cases are the ones that
// justify this rule replacing the package-local pins at all: neither deleted pin
// could resolve a vault root that arrived as a plain string parameter, which is
// how internal/archive, internal/reconcile, internal/surface and internal/check
// all receive it.
func TestFunnelFlagsBypasses(t *testing.T) {
	got := funnelIDs(t)
	for _, want := range []string{
		"fixture.RawRemove",
		"fixture.RawWriteFromParam",
		"fixture.RawWriteBackup",
		"fixture.RawAppend",
		"fixture.DefeatedRoot",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is a vault mutation outside the funnel and the rule did not flag it. "+
				"A ratchet that cannot see the defect it was written for is coverage in name only.\n  got: %v",
				want, got)
		}
	}
}

// TestFunnelDoesNotFlagCleanCode pins the precision claim, and it is the half
// that keeps the gate enabled. 203's asymmetry: a rule that reddens correct code
// teaches everyone to wave off its findings, and the wave-off is permanent.
//
// ReadHandle is the load-bearing case. It is the drawers.go:164 shape — a bare
// O_RDWR handle that is only scanned — and the anchor set's own comment
// (internal/sourceaudit/ungated_writer.go:49-52) has miscalled it a mutation for
// months. A replacement rule that reproduced that mistake would be worse than
// the pins it replaced.
func TestFunnelDoesNotFlagCleanCode(t *testing.T) {
	got := funnelIDs(t)
	for _, unwanted := range []string{
		"fixture.Sanctioned",
		"fixture.SanctionedFromParam",
		"fixture.RawWriteHostPath",
		"fixture.ReadHandle",
		"fixture.MakeDir",
	} {
		if slices.Contains(got, unwanted) {
			t.Errorf("%s is correct code and the rule flagged it — a noisy gate is a disabled gate.\n  got: %v",
				unwanted, got)
		}
	}
}

// TestFunnelReportsVacuity proves the anti-vacuity guard is itself live.
//
// The two deleted pins each carried a t.Fatalf floor check. This rule has no
// *testing.T, so vacuity is reported as a FINDING that is deliberately absent
// from the baseline — the gate then fails on it exactly like new debt. That
// substitution is only worth anything if the finding actually appears, so pin
// it: a tiny fixture cannot possibly meet the tree-wide floors, and must say so
// rather than passing green.
func TestFunnelReportsVacuity(t *testing.T) {
	findings, err := Run(writeFixture(t, "package fixture\n"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var vac string
	for _, f := range findings {
		if f.Kind == KindVaultWriteOutsideFunnel && strings.HasSuffix(f.Symbol, "/VACUOUS") {
			vac = f.Detail
		}
	}
	if vac == "" {
		t.Fatalf("a fixture with no vault writers at all did NOT report vacuity, so the floors are not "+
			"running. A silent walk that finds nothing is indistinguishable from a clean tree, which is "+
			"the false-green this package exists to eliminate.\n  findings: %v", ids(findings))
	}
	if !strings.Contains(vac, "Do NOT baseline this entry") {
		t.Errorf("the vacuity finding must tell the reader not to baseline it — otherwise the first "+
			"person to see it will silence it exactly like real debt.\n  detail: %s", vac)
	}
}
