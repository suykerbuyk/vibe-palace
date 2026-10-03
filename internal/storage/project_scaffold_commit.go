// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// ScaffoldCommit is what CommitProjectScaffold did.
type ScaffoldCommit struct {
	// Committed is true only when a commit was made; SHA is its hash.
	Committed bool
	SHA       string
	// Paths are the vault-relative paths the commit was asked to stage: the
	// eligible markers, plus Projects/<project>/.surface when IncludeStamp is
	// set and it exists. On a commit error they are the paths that are still
	// uncommitted, so a caller can print the exact command that finishes the job.
	Paths []string
	// Kept are dirty scaffold markers whose worktree bytes are NOT the current
	// stub — an operator's edit, an older binary's stub text, a deletion. They
	// are never committed here: vp does not vouch for bytes it did not write.
	Kept []string
	// Skipped, when non-empty, is why no commit was attempted although git is
	// enabled: the vault is not its own git repository (InspectVaultGit is not
	// VaultGitOK), or RequireTrackedStamp found the project's .surface
	// untracked. It is a reason to show, never a failure.
	Skipped string
}

// ScaffoldCommitOptions selects which caller's rule CommitProjectScaffold
// applies. The zero value is the strictest: markers only, any project.
type ScaffoldCommitOptions struct {
	// IncludeStamp adds Projects/<project>/.surface to a commit that is already
	// happening. Only `vp init` sets it: running init is the deliberate act that
	// makes a slug a project, so init may commit the stamp that, untracked,
	// marks a project nobody initialised.
	IncludeStamp bool
	// RequireTrackedStamp commits nothing unless Projects/<project>/.surface is
	// already tracked — i.e. the project was deliberately initialised before.
	// `vp config sync` sets it: it scaffolds every WithContent directory,
	// including a stray that hook capture created in a slug nobody initialised
	// (sessions/ plus an untracked .surface). Committing anything for that
	// stray would end its review: tidy sweeps its sessions once its stamp is
	// tracked, and the next sync pushes the whole project unreviewed.
	RequireTrackedStamp bool
	// Wrote reports that the caller's scaffold apply created or repaired a
	// marker on THIS run. It decides only whether a vault-wide reason not to
	// commit (the vault is not its own git repository) is returned in Skipped:
	// without a write there is nothing that reason explains, and a converged
	// `vp init` or `vp config sync` would otherwise print it on every run.
	Wrote bool
}

