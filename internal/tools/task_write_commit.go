// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"fmt"
	"path"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Commit outcome codes reported back on a vp_manage_task result. They describe
// what happened to the COMMIT, never to the write — the write already succeeded
// by the time any of these can be produced.
const (
	// taskCommitCommitted: a commit was created carrying this task's file.
	taskCommitCommitted = "committed"

	// taskCommitNoChange: nothing at the task's paths was dirty, so there was
	// nothing to commit. Two benign causes, and the caller does not need to tell
	// them apart: the write produced byte-identical content (a re-run amend
	// converges), or the vault is not a git repository at all
	// (HasUncommittedChanges reports a non-repo as clean, deliberately).
	taskCommitNoChange = "no_change"

	// taskCommitFailed: the write LANDED and the commit did not. See the
	// contract note on commitTaskWrite for why this is not an error return.
	taskCommitFailed = "failed"
)

// commitTaskWrite commits the file a typed task write just produced.
//
// This is the enforcement half of the ruling recorded in the task
// `task-writes-leave-the-vault-dirty-with-no-sweeper` (Operator decision,
// 2026-09-06): the exposure window closes AT THE WRITER, instead of waiting for
// a sweeper that nothing schedules. `vp_vault_tidy` classifies a task file as
// reported-never-swept and is correct to — it cannot tell a vp-written task file
// from one a human edited in Obsidian, because the classifier is stdlib-only
// path matching over a status code and no provenance record exists anywhere in
// the tree to give it one. The writer, by contrast, KNOWS it wrote the file. So
// the commit belongs here and nowhere else.
//
// 🔴 WHAT THIS ACTUALLY BUYS IS NOT THE TASK FILE. SyncVault refuses on genuine
// dirt at step 2 and returns BEFORE the capture-artifact commit at step 3
// (internal/storage/vaultsyncflow.go:96-103), so ONE uncommitted task file wedges
// the sync of session summaries, transcripts, drawers and knowledge-graph triples
// as well. Committing at the writer is what keeps a task mutation from blocking
// every other durability path in the vault.
//
// 🔴 IT COMMITS LOCALLY AND NEVER PUSHES. push=false means CommitAndPushPaths
// enumerates no remotes and performs no network I/O at all, which is what makes
// this safe to run inside a tool call: a push here would put a fetch, a possible
// rebase and N remote round-trips on the latency of every task mutation, and
// would introduce a strand path (commit lands, push fails) into a tool whose job
// is to write a markdown file. The local commit is sufficient for the harm being
// closed — it is the UNCOMMITTED state that refuses the sync, and once the file
// is committed the next vp_vault_sync carries it to the remotes along with
// everything else it was previously blocked from sending.
//
// 🔴 THE VAULT_WRITE_FUNNEL RULING DOES NOT BAR THIS, AND THE POINT IS NARROW.
// internal/sourceaudit/vault_write_funnel.go is a SOURCE-AUDIT LINT RULE about
// writers routing through the lock-mediated per-path sink, and it exempts the
// git channel from having to — a merge is N files in one operation and no
// per-path primitive can model it. It says nothing about whether a caller may
// invoke git. The practical consequence is only that this coupling is not
// POLICED by that ratchet, which is why it is justified here in prose and pinned
// by a test instead. Do not route a git commit through atomicfile to "satisfy"
// the rule; that would be modelling a merge as a per-path write, which is the
// exact thing the exemption exists to refuse.
//
// # The contract on failure
//
// 🔴 A FAILED COMMIT IS NEVER AN ERROR RETURN, AND NOTHING IS ROLLED BACK.
//
// By the time this runs, the task file is on disk, fsync'd through
// atomicfile.Write, under its own vaultlock which has already been released.
// The write is DONE. Two things follow, and both are the opposite of the
// instinct:
//
//   - Reporting a commit failure as the handler's error would tell the caller
//     the WRITE failed, which is false, and both plausible reactions to that lie
//     are harmful. Retrying is wrong: `create` is a hard error on an existing
//     slug, so the retry returns "already exists" and the agent now holds two
//     contradictory failures for one successful write. Abandoning is worse: the
//     content is on disk but the agent believes it is not, so it never syncs it
//     and may re-derive it differently — the write survives and the WORK is
//     still lost.
//   - Undoing the write to "restore consistency" would delete the thing this
//     whole change exists to make durable. There is no state to roll back TO for
//     a create (the file did not exist), and for an amend the prior bytes are
//     already gone. Losing the write is the one outcome that is strictly worse
//     than uncommitted dirt.
//
// So the write is reported as the success it is, and the commit outcome rides
// beside it as data the caller can read and act on: taskCommitFailed plus the
// git error and the command that finishes the job. The file stays dirty, which
// means the DETECTION half of the same ruling — the bootstrap dirt alert, see
// vault_dirt.go — raises it at the next session start. The two halves compose
// here on purpose: option 4 is the backstop for option 2's failures, not only
// for the writes option 2 cannot reach.
//
// # Path scoping
//
// The commit is scoped to the THREE paths a single task slug can occupy —
// tasks/<slug>.md, tasks/done/<slug>.md, tasks/cancelled/<slug>.md — and never
// to a directory, never to `git add -A`. Three explicit files rather than one
// resolved file because retire and cancel MOVE the file: the source deletion and
// the destination creation are both part of the write, and staging only one of
// them would commit half a rename. CommitAndPushPaths drops the paths that match
// nothing in either the worktree or the index, so the two that do not exist cost
// a filter pass and nothing else.
//
// Projects/<project>/.surface joins the list only when a commit is already
// happening. atomicfile.Write stamps it as a side effect of this very write
// (surface.StampForPath), so it is genuinely a path this write touched — but it
// is also stamped by every OTHER vault write in the process, so making it part
// of the DIRTINESS PROBE would let an unrelated write trigger a commit labelled
// "amend task X". Same shape as the memory harvest's commit path list.
func commitTaskWrite(vault *storage.Vault, project, taskSlug, action string) map[string]string {
	base := path.Join("Projects", project, "tasks")
	paths := []string{
		path.Join(base, taskSlug+".md"),
		path.Join(base, "done", taskSlug+".md"),
		path.Join(base, "cancelled", taskSlug+".md"),
	}

	// Probe the task's own paths only. A non-git vault reports clean here rather
	// than erroring, which is what makes this a silent no-op on the vaults that
	// have no repo behind them.
	dirty, err := storage.HasUncommittedChanges(vault.Root, paths...)
	if err != nil {
		return taskCommitResult(taskCommitFailed, "", fmt.Sprintf(
			"could not check whether the write left the vault dirty: %v — the task file IS written; "+
				"run vp_vault_sync (or `vp vault tidy`) to inspect and commit it", err))
	}
	if !dirty {
		return taskCommitResult(taskCommitNoChange, "", "")
	}

	paths = append(paths, path.Join("Projects", project, ".surface"))

	res, err := storage.CommitAndPushPaths(vault.Root, taskCommitMessage(action, project, taskSlug), paths, false)
	if err != nil {
		return taskCommitResult(taskCommitFailed, "", fmt.Sprintf(
			"%v — THE TASK FILE IS WRITTEN AND IS SAFE ON DISK; only the commit failed. "+
				"Do not re-send the write. Fix the cause (a missing git identity is the usual one) and "+
				"run vp_vault_sync, which will pick the file up. Until it is committed, vp_vault_sync "+
				"REFUSES, so sessions, transcripts, drawers and KG triples cannot be saved either", err))
	}
	sha := ""
	if res != nil {
		sha = res.CommitSHA
	}
	return taskCommitResult(taskCommitCommitted, sha, "")
}

