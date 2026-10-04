// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// Slice 2 of vp_vault_split: the three actions that touch a filesystem —
// `apply` (scaffold the destination and copy), `verify` (prove the destination
// holds exactly the manifest and nothing else) and `purge` (remove the source
// trees once verify has passed). `plan`, which mints the manifest they all bind
// to, is in vault_split.go and is unchanged by this file.
//
// 🔴 THE MANIFEST IS NEVER CARRIED BY THE CALLER, ONLY ITS DIGEST. Every action
// here re-derives the manifest from the SOURCE by calling buildSplitManifest
// again, and refuses unless the digest it computes equals the manifest_sha256
// the caller passed. That is the TOCTOU bind: a source that changed between
// plan and apply produces a different digest and the call refuses, rather than
// copying bytes an operator never approved. It is also why the payload can stay
// small — the rows are re-derivable, so nobody has to ship them across the wire
// and back.
//
// 🔴 apply NEVER MUTATES THE SOURCE. Removal is `purge`, a separate action with
// its own manifest bind and its own precondition (a passing verify against the
// destination). The two-step shape is operator decision 7: a split that deleted
// as it copied would have no state in which the operator could look at both
// halves before committing to one.

// splitDestRootAllowed is the set of top-level names a freshly split
// destination may contain, before the include_* flags widen it.
//
// It is the ROOT half of leak gate 1: the per-tree half (below) proves that
// palace/ and Projects/ hold only allow-listed slugs, and this proves that
// nothing arrived ALONGSIDE them. A stray `.obsidian`, a copied `Knowledge`, a
// loose file at the destination root — each is a leak the per-tree gate cannot
// see, because it never looks outside the two trees it walks.
//
// Every entry is something the destination recipe itself creates:
//
//   - .git, .gitignore   — reconcile.NewVault's git init and gitignore
//   - .vibe-palace       — the data-format stamp (vault.toml)
//   - Templates          — tolerated, never created: under Design B the
//     override-only reconcile writes no template, so a fresh destination has
//     no Templates/ directory at all and the embedded floor serves
//   - palace, Projects   — the two trees the copy writes into
//   - .surface           — atomicfile's own stamp, if a write resolves the
//     destination root as its stamp directory
//   - .vp-locks          — vaultlock's per-path sidecar dir, created
//     unconditionally by vaultlock.Acquire (openLockFile's own
//     os.MkdirAll) the moment anything locks a path under this vault —
//     here, ReconcileVaultGitignore's own create-path lock, taken against
//     THIS destination since vault-gitignore-create-bypasses-the-vault-lock.
//     Host-local and gitignored (CanonicalGitignorePatterns), same class as
//     .vibe-palace and .surface above.
var splitDestRootAllowed = map[string]bool{
	".git":         true,
	".gitignore":   true,
	".vibe-palace": true,
	".surface":     true,
	".vp-locks":    true,
	"Templates":    true,
	"palace":       true,
	"Projects":     true,
}

// vaultSplitApplyResult is the payload for action "apply".
type vaultSplitApplyResult struct {
	Action         string   `json:"action"`
	Destination    string   `json:"destination"`
	Slugs          []string `json:"slugs"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	FilesCopied    int      `json:"files_copied"`
	BytesCopied    int64    `json:"bytes_copied"`
	DestFormat     int      `json:"dest_format"`
	Notes          []string `json:"notes"`
	Complete       bool     `json:"complete"`
}

// vaultSplitVerifyResult is the payload for action "verify".
type vaultSplitVerifyResult struct {
	Action         string   `json:"action"`
	Destination    string   `json:"destination"`
	Slugs          []string `json:"slugs"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	FilesVerified  int      `json:"files_verified"`
	BytesVerified  int64    `json:"bytes_verified"`
	DestFormat     int      `json:"dest_format"`
	DestRemotes    []string `json:"dest_remotes"`
	GatesChecked   []string `json:"gates_checked"`
	Notes          []string `json:"notes"`
	Complete       bool     `json:"complete"`
}

