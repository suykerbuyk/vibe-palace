// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"fmt"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// vaultDirtSampleN is how many dirty paths ride in the structured field.
//
// It is a SAMPLE, not a list. Count carries the truth about how many there are,
// and vp_vault_tidy is the reader that enumerates them — the lesson already paid
// for by Health.RecentWarns, which spent 28% of everything ahead of the bulk on
// records that a named reader already served, in the one region of the payload a
// host preview keeps.
//
// TWO, measured rather than chosen. Vault paths run 70-80 B each here, so every
// extra sample costs about 4% of the worst-case instrument budget that
// TestBootstrapInstrumentBlockWorstCaseFitsHostPreview enforces. Two is what the
// common case actually needs: nearly all genuine dirt is one to three files, and
// two entries are enough to show whether they share a project — which is the one
// thing the count alone cannot say, and the thing the dotfiles specimen turned
// on. Beyond that the answer is the reader, not more bytes.
const vaultDirtSampleN = 2

// vaultDirtScanBudget is the wall-clock the bootstrap gives `git status`.
//
// 🔴 IT IS DELIBERATELY NOT storage.DefaultTidyScanTimeout. This runs on the
// session handshake, which 190 took from ~0.4 s to 0.012 s; inheriting tidy's
// 30 s would let one slow or wedged git hold every session start for half a
// minute. Measured cost of the scan itself on the live vault (64,893 tracked
// files, warm): see TestVaultDirtScanCost — the budget is roughly an order of
// magnitude above it, which is headroom for a cold page cache and not an
// invitation to a hang.
const vaultDirtScanBudget = 2 * time.Second

// VaultDirt is the bootstrap payload's report that the VAULT WORKTREE CARRIES
// UNCOMMITTED NON-ARTIFACT CHANGES — the structured half of the condition alert.
// It is attached only when the condition fires; NIL WHEN THE VAULT IS CLEAN, for
// the same reason Health, AuditStaleness and SurfaceMismatch are.
//
// This is the DETECTION half of the ruling recorded in the task
// `task-writes-leave-the-vault-dirty-with-no-sweeper` (Operator decision,
// 2026-09-06). The ENFORCEMENT half — the typed task writer committing its own
// write — lives in task_write_commit.go and closes the window for writes that go
// through vp_manage_task. This covers what that cannot reach: a task file edited
// by a human in Obsidian, dirt left by another machine or another project, and
// the case where the writer's own commit FAILED and reported so.
//
// 🔴 IT REPORTS A STOP, NOT A NAG, AND THE BLAST RADIUS IS THE REASON. SyncVault
// computes genuine dirt and REFUSES at step 2, returning before the
// capture-artifact commit at step 3 (internal/storage/vaultsyncflow.go:96-103).
// So one uncommitted file does not merely fail to sync itself — it wedges the
// sync of session summaries, transcripts, drawers and knowledge-graph triples
// too. A reader who takes this for "some file is untidy" will under-react; the
// message says what is actually blocked.
//
// 🔴 THE GATE IS genuineDirt, NOT Reported. Reported is tidy's full catch-all and
// it INCLUDES deliberately-pending user memory under Projects/<slug>/memory/,
// which is expected content that never blocks a sync (decision 7). Alerting on
// that would fire on a healthy vault, which is precisely how a reader is trained
// to skim every alert on this payload — the same reasoning that killed the
// `partial` capture status. The set is taken from storage.(*TidyResult).GenuineDirt,
// the SAME function SyncVault's refusal reads, so "a sync would refuse right now"
// is true by construction rather than by two filters happening to agree.
type VaultDirt struct {
	// Count is the whole genuine-dirt set, not the sample below. A reader
	// needs to know how much it is NOT seeing; this is the field that says so.
	Count int `json:"count"`

	// SamplePaths is a bounded sample (vaultDirtSampleN). len(SamplePaths) <
	// Count means there are more — derived at read time rather than stored as a
	// flag, because a stored derived value is a second source of a fact that
	// already has one.
	//
	// The Go name says `Sample` because that is what the field IS — the JSON name
	// stays `paths`, which is what a reader of the payload sees. This is now an
	// ordinary naming preference and nothing depends on it. It used to be
	// load-bearing: sourceaudit's write-only-field rule recorded composite-literal
	// assignments by BARE FIELD NAME, so setting `Paths:` here marked every `Paths`
	// field in the repository as assigned and silently retired the standing
	// `write-only-field skills.SkillFrontmatter.Paths` baseline entry. That rule now
	// qualifies composite literals by type, and the collision is pinned by
	// TestCompositeLiteralDoesNotMaskASameNamedFieldInAnotherPackage rather than by
	// this paragraph — a constraint enforced by a comment in another package is not
	// enforced at all.
	SamplePaths []string `json:"paths"`

	// Message is the one-line human form that rides in the alert slot of
	// post_bootstrap_instructions. Derived from the fields above, never authored
	// separately.
	Message string `json:"message"`
}