// taskCommitMessage renders the commit subject.
//
// It names the ACTION and the exact task, and claims nothing it cannot check —
// contrast tidy's "vault tidy: sweep N capture artifacts", which would become a
// false assertion of machine provenance if it were pointed at task paths. Here
// the assertion is true by construction: this function runs only on the far side
// of a vp_manage_task write that just succeeded.
func taskCommitMessage(action, project, taskSlug string) string {
	return fmt.Sprintf("vault: %s task %s/%s", action, project, taskSlug)
}

// taskCommitResult builds the fields merged onto a vp_manage_task result.
//
// The keys are additive: `status` and `task` keep meaning exactly what they meant
// before, so a caller that reads only those is unaffected. `commit` is always
// present on a mutating action, so its ABSENCE is never ambiguous with a healthy
// commit — the reverse of the silent-when-healthy rule that governs the bootstrap
// alerts, and deliberately so: this is a per-call receipt for an operation the
// caller asked for, not a background condition it did not.
func taskCommitResult(state, sha, detail string) map[string]string {
	out := map[string]string{"commit": state}
	if sha != "" {
		out["commit_sha"] = sha
	}
	if detail != "" {
		out["commit_error"] = detail
	}
	return out
}

// taskWriteResult is the ONE seam every mutating vp_manage_task action returns
// through: it commits the write and folds the receipt onto the action's own
// result map.
//
// 🔴 IT IS A SEAM, NOT A CONVENIENCE. Eight actions mutate a task file and every
// one of them must commit, so a per-case call to commitTaskWrite would be eight
// places for the ninth action to forget. Routing every mutating return through
// one function is what makes "a typed task write commits itself" a property of
// the handler rather than a habit of whoever last edited it —
// TestEveryMutatingTaskActionCommits pins that no action escapes it.
//
// The result map is map[string]any because set_relations already returned one
// (its depends_on value is a slice); the other actions widen from
// map[string]string, which changes no JSON on the wire.
func taskWriteResult(vault *storage.Vault, project, taskSlug, action string, result map[string]any) map[string]any {
	for k, v := range commitTaskWrite(vault, project, taskSlug, action) {
		result[k] = v
	}
	return result
}
