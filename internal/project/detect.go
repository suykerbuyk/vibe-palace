// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

// ConfigFileName is the name of the per-project configuration file.
const ConfigFileName = ".vibe-palace.toml"

// manifestFiles are ecosystem manifest files that mark a directory as a
// project even without a `.git` directory. Order is stable/alphabetical;
// detection picks the first one found.
var manifestFiles = []string{
	"Cargo.toml",
	"go.mod",
	"package.json",
	"pom.xml",
	"pyproject.toml",
}

// ProjectSignal identifies which evidence marked a directory as a project.
// Empty string means "no signal"; forceSkip callers should test that first.
type ProjectSignal string

const (
	SignalNone          ProjectSignal = ""
	SignalVibeConfig    ProjectSignal = ".vibe-palace.toml"
	SignalGit           ProjectSignal = ".git"
	SignalCargoToml     ProjectSignal = "Cargo.toml"
	SignalGoMod         ProjectSignal = "go.mod"
	SignalPackageJSON   ProjectSignal = "package.json"
	SignalPomXML        ProjectSignal = "pom.xml"
	SignalPyprojectToml ProjectSignal = "pyproject.toml"
)

// DetectSignal returns the first project signal found in dir (non-recursive
// for manifest files; walking upward for .vibe-palace.toml). If dir resolves
// to $HOME or the filesystem root, returns SignalNone regardless — those
// directories are force-skipped because running project-init there would
// pollute unrelated state.
//
// Precedence (first match wins):
//  1. .vibe-palace.toml (cwd or any parent — re-init case)
//  2. .git/ in cwd
//  3. Manifest files in cwd, in the order given by manifestFiles
func DetectSignal(dir string) ProjectSignal {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return SignalNone
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	resolved = filepath.Clean(resolved)

	if isForceSkipDir(resolved) {
		return SignalNone
	}

	homeBoundary, _ := resolvedHome()
	if _, err := findMarkerUpward(resolved, homeBoundary); err == nil {
		return SignalVibeConfig
	}

	if _, err := os.Stat(filepath.Join(resolved, ".git")); err == nil {
		return SignalGit
	}

	for _, name := range manifestFiles {
		if _, err := os.Stat(filepath.Join(resolved, name)); err == nil {
			return ProjectSignal(name)
		}
	}

	return SignalNone
}

// resolvedHome returns the symlink-resolved, cleaned absolute path of the
// user's home directory, and whether it could be determined. It is both the
// force-skip target (isForceSkipDir) and the boundary at which the
// .vibe-palace.toml marker walk stops (findMarkerUpward): a marker located
// exactly at $HOME is ignored and the walk never climbs above it. Resolving
// symlinks here keeps the boundary comparison exact even when $HOME itself is
// a symlink (as it was for the dotfile-manager case that motivated this).
func resolvedHome() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		resolved = home
	}
	return filepath.Clean(resolved), true
}

// isForceSkipDir reports whether dir is a directory where project-init
// must never run regardless of signals present (user's $HOME or fs root).
func isForceSkipDir(dir string) bool {
	if dir == "/" {
		return true
	}
	if home, ok := resolvedHome(); ok && home == dir {
		return true
	}
	return false
}

// ProjectConfig holds project identity parsed from a .vibe-palace.toml file.
type ProjectConfig struct {
	Name   string   `toml:"name"`
	Domain string   `toml:"domain"`
	Tags   []string `toml:"tags"`
}

// ProjectFile is the top-level TOML structure for .vibe-palace.toml.
// It carries the [project] block and an optional top-level vault_path
// override. Other top-level keys are ignored.
type ProjectFile struct {
	Project   ProjectConfig `toml:"project"`
	VaultPath string        `toml:"vault_path"`
}