// vaultDirtMessage renders the alert line.
//
// It deliberately does NOT inline the paths. They are already in the structured
// SamplePaths field, which sits ahead of the directive in declaration order and so
// survives a host cut that the directive absorbs — repeating them here would
// spend the bounded prefix twice on one fact. What the prose carries instead is
// the part no field can express: what a reader must not conclude (that this is
// only about these files) and where to get the rest.
func vaultDirtMessage(count int) string {
	return fmt.Sprintf(
		"🔴 VAULT DIRT: %d uncommitted non-artifact file(s) — vp_vault_sync REFUSES before it commits "+
			"anything, so sessions, transcripts, drawers and KG triples are blocked too, not just these "+
			"files. See vault_dirt.paths; vp_vault_tidy dry_run lists them all.",
		count)
}

// vaultDirtMessageFor picks the alert line for the host's git_enabled, read
// through storage.HostGitEnabled (this is a reporting reader the gitEnabledOwner
// rule allow-lists; it refuses nothing).
//
// On a git-disabled host the task-write and harvest commits are skipped by
// design, so their files stay uncommitted permanently. Telling that session
// "vp_vault_sync REFUSES" would point it at a tool that refuses by design, and
// invite it to edit the operator's config to make the alert go away. It says
// instead that the dirt is expected and that no vp tool commits here. An
// unreadable setting says so, never "disabled".
//
// Both lines are static and no longer than vaultDirtMessage's, so the bootstrap
// payload ceiling, measured on the enabled line, stays the worst case.
func vaultDirtMessageFor(count int, gitEnabled bool, readErr error) string {
	switch {
	case readErr != nil:
		return fmt.Sprintf(
			"🔴 VAULT DIRT: %d uncommitted non-artifact file(s), and git_enabled could not be read from the "+
				"host config, so every vp vault git operation refuses until it can. vp check names the cause.",
			count)
	case !gitEnabled:
		return fmt.Sprintf(
			"VAULT DIRT (expected): %d uncommitted file(s). git_enabled = false on this host, so vp commits "+
				"nothing here and no vp tool will commit them. See vault_dirt.paths.",
			count)
	}
	return vaultDirtMessage(count)
}

// computeVaultDirt scans the whole vault worktree and returns the alert, or nil
// when the vault is clean.
//
// 🔴 WHOLE VAULT, NOT THIS PROJECT. A project-scoped pathspec would be cheaper
// and would be wrong: the specimen that motivated this task
// (Projects/dotfiles/tasks/fetch-bins-install-bin-version-blind-root-cause.md)
// belonged to a SIBLING project, was reported at one session's start and again at
// its end unchanged, and was cleared only incidentally by a session of the other
// project. internal/wrapstate/gitprobe.go is project-scoped and would have missed
// it by construction. The refusal this alert predicts is whole-vault, so the scan
// has to be too.
//
// 🔴 BEST-EFFORT: A SCAN THAT FAILS OR TIMES OUT IS SILENT. It returns nil, and
// the bootstrap is never failed by it. Raising an alert for "I could not check"
// would fire on a condition the reader cannot act on and would put a
// non-silent-when-healthy line on the payload the moment git is slow; a vp fault
// that matters is already carried by the health alert.
func computeVaultDirt(vaultRoot string) *VaultDirt {
	scan, err := storage.TidyScanWithTimeout(vaultRoot, vaultDirtScanBudget)
	if err != nil {
		return nil
	}
	dirt := scan.GenuineDirt()
	if len(dirt) == 0 {
		return nil
	}
	sample := dirt
	if len(sample) > vaultDirtSampleN {
		sample = sample[:vaultDirtSampleN]
	}
	enabled, readErr := storage.HostGitEnabled()
	return &VaultDirt{
		Count:       len(dirt),
		SamplePaths: append([]string(nil), sample...),
		Message:     vaultDirtMessageFor(len(dirt), enabled, readErr),
	}
}