// vaultSplitPurgeResult is the payload for action "purge".
type vaultSplitPurgeResult struct {
	Action         string   `json:"action"`
	Destination    string   `json:"destination"`
	Slugs          []string `json:"slugs"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	FilesRemoved   int      `json:"files_removed"`
	DirsRemoved    int      `json:"dirs_removed"`
	BytesRemoved   int64    `json:"bytes_removed"`
	// DepartureRecords are the Audits/departures/<slug>.json records this
	// purge wrote, one per purged slug. On a vault that commits they are in the
	// purge commit with the removal (CommitSHA); committed alone they would
	// claim a move another host has not seen happen.
	DepartureRecords []string `json:"departure_records"`
	// CommitSHA is the purge commit, local and not pushed. Empty on a vault
	// that is not a git repository of its own, where nothing is committed.
	CommitSHA string `json:"commit_sha,omitempty"`
	// CleanupLeft lists the untracked paths the post-commit cleanup could not
	// remove, or that nobody collected. None is publishable; an "untracked"
	// one is a resurrection risk and is named in Notes.
	CleanupLeft []splitPurgeCleanupEntry `json:"cleanup_left,omitempty"`
	Notes       []string                 `json:"notes"`
	Complete    bool                     `json:"complete"`
}

// splitBindManifest is the shared precondition of apply, verify and purge: the
// caller must name a manifest_sha256, and the source must still hash to it.
//
// 🔴 THE EMPTY CHECK COMES FIRST, BEFORE ANY WALK. A call with no
// manifest_sha256 is refused on its own shape, not on the state of a vault it
// never had permission to read at this action. That ordering is what makes the
// refusal total: it holds for a format-0 source, an absent slug, a destination
// that could never be legal — every one of those still refuses for the missing
// bind rather than for whatever else is wrong.
func splitBindManifest(vault *storage.Vault, p vaultSplitParams) (*splitManifest, error) {
	if strings.TrimSpace(p.ManifestSHA256) == "" {
		return nil, apperr.Caller(fmt.Errorf(
			"manifest_sha256 is required for action %q: run action \"plan\" first and pass "+
				"the digest it returns. The manifest itself is server-side; the digest is "+
				"the whole of what binds this call to the plan an operator approved",
			p.Action))
	}

	// buildSplitManifest re-walks and re-hashes the source. That is the point:
	// it is the same derivation plan ran, so a source that changed in between
	// yields a different digest and the compare below refuses.
	m, err := buildSplitManifest(vault, p)
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(m.SHA256, strings.TrimSpace(p.ManifestSHA256)) {
		return nil, apperr.Caller(fmt.Errorf(
			"manifest_sha256 mismatch: the source vault now hashes to %s, not the %s this "+
				"call named. The source changed after the plan was taken, so the approval "+
				"does not describe what would be copied. Re-run action \"plan\"",
			m.SHA256, strings.TrimSpace(p.ManifestSHA256)))
	}
	return m, nil
}

// splitCheckDestination validates the destination path for an action that will
// touch it, and enforces the exists / does-not-exist rule that action needs.
//
// 🔴 apply REFUSES A DESTINATION THAT EXISTS AT ALL — not one that "looks like a
// vault". VaultReconciler.Plan emits ActionCreate only when the directory is
// missing (vault.go:120-128), and the data-format stamp is written only inside
// that branch (:186-198). An empty directory an operator pre-created with
// `mkdir -p` therefore yields ActionUnchanged and a destination born at format
// 0 — which then receives current-format bytes and reports itself as
// unmigrated. The older, more permissive guard (refuse only if the destination
// already holds Projects/ or palace/) does not catch that case at all.
func splitCheckDestination(vaultRoot, dest string, mustExist bool) error {
	if strings.TrimSpace(dest) == "" {
		return apperr.Caller(fmt.Errorf("destination is required"))
	}
	// A relative destination resolves against the SERVER's working directory,
	// which no caller can see and which has nothing to do with either vault.
	// The schema has always said "absolute host path"; this is that sentence
	// becoming a refusal.
	if !filepath.IsAbs(dest) {
		return apperr.Caller(fmt.Errorf(
			"destination %q is not an absolute path: it would resolve against the server's "+
				"working directory, which the caller cannot see", dest))
	}
	if err := vaultfs.RefuseDestinationInsideVault(vaultRoot, dest); err != nil {
		return apperr.Caller(err)
	}

	_, err := os.Stat(dest)
	switch {
	case mustExist:
		if err != nil {
			return apperr.Caller(fmt.Errorf(
				"destination %q is not readable: %w (run action \"apply\" first)", dest, err))
		}
		return nil
	case err == nil:
		return apperr.Caller(fmt.Errorf(
			"destination %q already exists: split refuses to reuse it. A destination that "+
				"already exists is not created by the reconciler, so it is never stamped "+
				"with the vault data format and would be born format 0 while receiving "+
				"current-format bytes. Remove the host path, or choose another destination",
			dest))
	case errors.Is(err, os.ErrNotExist):
		return splitRefuseNestedDestination(dest)
	default:
		// Anything other than "absent" — a permission error, a broken symlink
		// component — leaves us unable to say the destination is absent, and
		// that is exactly the state in which creating it is unsafe.
		return apperr.Caller(fmt.Errorf("stat destination %q: %w", dest, err))
	}
}

// splitRefuseNestedDestination refuses a not-yet-existing destination that
// would be created inside another git work tree.
//
// 🔴 THE VAULT RECONCILER SKIPS GIT INIT FOR A NESTED VAULT, and rightly so for
// config sync (tasks/done/config-sync-git-inits-a-vault-nested-in-another-
// repository). For a split that skip is silent: the destination gets no
// repository of its own, and verify's remote check then answers for the
// enclosing one. So apply refuses first, using the same predicate,
// storage.InspectVaultGit.
//
// The destination does not exist yet, and InspectVaultGit runs git inside the
// path it is given, so it is asked about the nearest EXISTING ancestor. A
// repository there, at its top level or below it, is a repository the
// destination would be inside. A state git cannot resolve refuses too: the
// scaffold needs a working git anyway, and "cannot tell" is not "not nested".
func splitRefuseNestedDestination(dest string) error {
	anc := filepath.Dir(filepath.Clean(dest))
	for {
		if _, err := os.Stat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			return nil
		}
		anc = parent
	}
	state, gerr := storage.InspectVaultGit(anc)
	switch state {
	case storage.VaultNotGit:
		return nil
	case storage.VaultGitOK, storage.VaultGitNested:
		top, err := storage.GitTopLevel(anc)
		if err != nil || top == "" {
			top = anc
		}
		return apperr.Caller(fmt.Errorf(
			"destination %q is inside the git repository at %q: the destination would not "+
				"get a repository of its own (the vault reconciler skips git init for a nested "+
				"vault), so verify's remote check would answer for %q and a published split "+
				"could land in the wrong repository. Choose a destination outside any git "+
				"work tree", dest, top, top))
	default:
		reason := "git is unavailable"
		if gerr != nil {
			reason = gerr.Error()
		}
		return apperr.Caller(fmt.Errorf(
			"cannot tell whether destination %q would be inside a git repository (%s, "+
				"inspecting %q): split refuses rather than risk a destination without a "+
				"repository of its own", dest, reason, anc))
	}
}

// vaultSplitApply scaffolds a NEW destination vault and copies the manifest
// into it. It writes nothing to the source.
func vaultSplitApply(ctx context.Context, vault *storage.Vault, p vaultSplitParams) (*vaultSplitApplyResult, error) {
	// Order is deliberate: the bind is checked on shape (cheap, total), then
	// the destination (cheap, and refuses a call that could never succeed
	// before hashing a vault for it), then the manifest itself (expensive), and
	// only then is anything created.
	if strings.TrimSpace(p.ManifestSHA256) == "" {
		return nil, apperr.Caller(fmt.Errorf(
			"manifest_sha256 is required for action %q: run action \"plan\" first and pass "+
				"the digest it returns", p.Action))
	}
	if err := splitCheckDestination(vault.Root, p.Destination, false); err != nil {
		return nil, err
	}

	// buildSplitManifest, inside the bind, is where the source data format is
	// checked — before any inventory and therefore before any copy. A format-0
	// source holds triple files in the old encoding; copying them and stamping
	// the destination format 1 would produce a vault that reports itself
	// current while its KG accessors silently undercount.
	m, err := splitBindManifest(vault, p)
	if err != nil {
		return nil, err
	}

	dest := p.Destination
	if err := splitScaffoldDestination(ctx, dest); err != nil {
		return nil, err
	}

	// The scaffold's whole job was to make this true. Asserting it HERE rather
	// than in verify is the difference between refusing to copy into an
	// unstamped vault and discovering afterwards that we already did.
	destFormat, err := surface.ReadFormat(dest)
	if err != nil {
		return nil, fmt.Errorf("read destination vault format: %w", err)
	}
	if destFormat != surface.RequiredDataFormat {
		return nil, fmt.Errorf(
			"destination was scaffolded at data format %d, required %d: refusing to copy "+
				"current-format data into a vault that reports itself unmigrated",
			destFormat, surface.RequiredDataFormat)
	}

	var files int
	var bytes int64
	for _, e := range m.Entries {
		if err := splitCopyEntry(vault.Root, dest, e); err != nil {
			return nil, err
		}
		files++
		bytes += e.Size
	}

	return &vaultSplitApplyResult{
		Action:         "apply",
		Destination:    dest,
		Slugs:          m.Slugs,
		ManifestSHA256: m.SHA256,
		FilesCopied:    files,
		BytesCopied:    bytes,
		DestFormat:     destFormat,
		Notes: append(splitPlanNotes(m),
			"The source is untouched. Removal is action \"purge\", which re-binds this "+
				"manifest and refuses unless action \"verify\" passes against the destination.",
			"No remotes were configured and nothing was committed. Wire the destination's "+
				"remote by hand after verify.",
		),
		Complete: true,
	}, nil
}

// splitScaffoldDestination creates the destination vault through the ONLY
// scaffold that stamps the vault data format: reconcile.ScaffoldNewVault, which
// `vp vault init` shares. See it for why dest (not a working directory) is the
// argument and why any Report.Errors entry aborts before a byte is copied.
func splitScaffoldDestination(ctx context.Context, dest string) error {
	if err := reconcile.ScaffoldNewVault(ctx, dest); err != nil {
		return fmt.Errorf("%w (nothing was copied; remove the destination host path before retrying)", err)
	}
	return nil
}

// vaultSplitVerify proves the destination holds exactly the manifest and
// nothing else. It writes nothing.
func vaultSplitVerify(vault *storage.Vault, p vaultSplitParams) (*vaultSplitVerifyResult, error) {
	if err := splitCheckDestination(vault.Root, p.Destination, true); err != nil {
		return nil, err
	}
	m, err := splitBindManifest(vault, p)
	if err != nil {
		return nil, err
	}
	return splitVerifyDestination(vault, p, m)
}

// splitVerifyDestination is the whole of verify, factored out so purge can
// require it as a precondition rather than trusting that an operator ran it.
//
// Every gate below is fail-closed and every failure is COLLECTED rather than
// returned at the first one: an operator who has to fix three things learns
// about three things, not about the first one three times.
func splitVerifyDestination(vault *storage.Vault, p vaultSplitParams, m *splitManifest) (*vaultSplitVerifyResult, error) {
	dest := p.Destination
	var problems []string

	destFormat, err := surface.ReadFormat(dest)
	if err != nil {
		return nil, fmt.Errorf("read destination vault format: %w", err)
	}
	if destFormat != surface.RequiredDataFormat {
		problems = append(problems, fmt.Sprintf(
			"destination is at data format %d, required %d", destFormat, surface.RequiredDataFormat))
	}

	// --- Inventory: path and hash, both directions. -------------------------
	//
	// Both directions matter and they catch different failures. A manifest row
	// missing from the destination is an incomplete copy; a destination file
	// missing from the manifest is a leak, and it is the direction a
	// "did everything arrive?" check would never look in.
	destEntries, err := splitDestInventory(dest, p, m.Slugs)
	if err != nil {
		return nil, err
	}
	want := make(map[string]splitEntry, len(m.Entries))
	for _, e := range m.Entries {
		want[e.Path] = e
	}
	got := make(map[string]splitEntry, len(destEntries))
	for _, e := range destEntries {
		got[e.Path] = e
	}
	var files int
	var bytes int64
	for _, e := range m.Entries {
		d, ok := got[e.Path]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("missing from destination: %s", e.Path))
		case d.SHA256 != e.SHA256:
			problems = append(problems, fmt.Sprintf(
				"content differs: %s (destination %s, manifest %s)", e.Path, d.SHA256, e.SHA256))
		default:
			files++
			bytes += e.Size
		}
	}
	for _, d := range destEntries {
		if _, ok := want[d.Path]; !ok {
			problems = append(problems, fmt.Sprintf(
				"present in destination but not in the manifest: %s", d.Path))
		}
	}

	// --- Leak gate 1: tree membership, by ReadDir. --------------------------
	problems = append(problems, splitLeakGateMembership(dest, m.Slugs, p)...)

	// --- Leak gate 2: vault-global artifacts. -------------------------------
	problems = append(problems, splitLeakGateGlobal(dest, p)...)

	// --- Stamps were not inherited. -----------------------------------------
	stampProblems, err := splitLeakGateStamps(dest)
	if err != nil {
		return nil, err
	}
	problems = append(problems, stampProblems...)

	// --- Remotes. ------------------------------------------------------------
	//
	// 🔴 AN ERROR FROM ListRemotes IS A HARD REFUSAL, NOT "no remotes". It shells
	// out to `git remote` (vaultsync.go:651-666) and returns (nil, err) when the
	// destination is not a repository at all — which is precisely the state a
	// swallowed git-init failure leaves behind. Treating that as an empty set
	// would let the remote gate PASS on the one destination it exists to catch.
	//
	// 🔴 AND A DESTINATION INSIDE ANOTHER WORK TREE ANSWERS `git remote` FOR THAT
	// TREE. It then passes when the enclosing repository has no remotes (and
	// purge deletes the source), or blames that repository's remotes on the
	// destination. So the destination must be its own top level before its
	// remotes mean anything; InspectVaultGit is the predicate config sync uses.
	var remotes []string
	if state, _ := storage.InspectVaultGit(dest); state == storage.VaultGitNested {
		top, terr := storage.GitTopLevel(dest)
		if terr != nil || top == "" {
			top = "an enclosing repository"
		}
		problems = append(problems, fmt.Sprintf(
			"destination is not its own repository: git resolves it to the work tree at %s, "+
				"so its remotes and history are that repository's, not the destination's", top))
	} else {
		remotes, err = storage.ListRemotes(dest)
		if err != nil {
			return nil, fmt.Errorf(
				"list destination remotes: %w (this is a refusal, not an empty remote set: "+
					"a destination that cannot answer `git remote` is not a repository)", err)
		}
		if len(remotes) > 0 {
			problems = append(problems, fmt.Sprintf(
				"destination has remote(s) %s: split configures none, so these were added "+
					"outside the tool", strings.Join(remotes, ", ")))
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("destination verification failed:\n  - %s",
			strings.Join(problems, "\n  - "))
	}

	return &vaultSplitVerifyResult{
		Action:         "verify",
		Destination:    dest,
		Slugs:          m.Slugs,
		ManifestSHA256: m.SHA256,
		FilesVerified:  files,
		BytesVerified:  bytes,
		DestFormat:     destFormat,
		DestRemotes:    remotes,
		GatesChecked: []string{
			"manifest bind (source re-hashed to the named digest)",
			"inventory path+content, both directions",
			"leak gate 1: destination palace/ and Projects/ hold only allow-listed slugs (ReadDir)",
			"leak gate 1: destination root holds only names the scaffold creates",
			"leak gate 2: Knowledge/ and Audits/ empty unless include_*",
			"leak gate 2: Templates/ holds no resource files and an empty lock",
			"destination .surface stamps carry no inherited provenance",
			"destination data format",
			"destination remotes (ListRemotes error is a refusal)",
		},
		Notes: []string{
			"Body-text mentions of other projects and host-rooted paths inside copied " +
				"documents are advisory and never fail verify. A slug named in prose is " +
				"not a slug that travelled.",
			"Historical session filenames keep the SOURCE writer fingerprint; future " +
				"writes in the destination will carry a new one. A split project's session " +
				"index legitimately spans two fingerprints and `vp check --check " +
				"writer-identity` reports both.",
		},
		Complete: true,
	}, nil
}

