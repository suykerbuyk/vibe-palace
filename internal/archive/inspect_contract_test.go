// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// captureArchiveLog redirects the default slog logger into a buffer for the
// duration of one test.
//
// 🔴 LevelInfo is load-bearing, not a default: it is what makes a Warn ->
// Debug mutation observable. A handler that accepted Debug would keep the
// assertion green under a demoted log line.
func captureArchiveLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// probeAdapter is a test-only Adapter whose Inspect the test controls.
//
// 🔴 It is a seam ONE LEVEL AWAY from the code under test. The defects these
// tests pin are Create's — the discarded Inspect error and the unbounded Inspect
// pass — so stubbing Create itself would prove nothing. Driving codex-raw into a
// real Inspect error is not an option either: Create reaches the Inspect call
// only after hashing and compressing the source, so the file is readable by
// then, and the only other way in is a source line past the 16 MiB scanner
// ceiling — a 16 MiB fixture for one assertion.
type probeAdapter struct {
	name    string
	source  string
	sleep   time.Duration
	turns   int
	model   string
	inspErr error
}

func (p *probeAdapter) Name() string    { return p.name }
func (p *probeAdapter) Version() string { return "0.0.1" }

func (p *probeAdapter) ResolveSource(CreateOptions) (string, func(), error) {
	return p.source, noopCleanup, nil
}

func (p *probeAdapter) Inspect(string) (int, string, error) {
	if p.sleep > 0 {
		time.Sleep(p.sleep)
	}
	return p.turns, p.model, p.inspErr
}

// registerProbe registers p and returns a vault root with "demo" initialised
// plus a real, readable source file for p to resolve.
//
// The registry is global and has no deregister, so every probe takes a name no
// other test uses. internal/archive runs no test with t.Parallel(), and cmd/vp's
// registry lives in a different test binary, so nothing races this.
func registerProbe(t *testing.T, p *probeAdapter) (vaultRoot string) {
	t.Helper()
	tmp := t.TempDir()
	vaultRoot = filepath.Join(tmp, "vault")
	testutil.InitProject(t, vaultRoot, "demo")
	p.source = filepath.Join(tmp, "probe.jsonl")
	if err := os.WriteFile(p.source, []byte("{\"type\":\"probe\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	RegisterAdapter(p)
	return vaultRoot
}

// A8: Create LOGS an Inspect failure and does not abort — the contract stated in
// adapter.go, which Create previously broke by discarding the error into _.
func TestCreateLogsAnInspectFailureAndStillWritesTheManifest(t *testing.T) {
	p := &probeAdapter{name: "u3-inspect-probe", inspErr: errors.New("probe: inspect refused")}
	vault := registerProbe(t, p)
	buf := captureArchiveLog(t)

	res, err := Create(CreateOptions{
		Adapter: p.name, SessionID: "thr_probe", VaultRoot: vault,
		ProjectSlug: "demo", LockPosture: LockNonBlocking,
	})
	if err != nil {
		t.Fatalf("Create must not abort on an Inspect failure: %v", err)
	}
	if _, rerr := ReadManifest(res.ManifestPath); rerr != nil {
		t.Fatalf("ReadManifest: %v", rerr)
	}
	if res.Manifest.TurnCount != 0 || res.Manifest.Model != "" {
		t.Errorf("manifest = (%d, %q), want (0, \"\") beside a failed Inspect", res.Manifest.TurnCount, res.Manifest.Model)
	}
	got := buf.String()
	for _, want := range []string{"adapter Inspect failed", "u3-inspect-probe", "probe: inspect refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("log does not contain %q; Create discarded the Inspect error.\nlog: %s", want, got)
		}
	}
}

// A8c: the warning is guarded. A successful Inspect logs nothing — hoisting the
// slog.Warn out of its error guard makes this red.
func TestCreateLogsNothingWhenInspectSucceeds(t *testing.T) {
	p := &probeAdapter{name: "u3-inspect-ok-probe", turns: 2, model: "m"}
	vault := registerProbe(t, p)
	buf := captureArchiveLog(t)

	res, err := Create(CreateOptions{
		Adapter: p.name, SessionID: "thr_probe_ok", VaultRoot: vault,
		ProjectSlug: "demo", LockPosture: LockNonBlocking,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.TurnCount != 2 || res.Manifest.Model != "m" {
		t.Errorf("manifest = (%d, %q), want (2, m)", res.Manifest.TurnCount, res.Manifest.Model)
	}
	if got := buf.String(); strings.Contains(got, "adapter Inspect failed") {
		t.Errorf("a successful Inspect logged a failure.\nlog: %s", got)
	}
}

// A9: the Inspect pass is BOUNDED by the context Create already holds.
//
// Why these fixture values fire. The probe's real return is (7, "probe-model") —
// deliberately NOT the degraded (0, ""). A probe returning (0, "") would make
// the manifest assertions agree under both the correct code and the mutation,
// and the test would be inert.
//
// Why the margins hold. The deadline is 100ms while hashing and compressing a
// 17-byte source is sub-millisecond, so Create always reaches the Inspect call
// before it fires — and an ALREADY-expired context never reaches it at all
// (Create returns at its opening ctx check, which
// TestCreateCodexRawArchiveStopsAtDeadlineBeforeManifestCommit pins). The
// probe's 2s sleep is 20x the deadline, so neither a loaded host nor -race can
// let the result arm win. Deleting the ctx arm of the select makes the mutation
// wait the full 2s and take the probe's real result: three assertions move at
// once, and the test still TERMINATES, so the red is a --- FAIL rather than a
// harness hang.
func TestCreateAbandonsInspectAtTheDeadline(t *testing.T) {
	p := &probeAdapter{
		name: "u3-slow-probe", sleep: 2 * time.Second,
		turns: 7, model: "probe-model",
	}
	vault := registerProbe(t, p)
	buf := captureArchiveLog(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := Create(CreateOptions{
		Context: ctx, Adapter: p.name, SessionID: "thr_slow", VaultRoot: vault,
		ProjectSlug: "demo", LockPosture: LockNonBlocking,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Create must still succeed when Inspect is abandoned: %v", err)
	}
	if res.Manifest.TurnCount != 0 || res.Manifest.Model != "" {
		t.Errorf("manifest = (%d, %q), want (0, \"\"): the abandoned adapter's result must be DISCARDED, never partially kept",
			res.Manifest.TurnCount, res.Manifest.Model)
	}
	got := buf.String()
	for _, want := range []string{"Inspect abandoned at the deadline", "context deadline exceeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("log does not contain %q; Inspect was not bounded by the context.\nlog: %s", want, got)
		}
	}
	if elapsed >= p.sleep {
		t.Errorf("Create took %s, which is the probe's whole %s sleep: Inspect was not abandoned at the deadline", elapsed, p.sleep)
	}
}
