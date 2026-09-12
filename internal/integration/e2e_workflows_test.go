// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

// Go port of the retired test/e2e/workflows/ bash tier's one case
// (01-tune-rooms-loop.sh): a two-iteration loop exercising the tune-rooms
// workflow end to end — seed drawers → `vp tune rooms --export` → two
// `vp tune rooms --apply` calls (idempotency check via decoded-TOML-struct
// equality). Hard-asserts mirror the case's own "hard-assert vs
// tracked-metric" contract: exit code, JSON-decodable report, .project,
// .samples_total, and apply-idempotency are ASSERTIONS; everything else
// (ms per command, sample/proposal/agreement counts) is a tracked METRIC,
// never asserted, because it depends on the mock LLM's canned response
// distribution rather than vp's callable contract.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// allTestingMockResponse inlines test/e2e/workflows/mockllm/responses/all-testing.json
// verbatim: five placeholder judgments whose drawer_ids never match the
// freshly-seeded IDs, so ParseJudgments silently skips them and
// judgments_total/proposals come back 0. That is fine — these are metrics
// here, not assertions — the loop still verifies vp tune's CLI contract
// end-to-end (export shape, apply idempotency).
const allTestingMockResponse = `[
  {"drawer_id": "unknown-1", "room": "testing", "confidence": 0.9, "reasoning": "test content"},
  {"drawer_id": "unknown-2", "room": "testing", "confidence": 0.9, "reasoning": "test content"},
  {"drawer_id": "unknown-3", "room": "testing", "confidence": 0.9, "reasoning": "test content"},
  {"drawer_id": "unknown-4", "room": "testing", "confidence": 0.9, "reasoning": "test content"},
  {"drawer_id": "unknown-5", "room": "testing", "confidence": 0.9, "reasoning": "test content"}
]`

// workflowsMetricsDir returns the directory this test writes metrics.jsonl
// into, and is the fix for a confirmed-real, pre-existing bug found while
// porting this tier: the retired test/e2e/workflows/run.sh's own cleanup
// trap deleted $TMPROOT on ANY success exit before ci.yml's separate
// "Upload workflows metrics on success" step ever ran, so that 14-day-
// retention upload has been silently uploading nothing on every green run
// (if-no-files-found: ignore swallowed it).
//
// Retention policy (criterion 3, and the local-dev-leak Medium finding from
// review): under CI (the CI env var GitHub Actions sets), return a plain
// os.MkdirTemp directory with NO t.Cleanup — the process exits long before
// any garbage collection would matter, and ci.yml's own upload step needs the
// directory to survive past this test process's exit, which t.TempDir()
// cannot do (its cleanup is unconditional, pass or fail). Outside CI, return
// t.TempDir() instead: a developer running `make integration` repeatedly
// over weeks must not accumulate one leaked directory per run forever, and
// local runs have no separate upload step reading it after the fact anyway.
func workflowsMetricsDir(t *testing.T) string {
	t.Helper()
	if os.Getenv("CI") == "true" {
		dir, err := os.MkdirTemp("", "vp-e2e-workflows-metrics.")
		if err != nil {
			t.Fatalf("workflowsMetricsDir: mkdtemp: %v", err)
		}
		return dir
	}
	return t.TempDir()
}

// metricEmitter appends one JSONL row per call, mirroring lib.sh's
// emit_metric (ts/case/iter auto-injected). Metrics are purely informational
// — no assertion ever reads this file back.
type metricEmitter struct {
	t    *testing.T
	f    *os.File
	iter int
}

func newMetricEmitter(t *testing.T, path string) *metricEmitter {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open metrics file %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return &metricEmitter{t: t, f: f}
}

func (m *metricEmitter) emit(fields map[string]any) {
	m.t.Helper()
	row := map[string]any{"ts": time.Now().UnixMilli(), "case": "01-tune-rooms-loop", "iter": m.iter}
	for k, v := range fields {
		row[k] = v
	}
	b, err := json.Marshal(row)
	if err != nil {
		m.t.Logf("metric marshal: %v", err)
		return
	}
	if _, err := m.f.Write(append(b, '\n')); err != nil {
		m.t.Logf("metric append: %v", err)
	}
}

