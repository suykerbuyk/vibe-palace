// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package memory implements the one-way harvest of Claude Code's host-local
// auto-memory into the synced vault. Claude writes typed memory files (with a
// metadata.type frontmatter) under ~/.claude/projects/<encoded-cwd>/memory/.
// Harvest drains that directory: it routes each typed file into the vault at
// Projects/<slug>/memory/, then DELETES the host-local original — so the vault
// becomes the single source of truth and recall happens via bootstrap, not via
// host-local re-injection. Grok/Zed (which have no native memory dir) are a
// clean skip, never an error.
package memory

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// memoryIndexFile is Claude's native memory index. It is an index of memories,
// not a memory itself: it is skipped from routing but still deleted host-local
// (an orphaned index would re-inject a stale, dangling pointer set next
// session).
const memoryIndexFile = "MEMORY.md"

// defaultType is the fallback memory type for native files whose frontmatter
// declares no type, or an unknown one. Such files are never silently dropped:
// they are routed under this type and recorded in Result.Unclassified.
const defaultType = "project"

// validTypes mirrors storage's accepted MemoryMeta.Type set.
var validTypes = map[string]bool{
	"user":      true,
	"feedback":  true,
	"project":   true,
	"reference": true,
}

// Options configures a Harvest run.
type Options struct {
	VaultRoot string // vault root
	Project   string // destination project slug
	NativeDir string // the native memory dir to drain (resolved by the caller)
	DryRun    bool   // classify + report only; ZERO filesystem/git mutation
	Push      bool   // after a non-dry harvest, push the memory commit to remotes
}

// Result reports the outcome of a Harvest run. Rels are filenames relative to
// the project's memory dir (the native basename, except Conflicted which are
// the suffixed names actually written).
type Result struct {
	NativeDir        string   `json:"native_dir"`
	NativeDirMissing bool     `json:"native_dir_missing"` // true → caller treats as a clean Skip
	Routed           []string `json:"routed"`             // rels written into the vault
	Conflicted       []string `json:"conflicted"`         // rels written with a .harvested-<ts> suffix
	DedupSkipped     []string `json:"dedup_skipped"`      // identical content already in vault → dropped
	IndexSkipped     []string `json:"index_skipped"`      // MEMORY.md / non-typed index files skipped
	Unclassified     []string `json:"unclassified"`       // missing/unknown type → default-routed (logged)
	DeletedHostLocal []string `json:"deleted_host_local"` // host-local originals removed (incl. MEMORY.md)
	Committed        bool     `json:"committed"`
	CommitSHA        string   `json:"commit_sha"`
	PushDowngraded   bool     `json:"push_downgraded"`
	// RemoteResults reports the per-remote push outcome ("ok" or the error
	// string). Empty when nothing was pushed (local-only commit, downgrade, or
	// no commit). Surfacing it prevents a failed push from being reported as a
	// silent success.
	RemoteResults map[string]string `json:"remote_results,omitempty"`
}

// NativeDirFromTranscript returns the native memory dir adjacent to a Claude
// transcript: filepath.Dir(transcriptPath)/memory. This is the authoritative
// resolution (the transcript already lives in the encoded project dir, so no
// re-encoding is needed) and is preferred whenever a transcript path is known.
func NativeDirFromTranscript(transcriptPath string) string {
	return filepath.Join(filepath.Dir(transcriptPath), "memory")
}

// NativeDirFromCwd returns the native memory dir for a working directory:
// ClaudeHome()/projects/<EncodeProjectDir(cwd)>/memory. This is the CLI/MCP
// fallback used when no transcript path is available. The cwd is made absolute
// before encoding so the encoded segment matches Claude's real dir (which keeps
// the leading dash). The encoding is lossy; this never reverse-decodes.
func NativeDirFromCwd(cwd string) (string, error) {
	home, err := archive.ClaudeHome()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	encoded := archive.EncodeProjectDir(abs)
	return filepath.Join(home, "projects", encoded, "memory"), nil
}

