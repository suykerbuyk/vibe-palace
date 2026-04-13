// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
func CheckConfig() (configPath, vaultPath string, r Result) {
	r.Name = "Config"

	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("cannot resolve config dir: %v", err)
		r.Err = err
		return
	}

	cwd, _ := os.Getwd()
	var source string
	vaultPath, source, err = storage.ResolveVaultPath(cwd)
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

	r.Status = Pass
	r.Summary = fmt.Sprintf("ONNX loaded, %d dimensions", emb.Dimensions())
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
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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
func CheckProject() Result {
	r := Result{Name: "Project"}
	r.Status = Info

	slug, err := project.DetectProject(".")
	if err != nil {
		r.Summary = "no project detected (not in a project directory)"
		return r
	}

	// Determine which detection strategy was used.
	cwd, _ := os.Getwd()
	if _, cfgErr := findProjectConfig(cwd); cfgErr == nil {
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

func hasNonWhitespace(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
	}
	return false
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