// DetectProject determines the project name for the given working directory.
// Detection priority:
//  1. .vibe-palace.toml in cwd or any parent directory
//  2. Git remote URL heuristics (origin remote)
//  3. Directory basename of cwd
//
// The returned name is validated as a slug. Callers that must not invent a
// slug on a weak signal should use DetectProjectHighConfidence instead.
func DetectProject(cwd string) (string, error) {
	if slug, err := DetectProjectHighConfidence(cwd); err == nil {
		return slug, nil
	} else if !strings.Contains(err.Error(), "no high-confidence project signal") {
		// Invalid config name etc. — preserve DetectProject's historical
		// fail-loud behavior rather than falling through to basename.
		// err is non-nil here because the == nil branch returned.
		return "", err
	}

	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	// Strategy 3: directory basename.
	slug := slugify(filepath.Base(cwd))
	if slug == "" {
		return "", fmt.Errorf("cannot detect project name from directory %q", cwd)
	}
	if err := slugpkg.Validate(slug); err != nil {
		return "", fmt.Errorf("directory basename: %w", err)
	}
	return slug, nil
}

// DetectProjectHighConfidence determines the project name for cwd using only
// high-confidence signals — never the directory-basename fallback that
// DetectProject uses as strategy 3. Priority:
//  1. .vibe-palace.toml [project].name in cwd or any parent (bounded at $HOME)
//  2. Git origin remote repository name
//
// Callers that would poison a whole session on a wrong default (e.g.
// vp_bootstrap_context) must use this, not DetectProject. A basename guess
// almost never errors and invents phantom slugs for worktrees whose folder
// name differs from the vault project (ADR-006: absence is not a value).
//
// Strategies 1–2 live only here; DetectProject calls this then adds basename.
func DetectProjectHighConfidence(cwd string) (string, error) {
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}

	// Strategy 1: config file walk, bounded at $HOME so a stray
	// ~/.vibe-palace.toml does not name every directory under the home tree
	// (see the home-marker hardening). The walk is symlink-resolved to match
	// the resolved home boundary; git strategy below keeps using the
	// unresolved cwd, preserving existing behavior.
	markerStart := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		markerStart = filepath.Clean(resolved)
	}
	homeBoundary, _ := resolvedHome()
	if configPath, err := findMarkerUpward(markerStart, homeBoundary); err == nil {
		cfg, err := ParseProjectConfig(configPath)
		if err == nil && cfg.Name != "" {
			if err := slugpkg.Validate(cfg.Name); err != nil {
				return "", fmt.Errorf("project name from config: %w", err)
			}
			return cfg.Name, nil
		}
		// Config exists but has no usable name — fall through to git.
	}

	// Strategy 2: git remote heuristics.
	if name, err := gitRemoteName(cwd); err == nil {
		slug := slugify(name)
		if slug != "" && slugpkg.Validate(slug) == nil {
			return slug, nil
		}
	}

	return "", fmt.Errorf("no high-confidence project signal at %q (need %s [project].name or a git origin remote); pass project explicitly or call vp_list_projects", cwd, ConfigFileName)
}

