// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import "encoding/json"

// This file holds the per-tool halves of the param-aware surface gate: the
// predicates that say, for ONE invocation of a Mutating tool, "this call writes
// nothing." The mechanism that consults them lives at the dispatch seam
// (internal/mcp, Tool.ReadOnlyWhen and readOnlyInvocation); what lives here is
// the knowledge only this package has — which parameter discriminates, and what
// value of it the handler below actually treats as non-writing.
//
// 🔴 A PREDICATE IS A CLAIM ABOUT THE HANDLER BESIDE IT, so it is written from
// the handler's own decoding, never from the schema's prose. Each one below
// decodes with the SAME params struct its handler uses, so a field rename
// breaks the compile rather than quietly flipping an answer. When you change a
// handler's write path, this file is part of that change.
//
// 🔴 AND IT DOES NOT TOUCH THE READ-ONLY SERVE FILTER. ReadOnlyServeToolNames
// (readonly_serve.go) strips tools before any invocation exists; none of these
// three may ever be served on a read-only `vp mcp serve`, because each one can
// also be called in its writing mode. Do not "reconcile" the two by teaching
// the serve filter about params: it has none to read, and the reconciliation
// would publish a write-capable tool. The asymmetry note on
// ReadOnlyServeToolNames is the long form.

// ParamAwareToolNames is the canonical list of tools whose surface-gate verdict
// depends on the invocation rather than on the registration — the tools
// carrying an mcp.Tool.ReadOnlyWhen predicate.
//
// It is pinned against the live registry by TestParamAwareToolNamesMatchRegistry,
// the same way MutatingToolNames is: the set of tools a stale binary will admit
// is a security-relevant fact, and it must not be possible to widen it by
// attaching a predicate somewhere without saying so here.
//
// Every entry must also appear in MutatingToolNames. A predicate on a tool that
// is not gated in the first place refines nothing, and reads as though it does.
var ParamAwareToolNames = []string{
	"vp_audit_vault",
	"vp_vault_sync",
	"vp_vault_tidy",
}

// readOnlyIf adapts a typed decision over a tool's own params struct into the
// mcp.Tool.ReadOnlyWhen shape, and owns the fail-closed unmarshal contract for
// every predicate in this file.
//
// 🔴 AN UNMARSHAL FAILURE GATES. It is centralised here rather than repeated in
// each predicate because that is the branch nobody writes a test for by hand:
// the schema has already validated the payload by the time the gate runs, so
// the error path looks unreachable — right up until a schema is loosened, a
// type changes, or a new discriminator is added with a different Go type than
// the wire carries. The one place it can be forgotten is the one place it is.
func readOnlyIf[T any](decide func(T) bool) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		var p T
		if err := json.Unmarshal(raw, &p); err != nil {
			return false
		}
		return decide(p)
	}
}

// vaultSyncReadOnly admits vp_vault_sync's pull — and ONLY its bare pull.
//
// 🔴 `action: "pull"` ALONE IS NOT THE PREDICATE, and reading it that way is the
// trap this comment exists to spring. vaultSyncHandler branches on `paths`
// BEFORE it branches on `action`: a non-empty paths list routes to
// storage.CommitAndPushPaths, which COMMITS regardless of the action, with the
// action deciding only whether the commit is also pushed. So `{"action":"pull",
// "paths":[...],"message":"..."}` writes vault history, and a predicate keyed on
// the action alone would have handed a stale binary exactly the write the gate
// exists to refuse. Both conditions, always.
//
// What the bare pull does is not nothing, and calling it "read-only" is a claim
// about AUTHORSHIP, not about bytes: git writes the worktree, and the
// phantom-template self-heal (storage.Pull) discards local dirt before the
// merge. But every byte it lands came from the remote, authored by whichever
// binary wrote it there, and the heal only drops local copies that provably
// equal the remote content it is about to receive. A stale binary running a
// pull cannot write anything in a stale format — which is the question the
// surface gate asks. It is also the recovery path: refusing it is what made an
// out-of-date host unable to fetch the binary that would fix it.
var vaultSyncReadOnly = readOnlyIf(func(p vaultSyncParams) bool {
	return p.Action == "pull" && len(p.Paths) == 0
})

// vaultTidyReadOnly admits vp_vault_tidy's dry run: dry_run:true returns after
// storage.TidyScan, which classifies the working tree and commits nothing.
//
// Absence gates on its own here — dry_run's zero value is false, which is the
// committing path — so this needs no explicit absent-field branch. It reads the
// same bool the handler reads, so the two cannot disagree about which branch a
// payload selects.
var vaultTidyReadOnly = readOnlyIf(func(p vaultTidyParams) bool {
	return p.DryRun
})

// auditVaultReadOnly admits vp_audit_vault's inline render: with write false the
// handler returns the report in the payload and never reaches atomicfile.Write,
// so nothing is persisted and no surface stamp fires.
//
// 🔴 IT REQUIRES AN EXPLICIT `write: false`, AND THAT IS DELIBERATE. The
// parameter is optional and its handler default is false, so absence and false
// mean the same thing to auditVaultHandler — but they do not mean the same
// thing HERE. This predicate is only allowed to answer from what the caller
// affirmatively said; inferring "read-only" from a field nobody sent is exactly
// the reasoning that must never admit a write. Hence its own params struct with
// a *bool rather than the handler's bool: absent stays distinguishable from
// false, and `{}` gates.
//
// The visible cost is that the natural `{}` audit call stays refused on a
// binary behind its vault, and the caller must pass write:false to get the
// read. That cost is paid knowingly and is the cheap wrong answer of the two.
var auditVaultReadOnly = readOnlyIf(func(p auditVaultGateParams) bool {
	return p.Write != nil && !*p.Write
})

// auditVaultGateParams is auditVaultParams with the discriminator as a pointer,
// so the gate can tell an omitted write from an explicit false. The handler
// keeps its value type: it has no use for the distinction, and giving it one
// would invite someone to "simplify" the two back together.
type auditVaultGateParams struct {
	Write *bool `json:"write"`
}