// CommitProjectScaffold commits the project scaffold vp itself laid down for
// project — the projectScaffoldMarkers READMEs, and with opts.IncludeStamp the
// project's .surface — as ONE local, path-scoped commit through
// CommitAndPushPaths (push=false).
//
// # Why this exists
//
// The scaffold markers match no tidy sweep rule, and a brand-new project's
// .surface is untracked, which tidy's status gate reports rather than sweeps
// (vaulttidy.go). Reported dirt makes SyncVault refuse. So a `vp init` of a
// project into an existing vault that wrote the scaffold and committed nothing
// left the vault unable to sync until someone hand-committed three files they
// never wrote. The act that wrote them was deliberate, so that act commits them.
// (The vault-level files init's vault step writes — .gitignore and
// .vibe-palace/vault.toml — are CommitVaultInit's business.)
//
// # What it will commit — verified on bytes, never on a run's memory
//
// A marker is committed only when it is dirty (untracked or modified) AND its
// worktree bytes equal templates.RenderReadmeStub for its kind. Bytes, not "the
// files this run created": the reconciler reports counts, not paths, and bytes
// also cover a concurrent writer's identical stub and a re-run over a scaffold
// an earlier binary left uncommitted. A dirty marker with any other bytes is
// returned in Kept and left exactly as it is.
//
// .surface joins the commit only when IncludeStamp is set AND a marker is
// committing. It is stamped by EVERY vault write in the process, so letting a
// dirty stamp alone trigger a commit would label an unrelated write's churn as
// a scaffold (the same reason commitTaskWrite keeps it out of its dirtiness
// probe).
//
// # Where it does nothing
//
// git_enabled = false returns RefuseIfGitDisabled's error before any git runs;
// callers map ErrGitDisabled and ErrGitConfigUnreadable to a skipped commit.
// A vault that is not its own repository — no repository, git unavailable or
// unusable, or a directory inside ANOTHER repository's work tree, which vp
// must never stage into — commits nothing and returns no error; Skipped names
// why only when opts.Wrote says this run scaffolded something.
//
// # The check-then-commit window
//
// The dirty probes and the byte comparison run BEFORE CommitAndPushPaths takes
// the vault commit lock, and the lock is not taken here first: CommitAndPushPaths
// acquires it itself and the lock does not nest. So a writer that replaces a
// marker's bytes between the comparison and the stage would have its bytes
// committed under this function's message. The window is the few git calls
// between the two; the only writers of these paths are the scaffold itself and
// an operator's hand edit, and the commit is local and revertible.
func CommitProjectScaffold(vaultRoot, project string, opts ScaffoldCommitOptions) (ScaffoldCommit, error) {
	// Built in locals and returned as keyed literals, never through
	// `result.Paths = ...`: sourceaudit's write-only-field gate keys a selector
	// assignment by bare field name, so one would mark every `Paths` in the tree
	// assigned and mask skills.SkillFrontmatter.Paths (sourceaudit.go, "The
	// qualified half exists because the bare half MASKED A REAL FINDING").
	var eligible, kept []string
	if err := slug.Validate(project); err != nil {
		return ScaffoldCommit{}, err
	}
	if err := RefuseIfGitDisabled(vaultRoot, "commit the project scaffold"); err != nil {
		return ScaffoldCommit{}, err
	}
	if reason := vaultGitSkipReason(vaultRoot); reason != "" {
		if !opts.Wrote {
			return ScaffoldCommit{}, nil
		}
		return ScaffoldCommit{Skipped: reason}, nil
	}

	base := path.Join("Projects", project)
	for _, marker := range projectScaffoldMarkers {
		rel := path.Join(base, marker)
		dirty, err := HasUncommittedChanges(vaultRoot, rel)
		if err != nil {
			return ScaffoldCommit{Kept: kept}, err
		}
		if !dirty {
			continue
		}
		// git collapses an untracked directory to one porcelain line, so a
		// marker that does not exist inside one still probes dirty. Absent and
		// never tracked is nothing to commit and nothing to keep; absent and
		// tracked is a deletion, which is not vp's stub and goes to Kept.
		if _, err := os.Lstat(filepath.Join(vaultRoot, filepath.FromSlash(rel))); errors.Is(err, fs.ErrNotExist) {
			tracked, terr := GitPathIsTracked(vaultRoot, rel)
			if terr != nil {
				return ScaffoldCommit{Kept: kept}, terr
			}
			if !tracked {
				continue
			}
		}
		ok, err := isCurrentScaffoldStub(vaultRoot, rel)
		if err != nil {
			return ScaffoldCommit{Kept: kept}, err
		}
		if ok {
			eligible = append(eligible, rel)
		} else {
			kept = append(kept, rel)
		}
	}
	if len(eligible) == 0 {
		return ScaffoldCommit{Kept: kept}, nil
	}

	stamp := path.Join(base, ".surface")
	if opts.RequireTrackedStamp {
		tracked, err := GitPathIsTracked(vaultRoot, stamp)
		if err != nil {
			return ScaffoldCommit{Kept: kept}, err
		}
		if !tracked {
			return ScaffoldCommit{Kept: kept, Skipped: stamp + " is not tracked, so its scaffold is left uncommitted for review; " +
				"run `vp init` in this project's checkout to adopt its scaffold"}, nil
		}
	}

	paths := eligible
	if opts.IncludeStamp {
		if _, err := os.Lstat(filepath.Join(vaultRoot, filepath.FromSlash(stamp))); err == nil {
			paths = append(paths, stamp)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return ScaffoldCommit{Paths: paths, Kept: kept}, fmt.Errorf("inspect %s: %w", stamp, err)
		}
	}

	res, err := CommitAndPushPaths(vaultRoot, fmt.Sprintf("Scaffold Projects/%s/{commands,skills}", project), paths, false)
	if err != nil {
		return ScaffoldCommit{Paths: paths, Kept: kept}, err
	}
	var sha string
	if res != nil {
		sha = res.CommitSHA
	}
	return ScaffoldCommit{Committed: sha != "", SHA: sha, Paths: paths, Kept: kept}, nil
}