// resolveDir returns the symlink-resolved, cleaned absolute form of dir — the
// same normalization DetectSignal applies — so a force-skip or boundary
// comparison against it is exact.
func resolveDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// RequireKnownProject reports whether vault artifacts may be written for slug on
// behalf of the project rooted at repoRoot. It authorizes on EITHER signal of
// legitimacy — a .vibe-palace.toml marker at/above repoRoot whose
// [project].name IS slug, or an already-existing Projects/<slug>/ directory in
// the vault — and refuses only when BOTH are absent. It is the
// write-authorization gate the commit/archive tools and `vp absorb` /
// `vp memory harvest` lacked: those tools derive a slug by cwd basename and
// then lazily scaffold Projects/<slug>/ on first write, so running one in any
// unmanaged directory silently materializes a phantom vault project.
//
// 🔴 The marker arm turns on the NAME, never on presence. A marker is evidence
// for exactly the project it names: standing in a correctly-marked repo for
// project "real" and passing slug "typo" is the phantom-scaffold class this
// gate exists to close, and presence alone would authorize it. A marker whose
// [project].name is empty, unreadable, or not a valid slug names no project, so
// it is not evidence either — those cases fall through to the exists check
// rather than authorizing.
//
// A name MISMATCH falls through, it does not refuse: Projects/<slug>/ existing
// is independent evidence, so a rename in progress (marker edited before the
// vault directory is moved) and a deliberate cross-project write from inside a
// marked repo both keep working. What no longer passes is a slug that is
// neither the marker's project nor an existing one.
//
// The name check deliberately does NOT go through DetectProjectHighConfidence:
// that falls back to the git origin basename, which would re-authorize on a
// signal nobody wrote to name a vault project — the junk-project class again,
// one strategy over.
//
// "marker OR exists" is deliberately weaker than the hook's marker-only gate:
// the hook is opportunistic (a false negative is a harmless skipped capture),
// whereas these tools run during a wrap where a false negative BREAKS it — and
// legitimate projects exist in Projects/<slug>/ without a repo-side marker
// (vp init's vault-tree creation is best-effort and skipped on re-init).
//
// The $HOME / filesystem-root force-skip takes precedence over the exists
// branch: a stray Projects/<home-basename>/ left by an earlier mis-scaffold must
// never re-authorize a write rooted at the home tree (the residue this gate
// exists to stop). Pass the resolved repo ROOT, not a subdirectory, so the
// marker walk keys on the right directory.
//
// 🔴 An EMPTY repoRoot means "no repo context", and only the exists branch is
// consulted. It does NOT mean "use the current directory". Callers like
// vp_manage_task take an explicit project SLUG and no path at all, so there is
// no repo to authorize against — and the process cwd is the wrong answer
// twice over: `vp mcp` is long-lived and its cwd is the AI host's launch
// directory rather than any project, and if that happens to be $HOME the
// force-skip above would refuse every write, including for projects that
// plainly exist. Deriving a directory from an absent one is the wrong-vault
// class the bound-vault pattern already rules out; this makes the absence
// explicit instead of letting filepath.Abs("") answer it.
func RequireKnownProject(slug, vaultRoot, repoRoot string) error {
	if strings.TrimSpace(repoRoot) == "" {
		if fi, err := os.Stat(filepath.Join(vaultRoot, "Projects", slug)); err == nil && fi.IsDir() {
			return nil
		}
		return fmt.Errorf(
			"refusing to write vault artifacts for project %q: no Projects/%s/ in the vault.\n"+
				"This write named the project by slug and carries no repo path, so the slug is "+
				"the only evidence there is — and an unknown one is far more likely a typo or a "+
				"hallucination than a new project.\n"+
				"If the project is real, run `vp init` in its directory first; otherwise check "+
				"the spelling against `vp_list_projects`.",
			slug, slug)
	}
	resolved := resolveDir(repoRoot)
	if isForceSkipDir(resolved) {
		return fmt.Errorf("refusing to write vault artifacts for %q: it is the home directory or filesystem root, not a project — run `vp init` in a project directory", repoRoot)
	}
	homeBoundary, _ := resolvedHome()
	markerPath, markerErr := findMarkerUpward(resolved, homeBoundary)
	// A marker authorizes the project it NAMES and no other. An empty,
	// unreadable or invalid-slug name names nothing, so markerName stays empty
	// and the arm contributes no evidence.
	markerName := ""
	if markerErr == nil {
		if cfg, err := ParseProjectConfig(markerPath); err == nil {
			if name := strings.TrimSpace(cfg.Name); slugpkg.Validate(name) == nil {
				markerName = name
			}
		}
	}
	if markerName != "" && markerName == slug {
		return nil
	}
	if fi, err := os.Stat(filepath.Join(vaultRoot, "Projects", slug)); err == nil && fi.IsDir() {
		return nil
	}
	switch {
	case markerErr != nil:
		return fmt.Errorf("refusing to write vault artifacts for %q: no %s marker and no Projects/%s/ in the vault — run `vp init` first", repoRoot, ConfigFileName, slug)
	case markerName == "":
		return fmt.Errorf(
			"refusing to write vault artifacts for project %q: the marker %s names no usable project "+
				"(its [project].name is empty or not a valid slug), and there is no Projects/%s/ in the vault.\n"+
				"A marker that names no project is not evidence for this slug — set [project].name, or check "+
				"the spelling against `vp_list_projects`.",
			slug, markerPath, slug)
	default:
		return fmt.Errorf(
			"refusing to write vault artifacts for project %q: the marker %s names project %q, not %q, "+
				"and there is no Projects/%s/ in the vault.\n"+
				"A marker authorizes only the project it names. If %q is real, run `vp init` in its "+
				"directory first; otherwise check the spelling against `vp_list_projects`.",
			slug, markerPath, markerName, slug, slug, slug)
	}
}

