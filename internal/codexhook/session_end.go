// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const sessionEndArchiveBudget = 2600 * time.Millisecond

// Options carries what cmd/vp resolves once per process and the handler cannot
// resolve for itself.
type Options struct {
	// VPVersion is the vp binary version string, recorded in the manifest for
	// schema forensics. cmd/vp passes cli.BuildInfo.Version; the zero value
	// writes no vp_version, which is what every Codex manifest before this
	// change recorded.
	VPVersion string

	// Launch starts the detached pending-archive ingester after a successful
	// archive + receipt. A nil Launch spawns nothing, which is the library and
	// test default — see hook.LaunchArchiveIngester.
	Launch detachlaunch.LaunchFunc

	// OnVaultResolved runs ONCE, after the vault is resolved from the event's
	// cwd and before any work, so the caller can point logging and the surface
	// check at the vault this run actually writes.
	//
	// It is a callback rather than a return value because the re-point has to
	// happen BEFORE the work for the work's own log lines to land in the right
	// file, and because initLoggingForVault lives in cmd/vp, which this package
	// cannot import. Nil is a no-op.
	OnVaultResolved func(*storage.Vault)

	// OnLiveVaultRefusal runs when resolution was REFUSED to protect the live
	// vault, immediately before the one diagnostic line is written, so the
	// caller can attach a log destination where there is otherwise none: the
	// refusal happens before any vault is known, so without this the diagnostic
	// has nowhere to go.
	//
	// This package decides WHETHER (the sentinel) and WHAT (the message); the
	// caller decides WHERE, because that is a vault-logging policy question and
	// initLoggingForVault lives in cmd/vp. Nil is a no-op.
	OnLiveVaultRefusal func()
}

type SessionEndEvent struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	Reason         string `json:"reason"`
	HookEventName  string `json:"hook_event_name"`
}

// HandleSessionEnd is the JSON protocol boundary. Unlike SessionStart, a
// missing or unusable transcript is a hard failure: no archive is claimed.
//
// It is the ONLY place that logs a resolution failure. Everything downstream of
// the vault — archive failure, receipt failure, a gate skip, a failed ingester
// launch — is logged by ProcessSessionEnd or by the hook package, because those
// happen after OnVaultResolved has pointed logging at the right vault.
func HandleSessionEnd(in io.Reader, out io.Writer, opts Options) error {
	var event SessionEndEvent
	if err := json.NewDecoder(in).Decode(&event); err != nil {
		return fmt.Errorf("decode Codex SessionEnd event: %w", err)
	}
	if event.HookEventName != "" && event.HookEventName != "SessionEnd" {
		return fmt.Errorf("unexpected Codex hook event %q", event.HookEventName)
	}
	if event.CWD == "" {
		return fmt.Errorf("Codex SessionEnd event has no cwd")
	}
	if project.DetectSignal(event.CWD) != project.SignalVibeConfig {
		return fmt.Errorf("Codex SessionEnd cwd has no Vibe-Palace project marker")
	}
	marker, err := project.LocateMarker(event.CWD)
	if err != nil {
		return fmt.Errorf("read Vibe-Palace project marker: %w", err)
	}
	if marker.Path == "" || marker.Name == "" || marker.NameErr != nil {
		return fmt.Errorf("Codex SessionEnd requires a valid [project].name marker")
	}
	vaultRoot, _, err := storage.ResolveVaultPath(event.CWD)
	if err != nil {
		logRefusedResolution(event, err, opts.OnLiveVaultRefusal)
		return fmt.Errorf("resolve Vibe-Palace vault: %w", err)
	}
	vault := storage.NewVault(vaultRoot)
	if opts.OnVaultResolved != nil {
		opts.OnVaultResolved(vault)
	}
	return ProcessSessionEnd(context.Background(), event, vault, marker.Name, out, opts)
}

