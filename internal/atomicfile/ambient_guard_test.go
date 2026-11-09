// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// ambientWriteFixture sets up the shared fixture for the two rows below: a temp
// vault installed as the ambient configured root, and the vault-relative target
// of a write that must be refused.
//
// 🔴 THE FIXTURE HAS TO REACH THE GUARD, AND THAT IS NOT AUTOMATIC. The write
// passes CreatingProject(). Without it, projectdir.RefuseUninitialisedAbs
// refuses the write for an unrelated reason the moment the guard is removed, so
// the mutation's "the write completed" assertion would pass on a write that
// never happened — a green row pinning nothing. "Create the project tree" is
// NOT a substitute: ClassifyProjectDir wants a scaffold marker
// (commands/README.md or skills/README.md) or real history, and a bare
// Projects/p/ classifies as a phantom.
//
// The ambient root is a TEMP directory so the non-temp arm cannot fire: a panic
// here can only have come from the ambient arm.
func ambientWriteFixture(t *testing.T) (vault, target string) {
	t.Helper()
	vault = t.TempDir()
	restore := surface.SetAmbientVaultRoot(vault)
	t.Cleanup(restore)
	return vault, filepath.Join(vault, "Projects", "p", "x.md")
}

// TestWriteAtomicRefusesAmbientVaultUnderTest drives the real
// atomicfile.Write -> writeAtomic and asserts the seat fires.
func TestWriteAtomicRefusesAmbientVaultUnderTest(t *testing.T) {
	vault, target := ambientWriteFixture(t)

	var err error
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("atomicfile.Write returned %v for a write to the ambient vault root %s; expected a panic", err, vault)
			}
			msg, _ := r.(string)
			if !strings.Contains(msg, "CONFIGURED vault") {
				t.Fatalf("panic came from the wrong arm: %v", r)
			}
			if !strings.Contains(msg, vault) {
				t.Errorf("panic message does not name the configured root %s: %v", vault, r)
			}
		}()
		err = Write(vault, target, []byte("body\n"), CreatingProject())
	}()

	// The pre-emption check, asserted rather than reasoned about: if this ever
	// fails, the row stopped reaching the seat and its mutation is inert.
	if errors.Is(err, projectdir.ErrUninitialisedProject) {
		t.Fatalf("the write was pre-empted by projectdir instead of reaching the guard: %v", err)
	}
}

// TestWriteAtomicRefusalCreatesNothing pins that the seat is the FIRST
// statement of writeAtomic, ahead of MkdirAll and the temp file.
//
// 🔴 THE PARENT DIRECTORY IS THE PIN. The ".vp-atomic-* is absent" check is
// cleanup hygiene and discriminates nothing: MkdirAll runs before CreateTemp,
// and writeAtomic's deferred cleanup removes the temp file during a panic, so
// no temp file remains whether the guard is seated first, seated after the temp
// file, or deleted outright. Only the absent parent directory separates those.
func TestWriteAtomicRefusalCreatesNothing(t *testing.T) {
	vault, target := ambientWriteFixture(t)
	dir := filepath.Dir(target)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected the ambient-vault panic")
			}
		}()
		_ = Write(vault, target, []byte("body\n"), CreatingProject())
	}()

	if _, err := os.Stat(dir); err == nil {
		t.Errorf("refusal left the directory %s on disk", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s: %v", dir, err)
	}

	// Logged, not asserted as the pin — see the note above.
	matches, _ := filepath.Glob(filepath.Join(dir, ".vp-atomic-*"))
	t.Logf("temp-file hygiene (not the pin): %d .vp-atomic-* under %s", len(matches), dir)
}
