// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// writeManifestText lays raw vault.toml bytes, so a test controls exactly what
// a hand edit could leave behind.
func writeManifestText(t *testing.T, root, text string) {
	t.Helper()
	dir := filepath.Join(root, vaultManifestDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, vaultManifestFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A format bump is a read-modify-write: the marker written beside the format
// survives it. HEAD's WriteFormat re-encoded VaultManifest{Format: n} alone and
// dropped every other key.
func TestWriteFormat_KeepsTheMarker(t *testing.T) {
	root := t.TempDir()
	if err := WriteVaultManifest(root, VaultManifest{Format: 2, AuthoredOnly: "2026-10-02"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFormat(root, 3); err != nil {
		t.Fatalf("WriteFormat: %v", err)
	}
	m, err := ReadVaultManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != 3 || m.AuthoredOnly != "2026-10-02" {
		t.Fatalf("after WriteFormat(3): %+v, want format 3 with the marker kept", m)
	}
}

// An unquoted TOML local date is what a hand edit leaves. It decodes as a
// time.Time, which a plain string field refuses; MarkerDate normalises it. A
// wrong-typed value is an error from the marker's reader only: ReadFormat, which
// every data-format-gated command calls, still answers.
func TestMarker_UnquotedDateAndWrongType(t *testing.T) {
	root := t.TempDir()
	writeManifestText(t, root, "format = 2\nauthored_only = 2026-10-02\n")
	m, err := ReadVaultManifest(root)
	if err != nil {
		t.Fatalf("unquoted date: %v", err)
	}
	if m.AuthoredOnly != "2026-10-02" {
		t.Fatalf("unquoted date read as %q, want 2026-10-02", m.AuthoredOnly)
	}
	if n, err := ReadFormat(root); err != nil || n != 2 {
		t.Fatalf("ReadFormat beside an unquoted date = %d, %v; want 2, nil", n, err)
	}

	writeManifestText(t, root, "format = 2\nauthored_only = 3\n")
	if _, err := ReadVaultManifest(root); err == nil || !strings.Contains(err.Error(), "authored_only") {
		t.Fatalf("wrong-typed marker: err = %v, want an error naming authored_only", err)
	}
	if n, err := ReadFormat(root); err != nil || n != 2 {
		t.Fatalf("ReadFormat beside a wrong-typed marker = %d, %v; want 2, nil", n, err)
	}
	if err := WriteFormat(root, 3); err == nil {
		t.Fatal("WriteFormat over a wrong-typed marker succeeded; it must refuse rather than drop the value")
	}
	got, _ := os.ReadFile(filepath.Join(root, vaultManifestDir, vaultManifestFile))
	if string(got) != "format = 2\nauthored_only = 3\n" {
		t.Fatalf("refused WriteFormat changed the file to %q", got)
	}

	writeManifestText(t, root, "format = 2\nauthored_only = \"October\"\n")
	if _, err := ReadVaultManifest(root); err == nil {
		t.Fatal("a string that is not a date was accepted as the marker")
	}
}

// A v8 binary decodes vault.toml into struct{Format int}. BurntSushi ignores the
// unknown key, so a file carrying the marker still reads (regression guard).
func TestMarker_V8DecodeIgnoresIt(t *testing.T) {
	data, err := ManifestBytes(VaultManifest{Format: 2, AuthoredOnly: "2026-10-02"})
	if err != nil {
		t.Fatal(err)
	}
	var v8 struct {
		Format int `toml:"format"`
	}
	if err := toml.Unmarshal(data, &v8); err != nil || v8.Format != 2 {
		t.Fatalf("v8 decode = %+v, %v; want format 2", v8, err)
	}
}

// The one encoder emits, with no marker, exactly the bytes vp has always
// written, so storage.isBornCurrentStamp still recognises every stamp.
func TestManifestBytes_UnchangedWithoutMarker(t *testing.T) {
	for n, want := range map[int]string{1: "format = 1\n", 2: "format = 2\n", 3: "format = 3\n"} {
		got, err := ManifestBytes(VaultManifest{Format: n})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("ManifestBytes(format %d) = %q, want %q", n, got, want)
		}
		fb, err := FormatManifestBytes(n)
		if err != nil || string(fb) != want {
			t.Errorf("FormatManifestBytes(%d) = %q, %v; want %q", n, fb, err, want)
		}
	}
	got, err := ManifestBytes(VaultManifest{Format: 2, AuthoredOnly: "2026-10-02"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "format = 2\nauthored_only = \"2026-10-02\"\n" {
		t.Errorf("with the marker: %q, want a quoted date after the format", got)
	}
}

// Every field reaches disk in ONE write, so a crash cannot leave a format
// without its marker. A failed write leaves the old file byte-identical.
func TestWriteVaultManifest_IsOneWrite(t *testing.T) {
	root := t.TempDir()
	writeManifestText(t, root, "format = 2\n")
	orig := manifestWriteFile
	t.Cleanup(func() { manifestWriteFile = orig })

	writes := 0
	manifestWriteFile = func(path string, data []byte) error {
		writes++
		return orig(path, data)
	}
	if err := WriteVaultManifest(root, VaultManifest{Format: 2, AuthoredOnly: "2026-10-03"}); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("WriteVaultManifest made %d writes, want exactly 1", writes)
	}

	crash := errors.New("crash before the write")
	manifestWriteFile = func(string, []byte) error { return crash }
	if err := WriteVaultManifest(root, VaultManifest{Format: 3, AuthoredOnly: "2026-10-04"}); !errors.Is(err, crash) {
		t.Fatalf("err = %v, want the injected crash", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, vaultManifestDir, vaultManifestFile))
	if string(got) != "format = 2\nauthored_only = \"2026-10-03\"\n" {
		t.Fatalf("after a crashed write the file is %q, want the previous manifest unchanged", got)
	}
}

// Only a TOML local date is a marker date. A local time, a local datetime and an
// offset datetime decode as time.Time too, and each is an error naming the key.
func TestMarker_OnlyALocalDate(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"07:32:00", "2026-10-02T07:32:00", "2026-10-02T07:32:00Z", "2026-10-02T07:32:00-06:00"} {
		writeManifestText(t, root, "format = 2\nauthored_only = "+v+"\n")
		if _, err := ReadVaultManifest(root); err == nil || !strings.Contains(err.Error(), "authored_only") {
			t.Errorf("authored_only = %s: err = %v, want an error naming authored_only", v, err)
		}
	}
}

// ParseVaultManifest is ReadVaultManifest's parser: the same bytes give the
// same manifest, and the same malformed marker gives an error naming the key,
// whether they came off disk or out of git.
func TestParseVaultManifest_SameAsTheFileReader(t *testing.T) {
	for _, text := range []string{
		"format = 2\n",
		"format = 2\nauthored_only = \"2026-10-04\"\n",
		"format = 2\nauthored_only = 2026-10-04\n",
	} {
		root := t.TempDir()
		writeManifestText(t, root, text)
		fromFile, err := ReadVaultManifest(root)
		if err != nil {
			t.Fatalf("%q: ReadVaultManifest: %v", text, err)
		}
		fromBytes, err := ParseVaultManifest([]byte(text))
		if err != nil {
			t.Fatalf("%q: ParseVaultManifest: %v", text, err)
		}
		if fromFile != fromBytes {
			t.Errorf("%q: file reader %+v, bytes parser %+v", text, fromFile, fromBytes)
		}
	}
	_, err := ParseVaultManifest([]byte("format = 2\nauthored_only = 5\n"))
	if err == nil || !strings.Contains(err.Error(), "authored_only") {
		t.Fatalf("malformed marker: want an error naming authored_only, got %v", err)
	}
}