// splitDestInventory walks the destination the same way plan walked the source,
// so the two inventories are comparable row for row.
//
// It reuses walkSplitTree and walkSplitGlobal deliberately: a second walker
// written for the destination would be a second definition of the subtract set,
// and the moment those two definitions differ, verify starts comparing two
// different questions and reports agreement.
func splitDestInventory(dest string, p vaultSplitParams, slugs []string) ([]splitEntry, error) {
	var entries []splitEntry
	for _, s := range slugs {
		for _, dir := range []string{
			filepath.Join(dest, "palace", s),
			filepath.Join(dest, "Projects", s),
		} {
			_, treeEntries, err := walkSplitTree(dest, dir)
			if err != nil {
				return nil, fmt.Errorf("inventory destination: %w", err)
			}
			entries = append(entries, treeEntries...)
		}
	}
	_, globalEntries, err := walkSplitGlobal(dest, p)
	if err != nil {
		return nil, fmt.Errorf("inventory destination: %w", err)
	}
	entries = append(entries, globalEntries...)
	return entries, nil
}

// splitLeakGateMembership is leak gate 1: the destination's project trees hold
// only allow-listed slugs, and its root holds only what the scaffold creates.
//
// 🔴 IT IS ReadDir, NOT ListAllProjects. ListAllProjects keeps only directories
// whose names pass slug.Validate and silently drops everything else
// (listProjectDirs) — files, symlinks, invalid-slug directories, and under
// palace/ any directory holding no file outside .local/. That is the
// right filter for the DRIFT report, which is asking which projects exist. It is
// the wrong one for a leak assertion, because it applies the same slug filter
// the copy path already applied: a bug that wrote a non-slug name into the
// destination is invisible to it, and the gate would pass by construction.
func splitLeakGateMembership(dest string, slugs []string, p vaultSplitParams) []string {
	allowed := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		allowed[s] = true
	}

	var problems []string
	for _, tree := range []string{"palace", "Projects"} {
		ents, err := os.ReadDir(filepath.Join(dest, tree))
		if err != nil {
			if os.IsNotExist(err) {
				// A tree can legitimately be absent: a drift slug present only
				// in Projects/ contributes no palace/ side, and if every
				// requested slug is like that the directory is never created.
				continue
			}
			problems = append(problems, fmt.Sprintf("read destination %s/: %v", tree, err))
			continue
		}
		for _, e := range ents {
			name := e.Name()
			// palace/.local is vault-wide machine-local state. It never travels,
			// but the destination may create its own.
			if tree == "palace" && name == ".local" {
				continue
			}
			if !e.IsDir() {
				problems = append(problems, fmt.Sprintf(
					"%s/%s is not a directory (type %s): a project tree holds slug "+
						"directories and nothing else", tree, name, e.Type()))
				continue
			}
			if !allowed[name] {
				problems = append(problems, fmt.Sprintf(
					"%s/%s is not in the allow-list %s", tree, name, strings.Join(slugs, ", ")))
			}
		}
	}

	rootEnts, err := os.ReadDir(dest)
	if err != nil {
		problems = append(problems, fmt.Sprintf("read destination root: %v", err))
		return problems
	}
	for _, e := range rootEnts {
		name := e.Name()
		if splitDestRootAllowed[name] {
			continue
		}
		if name == "Knowledge" && p.IncludeLearnings {
			continue
		}
		if name == "Audits" && p.IncludeAudits {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"destination root holds %q, which the split scaffold does not create", name))
	}
	return problems
}

