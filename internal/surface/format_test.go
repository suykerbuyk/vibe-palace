// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFormat_MissingFileIsZero(t *testing.T) {
	root := t.TempDir()
	got, err := ReadFormat(root)
	if err != nil {
		t.Fatalf("ReadFormat on vault with no manifest: %v", err)
	}
	if got != 0 {
		t.Fatalf("ReadFormat missing file = %d, want 0 (unmigrated)", got)
	}
}

func TestReadFormat_MissingFieldIsZero(t *testing.T) {
	root := t.TempDir()
	// A manifest that exists but carries no `format` field must still read as 0:
	// absence of the field is a positive unmigrated signal, not an error.
	dir := filepath.Join(root, vaultManifestDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, vaultManifestFile), []byte("# no format here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFormat(root)
	if err != nil {
		t.Fatalf("ReadFormat on manifest without field: %v", err)
	}
	if got != 0 {
		t.Fatalf("ReadFormat missing field = %d, want 0", got)
	}
}

func TestReadFormat_PresentReturnsValue(t *testing.T) {
	root := t.TempDir()
	if err := WriteFormat(root, 3); err != nil {
		t.Fatalf("WriteFormat: %v", err)
	}
	got, err := ReadFormat(root)
	if err != nil {
		t.Fatalf("ReadFormat: %v", err)
	}
	if got != 3 {
		t.Fatalf("ReadFormat present = %d, want 3", got)
	}
}

func TestReadFormat_EmptyRootIsZero(t *testing.T) {
	got, err := ReadFormat("")
	if err != nil {
		t.Fatalf("ReadFormat(\"\"): %v", err)
	}
	if got != 0 {
		t.Fatalf("ReadFormat empty root = %d, want 0", got)
	}
}

func TestReadFormat_MalformedIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, vaultManifestDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, vaultManifestFile), []byte("format = [not an int"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFormat(root); err == nil {
		t.Fatal("expected parse error on malformed vault.toml, got nil")
	}
}

func TestWriteFormat_AdvancesAndRoundTrips(t *testing.T) {
	root := t.TempDir()
	if err := WriteFormat(root, 1); err != nil {
		t.Fatalf("WriteFormat(1): %v", err)
	}
	if got, _ := ReadFormat(root); got != 1 {
		t.Fatalf("after WriteFormat(1), ReadFormat = %d, want 1", got)
	}
	if err := WriteFormat(root, 2); err != nil {
		t.Fatalf("WriteFormat(2): %v", err)
	}
	if got, _ := ReadFormat(root); got != 2 {
		t.Fatalf("after WriteFormat(2), ReadFormat = %d, want 2", got)
	}
}

func TestWriteFormat_RefusesToLower(t *testing.T) {
	root := t.TempDir()
	if err := WriteFormat(root, 2); err != nil {
		t.Fatalf("WriteFormat(2): %v", err)
	}
	if err := WriteFormat(root, 1); err == nil {
		t.Fatal("WriteFormat(1) after (2) should refuse to lower (monotone), got nil")
	}
	// The refusal must not have mutated the file.
	if got, _ := ReadFormat(root); got != 2 {
		t.Fatalf("after refused lower, ReadFormat = %d, want 2 (unchanged)", got)
	}
}

func TestWriteFormat_EqualIsNoOp(t *testing.T) {
	root := t.TempDir()
	if err := WriteFormat(root, 1); err != nil {
		t.Fatalf("WriteFormat(1): %v", err)
	}
	raw1, err := os.ReadFile(vaultManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFormat(root, 1); err != nil {
		t.Fatalf("WriteFormat(1) again: %v", err)
	}
	raw2, err := os.ReadFile(vaultManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw1, raw2) {
		t.Fatalf("re-writing the same format changed the file bytes: %q -> %q", raw1, raw2)
	}
}

func TestWriteFormat_ZeroDoesNotCreateManifest(t *testing.T) {
	root := t.TempDir()
	// Recording the unmigrated baseline must never materialize the manifest.
	if err := WriteFormat(root, 0); err != nil {
		t.Fatalf("WriteFormat(0): %v", err)
	}
	if _, err := os.Stat(vaultManifestPath(root)); !os.IsNotExist(err) {
		t.Fatalf("WriteFormat(0) created a manifest (stat err = %v), want none", err)
	}
}

func TestWriteFormat_EmptyRootErrors(t *testing.T) {
	if err := WriteFormat("", 1); err == nil {
		t.Fatal("WriteFormat with empty root should error")
	}
}

func TestEnforceFormatFailStop_PassesWhenAtOrAbove(t *testing.T) {
	root := t.TempDir()
	// Production posture: RequiredDataFormat == 0, absent manifest == 0, passes.
	if err := EnforceFormatFailStop(root, false); err != nil {
		t.Fatalf("EnforceFormatFailStop at required=0 should pass, got %v", err)
	}
	// And a vault at/above the required floor passes (test seam, required > 0).
	if err := WriteFormat(root, 1); err != nil {
		t.Fatal(err)
	}
	if err := enforceFormatFailStop(root, 1, false); err != nil {
		t.Fatalf("format 1 >= required 1 should pass, got %v", err)
	}
}

func TestEnforceFormatFailStop_FailsWhenBehind(t *testing.T) {
	root := t.TempDir() // format 0 (absent)
	// Exercise the fail-stop path via the test seam (required > 0), without
	// hardcoding a required > 0 into production.
	err := enforceFormatFailStop(root, 1, false)
	if err == nil {
		t.Fatal("format 0 < required 1 should fail, got nil")
	}
	var fe *FormatIncompatibleError
	if !errors.As(err, &fe) {
		t.Fatalf("want *FormatIncompatibleError, got %T: %v", err, err)
	}
	if fe.BinaryRequired != 1 || fe.VaultFormat != 0 {
		t.Fatalf("error fields = required %d / vault %d, want 1 / 0", fe.BinaryRequired, fe.VaultFormat)
	}
}

func TestEnforceFormatFailStop_WarnDowngrades(t *testing.T) {
	root := t.TempDir() // format 0, behind required 1

	// Without the escape hatch: fail-stop.
	if err := enforceFormatFailStop(root, 1, false); err == nil {
		t.Fatal("enforceFormatFailStop behind required with no escape hatch should error")
	}

	// With VP_FORMAT_GATE=warn: downgrade to nil + a logged warning.
	t.Setenv("VP_FORMAT_GATE", "warn")
	var derr error
	out := captureGateStderr(t, func() { derr = enforceFormatFailStop(root, 1, false) })
	if derr != nil {
		t.Fatalf("VP_FORMAT_GATE=warn should downgrade to nil, got %v", derr)
	}
	if out == "" {
		t.Fatal("VP_FORMAT_GATE=warn should have logged a warning to gateStderr")
	}
}

func TestEnforceFormatFailStop_MigratorExemptBypasses(t *testing.T) {
	root := t.TempDir() // format 0, behind required 1

	// A non-exempt caller fails-stop.
	if err := enforceFormatFailStop(root, 1, false); err == nil {
		t.Fatal("non-exempt caller behind required should error")
	}
	// The migrator-exempt caller bypasses the gate entirely, no escape hatch set.
	if err := enforceFormatFailStop(root, 1, true); err != nil {
		t.Fatalf("migrator-exempt caller should bypass the gate, got %v", err)
	}
}