// Harvest drains the native memory dir into the vault and (non-dry) deletes the
// host-local originals, then commits the routed memory files. See the package
// doc and Result fields for the full contract.
func Harvest(opts Options) (*Result, error) {
	res := &Result{NativeDir: opts.NativeDir}

	vault := storage.NewVault(opts.VaultRoot)
	ts := time.Now().Unix()

	// Host-local paths to delete after a successful drain (non-dry). Index files
	// are tracked separately so they are deleted even though they were not
	// routed.
	var toDelete []string
	var indexToDelete []string

	// Stat the native dir. A missing or non-dir native dir (Grok/Zed/never-used
	// host) is NOT an early return: drained stays false (no files to route or
	// delete), but we still fall through to the commit-if-dirty step so memories
	// written directly to the vault via vp_memory_write get committed. Only a
	// hard stat error aborts.
	drained := false
	info, statErr := os.Stat(opts.NativeDir)
	switch {
	case statErr == nil && info.IsDir():
		drained = true
	case statErr == nil, os.IsNotExist(statErr):
		res.NativeDirMissing = true
	default:
		return nil, fmt.Errorf("stat native dir %s: %w", opts.NativeDir, statErr)
	}

	if drained {
		matches, err := filepath.Glob(filepath.Join(opts.NativeDir, "*.md"))
		if err != nil {
			return nil, fmt.Errorf("glob native memory: %w", err)
		}
		sort.Strings(matches)

		for _, m := range matches {
			base := filepath.Base(m)

			// MEMORY.md (and any case variant) is an index, never a memory. Skip
			// from routing but mark for host-local deletion (step 4).
			if strings.EqualFold(base, memoryIndexFile) {
				res.IndexSkipped = append(res.IndexSkipped, base)
				indexToDelete = append(indexToDelete, m)
				continue
			}

			data, rerr := os.ReadFile(m)
			if rerr != nil {
				slog.Warn("harvest: skip unreadable native memory", "file", base, "err", rerr)
				continue
			}

			meta, body, perr := storage.ParseMemoryFile(data)
			if perr != nil {
				// Non-typed / unparseable index-like file: skip from routing and
				// leave host-local for human eyes rather than silently dropping it.
				slog.Warn("harvest: skip unparseable native memory (not routed, not deleted)",
					"file", base, "err", perr)
				res.IndexSkipped = append(res.IndexSkipped, base)
				continue
			}

			rel := base
			meta.Rel = rel
			if !validTypes[meta.Type] {
				slog.Warn("harvest: unclassified memory, defaulted to project",
					"file", base, "declared_type", meta.Type)
				meta.Type = defaultType
				res.Unclassified = append(res.Unclassified, rel)
			}

			// Compare against any existing vault file at the same rel.
			existMeta, existBody, readErr := vault.ReadMemory(opts.Project, rel)
			switch {
			case readErr == nil:
				if memoryEqual(existMeta, existBody, meta, body) {
					// Identical content already in the vault → drop, drain host-local.
					res.DedupSkipped = append(res.DedupSkipped, rel)
					toDelete = append(toDelete, m)
					continue
				}
				// Same rel, different content → write to a suffixed name so the
				// existing vault file is never clobbered.
				conflictRel := conflictName(rel, ts)
				if !opts.DryRun {
					if werr := vault.WriteMemory(opts.Project, conflictRel, meta, body); werr != nil {
						return nil, fmt.Errorf("write conflict memory %s: %w", conflictRel, werr)
					}
				}
				res.Conflicted = append(res.Conflicted, conflictRel)
				toDelete = append(toDelete, m)
				continue
			case !errors.Is(readErr, os.ErrNotExist):
				// An existing vault file is present but unreadable/unparseable (e.g.
				// hand-corrupted frontmatter — the vault is user-editable). Routing
				// normally would blind-overwrite it, so instead write to a conflict
				// name and leave the original for the human to reconcile. Never clobber.
				slog.Warn("harvest: existing vault memory unreadable; routing to conflict name to avoid clobber",
					"rel", rel, "err", readErr)
				conflictRel := conflictName(rel, ts)
				if !opts.DryRun {
					if werr := vault.WriteMemory(opts.Project, conflictRel, meta, body); werr != nil {
						return nil, fmt.Errorf("write conflict memory %s: %w", conflictRel, werr)
					}
				}
				res.Conflicted = append(res.Conflicted, conflictRel)
				toDelete = append(toDelete, m)
				continue
			}
			// readErr is os.ErrNotExist → no existing file; route normally.

			// Route normally.
			if !opts.DryRun {
				if werr := vault.WriteMemory(opts.Project, rel, meta, body); werr != nil {
					return nil, fmt.Errorf("write memory %s: %w", rel, werr)
				}
			}
			res.Routed = append(res.Routed, rel)
			toDelete = append(toDelete, m)
		}
	}

	// DryRun: ZERO mutation. Report what WOULD be deleted and return.
	if opts.DryRun {
		for _, m := range toDelete {
			res.DeletedHostLocal = append(res.DeletedHostLocal, filepath.Base(m))
		}
		for _, m := range indexToDelete {
			res.DeletedHostLocal = append(res.DeletedHostLocal, filepath.Base(m))
		}
		return res, nil
	}

	// Step 3+4: delete host-local originals (routed/deduped/conflict-handled)
	// and the index. Deleting the index is critical: a surviving MEMORY.md would
	// re-inject a stale pointer set next session for files just drained. Only
	// runs when there was a drain; native-missing has nothing to delete.
	if drained {
		for _, m := range append(toDelete, indexToDelete...) {
			if rmErr := os.Remove(m); rmErr != nil && !os.IsNotExist(rmErr) {
				return nil, fmt.Errorf("delete host-local %s: %w", m, rmErr)
			}
			res.DeletedHostLocal = append(res.DeletedHostLocal, filepath.Base(m))
		}
	}

	// Step 5: commit the memory dir (plus the .surface stamp it produces), staged
	// recursively. Nothing else commits memory (it is outside tidy's sweep
	// rules), so harvest owns the commit — and it must catch files written
	// directly to the vault via vp_memory_write, not just files it routed.
	//
	// Scope the commit to paths that ACTUALLY EXIST: `git add` aborts (exit 128)
	// when any pathspec matches nothing, so a bare `git add Projects/<p>/memory`
	// would fail whenever the memory dir is absent. If the memory dir does not
	// exist there are no memory changes for harvest to own — the .surface stamp
	// may still be dirty from unrelated vault writes, but those belong to /wrap,
	// not to harvest, so we leave them. Only commit when the surviving paths are
	// actually dirty: a no-writes session (or an all-dedup drain over
	// already-committed content) must be a no-op, never an empty commit or error.
	relMemDir := path.Join("Projects", opts.Project, "memory")
	if _, statErr := os.Stat(filepath.Join(opts.VaultRoot, relMemDir)); statErr != nil {
		return res, nil
	}
	commitPaths := []string{relMemDir}
	relSurface := path.Join("Projects", opts.Project, ".surface")
	if _, e := os.Stat(filepath.Join(opts.VaultRoot, relSurface)); e == nil {
		commitPaths = append(commitPaths, relSurface)
	}
	dirty, err := storage.HasUncommittedChanges(opts.VaultRoot, commitPaths...)
	if err != nil {
		return nil, fmt.Errorf("memory dirty check: %w", err)
	}
	if !dirty {
		return res, nil
	}

	msg := harvestCommitMessage(len(res.Routed), len(res.Conflicted))
	pushRes, downgraded, cerr := storage.CommitAndPushPathsWithDowngrade(opts.VaultRoot, msg, commitPaths, opts.Push)
	if cerr != nil {
		return nil, fmt.Errorf("commit harvested memory: %w", cerr)
	}
	res.PushDowngraded = downgraded
	if pushRes.CommitSHA != "" {
		res.Committed = true
		res.CommitSHA = pushRes.CommitSHA
	}
	if len(pushRes.RemoteResults) > 0 {
		res.RemoteResults = make(map[string]string, len(pushRes.RemoteResults))
		for remote, e := range pushRes.RemoteResults {
			if e != nil {
				res.RemoteResults[remote] = e.Error()
			} else {
				res.RemoteResults[remote] = "ok"
			}
		}
	}
	return res, nil
}