// splitLeakGateGlobal is leak gate 2: vault-global artifacts arrived only where
// the caller affirmatively asked for them.
//
// 🔴 TEMPLATES IS CHECKED AS "EMPTY", NEVER AS "MATCHES THE EMBEDDED CORPUS". A
// fresh vault's Templates/ tree is empty by design — materialize mode is
// override-only (planMaterialize's decision table) and every embedded resource
// resolves through the precedence chain's embedded tier instead. File-comparing the
// destination against templates.WalkEmbedded() would therefore FAIL VERIFY ON A
// CORRECT DESTINATION, and the obvious way to make it pass — copying the corpus
// onto disk — shadows the binary and is exactly what `vp config sync` prunes.
func splitLeakGateGlobal(dest string, p vaultSplitParams) []string {
	reports, _, err := walkSplitGlobal(dest, p)
	if err != nil {
		return []string{fmt.Sprintf("inventory destination vault-global artifacts: %v", err)}
	}

	var problems []string
	for _, r := range reports {
		if r.Included {
			continue
		}
		if r.Files > 0 {
			problems = append(problems, fmt.Sprintf(
				"destination %s holds %d file(s) but %s was not included in this split",
				r.Path, r.Files, r.Class))
		}
		if r.NonRegular > 0 {
			problems = append(problems, fmt.Sprintf(
				"destination %s holds %d non-regular entr(ies)", r.Path, r.NonRegular))
		}
	}
	return problems
}

