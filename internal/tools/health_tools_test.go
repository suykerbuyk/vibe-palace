// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestHealthToolHealthy(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := HealthTool(vault)

	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "healthy" {
		t.Errorf("status = %q, want healthy", hr.Status)
	}
}

func TestHealthToolWarnings(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entries := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"entity extraction: add entity failed","err":"disk full"}`, now),
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"entity extraction: duplicate entity skipped","entity":"test"}`, now),
		fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"normal operation"}`, now),
	}

	logPath := filepath.Join(logDir, "vp.log")
	var content string
	for _, e := range entries {
		content += e + "\n"
	}
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "warnings" {
		t.Errorf("status = %q, want warnings", hr.Status)
	}
	if len(hr.RecentWarns) != 2 {
		t.Errorf("recent warns = %d, want 2", len(hr.RecentWarns))
	}
	if hr.WarnCounts["entity extraction"] != 2 {
		t.Errorf("warn count for entity extraction = %d, want 2", hr.WarnCounts["entity extraction"])
	}
}

func TestHealthToolErrors(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entry := fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"critical failure"}`, now)
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(entry+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "errors" {
		t.Errorf("status = %q, want errors", hr.Status)
	}
}

func TestHealthToolMalformedLines(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	content := "this is not json\n" +
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"valid warn"}`, now) + "\n" +
		"{broken json\n"
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if len(hr.RecentWarns) != 1 {
		t.Errorf("recent warns = %d, want 1 (malformed lines skipped)", len(hr.RecentWarns))
	}
}

func TestHealthToolOldEntries(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	entry := fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"old warning"}`, old)
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(entry+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(projectParams{Project: "test"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(HealthResult)
	if hr.Status != "healthy" {
		t.Errorf("status = %q, want healthy (old entries excluded)", hr.Status)
	}
	if len(hr.RecentWarns) != 0 {
		t.Errorf("recent warns = %d, want 0", len(hr.RecentWarns))
	}
}

// writeHealthLog writes the given raw log lines to the vault's vp.log and
// returns the HealthTool ready to invoke.
func writeHealthLog(t *testing.T, vault *storage.Vault, lines []string) {
	t.Helper()
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func runHealth(t *testing.T, vault *storage.Vault, params any) HealthResult {
	t.Helper()
	tool := HealthTool(vault)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return result.(HealthResult)
}

// TestHealthToolDefaultBehavior confirms that omitting hours/limit reproduces
// today's contract exactly: a 24h window and a 20-entry recent_warns cap.
func TestHealthToolDefaultBehavior(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)

	var lines []string
	// 25 in-window warns (> the 20 cap) to prove the default cap.
	for i := 0; i < 25; i++ {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, now, i))
	}
	// An entry outside the default 24h window must be excluded.
	lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: stale"}`, old))
	writeHealthLog(t, vault, lines)

	// projectParams marshals to only {"project":...} — the true "no params" case.
	hr := runHealth(t, vault, projectParams{Project: "test"})

	if len(hr.RecentWarns) != 20 {
		t.Errorf("recent warns = %d, want 20 (default cap)", len(hr.RecentWarns))
	}
	// warn_counts tallies every in-window entry (25), independent of the cap;
	// the 48h-old entry is excluded by the default 24h window.
	if hr.WarnCounts["cat"] != 25 {
		t.Errorf("warn count cat = %d, want 25 (all in-window, stale excluded)", hr.WarnCounts["cat"])
	}
}

// TestHealthToolLimitCapsOnlyRecentWarns is THE key nuance: limit caps the
// recent_warns list but warn_counts still tallies every in-window entry.
func TestHealthToolLimitCapsOnlyRecentWarns(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)

	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, now, i))
	}
	writeHealthLog(t, vault, lines)

	hr := runHealth(t, vault, healthParams{Project: "test", Limit: 3})

	if len(hr.RecentWarns) != 3 {
		t.Errorf("recent warns = %d, want 3 (capped by limit)", len(hr.RecentWarns))
	}
	if hr.WarnCounts["cat"] != 10 {
		t.Errorf("warn count cat = %d, want 10 (limit must NOT cap warn_counts)", hr.WarnCounts["cat"])
	}
	// Assert the nuance directly: counts exceed the list cap.
	if hr.WarnCounts["cat"] <= len(hr.RecentWarns) {
		t.Errorf("expected warn_counts total (%d) > recent_warns len (%d)", hr.WarnCounts["cat"], len(hr.RecentWarns))
	}
}

