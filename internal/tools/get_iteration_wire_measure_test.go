// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Manual measurement against the live vault (skipped unless VP_VAULT is set).
// Not a gate — the ratchet is TestGetIteration_WireSizeAtMaxBudget.
func TestMeasureLiveWireAtMaxBudget(t *testing.T) {
	root := os.Getenv("VP_VAULT")
	if root == "" {
		// default live path used on this host
		root = "/home/johns/vibe-palace-vault"
	}
	if _, err := os.Stat(filepath.Join(root, "Projects")); err != nil {
		t.Skip("no live vault")
	}
	vault := storage.NewVault(root)
	tool := GetIterationTool(vault)
	projects := []string{"quantum-ng", "rezbldr", "rezbldrvault", "rusty-can", "recmeet", "vibe-palace"}
	t.Logf("max_bytes=%d host_cap=%d reserve=%d", MaxGetIterationMaxBytes, HostInlineCapBytes, getIterationEnvelopeReserve)
	for _, p := range projects {
		raw, _ := json.Marshal(map[string]any{"project": p, "recent": true, "max_bytes": MaxGetIterationMaxBytes})
		out, err := tool.Handler(context.Background(), raw)
		if err != nil {
			t.Logf("%s ERR %v", p, err)
			continue
		}
		wire, _ := json.Marshal(out)
		b, _ := json.Marshal(out)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		over := len(wire) > HostInlineCapBytes
		t.Logf("%-14s returned=%v inlined=%v WIRE=%d over_host=%v more=%v",
			p, m["returned"], m["bytes_inlined"], len(wire), over, m["more_available"])
		if over {
			t.Errorf("%s wire %d exceeds host cap %d", p, len(wire), HostInlineCapBytes)
		}
	}
}
