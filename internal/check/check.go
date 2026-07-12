// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Status represents the outcome of a diagnostic check.
type Status int

const (
	Pass Status = iota
	Fail
	Skip
	Info
)

// Result holds the outcome of a single diagnostic check.
type Result struct {
	Name    string
	Status  Status
	Summary string
	Details []string
	Err     error
}

// Run executes all diagnostic checks and returns ordered results.
// The embedder check may block for minutes on first run (model download).
func Run() []Result {
	var results []Result

	configPath, vaultPath, r := CheckConfig()
	results = append(results, r)

	if r.Status == Fail {
		results = append(results,
			Result{Name: "Vault", Status: Skip},
			Result{Name: "Settings", Status: Skip},
			Result{Name: "Embedder", Status: Skip},
		)
		results = append(results, CheckProject())
		return results
	}

	r = CheckVault(vaultPath)
	results = append(results, r)
	if r.Status == Fail {
		results = append(results,
			Result{Name: "Settings", Status: Skip},
			Result{Name: "Embedder", Status: Skip},
		)
		results = append(results, CheckProject())
		return results
	}

	v := storage.NewVault(vaultPath)

	cfg, r := CheckSettings(v)
	results = append(results, r)
	if r.Status == Fail {
		results = append(results, Result{Name: "Embedder", Status: Skip})
		results = append(results, CheckProject())
		return results
	}

	results = append(results, CheckEmbedder(cfg, v, configPath))
	results = append(results, CheckProject())
	return results
}

// CheckConfig verifies the config file can be found and parsed.
// Returns the global config file path, the resolved vault path (honoring
// any cwd-local .vibe-palace.toml override), and the check result.
//
// Uses os.Getwd() to resolve any cwd-local override. For explicit-root
// callers (reconcilers, tests), use CheckConfigAt.
func CheckConfig() (configPath, vaultPath string, r Result) {
	cwd, _ := os.Getwd()
	return CheckConfigAt(cwd)
}

// CheckConfigAt is the explicit-root variant of CheckConfig. The root
// argument is used in place of os.Getwd() when resolving any cwd-local
// .vibe-palace.toml vault_path override.
func CheckConfigAt(root string) (configPath, vaultPath string, r Result) {
	r.Name = "Config"

	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("cannot resolve config dir: %v", err)
		r.Err = err
		return
	}

	var source string
	vaultPath, source, err = storage.ResolveVaultPath(root)
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("not found at %s", configPath)
		r.Details = []string{"Run 'vp init' to create config and vault."}
		r.Err = err
		return
	}

	r.Status = Pass
	r.Summary = configPath
	r.Details = []string{
		fmt.Sprintf("vault_path = %s", vaultPath),
		fmt.Sprintf("vault_path source = %s", source),
	}
	return
}

// CheckVault verifies the vault directory exists.
func CheckVault(vaultPath string) Result {
	r := Result{Name: "Vault"}

	info, err := os.Stat(vaultPath)
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("%s does not exist", vaultPath)
		r.Details = []string{"Run 'vp init' to create vault."}
		r.Err = err
		return r
	}
	if !info.IsDir() {
		r.Status = Fail
		r.Summary = fmt.Sprintf("%s is not a directory", vaultPath)
		r.Err = fmt.Errorf("%s is not a directory", vaultPath)
		return r
	}

	r.Status = Pass
	r.Summary = fmt.Sprintf("%s (exists)", vaultPath)
	return r
}

// CheckSettings verifies that configuration loads successfully.
// Returns the loaded config and the check result.
func CheckSettings(v *storage.Vault) (storage.Config, Result) {
	r := Result{Name: "Settings"}

	cfg, err := v.LoadConfig("")
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("load config: %v", err)
		r.Err = err
		return cfg, r
	}

	r.Status = Pass
	r.Summary = fmt.Sprintf("model=%s  search_limit=%d", cfg.EmbedderModel, cfg.SearchDefaultLimit)
	return cfg, r
}

// CheckEmbedder verifies the ONNX model can be loaded.
// This may download the model on first run (~90MB).
func CheckEmbedder(cfg storage.Config, v *storage.Vault, configPath string) Result {
	r := Result{Name: "Embedder"}

	modelDir := v.VaultLocalDir() + "/models"
	emb, err := embedder.NewONNX(
		cfg.EmbedderModel, modelDir,
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("load model: %v", err)
		r.Err = err
		return r
	}
	defer emb.Close()

	dims, err := emb.Dimensions()
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("embedder dimensions: %v", err)
		r.Err = err
		return r
	}

	r.Status = Pass
	r.Summary = fmt.Sprintf("ONNX loaded, %d dimensions", dims)
	return r
}