// TestIntegrationE2EWorkflowsTuneRoomsLoop ports
// workflows/01-tune-rooms-loop.sh.
func TestIntegrationE2EWorkflowsTuneRoomsLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "workflows")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, projDir)

	testinfra.RunCLI(t, env.Environ(), projDir, nil, "init").Must(t)

	mock := newMockLLMServer(t, allTestingMockResponse)
	mockKeyEnv := writeProjectLLMConfig(t, filepath.Join(env.Home, "vibe-palace-vault"), "proj", mock.URL)
	tuneEnv := env.Environ(mockKeyEnv)

	metrics := newMetricEmitter(t, filepath.Join(workflowsMetricsDir(t), "metrics.jsonl"))

	vaultRoot := filepath.Join(env.Home, "vibe-palace-vault")

	for iter := 1; iter <= 2; iter++ {
		metrics.iter = iter

		// --- seed drawers ---
		capStart := time.Now()
		for i := 1; i <= 5; i++ {
			seedDrawer(t, env.Home, "proj", "facts",
				fmt.Sprintf("iter-%d drawer %d: testing batch subsystem regression coverage", iter, i))
		}
		metrics.emit(map[string]any{"cmd": "seed-drawers", "ms": time.Since(capStart).Milliseconds(), "count": 5})

		// --- tune --export (HARD shape asserts) ---
		reportPath := filepath.Join(caseDir, fmt.Sprintf("report-%d.json", iter))
		txStart := time.Now()
		exportRes := testinfra.RunCLI(t, tuneEnv, projDir, nil, "tune", "rooms", "--export", reportPath)
		exportMs := time.Since(txStart).Milliseconds()
		exportRes.Must(t)
		requireFileExists(t, reportPath)

		reportData, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatal(err)
		}
		var report palace.TuneReport
		if err := json.Unmarshal(reportData, &report); err != nil {
			t.Fatalf("iter %d: report.json does not decode as palace.TuneReport: %v\n%s", iter, err, reportData)
		}
		// Go's static typing already enforces "proposals/unmatched_flags are
		// arrays" and "report.json parses" — a decode error above IS that
		// assertion failing. What is left to hard-assert is the CONTENT
		// contract bash's jq expressions checked:
		if report.Project != "proj" {
			t.Errorf("iter %d: report.Project = %q, want %q", iter, report.Project, "proj")
		}
		if report.SamplesTotal < 4 {
			t.Errorf("iter %d: report.SamplesTotal = %d, want >= 4", iter, report.SamplesTotal)
		}
		if report.JudgmentsTotal < 0 {
			t.Errorf("iter %d: report.JudgmentsTotal = %d, want >= 0", iter, report.JudgmentsTotal)
		}

		metrics.emit(map[string]any{
			"cmd":             "tune --export",
			"ms":              exportMs,
			"samples":         report.SamplesTotal,
			"proposals":       len(report.Proposals),
			"agreements":      report.Agreements,
			"disagreements":   report.Disagreements,
			"judgments_total": report.JudgmentsTotal,
		})

		// --- tune --apply idempotency (HARD struct-equal assert) ---
		cfg := projectConfigPath(t, vaultRoot, "proj")

		testinfra.RunCLI(t, tuneEnv, projDir, nil, "tune", "rooms", "--apply").Must(t)
		requireFileExists(t, cfg)
		apply1 := filepath.Join(caseDir, fmt.Sprintf("config-%d-apply1.toml", iter))
		copyFile(t, cfg, apply1)

		testinfra.RunCLI(t, tuneEnv, projDir, nil, "tune", "rooms", "--apply").Must(t)
		apply2 := filepath.Join(caseDir, fmt.Sprintf("config-%d-apply2.toml", iter))
		copyFile(t, cfg, apply2)

		if !tomlStructEqual(t, apply1, apply2) {
			t.Fatalf("iter %d: tune rooms --apply is not idempotent: %s vs %s differ structurally", iter, apply1, apply2)
		}
		metrics.emit(map[string]any{"cmd": "tune --apply(idemp)", "struct_stable": 1})

		// Disagreements as reclassification proxy — TRACKED, not asserted.
		metrics.emit(map[string]any{
			"cmd":             "tune",
			"iter_summary":    1,
			"disagreements":   report.Disagreements,
			"judgments_total": report.JudgmentsTotal,
		})
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}