// splitLeakGateStamps proves the destination's .surface files are its own.
//
// 🔴 IT DOES NOT COMPARE STAMPS TO WriterFingerprint(dest). WriteStamp emits
// exactly "surface = N\n" and does NOT persist the fingerprint it is handed
// (version.go:80-83); the provenance fields survive on the Stamp struct only so
// ReadStamp can decode LEGACY on-disk stamps that still carry them. So a stamp
// matching a fingerprint is not a thing that can be checked — but a stamp
// CARRYING one is proof it was not written here, because nothing in this binary
// writes that field. Identity lives on session filenames, not on stamps.
func splitLeakGateStamps(dest string) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != ".surface" {
			return nil
		}
		st, err := surface.ReadStamp(filepath.Dir(p))
		if err != nil {
			problems = append(problems, fmt.Sprintf("read %s: %v", vaultRel(dest, p), err))
			return nil
		}
		if st.LastWriter != "" || st.LastWriteAt != "" {
			problems = append(problems, fmt.Sprintf(
				"%s carries provenance fields (last_writer=%q last_write_at=%q): this "+
					"binary never writes them, so the stamp was copied from the source",
				vaultRel(dest, p), st.LastWriter, st.LastWriteAt))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan destination stamps: %w", err)
	}
	return problems, nil
}

// splitPurgeAfterVerify is a TEST SEAM, nil in production: it is called after
// purge's bind and verify have passed and before any tree is walked. That is
// the window a concurrent writer must hit for its file to be absent from the
// manifest purge bound.
var splitPurgeAfterVerify func()