// Vault-relative paths of the vault-level files init's vault step writes.
const (
	vaultGitignoreRel   = ".gitignore"
	vaultFormatStampRel = ".vibe-palace/vault.toml"
)

// VaultInitCommitOptions carries what the vault step's plan knows about THIS
// run into CommitVaultInit.
type VaultInitCommitOptions struct {
	// CreatedVault is true only when this run's Vault plan created the vault
	// directory — the one case in which it stamps .vibe-palace/vault.toml
	// (reconcile.VaultReconciler's born-current stamp). It is the gate that
	// keeps the stamp out of every other commit: see CommitVaultInit.
	CreatedVault bool
	// Wrote reports that the vault step created or changed a file on THIS run.
	// As in ScaffoldCommitOptions, it decides only whether a vault-wide reason
	// not to commit is returned in Skipped.
	Wrote bool
}

// CommitVaultInit commits the vault-level files `vp init`'s vault step wrote —
// the vault .gitignore (created, or topped up with canonical lines) and, on a
// vault this run created, the .vibe-palace/vault.toml data-format stamp — as
// ONE local, path-scoped commit through CommitAndPushPaths (push=false). It is
// CommitProjectScaffold's sibling for the vault step, with the same gates, the
// same result type and the same rule: vp commits only bytes it can prove it
// wrote.
//
// # Why this exists
//
// Neither path matches a tidy sweep rule, so an uncommitted one is Reported and
// SyncVault refuses. A first `vp init` that created a git vault, or that topped
// up a cloned vault's .gitignore, therefore left a vault that could not sync
// until someone hand-committed files vp wrote.
//
// # What it will commit — verified on bytes
//
// .gitignore is committed only when it is dirty AND its bytes equal
// appendMissingGitignoreLines(<HEAD's .gitignore, or empty when HEAD has
// none>, CanonicalGitignorePatterns) — the function that produced them, so one
// rule covers both the create and the top-up. Any other edit (an operator's
// uncommitted change made before init ran) puts it in Kept, untouched.
//
// .vibe-palace/vault.toml is committed only when ALL of: opts.CreatedVault,
// the file is untracked, and its bytes are exactly
// surface.FormatManifestBytes(surface.RequiredDataFormat). A vault format
// advance commits its stamp WITH the data it describes; on a vault this run
// created there is no data, so the stamp is trivially consistent with it. A
// format bump stamps an existing, tracked file in a vault it did not create,
// so it can never pass these gates — this is not a stamp-only commit path a
// migration could reuse. A dirty stamp that fails a gate goes to Kept.
//
// # Where it does nothing
//
// Exactly CommitProjectScaffold's: git_enabled = false returns
// RefuseIfGitDisabled's error; a vault that is not its own repository (none,
// git unusable, nested inside another work tree) commits nothing and names why
// in Skipped only when opts.Wrote is set. The check-then-commit window is the
// same, and so is its bound.
func CommitVaultInit(vaultRoot string, opts VaultInitCommitOptions) (ScaffoldCommit, error) {
	var eligible, kept []string
	if err := RefuseIfGitDisabled(vaultRoot, "commit the vault's .gitignore and data-format stamp"); err != nil {
		return ScaffoldCommit{}, err
	}
	if reason := vaultGitSkipReason(vaultRoot); reason != "" {
		if !opts.Wrote {
			return ScaffoldCommit{}, nil
		}
		return ScaffoldCommit{Skipped: reason}, nil
	}

	dirty, err := HasUncommittedChanges(vaultRoot, vaultGitignoreRel)
	if err != nil {
		return ScaffoldCommit{}, err
	}
	if dirty {
		ok, err := isVaultGitignoreTopUp(vaultRoot)
		if err != nil {
			return ScaffoldCommit{}, err
		}
		if ok {
			eligible = append(eligible, vaultGitignoreRel)
		} else {
			kept = append(kept, vaultGitignoreRel)
		}
	}

	dirty, err = HasUncommittedChanges(vaultRoot, vaultFormatStampRel)
	if err != nil {
		return ScaffoldCommit{Kept: kept}, err
	}
	if dirty {
		ok, err := isBornCurrentStamp(vaultRoot, opts.CreatedVault)
		if err != nil {
			return ScaffoldCommit{Kept: kept}, err
		}
		if ok {
			eligible = append(eligible, vaultFormatStampRel)
		} else {
			kept = append(kept, vaultFormatStampRel)
		}
	}
	if len(eligible) == 0 {
		return ScaffoldCommit{Kept: kept}, nil
	}

	res, err := CommitAndPushPaths(vaultRoot, vaultInitCommitMessage(vaultRoot, eligible), eligible, false)
	if err != nil {
		return ScaffoldCommit{Paths: eligible, Kept: kept}, err
	}
	var sha string
	if res != nil {
		sha = res.CommitSHA
	}
	return ScaffoldCommit{Committed: sha != "", SHA: sha, Paths: eligible, Kept: kept}, nil
}