// memoryEqual reports whether two parsed memories are content-identical for
// dedup purposes: same routed metadata (name/description/type) and body. Rel is
// ignored (it is the destination filename, equal by construction here).
//
// Bodies are compared with trailing newlines trimmed: WriteMemory's canonical
// render appends a trailing "\n" that the native original may lack, so a raw
// body compare would spuriously treat unchanged content as a conflict on every
// re-harvest and accumulate duplicate .harvested-<ts> files.
func memoryEqual(aMeta storage.MemoryMeta, aBody string, bMeta storage.MemoryMeta, bBody string) bool {
	return aMeta.Name == bMeta.Name &&
		aMeta.Description == bMeta.Description &&
		aMeta.Type == bMeta.Type &&
		strings.TrimRight(aBody, "\n") == strings.TrimRight(bBody, "\n")
}

// conflictName turns "pref-foo.md" into "pref-foo.harvested-<ts>.md", inserting
// the suffix before the extension so the file keeps a .md extension.
func conflictName(rel string, ts int64) string {
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	if ext == "" {
		ext = ".md"
	}
	return fmt.Sprintf("%s.harvested-%d%s", stem, ts, ext)
}

// harvestCommitMessage builds the terse commit subject. The hostname stamp is
// appended by CommitAndPushPaths.
func harvestCommitMessage(routed, conflicted int) string {
	if conflicted > 0 {
		return fmt.Sprintf("memory harvest: route %d, %d conflict(s)", routed, conflicted)
	}
	if routed == 0 {
		// Nothing routed from the native dir — the commit carries memory written
		// directly to the vault (e.g. via vp_memory_write).
		return "memory harvest: commit pending memory writes"
	}
	return fmt.Sprintf("memory harvest: route %d memory file(s)", routed)
}