// logRefusedResolution writes the ONE diagnostic line for a resolution that was
// refused to protect the live vault, and writes nothing else anywhere.
//
// 🔴 THIS IS A DIAGNOSTIC, NOT A FALLBACK. The global vault is opened to reach
// a LOG FILE and for no other reason: this path captures nothing, into that
// vault or any other, and the caller still returns the error. An operator whose
// binding was refused currently gets no vp.log line at all, because the refusal
// happens before any vault is known; this is that line.
//
// 🔴 ErrVaultBindingRejected ONLY — deliberately. ErrHostConfigUnreadable does
// NOT need an arm here, and adding one would add a branch no test can show
// capable of failing. readProjectVaults() runs unconditionally in
// storage.ResolveVaultBinding, ahead of any tier selection, and its error
// returns immediately, so an unreadable host config never reaches the tier-3
// global fall-through and Codex never captures on it. Claude's paired guard
// (cmd/vp/cmd_hook.go) exists to stop CLAUDE's own extra storage.OpenVaultGlobal
// fallback, which re-reads the same config and may succeed. This path has no
// such fallback. See the task's Plan C6: examined twice, not a defect.
func logRefusedResolution(event SessionEndEvent, err error, attachLog func()) {
	if !errors.Is(err, storage.ErrVaultBindingRejected) {
		return
	}
	if attachLog != nil {
		attachLog()
	}
	slog.Error("codex hook: refusing to capture into the live vault",
		"cwd", event.CWD, "event", "SessionEnd", "session_id", event.SessionID,
		"host", storage.HostCodex, "err", err)
}

// ProcessSessionEnd archives Codex's supplied file path. Its split deadline
// keeps archive work bounded and leaves the remainder for the typed receipt.
func ProcessSessionEnd(parent context.Context, event SessionEndEvent, vault *storage.Vault, projectSlug string, out io.Writer, opts Options) error {
	if parent == nil {
		parent = context.Background()
	}
	if event.SessionID == "" || event.TranscriptPath == "" || vault == nil || projectSlug == "" {
		return fmt.Errorf("SessionEnd requires session_id, transcript_path, project marker, and vault")
	}
	info, err := os.Stat(event.TranscriptPath)
	if err != nil {
		return fmt.Errorf("Codex transcript unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("Codex transcript unavailable: expected a usable non-empty regular file")
	}
	// The capture opt-in gates, shared with the Claude hook. They run BEFORE
	// archive.Create because archive, receipt and link all lazily create
	// Projects/<slug>/, so a refusal afterwards would already have resurrected
	// a departed project's tree.
	//
	// A skip returns nil, not an error: the hook did exactly what it should
	// have, and cmd/vp turns a nil into exit 0. Returning an error here would
	// make every stale checkout read as a hook crash. GateCapture has already
	// logged which gate fired and why.
	if skip := hook.GateCapture(storage.HostCodex, vault.Root, projectSlug, event.CWD, event.SessionID); skip != hook.SkipNone {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, sessionEndArchiveBudget)
	defer cancel()
	created, err := archive.Create(archive.CreateOptions{
		Context: ctx, Adapter: archive.CodexRawAdapterName, SessionID: event.SessionID,
		SourcePath: event.TranscriptPath, SourceCWD: event.CWD, VaultRoot: vault.Root, ProjectSlug: projectSlug,
		VPVersion:   opts.VPVersion,
		LockPosture: archive.LockNonBlocking, MaxArchiveBytes: archive.DefaultMaxArchiveBytes,
	})
	if err != nil {
		slog.Warn("codex hook: archive failed; no receipt written",
			"project", projectSlug, "session_id", event.SessionID, "host", storage.HostCodex, "err", err)
		return fmt.Errorf("archive Codex transcript: %w", err)
	}
	if err := ctx.Err(); err != nil {
		slog.Warn("codex hook: archive created but the receipt budget expired; note and link incomplete",
			"project", projectSlug, "session_id", event.SessionID, "host", storage.HostCodex, "err", err)
		return fmt.Errorf("archive created; note/link incomplete: receipt budget exhausted: %w", err)
	}
	if _, err := capture.WriteCodexArchiveReceipt(vault, projectSlug, event.SessionID, created.ManifestPath); err != nil {
		slog.Warn("codex hook: archive created but the receipt failed; note and link incomplete",
			"project", projectSlug, "session_id", event.SessionID, "host", storage.HostCodex, "err", err)
		return fmt.Errorf("archive created; note/link incomplete: %w", err)
	}
	if _, err := fmt.Fprintf(out, "archived: %s\n", created.ManifestPath); err != nil {
		return fmt.Errorf("write SessionEnd result: %w", err)
	}
	// LAST, and only on full success. After the receipt so the detached drain
	// cannot race the note write, and after the success line so the hook's
	// observable result never depends on an exec that may not start. A launch
	// failure is one Warn inside LaunchArchiveIngester and nothing else.
	//
	// 🔴 The --first argument is the archive's source_sha256, never its path:
	// `vp drain archives` silently ignores a non-hash --first.
	hook.LaunchArchiveIngester(opts.Launch, vault.Root, projectSlug, created.Manifest.SourceSHA256)
	return nil
}