// CheckGit checks git availability and vault repo status.
// This always returns Info or Pass — git is optional, never Fail.
func CheckGit(vaultPath string, gitEnabled bool) Result {
	r := Result{Name: "Git"}

	if !gitEnabled {
		r.Status = Info
		r.Summary = "disabled (git_enabled = false)"
		return r
	}

	if !storage.GitAvailable() {
		r.Status = Info
		r.Summary = "git not found in PATH"
		return r
	}

	if !storage.GitIsRepo(vaultPath) {
		r.Status = Info
		r.Summary = "vault is not a git repository"
		return r
	}

	// Check for configured remotes.
	cmd := exec.Command("git", "-C", vaultPath, "remote")
	out, err := cmd.Output()
	if err != nil {
		r.Status = Info
		r.Summary = "no remotes configured"
		return r
	}

	var remotes []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			remotes = append(remotes, line)
		}
	}
	if len(remotes) == 0 {
		r.Status = Info
		r.Summary = "no remotes configured"
		return r
	}

	r.Status = Pass
	r.Summary = fmt.Sprintf("remotes: %s", strings.Join(remotes, ", "))
	return r
}

// CheckConfigStaleness checks whether the user's config is missing keys
// present in the canonical defaults. Returns Info with a list of missing
// settings, or Skip if the config is up to date.
func CheckConfigStaleness(configPath string) Result {
	r := Result{Name: "Config Staleness"}

	missing, err := storage.DetectMissingKeys(configPath)
	if err != nil {
		r.Status = Skip
		r.Summary = fmt.Sprintf("cannot check: %v", err)
		return r
	}

	if len(missing) == 0 {
		r.Status = Pass
		r.Summary = "config is up to date"
		return r
	}

	r.Status = Info
	r.Summary = fmt.Sprintf("%d new setting(s) available", len(missing))
	r.Details = append(r.Details, "Run 'vp config upgrade' to add them.")
	for _, k := range missing {
		r.Details = append(r.Details, "  "+k)
	}
	return r
}

// CheckProject checks whether a project is detected in the current directory.
// This always returns Info status — it never causes a failure.
//
// Uses os.Getwd() as the project root. For explicit-root callers
// (reconcilers, tests), use CheckProjectAt.
func CheckProject() Result {
	cwd, _ := os.Getwd()
	return CheckProjectAt(cwd)
}

// CheckProjectAt is the explicit-root variant of CheckProject. The root
// argument is used in place of os.Getwd() when detecting the project and
// deciding which detection strategy surfaced it.
func CheckProjectAt(root string) Result {
	r := Result{Name: "Project"}
	r.Status = Info

	slug, err := project.DetectProject(root)
	if err != nil {
		r.Summary = "no project detected (not in a project directory)"
		return r
	}

	if _, cfgErr := findProjectConfig(root); cfgErr == nil {
		r.Summary = fmt.Sprintf("%s (from %s)", slug, project.ConfigFileName)
	} else {
		r.Summary = fmt.Sprintf("%s (from directory name)", slug)
	}
	return r
}

// CheckAgentDrift reports whether any detected agent-context file in
// projectRoot contains non-whitespace content outside the managed
// vibe-palace block. This is a warning-only check (Status: Info) — the
// user can migrate via `vp absorb`, or suppress per-file with a
// `<!-- vibe-palace:allow-local -->` marker.
func CheckAgentDrift(projectRoot string) Result {
	r := Result{Name: "Agent-file drift"}
	r.Status = Pass

	targets, _ := agentfile.Detect(projectRoot)
	if len(targets) == 0 {
		r.Status = Skip
		r.Summary = "no agent-context files detected"
		return r
	}

	const allowMarker = "vibe-palace:allow-local"
	var dirty []string
	for _, t := range targets {
		data, err := os.ReadFile(t.Path)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte(allowMarker)) {
			continue
		}
		start, end := agentfile.FindBlock(data)
		var outside []byte
		if start < 0 {
			outside = data
		} else {
			outside = append([]byte{}, data[:start]...)
			if end <= len(data) {
				outside = append(outside, data[end:]...)
			}
		}
		if hasNonWhitespace(outside) {
			dirty = append(dirty, t.DisplayName)
		}
	}
	if len(dirty) == 0 {
		r.Summary = "all agent-context files are clean"
		return r
	}
	r.Status = Info
	r.Summary = fmt.Sprintf("%d file(s) hold content outside the managed block", len(dirty))
	for _, d := range dirty {
		r.Details = append(r.Details, "  "+d)
	}
	r.Details = append(r.Details,
		"Run `vp absorb` to migrate into the vault, or add `<!-- vibe-palace:allow-local -->` to suppress.")
	return r
}