// vaultInitCommitMessage names a CommitVaultInit commit by what it holds. The
// .gitignore half is a CREATE when HEAD has no .gitignore (a new vault, or a
// pre-created empty directory init turned into one — which gets no stamp) and
// a TOP-UP when HEAD has one; it is derived from HEAD, never from whether the
// stamp is travelling with it.
func vaultInitCommitMessage(vaultRoot string, paths []string) string {
	var gitignore, stamp bool
	for _, p := range paths {
		switch p {
		case vaultGitignoreRel:
			gitignore = true
		case vaultFormatStampRel:
			stamp = true
		}
	}
	verb := "Top up"
	if _, err := gitCmd(vaultRoot, 10*time.Second, "rev-parse", "--verify", "--quiet", "HEAD:"+vaultGitignoreRel); err != nil {
		verb = "Create"
	}
	switch {
	case gitignore && stamp:
		return verb + " the vault .gitignore and record its data-format stamp"
	case stamp:
		return "Record the new vault's data-format stamp"
	default:
		return verb + " the vault .gitignore with vp's canonical lines"
	}
}

// isVaultGitignoreTopUp reports whether the vault .gitignore on disk is a
// regular file holding exactly HEAD's .gitignore (empty when HEAD has none,
// including an unborn HEAD) with vp's missing canonical lines appended.
func isVaultGitignoreTopUp(vaultRoot string) (bool, error) {
	got, ok, err := readRegularVaultFile(vaultRoot, vaultGitignoreRel)
	if err != nil || !ok {
		return false, err
	}
	base, err := headBlob(vaultRoot, vaultGitignoreRel)
	if err != nil {
		return false, err
	}
	want, _ := appendMissingGitignoreLines(base, CanonicalGitignorePatterns)
	return bytes.Equal(got, want), nil
}

// isBornCurrentStamp reports whether .vibe-palace/vault.toml is the stamp a
// vault this run created was born with: createdVault, untracked, and exactly
// the RequiredDataFormat manifest bytes.
func isBornCurrentStamp(vaultRoot string, createdVault bool) (bool, error) {
	if !createdVault {
		return false, nil
	}
	tracked, err := GitPathIsTracked(vaultRoot, vaultFormatStampRel)
	if err != nil || tracked {
		return false, err
	}
	got, ok, err := readRegularVaultFile(vaultRoot, vaultFormatStampRel)
	if err != nil || !ok {
		return false, err
	}
	want, err := surface.FormatManifestBytes(surface.RequiredDataFormat)
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, want), nil
}

