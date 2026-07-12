// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// seedResume plants an initial resume.md for project and returns its path. It
// writes through the os directly rather than WriteResume so the seed is not
// itself subject to the compare-and-set guard the tests are exercising.
func seedResume(t *testing.T, v *Vault, project, content string) string {
	t.Helper()
	path, err := v.ResumeFile(project)
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}
	return path
}

// TestWriteResumeCAS is the compare-and-set contract of the whole-file resume
// writer: a matching guard writes, a mismatched guard is REFUSED with the file
// untouched, an empty guard means "assert absent", and the refusal carries the
// actual current digest so the caller can retry without a second read.
func TestWriteResumeCAS(t *testing.T) {
	t.Run("matching sha writes", func(t *testing.T) {
		v := testVault(t)
		path := seedResume(t, v, "proj", "# Resume\nv1\n")
		if err := v.WriteResume("proj", "# Resume\nv2\n", sha256Hex("# Resume\nv1\n")); err != nil {
			t.Fatalf("WriteResume: %v", err)
		}
		if got := readResumeFile(t, path); got != "# Resume\nv2\n" {
			t.Errorf("content = %q, want the replacement", got)
		}
	})

	t.Run("mismatched sha is refused and the file is untouched", func(t *testing.T) {
		v := testVault(t)
		const seeded = "# Resume\nv1\n"
		path := seedResume(t, v, "proj", seeded)

		err := v.WriteResume("proj", "# CLOBBER\n", sha256Hex("something else entirely"))
		if err == nil {
			t.Fatal("WriteResume with a mismatched sha succeeded; want ErrShaConflict")
		}
		if !errors.Is(err, vaultfs.ErrShaConflict) {
			t.Fatalf("error = %v, want errors.Is(err, vaultfs.ErrShaConflict)", err)
		}
		// A refused write that already corrupted the file is worse than useless.
		if got := readResumeFile(t, path); got != seeded {
			t.Errorf("refused write mutated the file: got %q, want %q", got, seeded)
		}
	})

	t.Run("empty sha with the file absent creates it", func(t *testing.T) {
		v := testVault(t)
		if err := v.WriteResume("proj", "# Resume\nfirst\n", ""); err != nil {
			t.Fatalf("first-write with empty guard: %v", err)
		}
		path, err := v.ResumeFile("proj")
		if err != nil {
			t.Fatalf("ResumeFile: %v", err)
		}
		if got := readResumeFile(t, path); got != "# Resume\nfirst\n" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("empty sha with the file present is a conflict", func(t *testing.T) {
		v := testVault(t)
		const seeded = "# Resume\nalready here\n"
		path := seedResume(t, v, "proj", seeded)

		err := v.WriteResume("proj", "# CLOBBER\n", "")
		if err == nil {
			t.Fatal("empty guard against an existing resume succeeded; the blind-overwrite path must not exist")
		}
		if !errors.Is(err, vaultfs.ErrShaConflict) {
			t.Fatalf("error = %v, want errors.Is(err, vaultfs.ErrShaConflict)", err)
		}
		if got := readResumeFile(t, path); got != seeded {
			t.Errorf("refused write mutated the file: got %q", got)
		}
	})

	t.Run("the refusal carries the actual current sha", func(t *testing.T) {
		v := testVault(t)
		const seeded = "# Resume\nv1\n"
		seedResume(t, v, "proj", seeded)
		want := sha256Hex(seeded)

		err := v.WriteResume("proj", "# CLOBBER\n", sha256Hex("stale"))
		if err == nil {
			t.Fatal("want a conflict")
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry the current sha %s", err, want)
		}
		var conflict *ResumeConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("error %v is not a *ResumeConflictError", err)
		}
		if conflict.Current != want {
			t.Errorf("conflict.Current = %s, want %s", conflict.Current, want)
		}
	})
}

// TestWriteResumeRefusesStaleRead pins the exact production bug: an agent reads
// the resume, spends minutes composing a full-body rewrite, and in the meantime a
// second writer lands an edit. The first writer's blind overwrite used to
// silently revert it. Under compare-and-set the stale write must be REFUSED and
// the second writer's content must survive.
func TestWriteResumeRefusesStaleRead(t *testing.T) {
	v := testVault(t)
	path := seedResume(t, v, "proj", "# Resume\n\n## Open Threads\n")

	// Writer A reads and captures the digest of what it read.
	base := readResumeFile(t, path)
	staleSha := sha256Hex(base)

	// Writer B lands an edit in the meantime, through the same compare-and-set
	// door with a guard that is still current.
	bBase := readResumeFile(t, path)
	if err := v.WriteResume("proj", bBase+"\n### from-writer-b\n", sha256Hex(bBase)); err != nil {
		t.Fatalf("writer B WriteResume: %v", err)
	}

	// Writer A now submits its full-body rewrite composed from the stale read.
	err := v.WriteResume("proj", base+"\n### from-writer-a\n", staleSha)
	if err == nil {
		t.Fatal("the stale full-body write was accepted; writer B's edit would have been silently reverted")
	}
	if !errors.Is(err, vaultfs.ErrShaConflict) {
		t.Fatalf("error = %v, want errors.Is(err, vaultfs.ErrShaConflict)", err)
	}

	final := readResumeFile(t, path)
	if !strings.Contains(final, "### from-writer-b") {
		t.Errorf("writer B's edit was lost:\n%s", final)
	}
	if strings.Contains(final, "### from-writer-a") {
		t.Errorf("the refused write landed anyway:\n%s", final)
	}
}

// readResumeFile reads path or fails the test.
func readResumeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// sha256Hex is the digest of content in the same form WriteResume compares
// against: the hex sha256 of the exact on-disk bytes. atomicfile writes verbatim,
// so hashing the bytes a test handed to WriteResume and hashing the resulting
// file are the same thing.
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