// CheckProjectGitignore reports whether the project repo-root .gitignore
// is missing any of the canonical vp-owned host-local artifact patterns
// (CLAUDE.md, commit.msg, .claude/, .grok/, .vibe-palace/). This is an
// advisory check (Status: Info) — never Fail — pointing the user at
// `vp commands upgrade` (or `vp init`) to self-heal. A clean file is Pass.
func CheckProjectGitignore(projectRoot string) Result {
	r := Result{Name: "Project .gitignore", Status: Pass}
	missing, err := storage.MissingProjectGitignorePatterns(projectRoot)
	if err != nil {
		r.Status = Info
		r.Summary = "could not read .gitignore: " + err.Error()
		return r
	}
	if len(missing) == 0 {
		r.Summary = "host-local vp artifacts ignored"
		return r
	}
	r.Status = Info
	r.Summary = fmt.Sprintf("%d canonical entry(ies) missing", len(missing))
	for _, m := range missing {
		r.Details = append(r.Details, "  "+m)
	}
	r.Details = append(r.Details,
		"Run `vp commands upgrade` (or `vp init`) to reconcile the project .gitignore.")
	return r
}

func hasNonWhitespace(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
	}
	return false
}

// CheckStrayScaffolds scans <vault>/Projects/* for scaffold-only orphan
// projects: directories that carry the init scaffold (config.toml) but have no
// real content — no resume.md, no iterations.md, no sessions/, and no task
// files under tasks/. These are typically the residue of a stray `vp init`
// against the wrong vault or an un-isolated test (the recurring `Projects/p/`).
// Always Info or Pass, never Fail — removal is a human decision. The .surface
// timestamp is surfaced as provenance so the operator can judge.
func CheckStrayScaffolds(v *storage.Vault) Result {
	r := Result{Name: "Stray scaffolds"}
	projectsDir := filepath.Join(v.Root, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	var stray []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		dir := filepath.Join(projectsDir, name)
		if !isScaffoldOnly(dir) {
			continue
		}
		row := name
		if s, serr := surface.ReadStamp(dir); serr == nil && s.LastWriteAt != "" {
			row = fmt.Sprintf("%s (scaffolded %s)", name, s.LastWriteAt)
		}
		stray = append(stray, row)
	}

	if len(stray) == 0 {
		r.Status = Pass
		r.Summary = "none"
		return r
	}
	sort.Strings(stray)
	r.Status = Info
	r.Summary = fmt.Sprintf("%d scaffold-only project(s) with no content", len(stray))
	r.Details = append([]string{
		"a stray `vp init` or an unused/un-isolated-test project — `rm -rf` from the vault if unwanted:",
	}, stray...)
	return r
}

// isScaffoldOnly reports whether a Projects/<slug> directory carries the init
// scaffold (config.toml) but no real content: no resume.md, no iterations.md,
// no non-empty sessions/, and no task files anywhere under tasks/.
func isScaffoldOnly(dir string) bool {
	if !fileExists(filepath.Join(dir, "config.toml")) {
		return false
	}
	if fileExists(filepath.Join(dir, "resume.md")) || fileExists(filepath.Join(dir, "iterations.md")) {
		return false
	}
	if dirHasAnyFile(filepath.Join(dir, "sessions")) {
		return false
	}
	if dirHasAnyFile(filepath.Join(dir, "tasks")) {
		return false
	}
	return true
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirHasAnyFile reports whether dir contains at least one regular file at any
// depth. A missing directory or one holding only empty subdirectories counts as
// "no files" (the scaffold's tasks/{done,cancelled}/ are empty dirs).
func dirHasAnyFile(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// findProjectConfig walks upward from dir looking for .vibe-palace.toml.
func findProjectConfig(dir string) (string, error) {
	for {
		candidate := dir + "/" + project.ConfigFileName
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not found")
		}
		dir = parent
	}
}