// vaultSplitPurge removes the source slug trees, and only after proving the
// destination holds them.
//
// 🔴 THERE IS NO os.RemoveAll ANYWHERE IN THIS PATH, and that is not squeamish-
// ness. vaultfs.Delete owns the policy every vault removal is meant to carry —
// the .git-segment refusal, ResolveSafePath containment, the per-path advisory
// lock and the compare-and-set — and RemoveNoLock is the single sink beneath it
// (raw.go:91-105, os.Remove: a file or an EMPTY directory). A recursive
// primitive would have none of that, and there is deliberately no sanctioned
// one in the tree. So this walks: regular files through Delete, then empty
// directories bottom-up through RemoveNoLock.
//
// Task files are removable here. IsTaskFilePath gates Write and Edit
// (write.go:47,117) and not Delete, because the reason task paths refuse a
// generic writer — that every header field has exactly one typed writer — says
// nothing about removing the file entirely.
func vaultSplitPurge(vault *storage.Vault, p vaultSplitParams) (*vaultSplitPurgeResult, error) {
	// The label is checked BEFORE anything is removed: once the trees are gone
	// the manifest no longer binds and purge cannot be re-run with a better one.
	if err := departure.ValidateLabel(p.DepartureTo); err != nil {
		return nil, apperr.Caller(fmt.Errorf("departure_to: %w", err))
	}
	if err := splitCheckDestination(vault.Root, p.Destination, true); err != nil {
		return nil, err
	}
	m, err := splitBindManifest(vault, p)
	if err != nil {
		return nil, err
	}

	// 🔴 PURGE RE-RUNS VERIFY RATHER THAN TRUSTING THAT SOMEONE ELSE DID. The
	// destination is the only copy of what is about to be deleted, and "the
	// operator ran verify a moment ago" is not a fact this process can observe.
	if _, err := splitVerifyDestination(vault, p, m); err != nil {
		return nil, fmt.Errorf(
			"refusing to purge: the destination does not verify, so the source is still "+
				"the only copy of this data\n%w", err)
	}
	if splitPurgeAfterVerify != nil {
		splitPurgeAfterVerify()
	}

	// The compare-and-set guard for each file that travelled. Rows outside the
	// slug trees — an included learning, an audit report — are in this map but
	// are never looked up, because the walk below only ever enters
	// palace/<slug>, Projects/<slug> and the slug's embed cache under
	// palace/.local/embed-cache/<slug>. Vault-global artifacts are not this
	// action's to remove.
	hashes := make(map[string]string, len(m.Entries))
	for _, e := range m.Entries {
		hashes[e.Path] = e.SHA256
	}

	// The slug's embed cache lives outside both of its own trees
	// (storage.Vault.EmbedCacheDir). It never travels, but purge used to remove
	// it along with palace/<slug>/, and leaving it now would serve old vectors to
	// a slug later reused on this host: note and iteration cache IDs are
	// positional, and the orphan reaper keeps any ID that is live again.
	//
	// On a vault that commits, it is removed LAST, with the other untracked
	// rows, after the purge commit has landed (see splitPurgeCleanup): a
	// leftover cache is regenerable and never publishable, so it may fail
	// there without failing the purge.
	// 🔴 EVERY TREE IS COLLECTED AND CLASSIFIED BEFORE THE FIRST DELETION. A
	// file that reached a slug tree after the bind above re-derived the
	// manifest is in no manifest row, so it was never copied; deleting it is
	// silent loss. Refusing only when the walk reaches it would be too late,
	// because the trees walked before it would already be gone.
	var trees []splitPurgeSet
	var unaccounted []string
	for _, s := range m.Slugs {
		for _, tree := range []string{"palace/.local/embed-cache/" + s, "palace/" + s, "Projects/" + s} {
			set, err := splitPurgeCollect(vault.Root, tree)
			if err != nil {
				return nil, err
			}
			for _, rel := range set.Files {
				if hashes[rel] == "" && !splitSubtracted(rel) {
					unaccounted = append(unaccounted, rel)
				}
			}
			trees = append(trees, set)
		}
	}
	if len(unaccounted) > 0 {
		return nil, splitPurgeUnaccountedError(unaccounted)
	}

	// A vault that commits gets the atomic purge; one that cannot commit — no
	// repository of its own, or one nested inside another — keeps the plain
	// removal: nothing can publish half of it there.
	state, _ := storage.InspectVaultGit(vault.Root)
	switch state {
	case storage.VaultGitOK:
		return splitPurgeCommitted(vault, p, m, trees, hashes)
	case storage.VaultNotGit, storage.VaultGitNested:
		return splitPurgeUncommitted(vault, p, m, trees, hashes)
	default:
		return nil, fmt.Errorf("refusing to purge: cannot tell whether the vault is a git repository of its own, so cannot tell whether the removal must be committed")
	}
}

// splitPurgeAfterRecords is a TEST SEAM, nil in production: it is called after
// the departure records are written and before the tracked removal is
// committed — the window a crash, or a pull landing, falls into.
var splitPurgeAfterRecords func()

// splitPurgeBeforeCleanup is a TEST SEAM, nil in production: it is called after
// the purge commit has landed and before the untracked cleanup.
var splitPurgeBeforeCleanup func()

