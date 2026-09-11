// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
	"io"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcphost"
)

// stubHost is a controllable mcphost.Host for exercising the report logic.
type stubHost struct {
	name      string
	flag      string
	detected  bool
	installed bool
	statusErr error
}

func (s stubHost) Name() string                           { return s.name }
func (s stubHost) Flag() string                           { return s.flag }
func (s stubHost) Detected() bool                         { return s.detected }
func (s stubHost) Installed() (bool, error)               { return s.installed, s.statusErr }
func (s stubHost) Install(_, _ string, _ io.Writer) error { return nil }
func (s stubHost) Uninstall(_ io.Writer) error            { return nil }
func (s stubHost) Executables() []string                  { return nil }

var _ mcphost.Host = stubHost{}

func TestCheckMCPHosts(t *testing.T) {
	rows := CheckMCPHosts([]mcphost.Host{
		stubHost{name: "claude", flag: "--claude-plugin", detected: true, installed: true},
		stubHost{name: "grok", flag: "--grok", detected: true, installed: false},
		stubHost{name: "zed", flag: "--zed", detected: false}, // omitted (undetected)
		stubHost{name: "err", flag: "--err", detected: true, statusErr: errors.New("boom")},
	})

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (undetected zed omitted), got %d: %+v", len(rows), rows)
	}
	if rows[0].Status != Pass {
		t.Errorf("claude (installed) = %v, want Pass", rows[0].Status)
	}
	if rows[1].Status != Info || len(rows[1].Details) == 0 {
		t.Errorf("grok (detected, not installed) = %v details=%v, want Info+hint", rows[1].Status, rows[1].Details)
	}
	if rows[2].Status != Info {
		t.Errorf("err (status error) = %v, want Info", rows[2].Status)
	}
}

func TestCheckMCPHosts_NoneDetected(t *testing.T) {
	rows := CheckMCPHosts([]mcphost.Host{
		stubHost{name: "claude", detected: false},
		stubHost{name: "grok", detected: false},
	})
	if len(rows) != 1 || rows[0].Status != Skip {
		t.Fatalf("expected single Skip row when none detected, got %+v", rows)
	}
}

// TestCheckMCPHosts_NilRegistry pins the empty-input case: no hosts at all (a
// caller whose registry is empty, or a test stub returning nil) reads exactly
// like "none detected" — one Skip row, never an empty report.
func TestCheckMCPHosts_NilRegistry(t *testing.T) {
	rows := CheckMCPHosts(nil)
	if len(rows) != 1 || rows[0].Status != Skip || rows[0].Name != "MCP hosts" {
		t.Fatalf("CheckMCPHosts(nil) = %+v, want the single \"MCP hosts\" Skip row", rows)
	}
}
