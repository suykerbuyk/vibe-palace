// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// VaultSettingsReconciler verifies that the three-layer settings (embedded
// defaults, vault overlay, project overlay) load cleanly. It does not
// write. CheckEmbedder is intentionally out of scope — it stays with
// `vp check`.
type VaultSettingsReconciler struct {
	vault *storage.Vault
}

func NewVaultSettings(v *storage.Vault) *VaultSettingsReconciler {
	return &VaultSettingsReconciler{vault: v}
}

func (r *VaultSettingsReconciler) Name() string { return "VaultSettings" }
func (r *VaultSettingsReconciler) Tier() Tier   { return TierVault }

func (r *VaultSettingsReconciler) Check(_ context.Context) []check.Result {
	if r.vault == nil {
		return []check.Result{{
			Name: "Settings", Status: check.Skip,
			Summary: "vault not open — run `vp init`",
		}}
	}
	_, row := check.CheckSettings(r.vault)
	return []check.Result{row}
}

func (r *VaultSettingsReconciler) Plan(ctx context.Context) (Plan, error) {
	rows := r.Check(ctx)
	kind := ActionUnchanged
	summary := "settings load cleanly at all three layers"
	if len(rows) == 0 || rows[0].Status != check.Pass {
		kind = ActionSkip
		if len(rows) > 0 {
			summary = rows[0].Summary
		}
	}
	return Plan{Actions: []Action{{Kind: kind, Target: "settings", Summary: summary}}}, nil
}

// Apply is a no-op for VaultSettings — this reconciler only verifies.
func (r *VaultSettingsReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		}
	}
	return rep, nil
}
