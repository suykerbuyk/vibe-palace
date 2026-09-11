// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestHashFile verifies the sha256 helper against known input.
func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := templates.HashFile(p)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
	if _, err := templates.HashFile(filepath.Join(dir, "nope")); !os.IsNotExist(err) {
		t.Errorf("missing file err = %v, want IsNotExist", err)
	}
}

// TestBackupNameIsContentAddressed pins the name: "<rel>.<sha12>.bak", with the
// twelve hex characters checked against an independently computed digest, a
// ".bak" suffix and no ':' (Windows-safe).
func TestBackupNameIsContentAddressed(t *testing.T) {
	data := []byte("# my wrap override\n")
	sum := sha256.Sum256(data)
	want := "Templates/commands/wrap.md." + hex.EncodeToString(sum[:])[:12] + ".bak"
	got := templates.BackupName("Templates/commands/wrap.md", data)
	if got != want {
		t.Fatalf("BackupName = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, ".bak") || strings.HasSuffix(got, ".md") || strings.Contains(got, ":") {
		t.Errorf("unsafe backup name %q", got)
	}
	if templates.BackupName("a.md", []byte("x")) == templates.BackupName("a.md", []byte("y")) {
		t.Error("different bytes got the same backup name")
	}
}

func TestPreserveBackup(t *testing.T) {
	const rel = "Templates/commands/wrap.md"
	override := []byte("# my wrap override\n")
	name := templates.BackupName(rel, override)

	t.Run("absent-is-created", func(t *testing.T) {
		vault := t.TempDir()
		b, err := templates.PreserveBackup(vault, rel, override)
		if err != nil {
			t.Fatalf("PreserveBackup: %v", err)
		}
		if b.Rel != name || b.Reused {
			t.Errorf("backup = %+v, want {%s false}", b, name)
		}
		got, err := os.ReadFile(filepath.Join(vault, filepath.FromSlash(name)))
		if err != nil || string(got) != string(override) {
			t.Errorf("backup bytes = %q (err=%v)", got, err)
		}
		// A .bak never stamps: the stamp resolver skips the suffix.
		if _, err := os.Stat(filepath.Join(vault, "Templates", ".surface")); !os.IsNotExist(err) {
			t.Errorf("a backup stamped Templates/.surface (err=%v)", err)
		}
	})

	t.Run("identical-is-reused", func(t *testing.T) {
		vault := t.TempDir()
		if _, err := templates.PreserveBackup(vault, rel, override); err != nil {
			t.Fatal(err)
		}
		b, err := templates.PreserveBackup(vault, rel, override)
		if err != nil {
			t.Fatalf("second PreserveBackup: %v", err)
		}
		if !b.Reused || b.Rel != name {
			t.Errorf("backup = %+v, want reused %s", b, name)
		}
	})

	t.Run("different-bytes-collide", func(t *testing.T) {
		vault := t.TempDir()
		p := filepath.Join(vault, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		const edited = "an operator edited this backup\n"
		if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := templates.PreserveBackup(vault, rel, override)
		if !errors.Is(err, templates.ErrBackupCollision) {
			t.Fatalf("err = %v, want ErrBackupCollision", err)
		}
		if !strings.Contains(err.Error(), "move or rename it") {
			t.Errorf("the collision does not say how to fix it: %v", err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != edited {
			t.Errorf("the existing backup changed: %q", got)
		}
	})

	t.Run("symlink-at-the-name-collides", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need a privilege on Windows")
		}
		vault := t.TempDir()
		p := filepath.Join(vault, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(vault, "same-bytes.txt")
		if err := os.WriteFile(target, override, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if _, err := templates.PreserveBackup(vault, rel, override); !errors.Is(err, templates.ErrBackupCollision) {
			t.Fatalf("err = %v, want ErrBackupCollision for a symlink at the backup name", err)
		}
	})

	t.Run("legacy-bak-siblings-untouched", func(t *testing.T) {
		vault := t.TempDir()
		base := filepath.Join(vault, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
			t.Fatal(err)
		}
		siblings := map[string]string{
			base + ".bak":     "an old binary's single-generation backup\n",
			base + ".new.bak": "someone else's\n",
		}
		for p, body := range siblings {
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := templates.PreserveBackup(vault, rel, override); err != nil {
			t.Fatal(err)
		}
		for p, body := range siblings {
			got, _ := os.ReadFile(p)
			if string(got) != body {
				t.Errorf("%s changed: %q", p, got)
			}
		}
	})

	t.Run("refused-path", func(t *testing.T) {
		vault := t.TempDir()
		if _, err := templates.PreserveBackup(vault, ".git/x.md", override); err == nil {
			t.Error("a backup under .git/ was written")
		}
	})
}
