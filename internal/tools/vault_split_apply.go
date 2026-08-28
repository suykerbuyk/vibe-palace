// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
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
//   - .vibe-palace       — the data-format stamp, and templates.lock
//   - Templates          — reconcile.NewTemplateTree's directory (Design B:
//     the directory exists and stays EMPTY; the embedded floor serves)
//   - palace, Projects   — the two trees the copy writes into
//   - .surface           — atomicfile's own stamp, if a write resolves the
//     destination root as its stamp directory
var splitDestRootAllowed = map[string]bool{
	".git":         true,
	".gitignore":   true,
	".vibe-palace": true,
	".surface":     true,
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
	Notes          []string `json:"notes"`
	Complete       bool     `json:"complete"`
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
		return nil
	default:
		// Anything other than "absent" — a permission error, a broken symlink
		// component — leaves us unable to say the destination is absent, and
		// that is exactly the state in which creating it is unsafe.
		return apperr.Caller(fmt.Errorf("stat destination %q: %w", dest, err))
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
// scaffold that stamps the vault data format.
//
// 🔴 THE FIRST ARGUMENT IS dest, NOT A WORKING DIRECTORY. VaultReconciler
// resolves its target as seed.VaultPath whenever the seed is set and that field
// is non-empty (vault.go:45-51), and falls through to
// storage.ResolveVaultPath(root) otherwise. `vp init` passes cwd because cwd is
// the project tree it is initialising from; an MCP handler passing cwd would
// resolve the SOURCE vault through .vibe-palace.toml or the global vault_path
// and reconcile the vault it is splitting FROM. Passing dest is the fail-closed
// argument, and VaultPath: dest plus WithCreate() is what makes it binding.
//
// 🔴 Vault.Apply RETURNS (Report, nil) ALWAYS (vault.go:169-211). Its failures —
// mkdir, git init, and the data-format stamp — land in Report.Errors, and the
// stamp failure in particular still increments Created. So err is not the
// channel here, Report.Errors is, and ANY entry in it aborts before a single
// byte is copied. That is stricter than cmd_init.go:228-240, which triages the
// same slice by error prefix and treats a failed stamp as non-fatal; a CLI that
// keeps going leaves a human looking at the terminal, and this does not.
//
// TemplateTree does NOT follow that rule and must not be handled as though it
// did: its Apply returns a real error for walk-embedded and prompt failures
// (template_tree.go:428-443) and puts write/lock failures in Report.Errors
// (:523-526). Both channels abort, exactly as cmd_init.go:271-288 does.
func splitScaffoldDestination(ctx context.Context, dest string) error {
	vr := reconcile.NewVault(dest, reconcile.VaultSeed{
		VaultPath: dest,
		// Unconditionally true, with no GitAvailable probe. A missing git
		// binary surfaces as a GitInit error in Report.Errors and aborts, which
		// is the fail-closed outcome: a publishable split whose destination is
		// not a repository cannot be published, and storage.ListRemotes — which
		// verify calls — needs a repository to answer at all.
		GitEnabled: true,
	}.WithCreate())

	plan, err := vr.Plan(ctx)
	if err != nil {
		return fmt.Errorf("plan destination vault: %w", err)
	}
	rep, err := vr.Apply(ctx, plan)
	if err != nil {
		return fmt.Errorf("scaffold destination vault: %w", err)
	}
	if len(rep.Errors) > 0 {
		return fmt.Errorf(
			"scaffold destination vault: %w (nothing was copied; remove the destination "+
				"host path before retrying)", errors.Join(rep.Errors...))
	}

	// Materialize mode on a fresh destination writes no resource files. Design B
	// is override-only (template_tree.go:254-274, pinned by
	// TestTemplateTree_MaterializeFreshVault): every embedded resource is
	// ActionUnchanged, "served from embedded floor", Created is 0 and the lock
	// stays empty. What this call is actually here for is the gitignore and lock
	// reconciliation inside Apply. AutoAccept is deliberately unset — the field
	// is unread (template_tree.go:51-56) and setting it would read as though a
	// prompt were being suppressed.
	tt := reconcile.NewTemplateTree(dest, "Templates", reconcile.TemplateTreeSeed{
		Mode: reconcile.TemplateModeMaterialize,
	})
	ttPlan, err := tt.Plan(ctx)
	if err != nil {
		return fmt.Errorf("plan destination templates: %w", err)
	}
	ttRep, err := tt.Apply(ctx, ttPlan)
	if err != nil {
		return fmt.Errorf("reconcile destination templates: %w", err)
	}
	if len(ttRep.Errors) > 0 {
		return fmt.Errorf("reconcile destination templates: %w", errors.Join(ttRep.Errors...))
	}
	return nil
}

// splitCopyEntry copies one manifest row into the destination.
//
// 🔴 Lstat FIRST, AND A NON-REGULAR SOURCE REFUSES — the same contract plan
// enforced, re-enforced here because plan and apply are separate calls and the
// tree can change between them. os.Open follows symlinks, so a check made only
// at plan time is a check made against a different filesystem than the one
// being read.
//
// The content is hashed WHILE it streams, and the result is compared to the
// manifest row. That costs nothing — the bytes are already passing through — and
// it closes the last gap the digest bind cannot: buildSplitManifest hashed this
// file moments ago, and this is the read that proves the bytes landing in the
// destination are those same bytes and not a racing rewrite.
//
// atomicfile.WriteStream is the streaming half of the whole-file primitive: it
// creates parents, writes a temp file beside the target and renames. Passing
// vaultRoot = dest makes the DESTINATION stamp itself (atomicfile.go:166), which
// is why no .surface file ever has to travel.
func splitCopyEntry(srcRoot, destRoot string, e splitEntry) error {
	src := filepath.Join(srcRoot, filepath.FromSlash(e.Path))

	st, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("lstat %s: %w", e.Path, err)
	}
	if st.Mode().Type() != 0 {
		return fmt.Errorf(
			"%s is not a regular file (mode %s): it was regular when the manifest was "+
				"taken, so the source changed under this call", e.Path, st.Mode().Type())
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", e.Path, err)
	}
	defer f.Close()

	h := sha256.New()
	var n int64
	dst := filepath.Join(destRoot, filepath.FromSlash(e.Path))
	if err := atomicfile.WriteStream(destRoot, dst, func(w io.Writer) error {
		var cerr error
		n, cerr = io.Copy(io.MultiWriter(w, h), f)
		return cerr
	}); err != nil {
		return fmt.Errorf("copy %s: %w", e.Path, err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
		return fmt.Errorf(
			"copy %s: content hashed %s, manifest says %s — the source changed while it "+
				"was being read", e.Path, got, e.SHA256)
	}
	if n != e.Size {
		return fmt.Errorf("copy %s: copied %d bytes, manifest says %d", e.Path, n, e.Size)
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
	remotes, err := storage.ListRemotes(dest)
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
// (projects.go:103-126) — files, symlinks, invalid-slug directories. That is the
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
// override-only (template_tree.go:254-274) and every embedded resource resolves
// through the precedence chain's embedded tier instead. File-comparing the
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

	lock, err := templates.ReadLock(dest)
	if err != nil {
		problems = append(problems, fmt.Sprintf("read destination templates lock: %v", err))
	} else if len(lock.Entries) > 0 {
		problems = append(problems, fmt.Sprintf(
			"destination templates lock has %d entr(ies): a fresh vault materializes "+
				"nothing and its lock stays empty", len(lock.Entries)))
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

	// The compare-and-set guard for each file that travelled. Rows outside the
	// slug trees — an included learning, an audit report — are in this map but
	// are never looked up, because the walk below only ever enters
	// palace/<slug> and Projects/<slug>. Vault-global artifacts are not this
	// action's to remove.
	hashes := make(map[string]string, len(m.Entries))
	for _, e := range m.Entries {
		hashes[e.Path] = e.SHA256
	}

	var files, dirs int
	var bytes int64
	for _, s := range m.Slugs {
		for _, tree := range []string{"palace/" + s, "Projects/" + s} {
			f, d, b, err := splitPurgeTree(vault.Root, tree, hashes)
			if err != nil {
				return nil, err
			}
			files += f
			dirs += d
			bytes += b
		}
	}

	return &vaultSplitPurgeResult{
		Action:         "purge",
		Destination:    p.Destination,
		Slugs:          m.Slugs,
		ManifestSHA256: m.SHA256,
		FilesRemoved:   files,
		DirsRemoved:    dirs,
		BytesRemoved:   bytes,
		Notes: []string{
			"Vault-global artifacts were not touched. Knowledge/, Audits/ and Templates/ " +
				"do not partition by slug, so no part of them is derivable from this " +
				"allow-list and purge removes none of it.",
			"The split's git history stays in the source repository. Purge removes files; " +
				"it does not rewrite history, and the destination was born as a fresh " +
				"repository with none.",
		},
		Complete: true,
	}, nil
}

// splitPurgeTree removes one source slug tree: every regular file through
// vaultfs.Delete, then every directory bottom-up through vaultfs.RemoveNoLock.
//
// A non-regular entry ANYWHERE in the tree refuses the whole purge, including
// inside the machine-local subtrees plan prunes rather than scans. Plan may
// ignore a symlink in an embed cache because that cache was never going to
// travel; purge may not, because it is about to remove the directory containing
// it and neither named primitive can classify what it would be removing.
func splitPurgeTree(root, treeRel string, hashes map[string]string) (int, int, int64, error) {
	dir := filepath.Join(root, filepath.FromSlash(treeRel))
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Drift: this slug lives in only one of the two trees. Nothing to
			// remove on this side, and that is not an error.
			return 0, 0, 0, nil
		}
		return 0, 0, 0, fmt.Errorf("stat %s: %w", treeRel, err)
	}
	if !info.IsDir() {
		return 0, 0, 0, fmt.Errorf(
			"%s is not a directory (mode %s): purge refuses it", treeRel, info.Mode().Type())
	}

	// Collect first, mutate second. A walk that deleted as it went would be
	// mutating the tree it is enumerating, and the failure mode of that is a
	// partial purge that reports success.
	var fileRels []string
	var dirAbs []string
	var bytes int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			dirAbs = append(dirAbs, p)
			return nil
		}
		rel := vaultRel(root, p)
		st, lerr := os.Lstat(p)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", rel, lerr)
		}
		if st.Mode().Type() != 0 {
			return fmt.Errorf(
				"%s is not a regular file (mode %s): purge refuses a tree it cannot "+
					"classify entry by entry, because neither vaultfs.Delete nor "+
					"RemoveNoLock is a recursive primitive and there is no third one",
				rel, st.Mode().Type())
		}
		fileRels = append(fileRels, rel)
		bytes += st.Size()
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}

	var files int
	for _, rel := range fileRels {
		// hashes[rel] is the compare-and-set guard for a file that travelled,
		// and "" — no guard — for one the subtract set removed from the
		// manifest (.surface, .local/**, commit-log.anchor). Those were never
		// copied, so there is no destination hash to compare against; they are
		// removed because the tree they live in is going away.
		if _, derr := vaultfs.Delete(root, rel, hashes[rel]); derr != nil {
			return files, 0, bytes, fmt.Errorf("purge %s: %w", rel, derr)
		}
		files++
	}

	// Bottom-up: a child's path is always strictly longer than its parent's, so
	// ordering by descending length removes every directory only after its
	// contents. RemoveNoLock is os.Remove, which refuses a non-empty directory —
	// so a miscount here fails loudly instead of removing something unexamined.
	sort.Slice(dirAbs, func(i, j int) bool { return len(dirAbs[i]) > len(dirAbs[j]) })
	var dirs int
	for _, d := range dirAbs {
		if rerr := vaultfs.RemoveNoLock(d); rerr != nil {
			return files, dirs, bytes, fmt.Errorf("purge directory %s: %w", vaultRel(root, d), rerr)
		}
		dirs++
	}
	return files, dirs, bytes, nil
}
