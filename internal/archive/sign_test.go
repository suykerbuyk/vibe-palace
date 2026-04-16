// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireCmd skips the test if the named binary is not on PATH.
func requireCmd(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not available; skipping", name)
	}
}

// genSSHKey generates an ed25519 keypair under dir and returns the
// private key path, public key bytes, and an allowed_signers entry
// bound to the given identity.
func genSSHKey(t *testing.T, dir, identity string) (keyPath string, allowedSignersPath string) {
	t.Helper()
	keyPath = filepath.Join(dir, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", identity, "-f", keyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	// allowed_signers format: "<identity> <pubkey-line>"
	allowedSignersPath = filepath.Join(dir, "allowed_signers")
	entry := fmt.Sprintf("%s %s\n", identity, strings.TrimSpace(string(pub)))
	if err := os.WriteFile(allowedSignersPath, []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	return keyPath, allowedSignersPath
}

func TestSign_SSH_RoundTrip(t *testing.T) {
	requireCmd(t, "ssh-keygen")

	tmp := t.TempDir()
	identity := "archive-test@example.com"
	keyPath, allowedSigners := genSSHKey(t, tmp, identity)

	vault := filepath.Join(tmp, "vault")
	srcPath := filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Create(CreateOptions{
		Adapter: ClaudeCodeAdapterName, SessionID: "sign-rt",
		SourcePath: srcPath, VaultRoot: vault, ProjectSlug: "demo",
		Now: time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		Sign: SignOptions{
			Mode: SignModeSSH,
			Key:  keyPath,
		},
	})
	if err != nil {
		t.Fatalf("Create with sign: %v", err)
	}

	// Signature file should exist.
	sigPath := SignaturePath(res.ManifestPath)
	if _, err := os.Stat(sigPath); err != nil {
		t.Fatalf("no signature written: %v", err)
	}
	body, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "-----BEGIN SSH SIGNATURE-----") {
		t.Fatalf("signature does not look like SSH sig: %q", string(body[:64]))
	}

	// Standalone VerifySignature should succeed.
	if err := VerifySignature(res.ManifestPath, VerifyOptions{
		AllowedSigners: allowedSigners, Identity: identity,
	}); err != nil {
		t.Fatalf("VerifySignature: %v", err)
	}

	// And via VerifyWithOptions on the full entry.
	entries, err := ListEntries(vault, "demo")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListEntries: %v len=%d", err, len(entries))
	}
	vr := VerifyWithOptions(entries[0], VerifyOptions{
		AllowedSigners: allowedSigners, Identity: identity,
	})
	if !vr.OK {
		t.Errorf("VerifyWithOptions: problems=%v", vr.Problems)
	}
	if !vr.SignatureChecked || !vr.SignatureOK {
		t.Errorf("SignatureChecked=%v SignatureOK=%v", vr.SignatureChecked, vr.SignatureOK)
	}
}

func TestVerifyWithOptions_PresentSigNoKeys_Tolerated(t *testing.T) {
	requireCmd(t, "ssh-keygen")

	tmp := t.TempDir()
	keyPath, _ := genSSHKey(t, tmp, "nokey@example.com")
	vault := filepath.Join(tmp, "vault")
	srcPath := filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Create(CreateOptions{
		Adapter: ClaudeCodeAdapterName, SessionID: "sig-unchecked",
		SourcePath: srcPath, VaultRoot: vault, ProjectSlug: "demo",
		Now:  time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		Sign: SignOptions{Mode: SignModeSSH, Key: keyPath},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	entries, _ := ListEntries(vault, "demo")
	// No allowed_signers supplied — verify should pass on the hash
	// and simply not check the signature.
	vr := VerifyWithOptions(entries[0], VerifyOptions{})
	if !vr.OK {
		t.Errorf("expected OK when sig unverifiable but hash correct: %v", vr.Problems)
	}
	if vr.SignatureChecked {
		t.Errorf("SignatureChecked should be false when no keys supplied")
	}
}

func TestVerifyWithOptions_TamperedManifestAfterSigning(t *testing.T) {
	requireCmd(t, "ssh-keygen")

	tmp := t.TempDir()
	identity := "tamper@example.com"
	keyPath, allowedSigners := genSSHKey(t, tmp, identity)
	vault := filepath.Join(tmp, "vault")
	srcPath := filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Create(CreateOptions{
		Adapter: ClaudeCodeAdapterName, SessionID: "tamper-sig",
		SourcePath: srcPath, VaultRoot: vault, ProjectSlug: "demo",
		Now:  time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		Sign: SignOptions{Mode: SignModeSSH, Key: keyPath},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Tamper: flip a byte in the manifest. The hash check still
	// passes (source_sha256 unchanged), but the signature must fail
	// because it covers the manifest bytes.
	m, _ := ReadManifest(res.ManifestPath)
	m.Model = "claude-tampered"
	if err := WriteManifest(res.ManifestPath, m); err != nil {
		t.Fatal(err)
	}

	entries, _ := ListEntries(vault, "demo")
	vr := VerifyWithOptions(entries[0], VerifyOptions{
		AllowedSigners: allowedSigners, Identity: identity,
	})
	if vr.OK {
		t.Fatal("tampered manifest must fail signature verify")
	}
	if !vr.SignatureChecked || vr.SignatureOK {
		t.Errorf("signature status: checked=%v ok=%v problems=%v",
			vr.SignatureChecked, vr.SignatureOK, vr.Problems)
	}
}

func TestSign_DisabledByDefault(t *testing.T) {
	tmp := t.TempDir()
	vault := filepath.Join(tmp, "vault")
	srcPath := filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Create(CreateOptions{
		Adapter: ClaudeCodeAdapterName, SessionID: "unsigned",
		SourcePath: srcPath, VaultRoot: vault, ProjectSlug: "demo",
		Now: time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SignaturePath(res.ManifestPath)); !os.IsNotExist(err) {
		t.Fatalf("expected no signature; got stat err=%v", err)
	}
}
