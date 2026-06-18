// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// copyAgentctx migrates one vibe-vault project's agentctx tree into the
// destination vault, remapping the legacy `agentctx/<x>` layout onto the
// vibe-palace `Projects/<slug>/<x>` layout.
//
// Two tiers:
//
//   - Load-bearing data → canonical dest slots: resume.md, iterations.md,
//     workflow.md, knowledge.md (project-level), tasks/ + tasks/done/,
//     and memory/.
//   - Preserve-verbatim → Projects/<slug>/migrated/: commands/, skills/,
//     snippets/, features.md. vibe-palace does not load these (its commands
//     are global templates + shims), but they carry unique authored content
//     (e.g. res_* domain commands) that must survive VibeVault's retirement.
//
// Copy-if-absent by default; opts.Force overwrites. Host/meta artifacts
// (config.toml, commit.msg, .version, .surface, CLAUDE.md, *.baseline,
// .gitkeep) are never copied. Pure file IO — no embedder or search engine.
// When opts.DryRun is set, nothing is written; the result still reports what
// WOULD be copied or skipped (crown-jewel skips included).
//
// sourceProjDir is the project's (absolute) directory under the source
// vault's Projects/ tree — the same dirPath the session loop iterates.
// projSlug is the post-remap canonical slug, so agentctx lands in the same
// dir as the project's captured sessions.
func copyAgentctx(destination *storage.Vault, sourceProjDir, projSlug string, opts ImportOptions) (AgentctxResult, error) {
	var res AgentctxResult

	agentctxDir := filepath.Join(sourceProjDir, "agentctx")
	if fi, err := os.Stat(agentctxDir); err != nil || !fi.IsDir() {
		// No agentctx to carry (some source projects only hold sessions).
		return res, nil
	}

	destRoot, err := destination.ProjectDir(projSlug)
	if err != nil {
		return res, fmt.Errorf("dest project dir: %w", err)
	}

	// Tier 1 — load-bearing singletons → canonical dest accessors. The
	// crown jewels (resume/iterations/workflow/knowledge) report a present
	// destination loudly so a scaffold placeholder can never silently
	// shadow real history.
	singles := []struct {
		src        string // relative to the source project dir
		destAbs    string
		crownJewel bool
	}{}
	add := func(src string, destFn func(string) (string, error), crown bool) error {
		dest, derr := destFn(projSlug)
		if derr != nil {
			return derr
		}
		singles = append(singles, struct {
			src        string
			destAbs    string
			crownJewel bool
		}{src, dest, crown})
		return nil
	}
	for _, s := range []struct {
		src    string
		destFn func(string) (string, error)
	}{
		{"agentctx/resume.md", destination.ResumeFile},
		{"agentctx/iterations.md", destination.IterationsFile},
		{"agentctx/workflow.md", destination.WorkflowFile},
		{"knowledge.md", destination.KnowledgeFile}, // project-level, not under agentctx/
	} {
		if err := add(s.src, s.destFn, true); err != nil {
			return res, err
		}
	}
	// features.md is preserved (vibe-palace has no features slot) — route
	// it into the verbatim archive rather than a load-bearing slot.
	singles = append(singles, struct {
		src        string
		destAbs    string
		crownJewel bool
	}{"agentctx/features.md", filepath.Join(destRoot, "migrated", "features.md"), false})

	for _, s := range singles {
		srcAbs := filepath.Join(sourceProjDir, s.src)
		if err := copyOne(destination.Root, srcAbs, s.destAbs, s.crownJewel, opts, &res); err != nil {
			return res, err
		}
	}

	// Tier 1 — tasks: active → tasks/, done → tasks/done/ (one level each,
	// .baseline/.gitkeep excluded). done/ is handled explicitly so the
	// active scan can skip it.
	tasksDir, err := destination.TasksDir(projSlug)
	if err != nil {
		return res, err
	}
	doneDir, err := destination.TaskDoneDir(projSlug)
	if err != nil {
		return res, err
	}
	if err := copyDirFlat(destination.Root, filepath.Join(agentctxDir, "tasks"), tasksDir, opts, &res); err != nil {
		return res, err
	}
	if err := copyDirFlat(destination.Root, filepath.Join(agentctxDir, "tasks", "done"), doneDir, opts, &res); err != nil {
		return res, err
	}

	// Tier 1 — memory tree → Projects/<slug>/memory/ (recursive).
	if err := copyTree(destination.Root, filepath.Join(agentctxDir, "memory"), filepath.Join(destRoot, "memory"), opts, &res); err != nil {
		return res, err
	}

	// Tier 2 — verbatim archive → Projects/<slug>/migrated/<x>/ (recursive).
	for _, sub := range []string{"commands", "skills", "snippets"} {
		if err := copyTree(destination.Root, filepath.Join(agentctxDir, sub), filepath.Join(destRoot, "migrated", sub), opts, &res); err != nil {
			return res, err
		}
	}

	sort.Strings(res.CopiedPaths)
	sort.Strings(res.SkippedPaths)
	sort.Strings(res.CrownJewelSkipped)
	return res, nil
}

