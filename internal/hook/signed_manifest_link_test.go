// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// The hook never signs (archive.Create is called without Sign), so a signed
// manifest only exists here because an operator made one with `vp archive
// create`. These tests pin what the hook's back-link does to such a manifest, and
// that a refusal never costs the capture.

type hookSignFix struct {
	vaultRoot, cwd, transcript string
	keyPath, allowedSigners    string
	manifestPath               string
}

const hookSignIdentity = "hook-g1@example.com"

func newHookSignFix(t *testing.T, signingConfigured bool) *hookSignFix {
	t.Helper()
	requireSigningTool(t, "ssh-keygen")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))

	f := &hookSignFix{vaultRoot: t.TempDir(), cwd: t.TempDir()}
	f.keyPath = filepath.Join(home, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", hookSignIdentity, "-f", f.keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(f.keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	f.allowedSigners = filepath.Join(home, "allowed_signers")
	if err := os.WriteFile(f.allowedSigners, []byte(hookSignIdentity+" "+string(bytes.TrimSpace(pub))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "# hook signing fixture\n"
	if signingConfigured {
		cfg += fmt.Sprintf("[archive]\nsign_mode = \"ssh\"\nsign_key = %q\nallowed_signers = %q\nsigner_identity = %q\n",
			f.keyPath, f.allowedSigners, hookSignIdentity)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	testutil.InitProject(t, f.vaultRoot, "test-project")
	writeVibeMarker(t, f.cwd)
	if err := os.MkdirAll(filepath.Join(f.cwd, ".vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.transcript = filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(f.transcript, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, f.cwd, "initial commit")

	// The operator's signed manifest, made the way `vp archive create` makes one,
	// for the SAME session the hook will capture, so the hook's own Create is an
	// idempotent skip and goes straight to the note link.
	res, err := archive.Create(archive.CreateOptions{
		Adapter: archive.ClaudeCodeAdapterName, SessionID: "test-session", SourcePath: f.transcript,
		SourceCWD: f.cwd, VaultRoot: f.vaultRoot, ProjectSlug: "test-project", Now: time.Now(),
		Sign: archive.SignOptions{Mode: archive.SignModeSSH, Key: f.keyPath},
	})
	if err != nil {
		t.Fatalf("pre-creating the signed manifest: %v", err)
	}
	f.manifestPath = res.ManifestPath
	return f
}

func (f *hookSignFix) runHook(t *testing.T) *Result {
	t.Helper()
	res, err := Run(context.Background(), Payload{
		SessionID: "test-session", TranscriptPath: f.transcript, CWD: f.cwd, HookEventName: "SessionEnd",
	}, RunOptions{
		VaultRoot: f.vaultRoot, ProjectSlug: "test-project", VPVersion: "test-0.1",
		ClaimDir: filepath.Join(f.cwd, ".vibe-palace"),
	})
	if err != nil {
		t.Fatalf("hook.Run must succeed: %v", err)
	}
	return res
}

func (f *hookSignFix) verify() error {
	return archive.VerifySignature(f.manifestPath, archive.VerifyOptions{AllowedSigners: f.allowedSigners, Identity: hookSignIdentity})
}

func (f *hookSignFix) noteExists(t *testing.T, res *Result) {
	t.Helper()
	if res.SessionNoteID == "" {
		t.Fatalf("the hook wrote no session note: %+v", res)
	}
	notes, _ := filepath.Glob(filepath.Join(f.vaultRoot, "Projects", "test-project", "sessions", "*.md"))
	if len(notes) == 0 {
		t.Fatal("no session note exists on disk")
	}
}

// L9a: signing is usable, so the hook's back-link re-signs and the signature
// still verifies.
func TestHookLink_ReSignsASignedManifest(t *testing.T) {
	f := newHookSignFix(t, true)
	if err := f.verify(); err != nil {
		t.Fatalf("control: the signed manifest must verify before the hook runs: %v", err)
	}
	res := f.runHook(t)
	f.noteExists(t, res)
	m, err := archive.ReadManifest(f.manifestPath)
	if err != nil || m.VaultRelSessionNote == "" {
		t.Fatalf("the hook did not link the note into the manifest (err=%v, link=%q)", err, m.VaultRelSessionNote)
	}
	if err := f.verify(); err != nil {
		t.Fatalf("the signature does not verify after the hook linked the note (stale signature): %v", err)
	}
}

// L9b: the manifest is signed but signing options are unusable. The hook still
// succeeds, the archive and the note are written, and the old manifest and its
// signature are byte-identical and still verify: the refusal costs the reverse
// back-link and nothing else.
func TestHookLink_UnusableSigningOptionsRefuseTheLinkWithoutLosingTheCapture(t *testing.T) {
	f := newHookSignFix(t, false)
	m0 := mustReadFile(t, f.manifestPath)
	s0 := mustReadFile(t, archive.SignaturePath(f.manifestPath))

	res := f.runHook(t)
	f.noteExists(t, res)

	if !bytes.Equal(m0, mustReadFile(t, f.manifestPath)) {
		t.Error("the manifest changed though the link could not be re-signed")
	}
	if !bytes.Equal(s0, mustReadFile(t, archive.SignaturePath(f.manifestPath))) {
		t.Error("the signature changed though the link could not be re-signed")
	}
	if err := f.verify(); err != nil {
		t.Errorf("the untouched signed pair no longer verifies: %v", err)
	}
	m, err := archive.ReadManifest(f.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote != "" {
		t.Errorf("the manifest was linked (%q) despite the refusal", m.VaultRelSessionNote)
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

// requireSigningTool skips locally and FAILS under CI when a program the signing
// tests drive is missing (GitHub Actions sets CI=true). A clean skip on a runner
// without it would let the signed path run nowhere while the job stays green.
// Same rule as the archive package's requireCmd; it lives in a test file on
// purpose, because a shared exported helper in non-test code would be flagged as
// uninvoked by the source audit.
func requireSigningTool(t testing.TB, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is required under CI and was not found: %v (see .github/workflows/ci.yml)", name, err)
			return
		}
		t.Skipf("%s not available; skipping", name)
	}
}