// readRegularVaultFile reads rel when it is a regular file. ok is false for a
// missing file, a symlink or any other non-regular entry; any other inspection
// error is returned.
func readRegularVaultFile(vaultRoot, rel string) (data []byte, ok bool, err error) {
	full := filepath.Join(vaultRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err = os.ReadFile(full)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", rel, err)
	}
	return data, true, nil
}

// headBlob returns rel's exact bytes at HEAD, or nil when HEAD has no such
// path or is unborn. Raw stdout, never gitCmd's trimmed output: the caller
// compares bytes.
func headBlob(vaultRoot, rel string) ([]byte, error) {
	if _, err := gitCmd(vaultRoot, 10*time.Second, "rev-parse", "--verify", "--quiet", "HEAD:"+rel); err != nil {
		return nil, nil
	}
	cmd := exec.Command("git", "-C", vaultRoot, "cat-file", "blob", "HEAD:"+rel)
	cmd.Env = SafeGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read HEAD:%s: %w", rel, err)
	}
	return out, nil
}

// vaultGitSkipReason is "" when the vault is the top level of its own git work
// tree, and otherwise why a scaffold commit is not attempted there.
func vaultGitSkipReason(vaultRoot string) string {
	state, err := InspectVaultGit(vaultRoot)
	switch state {
	case VaultGitOK:
		return ""
	case VaultNotGit:
		return "the vault is not a git repository"
	case VaultGitUnavailable:
		return "git is not on PATH, so the vault's repository cannot be used"
	case VaultGitNested:
		return "the vault is a directory inside another repository's work tree, which vp never commits into"
	default:
		return fmt.Sprintf("git cannot use the vault's repository (%v)", err)
	}
}

// ScaffoldCommitRemedy says how to finish a scaffold commit that failed: re-run
// `vp init` (it commits a scaffold an earlier run left uncommitted), or commit
// the paths by hand in the CLI or MCP spelling. A missing committer identity is
// named first, because nothing commits until one is set.
func ScaffoldCommitRemedy(vaultRoot, project string, paths []string) string {
	return commitRemedy(vaultRoot, fmt.Sprintf("Scaffold Projects/%s/{commands,skills}", project), paths,
		"re-run `vp init` in the project, or ")
}

// VaultInitCommitRemedy says how to finish a CommitVaultInit commit that
// failed. It never suggests re-running `vp init`: once the global config
// exists, init no longer runs its vault step, so a re-run would not commit
// these paths.
func VaultInitCommitRemedy(vaultRoot string, paths []string) string {
	return commitRemedy(vaultRoot, vaultInitCommitMessage(vaultRoot, paths), paths, "")
}

// commitRemedy is the remedy text both init commits share: the hand commit in
// its CLI and MCP spellings, after rerun, with a missing committer identity
// named first.
func commitRemedy(vaultRoot, msg string, paths []string, rerun string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	remedy := fmt.Sprintf("%scommit them with `vp vault commit --paths %s --message '%s'` "+
		"or vp_vault_sync {action: \"sync\", paths: [%s], message: %q}",
		rerun, strings.Join(paths, ","), msg, strings.Join(quoted, ", "), msg)
	if CheckCommitIdentity(vaultRoot) != nil {
		remedy = fmt.Sprintf("set a git committer identity — `git -C %s config user.name <name>` and "+
			"`git -C %s config user.email <addr>` (or `git config --global` for every repository) — then %s",
			vaultRoot, vaultRoot, remedy)
	}
	return remedy
}

// isCurrentScaffoldStub reports whether rel is a regular file holding exactly
// the current stub for its kind (the marker's parent directory name). A missing
// file, a symlink or any other non-regular entry is not the stub; any other
// inspection error is returned, never read as "not the stub".
func isCurrentScaffoldStub(vaultRoot, rel string) (bool, error) {
	full := filepath.Join(vaultRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	want := templates.RenderReadmeStub(path.Base(path.Dir(rel)))
	if want == "" {
		return false, nil
	}
	got, err := os.ReadFile(full)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", rel, err)
	}
	return bytes.Equal(got, []byte(want)), nil
}