// splitPurgeCommitted is purge on a vault that commits, in the order that
// leaves nothing another committer can publish half of
// (split-purge-commits-its-own-result):
//
//  1. preflight — changes nothing;
//  2. the departure records, FIRST: from here the commit guard refuses every
//     other vp commit, so an interrupted purge cannot be half-published;
//  3. under the vault commit lock: HEAD unchanged, `git rm` of the TRACKED
//     files, stage the records, ONE local commit (storage.CommitSplitPurge);
//  4. after the commit, best-effort: the untracked and ignored rows this purge
//     collected, the embed cache and the empty directories. What is left is
//     reported in cleanup_left; none of it is tracked, so none of it is
//     publishable.
//
// A failure in 2–3 rolls back losslessly — git restores the tracked files,
// and no untracked file has been touched yet — so purge can simply be re-run.
func splitPurgeCommitted(vault *storage.Vault, p vaultSplitParams, m *splitManifest, trees []splitPurgeSet, hashes map[string]string) (*vaultSplitPurgeResult, error) {
	collected := make(map[string]bool)
	for _, set := range trees {
		for _, rel := range set.Files {
			collected[rel] = true
		}
	}
	head, err := storage.SplitPurgePreflight(vault.Root, m.Slugs, collected)
	if err != nil {
		return nil, apperr.Caller(err)
	}

	type written struct {
		rel     string
		created bool
	}
	var records []written
	// The record write restamps Audits/.surface (atomicfile), so its state
	// before step 1 is part of what a rollback restores.
	const auditsSurface = "Audits/.surface"
	surfaceAbs := filepath.Join(vault.Root, filepath.FromSlash(auditsSurface))
	surfaceBefore, statErr := os.ReadFile(surfaceAbs)
	surfaceExisted := statErr == nil
	undoRecords := func(cause error) error {
		var errs []string
		if surfaceExisted {
			// Its bytes are restored as they were. atomicfile does not stamp a
			// .surface path (surface.ResolveStampDir skips it), so this
			// restore cannot restamp itself.
			// Read and write under the stamp's own advisory lock (ADR-003); the
			// vault commit lock is already released here, so this nests
			// nothing.
			if release, err := vaultlock.Acquire(vault.Root, surfaceAbs); err != nil {
				errs = append(errs, fmt.Sprintf("%s: lock: %v", auditsSurface, err))
			} else {
				if now, err := os.ReadFile(surfaceAbs); err != nil || string(now) != string(surfaceBefore) {
					if err := atomicfile.Write(vault.Root, surfaceAbs, surfaceBefore); err != nil {
						errs = append(errs, fmt.Sprintf("%s: %v", auditsSurface, err))
					}
				}
				_ = release()
			}
		} else if _, err := os.Lstat(surfaceAbs); err == nil {
			if _, err := vaultfs.Delete(vault.Root, auditsSurface, ""); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", auditsSurface, err))
			}
		}
		// Removed last, in reverse: while one is left, the commit guard stays
		// armed.
		for i := len(records) - 1; i >= 0; i-- {
			r := records[i]
			var err error
			if r.created {
				// The one privileged removal of a record this purge wrote,
				// under the root lock (released by CommitSplitPurge by now).
				err = removeOwnDepartureRecord(vault.Root, strings.TrimSuffix(path.Base(r.rel), ".json"))
			} else {
				err = storage.RestoreFromHEAD(vault.Root, r.rel)
			}
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", r.rel, err))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%w; and restoring the departure records failed (%s): the commit guard will refuse every vp commit until they are restored or removed by hand", cause, strings.Join(errs, "; "))
		}
		return cause
	}
	for _, s := range m.Slugs {
		rel, created, err := vault.RecordDepartureForPurge(s, departure.MovedToVault, p.DepartureTo)
		if err != nil {
			return nil, undoRecords(fmt.Errorf("record the departure of %q: %w (nothing was removed)", s, err))
		}
		records = append(records, written{rel, created})
	}
	if splitPurgeAfterRecords != nil {
		splitPurgeAfterRecords()
	}

	recRels := make([]string, 0, len(records))
	for _, r := range records {
		recRels = append(recRels, r.rel)
	}
	res, err := storage.CommitSplitPurge(vault.Root, storage.SplitPurgeCommit{
		Slugs:      m.Slugs,
		Records:    recRels,
		Message:    splitPurgeCommitMessage(m, p.DepartureTo, recRels),
		ExpectHead: head,
	})
	if err != nil && (res == nil || res.CommitSHA == "") {
		return nil, undoRecords(err)
	}
	if err != nil {
		// The commit landed; an assertion after it failed. Nothing is rolled
		// back over a landed commit.
		return nil, err
	}

	if splitPurgeBeforeCleanup != nil {
		splitPurgeBeforeCleanup()
	}
	files, dirs, left := splitPurgeCleanup(vault.Root, trees, hashes)
	// The purged projects' host-local index stores, each under its own index
	// commit lock, now that the purge commit has released the vault lock. A
	// store kept here (a busy lock) is logged and left to the next index sweep.
	for _, r := range indexstore.RemoveGoneProjects(context.Background(), vault, m.Slugs, indexstore.LifecycleRemovalTimeout) {
		if r.Error == "" && r.Outcome != indexstore.RemovalRemoved && r.Outcome != indexstore.RemovalAbsent {
			slog.Warn("split purge: host-local index store kept; the next index sweep retries it", "project", r.Slug, "outcome", r.Outcome)
		}
	}
	result := &vaultSplitPurgeResult{
		Action:           "purge",
		Destination:      p.Destination,
		Slugs:            m.Slugs,
		ManifestSHA256:   m.SHA256,
		FilesRemoved:     res.TrackedRemoved + files,
		DirsRemoved:      dirs,
		BytesRemoved:     splitPurgeBytes(trees),
		DepartureRecords: recRels,
		CommitSHA:        res.CommitSHA,
		CleanupLeft:      left,
		Notes: []string{
			"Vault-global artifacts were not removed. Knowledge/, Audits/ and Templates/ " +
				"do not partition by slug, so no part of them is derivable from this " +
				"allow-list and purge removes none of it.",
			fmt.Sprintf("Committed as %s, with the departure records, and not pushed: "+
				"`vp vault sync` publishes it. Nothing of this purge is left uncommitted.", res.CommitSHA),
			"The split's git history stays in the source repository. Purge removes files; " +
				"it does not rewrite history, and the destination was born as a fresh " +
				"repository with none.",
		},
		Complete: true,
	}
	for _, l := range left {
		if l.Class == "untracked" {
			result.Notes = append(result.Notes, fmt.Sprintf(
				"RESURRECTION RISK: %s is an untracked file under a purged tree that this purge did not collect "+
					"(something wrote there during the purge). It makes the slug read as present again, and tidy "+
					"would commit it back; move it to the destination vault or delete it.", l.Path))
		}
	}
	return result, nil
}

