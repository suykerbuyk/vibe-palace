// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"sort"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestReadOnlyInvocationPredicates is the per-tool truth table. Each row is a
// payload a caller can actually send, and the want column is whether the
// surface gate should treat that CALL as a read.
//
// Both directions per tool, deliberately: a table of read-only cases alone
// would pass against a predicate that returned true unconditionally, which is
// the same vacuity that makes "the read passes" a meaningless assertion on its
// own.
func TestReadOnlyInvocationPredicates(t *testing.T) {
	cases := []struct {
		name      string
		predicate func(json.RawMessage) bool
		params    string
		want      bool
	}{
		// --- vp_vault_sync -------------------------------------------------
		{"sync/bare_pull_is_read", vaultSyncReadOnly, `{"action":"pull"}`, true},
		{"sync/pull_with_empty_paths_is_read", vaultSyncReadOnly, `{"action":"pull","paths":[]}`, true},
		{"sync/push_writes", vaultSyncReadOnly, `{"action":"push"}`, false},
		{"sync/sync_writes", vaultSyncReadOnly, `{"action":"sync"}`, false},
		// 🔴 THE SHARP ONE. vaultSyncHandler branches on paths BEFORE action, so
		// this payload commits vault history through CommitAndPushPaths even
		// though the action says "pull". A predicate keyed on the action alone
		// would hand a stale binary exactly the write the gate exists to refuse.
		{"sync/pull_with_paths_commits", vaultSyncReadOnly, `{"action":"pull","paths":["a.md"],"message":"m"}`, false},
		{"sync/absent_action", vaultSyncReadOnly, `{}`, false},
		{"sync/unknown_action", vaultSyncReadOnly, `{"action":"fetch"}`, false},
		{"sync/null_payload", vaultSyncReadOnly, `null`, false},
		{"sync/unparseable", vaultSyncReadOnly, `{"action":`, false},
		{"sync/wrong_type_for_discriminator", vaultSyncReadOnly, `{"action":7}`, false},

		// --- vp_vault_tidy -------------------------------------------------
		{"tidy/dry_run_is_read", vaultTidyReadOnly, `{"dry_run":true}`, true},
		{"tidy/dry_run_with_push_is_still_read", vaultTidyReadOnly, `{"dry_run":true,"push":true}`, true},
		{"tidy/explicit_false_writes", vaultTidyReadOnly, `{"dry_run":false}`, false},
		{"tidy/absent_writes", vaultTidyReadOnly, `{}`, false},
		{"tidy/null_payload", vaultTidyReadOnly, `null`, false},
		{"tidy/unparseable", vaultTidyReadOnly, `{"dry_run":`, false},

		// --- vp_audit_vault ------------------------------------------------
		{"audit/explicit_false_is_read", auditVaultReadOnly, `{"write":false}`, true},
		{"audit/explicit_false_with_date_is_read", auditVaultReadOnly, `{"write":false,"date":"2026-08-20"}`, true},
		{"audit/true_writes", auditVaultReadOnly, `{"write":true}`, false},
		// Absent is NOT false here, on purpose — see auditVaultReadOnly. The
		// handler defaults it to false; this predicate refuses to infer that.
		{"audit/absent_gates", auditVaultReadOnly, `{}`, false},
		{"audit/null_payload", auditVaultReadOnly, `null`, false},
		{"audit/unparseable", auditVaultReadOnly, `{"write":`, false},
		{"audit/wrong_type_for_discriminator", auditVaultReadOnly, `{"write":"false"}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.predicate(json.RawMessage(tc.params)); got != tc.want {
				verb := map[bool]string{true: "read-only", false: "writing"}
				t.Errorf("params %s: predicate says %s, want %s", tc.params, verb[got], verb[tc.want])
			}
		})
	}
}

// TestReadOnlyIfFailsClosedOnUnmarshalError pins the shared adapter's own
// contract directly, rather than only through the three predicates that happen
// to use it today. The next predicate added to this file inherits this branch,
// and inherits it silently — so it is pinned where it is written.
func TestReadOnlyIfFailsClosedOnUnmarshalError(t *testing.T) {
	// A decision that would say "read-only" for the zero value, which is what a
	// carelessly written predicate looks like.
	pred := readOnlyIf(func(p struct {
		Mode string `json:"mode"`
	}) bool {
		return p.Mode != "write"
	})

	if pred(json.RawMessage(`{"mode":"read"}`)) != true {
		t.Fatal("sanity: a parseable read-only payload should be admitted")
	}
	for _, bad := range []string{`{"mode":`, `[1,2,3]`, `"a string"`, `{"mode":{}}`} {
		if pred(json.RawMessage(bad)) {
			t.Errorf("payload %s: readOnlyIf admitted an unparseable payload", bad)
		}
	}
}

// TestParamAwareToolNamesMatchRegistry is the mechanical safeguard for the
// param-aware gate, and the sibling of TestMutatingToolNamesMatchRegistry: the
// set of tools whose gate verdict can be relaxed by their parameters must be
// exactly ParamAwareToolNames.
//
// Attaching a predicate is a widening of what a stale binary will admit. It has
// to be a declared act, not a diff in one struct literal that nobody reviews.
func TestParamAwareToolNamesMatchRegistry(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)

	RegisterAll(srv.Registry(), resolver, vault, eng)

	got := srv.Registry().ParamAwareToolNames()
	want := append([]string(nil), ParamAwareToolNames...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("registry has %d param-aware tools, ParamAwareToolNames has %d\n got:  %v\n want: %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("param-aware set mismatch at %d: registry=%q want=%q\n got:  %v\n want: %v",
				i, got[i], want[i], got, want)
		}
	}
}

// TestParamAwareToolsAreAllMutating pins the containment that makes a predicate
// mean anything: it can only RELAX a gate, so a predicate on a tool that is not
// gated in the first place refines nothing while reading as though it does.
func TestParamAwareToolsAreAllMutating(t *testing.T) {
	mutating := make(map[string]bool, len(MutatingToolNames))
	for _, n := range MutatingToolNames {
		mutating[n] = true
	}
	for _, n := range ParamAwareToolNames {
		if !mutating[n] {
			t.Errorf("%s carries a ReadOnlyWhen predicate but is not in MutatingToolNames — it is not gated, so the predicate relaxes nothing", n)
		}
	}
}

// TestParamAwareToolsAreNotReadOnlyServed is requirement 3 as a test rather
// than a comment: a sometimes-read-only tool must still be STRIPPED from a
// read-only `vp mcp serve`.
//
// The serve filter runs at registration, where no invocation exists — serving
// vp_vault_sync serves push along with pull. If someone ever "reconciles" the
// two predicates by promoting these three into the allow-list, this fails, and
// the failure names the security regression rather than a style disagreement.
func TestParamAwareToolsAreNotReadOnlyServed(t *testing.T) {
	served := make(map[string]bool, len(ReadOnlyServeToolNames))
	for _, n := range ReadOnlyServeToolNames {
		served[n] = true
	}
	for _, n := range ParamAwareToolNames {
		if served[n] {
			t.Errorf("%s is param-aware at the surface gate AND allow-listed for read-only serve: the serve filter has no params to read, so serving it publishes its WRITING mode too", n)
		}
	}
}

// TestParamAwareToolNamesNoDuplicates guards the declaration itself.
func TestParamAwareToolNamesNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(ParamAwareToolNames))
	for _, n := range ParamAwareToolNames {
		if seen[n] {
			t.Errorf("duplicate entry in ParamAwareToolNames: %q", n)
		}
		seen[n] = true
	}
}
