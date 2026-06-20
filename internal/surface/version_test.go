// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"errors"
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
	s, err := ReadStamp(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Surface != 1 || s.LastWriter != "abcd1234" {
		t.Fatalf("roundtrip stamp = %+v", s)
	}
	if s.LastWriteAt == "" {
		t.Fatal("last_write_at not set")
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
	if s.Surface != 5 || s.LastWriter != "high" {
		t.Fatalf("lower write clobbered higher stamp: %+v", s)
	}
}

func TestWriteStamp_EqualRefreshesWriter(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStamp(dir, 3, "first"); err != nil {
		t.Fatal(err)
	}
	if err := WriteStamp(dir, 3, "second"); err != nil {
		t.Fatal(err)
	}
	s, _ := ReadStamp(dir)
	if s.LastWriter != "second" {
		t.Fatalf("equal-version write did not refresh writer: %+v", s)
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

func TestCheckCompatible_EmptyAndMissing(t *testing.T) {
	if err := CheckCompatible(""); err != nil {
		t.Fatalf("empty vault path: %v", err)
	}
	if err := CheckCompatible(filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Fatalf("missing vault: %v", err)
	}
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