// splitPurgeUncommitted is purge on a vault that cannot commit — no repository
// of its own, or one nested inside another, into which vp never commits. It is
// the removal as it has always been: every tree, then the records, and no
// commit, because nothing can publish half of it.
func splitPurgeUncommitted(vault *storage.Vault, p vaultSplitParams, m *splitManifest, trees []splitPurgeSet, hashes map[string]string) (*vaultSplitPurgeResult, error) {
	var files, dirs int
	for _, set := range trees {
		f, d, err := splitPurgeRemove(vault.Root, set, hashes)
		if err != nil {
			return nil, err
		}
		files += f
		dirs += d
	}
	var records []string
	for _, s := range m.Slugs {
		rel, err := vault.RecordDeparture(s, departure.MovedToVault, p.DepartureTo)
		if err != nil {
			return nil, fmt.Errorf(
				"purge removed every slug tree (%d files), but recording the departure of %q failed: %w. "+
					"Records written before it: %v", files, s, err, records)
		}
		records = append(records, rel)
	}
	return &vaultSplitPurgeResult{
		Action:           "purge",
		Destination:      p.Destination,
		Slugs:            m.Slugs,
		ManifestSHA256:   m.SHA256,
		FilesRemoved:     files,
		DirsRemoved:      dirs,
		BytesRemoved:     splitPurgeBytes(trees),
		DepartureRecords: records,
		Notes: []string{
			"Vault-global artifacts were not removed. Knowledge/, Audits/ and Templates/ " +
				"do not partition by slug, so no part of them is derivable from this " +
				"allow-list and purge removes none of it.",
			"Nothing was committed: this vault is not a git repository of its own, so " +
				"vp commits nothing into it.",
		},
		Complete: true,
	}, nil
}

func splitPurgeBytes(trees []splitPurgeSet) int64 {
	var n int64
	for _, set := range trees {
		n += set.Bytes
	}
	return n
}

// splitPurgeCommitMessage is the purge commit's message. It names no host
// path: the commit syncs to every host.
func splitPurgeCommitMessage(m *splitManifest, to string, records []string) string {
	where := to
	if where == "" {
		where = "a vault that was not recorded"
	}
	return fmt.Sprintf("vault split: purge %s (moved to %s)\n\nmanifest_sha256: %s\ndeparture records: %s",
		strings.Join(m.Slugs, ", "), where, m.SHA256, strings.Join(records, ", "))
}

// splitCopyEntry and the purge collect, cleanup and remove helpers are the
// storage project-tree primitives. See internal/storage/project_tree.go.
type (
	splitPurgeSet          = storage.PurgeSet
	splitPurgeCleanupEntry = storage.PurgeCleanupEntry
)

func splitCopyEntry(srcRoot, destRoot string, e splitEntry) error {
	return storage.CopyProjectTreeEntry(srcRoot, destRoot, e)
}

func splitPurgeCollect(root, treeRel string) (splitPurgeSet, error) {
	return storage.CollectPurgeTree(root, treeRel)
}

func splitPurgeCleanup(root string, trees []splitPurgeSet, hashes map[string]string) (files, dirs int, left []splitPurgeCleanupEntry) {
	return storage.CleanupPurgedTrees(root, trees, hashes)
}

func splitPurgeRemove(root string, set splitPurgeSet, hashes map[string]string) (int, int, error) {
	return storage.RemovePurgeTree(root, set, hashes)
}

// splitPurgeUnaccountedError is the refusal for files that are neither manifest
// rows nor subtract-set members: bytes that reached a slug tree after purge
// re-derived the manifest, and so were never copied to the destination.
func splitPurgeUnaccountedError(rels []string) error {
	sort.Strings(rels)
	const shown = 20
	var b strings.Builder
	noun, verb := "files", "are"
	if len(rels) == 1 {
		noun, verb = "file", "is"
	}
	fmt.Fprintf(&b, "refusing to purge: %d %s under the purged slug trees %s not in the "+
		"manifest this purge bound, so %s never copied to the destination:",
		len(rels), noun, verb, map[bool]string{true: "it was", false: "they were"}[len(rels) == 1])
	for i, r := range rels {
		if i == shown {
			fmt.Fprintf(&b, "\n  - … and %d more", len(rels)-shown)
			break
		}
		fmt.Fprintf(&b, "\n  - %s", r)
	}
	b.WriteString("\nSomething wrote to these slugs after this purge re-derived the manifest " +
		"(a capture, a task amend or a memory write). Nothing was deleted: the source and " +
		"the destination are both as they were. Stop every writer to these slugs, then run " +
		"plan, apply, verify and purge again into a fresh destination path; the current " +
		"destination lacks these files, so it cannot verify against a new plan. Removing " +
		"the old destination afterwards is optional cleanup.")
	return apperr.Caller(errors.New(b.String()))
}

// removeOwnDepartureRecord removes a departure record the split purge wrote and
// is rolling back, through vaultfs.RemoveDepartureRecord under the root lock:
// an ordinary vaultfs.Delete of a record is refused.
func removeOwnDepartureRecord(root, slug string) error {
	held, err := vaultlock.AcquireHeld(root, root)
	if err != nil {
		return err
	}
	defer held.Release()
	return vaultfs.RemoveDepartureRecord(held, slug)
}
