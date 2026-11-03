// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/codexhook"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// stubCodexMarkerDir points the Codex derivation at a directory under the
// test's control.
//
// 🔴 It stubs the CWD SOURCE, never the reader. The glob, the parse, the TTL,
// the filename/body agreement and the exactly-one-candidate rule all run for
// real — stubbing codexSessionID instead would prove the assignment and
// nothing else, which is the shape that has blocked this task family before.
func stubCodexMarkerDir(t *testing.T, dir string) {
	t.Helper()
	orig := codexMarkerDir
	codexMarkerDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { codexMarkerDir = orig })
}

// codexCwd returns a directory holding real markers for each id, written by
// the production writer.
func codexCwd(t *testing.T, ids ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, id := range ids {
		if err := codexhook.WriteSessionMarker(dir, id, "marker-model-"+id); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// captureAsCodex drives the real handler as a Codex client and returns the
// written note's meta. `cwd` is deliberately NOT passed unless a test is about
// that argument.
func captureAsCodex(t *testing.T, vault *storage.Vault, extra map[string]any) storage.SessionMeta {
	t.Helper()
	params := map[string]any{
		"project":     "test-proj",
		"summary":     "Codex MCP capture driven through the real handler.",
		"client_info": map[string]string{"name": "codex-cli", "version": "0.166.0"},
	}
	for k, v := range extra {
		params[k] = v
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CaptureSessionTool(vault, nil).Handler(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	r := result.(captureSessionResult)
	meta, _, err := vault.ReadSession("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	return meta
}

func codexVault(t *testing.T) *storage.Vault {
	t.Helper()
	vault := testSessionVault(t)
	testutil.InitProject(t, vault.Root, "test-proj")
	return vault
}

// A3 — the note carries the marker's id and the new provenance, with `cwd`
// omitted from the call entirely.
func TestCaptureStampsCodexArchiveSessionIDFromMarker(t *testing.T) {
	stubHostSessionID(t, "")
	stubCodexMarkerDir(t, codexCwd(t, "thr_a3"))

	meta := captureAsCodex(t, codexVault(t), nil)

	if meta.Host != storage.HostCodex {
		t.Fatalf("host = %q, want %q", meta.Host, storage.HostCodex)
	}
	if meta.ArchiveSessionID != "thr_a3" {
		t.Errorf("archive_session_id = %q, want %q", meta.ArchiveSessionID, "thr_a3")
	}
	if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceCodexSessionStart {
		t.Errorf("archive_session_id_source = %q, want %q", meta.ArchiveSessionIDSource, storage.ArchiveIDSourceCodexSessionStart)
	}
}

// A4 — two live markers mean two concurrent sessions and nothing says which is
// asking. The note is written, honestly unstamped.
func TestCaptureRefusesAmbiguousCodexMarkers(t *testing.T) {
	stubHostSessionID(t, "")
	stubCodexMarkerDir(t, codexCwd(t, "thr_one", "thr_two"))

	meta := captureAsCodex(t, codexVault(t), nil)

	if meta.ArchiveSessionID != "" {
		t.Fatalf("ambiguous markers stamped %q; correct-or-absent requires empty", meta.ArchiveSessionID)
	}
	if meta.Summary == "" {
		t.Error("the note itself must still be written — an unjoined note is a gap, not a refusal")
	}
}

// A5 — the whole loop: a note written by the real handler is found by the
// receipt's linker, so the manifest's reverse link names it instead of staying
// empty.
//
// 🔴 TestWriteCodexArchiveReceiptUsesCanonicalLinkerInsteadOfStubPath
// (internal/capture/session_test.go) remains the DIRECT lock on the linker.
// This is additional: it proves the handler feeds it.
func TestCodexReceiptBackLinksAHandlerWrittenNote(t *testing.T) {
	stubHostSessionID(t, "")
	stubCodexMarkerDir(t, codexCwd(t, "thr_a5"))
	vault := codexVault(t)

	meta := captureAsCodex(t, vault, nil)
	if meta.ArchiveSessionID != "thr_a5" {
		t.Fatalf("precondition: note was not stamped (%q)", meta.ArchiveSessionID)
	}

	source := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(source, []byte("{\"codex_record\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := archive.Create(archive.CreateOptions{
		Adapter: archive.CodexRawAdapterName, SessionID: "thr_a5", SourcePath: source,
		VaultRoot: vault.Root, ProjectSlug: "test-proj", LockPosture: archive.LockNonBlocking,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capture.WriteCodexArchiveReceipt(vault, "test-proj", "thr_a5", created.ManifestPath); err != nil {
		t.Fatal(err)
	}
	manifest, err := archive.ReadManifest(created.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.VaultRelSessionNote != meta.NotePath {
		t.Fatalf("manifest back-link = %q, want the handler-written note %q", manifest.VaultRelSessionNote, meta.NotePath)
	}
}

// A8 — the marker supplies the model only when the caller did not.
func TestCodexCaptureStampsModelFromMarker(t *testing.T) {
	t.Run("call omits model", func(t *testing.T) {
		stubHostSessionID(t, "")
		stubCodexMarkerDir(t, codexCwd(t, "thr_a8"))
		meta := captureAsCodex(t, codexVault(t), nil)
		if meta.Model != "marker-model-thr_a8" {
			t.Fatalf("model = %q, want the marker's %q", meta.Model, "marker-model-thr_a8")
		}
	})
	t.Run("caller-supplied model wins", func(t *testing.T) {
		stubHostSessionID(t, "")
		stubCodexMarkerDir(t, codexCwd(t, "thr_a8"))
		meta := captureAsCodex(t, codexVault(t), map[string]any{"model": "caller-model"})
		if meta.Model != "caller-model" {
			t.Fatalf("model = %q, want the caller's %q — the marker must not override", meta.Model, "caller-model")
		}
	})
}

// A9 — a `cwd` argument naming another checkout must not point the lookup
// there. The agent supplies that argument; the id must not be agent-reachable.
func TestCaptureIgnoresAForeignCWDArgument(t *testing.T) {
	stubHostSessionID(t, "")
	serverCwd := codexCwd(t, "thr_server")
	foreignCwd := codexCwd(t, "thr_foreign")
	stubCodexMarkerDir(t, serverCwd)

	meta := captureAsCodex(t, codexVault(t), map[string]any{"cwd": foreignCwd})

	if meta.ArchiveSessionID != "thr_server" {
		t.Fatalf("archive_session_id = %q, want the SERVER cwd's %q — a caller cwd must never widen the lookup",
			meta.ArchiveSessionID, "thr_server")
	}
}

// Wiring lock: codexSessionID must actually read the marker directory rather
// than being a stub seam of its own. The reader's own table lives in
// internal/codexhook, where the injectable clock is reachable.
func TestCodexSessionIDDelegatesToTheMarkerReader(t *testing.T) {
	dir := codexCwd(t, "thr_delegate")
	id, model := codexSessionID(dir)
	if id != "thr_delegate" || model != "marker-model-thr_delegate" {
		t.Fatalf("codexSessionID = (%q, %q), want the marker's values", id, model)
	}
	if id, model := codexSessionID(t.TempDir()); id != "" || model != "" {
		t.Fatalf("an empty directory resolved to (%q, %q)", id, model)
	}
}
