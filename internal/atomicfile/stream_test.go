// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// tempSiblings counts the primitive's own temp files left in dir. Every one of
// them is a leak: the temp is an implementation detail that must not outlive
// the call, on the success path or on any failure path.
func tempSiblings(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".vp-atomic-") {
			n++
		}
	}
	return n
}

func TestWriteStream_RoundTripCreatesParentAndDefaultPerm(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "stream.bin")

	if err := WriteStream("", p, func(w io.Writer) error {
		// Two writes, because a streaming caller emits incrementally — that is
		// the whole reason this primitive is not Write.
		if _, err := io.WriteString(w, "chunk-one "); err != nil {
			return err
		}
		_, err := io.WriteString(w, "chunk-two")
		return err
	}); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "chunk-one chunk-two" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %o, want 0644", info.Mode().Perm())
	}
	if n := tempSiblings(t, filepath.Dir(p)); n != 0 {
		t.Fatalf("%d temp file(s) left after a successful write", n)
	}
}

// TestWriteStream_FillErrorLeavesTargetIntact pins the half that matters most:
// a stream that fails PART WAY THROUGH must not be able to damage the
// destination. The hand-rolled temp+rename this replaced got that right, and a
// primitive that lost it would be a regression disguised as consolidation.
func TestWriteStream_FillErrorLeavesTargetIntact(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stream.bin")
	if err := os.WriteFile(p, []byte("prior"), 0o644); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("encoder exploded")
	err := WriteStream("", p, func(w io.Writer) error {
		if _, werr := io.WriteString(w, "partial garbage"); werr != nil {
			return werr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the caller's own error unwrapped", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "prior" {
		t.Fatalf("target = %q, want the prior content untouched", got)
	}
	if n := tempSiblings(t, dir); n != 0 {
		t.Fatalf("%d temp file(s) left after a failed write", n)
	}
}

// TestWriteStream_StampsVaultDestination proves the streamed write is treated as
// CONTENT: it stamps, like Write and unlike the removal sink. A fresh temp vault
// gives a stamp-dir path the per-process cache has never seen, so a missing
// stamp cannot be masked by an earlier test's.
func TestWriteStream_StampsVaultDestination(t *testing.T) {
	vaultRoot := t.TempDir()
	p := filepath.Join(vaultRoot, "Projects", "demo", "transcripts", "x.jsonl.zst")

	if err := WriteStream(vaultRoot, p, func(w io.Writer) error {
		_, err := io.WriteString(w, "body")
		return err
	}); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}

	s, err := surface.ReadStamp(filepath.Join(vaultRoot, "Projects", "demo"))
	if err != nil {
		t.Fatalf("ReadStamp: %v", err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp = surface %d, want %d", s.Surface, surface.MCPSurfaceVersion)
	}
}

// TestWriteStream_EmptyVaultRootSkipsStamp is the other half of the contract,
// and it is the one a caller gets WRONG silently: passing "" for a real vault
// path writes the file and skips the stamp. The funnel rule polices the call
// sites; this pins the behaviour those sites are policed against.
func TestWriteStream_EmptyVaultRootSkipsStamp(t *testing.T) {
	vaultRoot := t.TempDir()
	p := filepath.Join(vaultRoot, "Projects", "demo", "transcripts", "x.jsonl.zst")

	if err := WriteStream("", p, func(w io.Writer) error {
		_, err := io.WriteString(w, "body")
		return err
	}); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not written: %v", err)
	}
	// ReadStamp reports an ABSENT stamp as Surface 0 with a nil error, so the
	// assertion is on the version, not on err — checking err here would pass
	// vacuously whether or not the stamp was written.
	s, err := surface.ReadStamp(filepath.Join(vaultRoot, "Projects", "demo"))
	if err != nil {
		t.Fatalf("ReadStamp: %v", err)
	}
	if s.Surface != 0 {
		t.Fatalf("stamp = surface %d, want none — a \"\" vault root must skip stamping", s.Surface)
	}
}
