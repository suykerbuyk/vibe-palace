// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestRunAuditRooms_Empty(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, false, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	out := buf.String()
	if !strings.Contains(out, "0 drawers") {
		t.Errorf("expected '0 drawers' in output: %s", out)
	}
	if !strings.Contains(out, "No drawers found") {
		t.Errorf("expected 'No drawers found' in output: %s", out)
	}
}

func TestRunAuditRooms_Report(t *testing.T) {
	v := testVault(t)
	v.AppendDrawer("proj", "wing", "devops", storage.Drawer{
		Content: "Set up the kubernetes cluster for deployment.", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})
	v.AppendDrawer("proj", "wing", "api", storage.Drawer{
		Content: "Set up kubernetes to deploy containers.", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, false, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Room Distribution") {
		t.Errorf("missing Room Distribution header: %s", out)
	}
	if !strings.Contains(out, "Reclassification Candidates") {
		t.Errorf("expected mismatches in output: %s", out)
	}
}

func TestRunAuditRooms_JSON(t *testing.T) {
	v := testVault(t)
	v.AppendDrawer("proj", "wing", "devops", storage.Drawer{
		Content: "deploy the kubernetes cluster", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	exportPath := t.TempDir() + "/report.json"
	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, false, false, false, exportPath, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	// Verify JSON file was created and is valid.
	data, err := readFile(exportPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var report palace.AuditReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	if report.TotalDrawers != 1 {
		t.Errorf("TotalDrawers = %d, want 1", report.TotalDrawers)
	}
}

func TestRunAuditRooms_DryRun(t *testing.T) {
	v := testVault(t)
	// kubernetes content in wrong room.
	v.AppendDrawer("proj", "wing", "api", storage.Drawer{
		Content: "Set up the kubernetes cluster for deployment.", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, false, true, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "candidates") {
		t.Errorf("expected candidates output: %s", out)
	}

	// Verify drawer was NOT moved.
	drawers, _ := v.ListDrawers("proj", "wing", "api")
	if len(drawers) != 1 {
		t.Errorf("drawer should still be in api room, got %d drawers", len(drawers))
	}
}

func TestRunAuditRooms_Apply(t *testing.T) {
	v := testVault(t)
	// kubernetes content in wrong room.
	v.AppendDrawer("proj", "wing", "api", storage.Drawer{
		Content: "Set up the kubernetes cluster for deployment.", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, true, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "MOVED") {
		t.Errorf("expected MOVED in output: %s", out)
	}

	// Verify drawer was moved from api to devops.
	apiDrawers, _ := v.ListDrawers("proj", "wing", "api")
	if len(apiDrawers) != 0 {
		t.Errorf("api room should be empty, got %d", len(apiDrawers))
	}
	devopsDrawers, _ := v.ListDrawers("proj", "wing", "devops")
	if len(devopsDrawers) != 1 {
		t.Errorf("devops room should have 1 drawer, got %d", len(devopsDrawers))
	}
}

func TestRunAuditRooms_MutualExclusive(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, true, true, false, "", &buf)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
	out := buf.String()
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("expected mutual exclusion error: %s", out)
	}
}

func TestRunAuditRooms_Verbose(t *testing.T) {
	v := testVault(t)
	v.AppendDrawer("proj", "wing", "devops", storage.Drawer{
		Content: "deploy the kubernetes cluster using terraform", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, false, false, true, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Keyword Coverage") {
		t.Errorf("verbose mode should show keyword coverage: %s", out)
	}
	if !strings.Contains(out, "fired") {
		t.Errorf("verbose mode should show fired counts: %s", out)
	}
}

func TestRunAuditRooms_ApplyNoMismatches(t *testing.T) {
	v := testVault(t)
	v.AppendDrawer("proj", "wing", "devops", storage.Drawer{
		Content: "Set up the kubernetes cluster for deployment.", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, true, false, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No reclassification candidates") {
		t.Errorf("expected no candidates message: %s", buf.String())
	}
}

func TestRunAuditRooms_DryRunNoMismatches(t *testing.T) {
	v := testVault(t)
	v.AppendDrawer("proj", "wing", "devops", storage.Drawer{
		Content: "Set up the kubernetes cluster for deployment.", Hall: "facts",
		SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z",
	})

	cfg := storage.Config{}
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", cfg, false, true, false, "", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No reclassification candidates") {
		t.Errorf("expected no candidates message: %s", buf.String())
	}
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