// ParseProjectConfig parses a .vibe-palace.toml file and returns the project
// configuration. Returns an error if the file cannot be read or is invalid TOML.
// Unknown top-level keys are tolerated.
func ParseProjectConfig(path string) (ProjectConfig, error) {
	pf, err := ParseProjectFile(path)
	if err != nil {
		return ProjectConfig{}, err
	}
	return pf.Project, nil
}

// ParseProjectFile parses a .vibe-palace.toml file and returns the full
// file contents, including any top-level vault_path override. Unknown
// top-level keys are tolerated.
func ParseProjectFile(path string) (ProjectFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectFile{}, fmt.Errorf("read config: %w", err)
	}

	var pf ProjectFile
	if err := toml.Unmarshal(data, &pf); err != nil {
		return ProjectFile{}, fmt.Errorf("parse config: %w", err)
	}
	return pf, nil
}

// findMarkerUpward walks from dir toward the filesystem root looking for a
// .vibe-palace.toml marker, but STOPS at the home boundary: it never inspects
// or climbs above $HOME. This honors the marker-file template's promise that a
// file at $HOME/.vibe-palace.toml is ignored — so a single stray marker in the
// home directory (e.g. linked there by a dotfile manager) does not enroll the
// entire home tree into auto-capture.
//
// dir must already be symlink-resolved by the caller so the boundary comparison
// is exact. A zero homeBoundary disables the stop (used only when the home
// directory cannot be determined), degrading to findFileUpward's walk-to-root.
// This bound applies to the marker walk ONLY; the .git walk in gitRemoteName
// still climbs to the filesystem root, since a project legitimately lives above
// $HOME on some hosts.
func findMarkerUpward(dir, homeBoundary string) (string, error) {
	for {
		if homeBoundary != "" && dir == homeBoundary {
			return "", fmt.Errorf("%s not found (stopped at home boundary)", ConfigFileName)
		}
		candidate := filepath.Join(dir, ConfigFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root.
			return "", fmt.Errorf("%s not found", ConfigFileName)
		}
		dir = parent
	}
}

// findFileUpward walks from dir toward the filesystem root looking for a file
// with the given name. Returns the full path if found, or an error.
func findFileUpward(dir, filename string) (string, error) {
	for {
		candidate := filepath.Join(dir, filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root.
			return "", fmt.Errorf("%s not found", filename)
		}
		dir = parent
	}
}

// gitRemoteName extracts the repository name from the origin remote URL of
// the git repository containing dir. It walks upward to find .git first.
func gitRemoteName(dir string) (string, error) {
	// Find the git root by walking upward for .git.
	gitDir, err := findFileUpward(dir, ".git")
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	repoRoot := filepath.Dir(gitDir)

	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote: %w", err)
	}
	return extractRepoName(strings.TrimSpace(string(out))), nil
}

// extractRepoName extracts the repository name from a git remote URL.
// Handles SSH (git@host:user/repo.git) and HTTPS (https://host/user/repo.git).
func extractRepoName(url string) string {
	// Strip trailing .git suffix.
	url = strings.TrimSuffix(url, ".git")

	// SSH format: git@github.com:user/repo
	if idx := strings.LastIndex(url, ":"); idx != -1 && !strings.Contains(url, "://") {
		url = url[idx+1:]
	}

	// Take the last path segment.
	if idx := strings.LastIndex(url, "/"); idx != -1 {
		return url[idx+1:]
	}
	return url
}

// nonAlphanumeric matches sequences of characters that are not lowercase
// alphanumeric.
var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a name to a valid slug: lowercase, non-alphanumeric runs
// replaced with single hyphens, leading/trailing hyphens trimmed.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