// copyMetaName reports whether a filename is host/meta scaffolding that
// must never be carried (it is regenerated by vibe-palace).
func copyMetaName(name string) bool {
	if strings.HasSuffix(name, ".baseline") {
		return true
	}
	switch name {
	case ".gitkeep", ".version", ".surface", "config.toml", "commit.msg", "CLAUDE.md":
		return true
	}
	return false
}

// copyOne copies a single source file to destAbs, copy-if-absent unless
// opts.Force. A missing source is a no-op. Crown-jewel skips (present dest)
// are recorded separately for loud reporting. Honors opts.DryRun.
func copyOne(destRoot, srcAbs, destAbs string, crownJewel bool, opts ImportOptions, res *AgentctxResult) error {
	info, err := os.Stat(srcAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to copy
		}
		return fmt.Errorf("stat %s: %w", srcAbs, err)
	}
	if info.IsDir() {
		return nil // singles are files; dirs handled elsewhere
	}

	rel := vaultRel(destRoot, destAbs)

	if _, err := os.Stat(destAbs); err == nil && !opts.Force {
		res.Skipped++
		res.SkippedPaths = append(res.SkippedPaths, rel)
		if crownJewel {
			res.CrownJewelSkipped = append(res.CrownJewelSkipped, rel)
		}
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", destAbs, err)
	}

	if !opts.DryRun {
		data, rerr := os.ReadFile(srcAbs)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", srcAbs, rerr)
		}
		if derr := storage.EnsureDir(filepath.Dir(destAbs)); derr != nil {
			return fmt.Errorf("ensure dir for %s: %w", rel, derr)
		}
		if werr := atomicfile.Write(destRoot, destAbs, data); werr != nil {
			return fmt.Errorf("write %s: %w", rel, werr)
		}
	}
	res.Copied++
	res.Bytes += info.Size()
	res.CopiedPaths = append(res.CopiedPaths, rel)
	return nil
}

// copyDirFlat copies the immediate *.md files of srcDir into destDir
// (non-recursive), excluding meta files. A missing srcDir is a no-op.
func copyDirFlat(destRoot, srcDir, destDir string, opts ImportOptions, res *AgentctxResult) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read dir %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || copyMetaName(e.Name()) || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dest := filepath.Join(destDir, e.Name())
		if err := copyOne(destRoot, src, dest, false, opts, res); err != nil {
			return err
		}
	}
	return nil
}

// copyTree recursively copies srcDir into destDir, preserving relative
// structure and excluding meta files. A missing or empty srcDir is a no-op.
func copyTree(destRoot, srcDir, destDir string, opts ImportOptions, res *AgentctxResult) error {
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == srcDir {
				return filepath.SkipDir // missing tree → no-op
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if copyMetaName(d.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(srcDir, path)
		if rerr != nil {
			return rerr
		}
		return copyOne(destRoot, path, filepath.Join(destDir, rel), false, opts, res)
	})
}

// vaultRel renders an absolute dest path as a vault-relative path for
// reporting (e.g. "Projects/foo/resume.md"). Falls back to the absolute
// path if it is not under destRoot.
func vaultRel(destRoot, abs string) string {
	if rel, err := filepath.Rel(destRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}
