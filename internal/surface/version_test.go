// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadStamp_MissingReturnsZero(t *testing.T) {
	dir := t.TempDir()
	s, err := ReadStamp(dir)
	if err != nil {
		t.Fatalf("ReadStamp on empty dir: %v", err)
	}
	if s.Surface != 0 {
		t.Fatalf("missing stamp surface = %d, want 0", s.Surface)
	}
}

func TestReadStamp_Malformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stampFilename), []byte("not = [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStamp(dir); err == nil {
		t.Fatal("expected parse error on malformed .surface, got nil")
	}
}

func TestWriteStamp_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStamp(dir, 1, "abcd1234"); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}
	// The emitted file must be exactly the surface line — no provenance.
	raw, err := os.ReadFile(filepath.Join(dir, stampFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "surface = 1\n" {
		t.Fatalf("stamp bytes = %q, want %q", string(raw), "surface = 1\n")
	}
	s, err := ReadStamp(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Surface != 1 || s.LastWriter != "" || s.LastWriteAt != "" {
		t.Fatalf("roundtrip stamp = %+v; want Surface=1 and empty provenance", s)
	}
}

func TestWriteStamp_MonotonicShortCircuit(t *testing.T) {
	dir := t.TempDir()
	// Existing higher version must not be overwritten by a lower one.
	if err := WriteStamp(dir, 5, "high"); err != nil {
		t.Fatal(err)
	}
	if err := WriteStamp(dir, 2, "low"); err != nil {
		t.Fatalf("WriteStamp lower: %v", err)
	}
	s, _ := ReadStamp(dir)
	if s.Surface != 5 {
		t.Fatalf("lower write clobbered higher stamp: %+v", s)
	}
}

func TestWriteStamp_EqualVersionNoOp(t *testing.T) {
	dir := t.TempDir()
	// Pre-write a sentinel stamp at surface=3 carrying a bogus last_writer.
	// A no-op equal-version write must leave these exact bytes untouched.
	sentinel := []byte("surface = 3\nlast_writer = \"sentinel\"\n")
	if err := os.WriteFile(filepath.Join(dir, stampFilename), sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, stampFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteStamp(dir, 3, "second"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, stampFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("equal-version write rewrote the file: before=%q after=%q", before, after)
	}
}

func TestWriteStamp_ByteIdenticalAcrossWriters(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := WriteStamp(dirA, 2, "hostAAAAA"); err != nil {
		t.Fatal(err)
	}
	if err := WriteStamp(dirB, 2, "hostBBBBB"); err != nil {
		t.Fatal(err)
	}
	rawA, err := os.ReadFile(filepath.Join(dirA, stampFilename))
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := os.ReadFile(filepath.Join(dirB, stampFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawA, rawB) {
		t.Fatalf("stamps differ across writers: A=%q B=%q", rawA, rawB)
	}
	if string(rawA) != "surface = 2\n" {
		t.Fatalf("stamp bytes = %q, want %q", string(rawA), "surface = 2\n")
	}
}

func TestResolveStampDir_Roots(t *testing.T) {
	vault := t.TempDir()
	cases := []struct {
		name     string
		writeRel string
		wantSub  string // relative to vault; "" means skip ("")
	}{
		{"projects file", "Projects/foo/resume.md", "Projects/foo"},
		{"projects nested", "Projects/foo/sessions/2026-01-01-01.md", "Projects/foo"},
		{"palace drawers", "palace/foo/drawers/halls/facts/drawers.jsonl", "palace/foo"},
		{"palace kg", "palace/foo/kg/entities.jsonl", "palace/foo"},
		{"templates", "Templates/commands/restart.md", "Templates"},
		{"audits report", "Audits/2026-07-13-vault-audit.md", "Audits"},
		{"audits baseline", "Audits/baseline.json", "Audits"},
		{"palace local excluded", "palace/foo/.local/imported-sessions.jsonl", ""},
		{"vault local excluded", "palace/.local/x", ""},
		{"git excluded", "Projects/foo/.git/config", ""},
		{"bak excluded", "Projects/foo/config.toml.bak", ""},
		{"surface self excluded", "Projects/foo/.surface", ""},
		{"gitignore at root excluded", ".gitignore", ""},
		{"projects missing slug", "Projects", ""},
		{"palace missing slug", "palace", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveStampDir(vault, filepath.Join(vault, tc.writeRel))
			if err != nil {
				t.Fatalf("ResolveStampDir: %v", err)
			}
			want := ""
			if tc.wantSub != "" {
				want = filepath.Join(vault, tc.wantSub)
			}
			if got != want {
				t.Fatalf("ResolveStampDir(%q) = %q, want %q", tc.writeRel, got, want)
			}
		})
	}
}

func TestResolveStampDir_OutsideVault(t *testing.T) {
	vault := t.TempDir()
	other := t.TempDir()
	got, err := ResolveStampDir(vault, filepath.Join(other, "Projects/foo/resume.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("outside-vault write resolved to %q, want empty", got)
	}
}

func TestResolveStampDir_UnrecognizedWarnsOnce(t *testing.T) {
	resetUnrecognizedTopWarnForTest()
	// warnUnrecognizedTopOnce writes to os.Stderr directly; capture it.
	osStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = osStderr }()

	vault := t.TempDir()
	for range 3 {
		got, err := ResolveStampDir(vault, filepath.Join(vault, "Bogus/thing.md"))
		if err != nil || got != "" {
			t.Fatalf("unrecognized top: got=%q err=%v", got, err)
		}
	}
	w.Close()
	os.Stderr = osStderr

	bts := make([]byte, 4096)
	n, _ := r.Read(bts)
	out := string(bts[:n])
	if c := strings.Count(out, "unrecognized path"); c != 1 {
		t.Fatalf("warning fired %d times, want exactly 1; output=%q", c, out)
	}
}

func TestStampForPath_StampsCorrectRoot(t *testing.T) {
	resetStampCacheForTest()
	vault := t.TempDir()
	wpath := filepath.Join(vault, "Projects/foo/resume.md")
	if err := os.MkdirAll(filepath.Dir(wpath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := StampForPath(vault, wpath); err != nil {
		t.Fatalf("StampForPath: %v", err)
	}
	s, err := ReadStamp(filepath.Join(vault, "Projects/foo"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Surface != MCPSurfaceVersion {
		t.Fatalf("stamp surface = %d, want %d", s.Surface, MCPSurfaceVersion)
	}
}

func TestStampForPath_CacheSkipsSecondWrite(t *testing.T) {
	resetStampCacheForTest()
	vault := t.TempDir()
	dir := filepath.Join(vault, "palace/foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w1 := filepath.Join(vault, "palace/foo/drawers/h/r/drawers.jsonl")
	if err := StampForPath(vault, w1); err != nil {
		t.Fatal(err)
	}
	first, _ := ReadStamp(dir)

	// Tamper the stamp on disk; a cached second call must NOT rewrite it.
	if err := os.WriteFile(filepath.Join(dir, stampFilename),
		[]byte("surface = 99\nlast_writer = \"tamper\"\nlast_write_at = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w2 := filepath.Join(vault, "palace/foo/kg/entities.jsonl")
	if err := StampForPath(vault, w2); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadStamp(dir)
	if after.LastWriter != "tamper" {
		t.Fatalf("cached second StampForPath rewrote the stamp: %+v (first=%+v)", after, first)
	}
}

func TestStampForPath_ExcludedNoOp(t *testing.T) {
	resetStampCacheForTest()
	vault := t.TempDir()
	w := filepath.Join(vault, "palace/foo/.local/marker.jsonl")
	if err := os.MkdirAll(filepath.Dir(w), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := StampForPath(vault, w); err != nil {
		t.Fatal(err)
	}
	// No stamp anywhere under palace/foo.
	if _, err := os.Stat(filepath.Join(vault, "palace/foo", stampFilename)); !os.IsNotExist(err) {
		t.Fatalf("excluded write produced a stamp (err=%v)", err)
	}
}

func TestWriterFingerprint_Deterministic(t *testing.T) {
	a := WriterFingerprint("/some/vault")
	b := WriterFingerprint("/some/vault")
	if a != b {
		t.Fatalf("fingerprint not deterministic: %q vs %q", a, b)
	}
	if len(a) != 8 {
		t.Fatalf("fingerprint len = %d, want 8", len(a))
	}
	if c := WriterFingerprint("/other/vault"); c == a {
		t.Fatal("different vault paths produced identical fingerprint")
	}
}

// TestCheckCompatible_EmptyAndMissing pins the two vault-unreachable conditions
// as DISTINCT reported errors. Both previously returned nil ("best-effort; gates
// proceed"), which let a mutating tool through with no vault in context at all
// and let a write proceed against a deleted vault root.
func TestCheckCompatible_EmptyAndMissing(t *testing.T) {
	t.Run("empty path reports ErrNoVault", func(t *testing.T) {
		err := CheckCompatible("")
		if !errors.Is(err, ErrNoVault) {
			t.Fatalf("empty vault path: got %v, want ErrNoVault", err)
		}
	})

	t.Run("missing root reports VaultUnreachableError", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		err := CheckCompatible(missing)

		var ue *VaultUnreachableError
		if !errors.As(err, &ue) {
			t.Fatalf("missing vault: got %v, want *VaultUnreachableError", err)
		}
		if ue.Path != missing {
			t.Errorf("Path = %q, want %q", ue.Path, missing)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected wrapped os.Stat error to unwrap to fs.ErrNotExist, got %v", err)
		}
	})

	t.Run("the two conditions are not interchangeable", func(t *testing.T) {
		var ue *VaultUnreachableError
		if errors.As(CheckCompatible(""), &ue) {
			t.Error("empty path must not report as VaultUnreachableError")
		}
		if errors.Is(CheckCompatible(filepath.Join(t.TempDir(), "nope")), ErrNoVault) {
			t.Error("missing root must not report as ErrNoVault")
		}
	})
}

func TestCheckCompatible_NoStampsCompatible(t *testing.T) {
	vault := t.TempDir()
	if err := CheckCompatible(vault); err != nil {
		t.Fatalf("fresh vault should be compatible, got %v", err)
	}
}

func TestCheckCompatible_AheadVaultIncompatible(t *testing.T) {
	vault := t.TempDir()
	dir := filepath.Join(vault, "Projects", "foo")
	if err := WriteStamp(dir, MCPSurfaceVersion+1, "newer"); err != nil {
		t.Fatal(err)
	}
	err := CheckCompatible(vault)
	if err == nil {
		t.Fatal("ahead vault should be incompatible, got nil")
	}
	var ie *IncompatibleError
	if !errors.As(err, &ie) {
		t.Fatalf("error type = %T, want *IncompatibleError", err)
	}
	if ie.VaultSurface != MCPSurfaceVersion+1 || ie.BinarySurface != MCPSurfaceVersion {
		t.Fatalf("incompatible fields = %+v", ie)
	}
	if !strings.Contains(ie.Error(), "VP_SURFACE_GATE=warn") {
		t.Fatalf("remediation message missing escape hatch: %q", ie.Error())
	}
}

// TestCheckCompatible_EveryStampedRootGates pins the invariant that ties
// ResolveStampDir and CheckCompatible together: a stamp WRITTEN by one must be
// READ by the other.
//
// The two have independent notions of where stamps live -- ResolveStampDir
// switches on the top-level dir, CheckCompatible globs a hardcoded list -- so
// adding a root to one and not the other yields a stamp that gates NOTHING: the
// vault's version floor silently ignores it, and an older binary is told `pass`
// for a vault only a newer binary can safely write. That is not a hypothetical;
// Audits/ was added to ResolveStampDir and CheckCompatible in the same change
// precisely because splitting them is undetectable at runtime.
//
// Drive each root through the REAL resolver rather than hardcoding stamp dirs,
// so a fifth root that forgets the glob fails here instead of in production.
func TestCheckCompatible_EveryStampedRootGates(t *testing.T) {
	// One representative write path per root ResolveStampDir recognizes.
	writes := []struct {
		name     string
		writeRel string
	}{
		{"Projects", "Projects/foo/resume.md"},
		{"palace", "palace/foo/kg/entities.jsonl"},
		{"Templates", "Templates/commands/restart.md"},
		{"Audits", "Audits/2026-07-13-vault-audit.md"},
	}

	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			vault := t.TempDir()

			dir, err := ResolveStampDir(vault, filepath.Join(vault, w.writeRel))
			if err != nil {
				t.Fatalf("ResolveStampDir: %v", err)
			}
			if dir == "" {
				t.Fatalf("%s is not a stamped root: ResolveStampDir(%q) = \"\"", w.name, w.writeRel)
			}

			// A newer binary stamps this root, and only this root.
			if err := WriteStamp(dir, MCPSurfaceVersion+1, "newer"); err != nil {
				t.Fatal(err)
			}

			err = CheckCompatible(vault)
			if err == nil {
				t.Fatalf("a stamp at %s (surface %d > binary %d) did not gate: CheckCompatible "+
					"does not read this root, so the stamp is written and never read",
					dir, MCPSurfaceVersion+1, MCPSurfaceVersion)
			}
			var ie *IncompatibleError
			if !errors.As(err, &ie) {
				t.Fatalf("error type = %T, want *IncompatibleError", err)
			}
			if ie.VaultSurface != MCPSurfaceVersion+1 {
				t.Fatalf("vault surface = %d, want %d", ie.VaultSurface, MCPSurfaceVersion+1)
			}
		})
	}
}

func TestIncompatibleError_UnknownWriterFallback(t *testing.T) {
	e := &IncompatibleError{BinarySurface: 1, VaultSurface: 2, StampDir: "/v/Projects/foo"}
	if !strings.Contains(e.Error(), "last writer: unknown") {
		t.Fatalf("empty LastWriter should render as 'unknown': %q", e.Error())
	}
}

func TestCheckCompatible_MalformedSkipped(t *testing.T) {
	vault := t.TempDir()
	dir := filepath.Join(vault, "palace", "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stampFilename), []byte("garbage = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompatible(vault); err != nil {
		t.Fatalf("malformed stamp should be skipped, got %v", err)
	}
}