// TestHealthToolHoursWindow confirms hours widens/narrows the window for BOTH
// warn_counts and recent_warns.
func TestHealthToolHoursWindow(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	mid := time.Now().UTC().Add(-10 * time.Hour).Format(time.RFC3339)
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: recent"}`, recent),
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: mid"}`, mid),
	}
	writeHealthLog(t, vault, lines)

	// Narrow 5h window: only the 1h-old entry qualifies.
	narrow := runHealth(t, vault, healthParams{Project: "test", Hours: 5})
	if len(narrow.RecentWarns) != 1 || narrow.WarnCounts["cat"] != 1 {
		t.Errorf("narrow(5h): recent=%d counts=%d, want 1/1", len(narrow.RecentWarns), narrow.WarnCounts["cat"])
	}

	// Wide 48h window: both entries qualify.
	wide := runHealth(t, vault, healthParams{Project: "test", Hours: 48})
	if len(wide.RecentWarns) != 2 || wide.WarnCounts["cat"] != 2 {
		t.Errorf("wide(48h): recent=%d counts=%d, want 2/2", len(wide.RecentWarns), wide.WarnCounts["cat"])
	}
}

// TestHealthToolClamping exercises the clamp bounds for hours and limit.
func TestHealthToolClamping(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	// An entry 30 days minus a margin old, inside the 720h ceiling but well
	// outside a 24h default — used to prove hours:99999 clamps to 720 (not
	// unbounded, and not the default).
	within720 := time.Now().UTC().Add(-700 * time.Hour).Format(time.RFC3339)
	beyond720 := time.Now().UTC().Add(-800 * time.Hour).Format(time.RFC3339)

	t.Run("hours negative -> 24 default", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
		writeHealthLog(t, vault, []string{
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: fresh"}`, now),
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: old"}`, old),
		})
		hr := runHealth(t, vault, healthParams{Project: "test", Hours: -5})
		if hr.WarnCounts["cat"] != 1 {
			t.Errorf("hours:-5 -> counts=%d, want 1 (24h default excludes 48h-old)", hr.WarnCounts["cat"])
		}
	})

	t.Run("hours huge -> 720 ceiling", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		writeHealthLog(t, vault, []string{
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: within720"}`, within720),
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: beyond720"}`, beyond720),
		})
		hr := runHealth(t, vault, healthParams{Project: "test", Hours: 99999})
		if hr.WarnCounts["cat"] != 1 {
			t.Errorf("hours:99999 -> counts=%d, want 1 (720h ceiling includes 700h, excludes 800h)", hr.WarnCounts["cat"])
		}
	})

	t.Run("limit zero -> 20 default", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		var lines []string
		for i := 0; i < 25; i++ {
			lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: e%d"}`, now, i))
		}
		writeHealthLog(t, vault, lines)
		hr := runHealth(t, vault, healthParams{Project: "test", Limit: 0})
		if len(hr.RecentWarns) != 20 {
			t.Errorf("limit:0 -> recent=%d, want 20 (default)", len(hr.RecentWarns))
		}
	})

	t.Run("limit huge -> 1000 ceiling", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		var lines []string
		for i := 0; i < 25; i++ {
			lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: e%d"}`, now, i))
		}
		writeHealthLog(t, vault, lines)
		hr := runHealth(t, vault, healthParams{Project: "test", Limit: 99999})
		// 25 entries all fit under the 1000 ceiling: none dropped.
		if len(hr.RecentWarns) != 25 {
			t.Errorf("limit:99999 -> recent=%d, want 25 (all fit under 1000 ceiling)", len(hr.RecentWarns))
		}
	})
}
