// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"

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
// Returns the config file path, resolved vault path, and the check result.
func CheckConfig() (configPath, vaultPath string, r Result) {
	r.Name = "Config"

	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("cannot resolve config dir: %v", err)
		r.Err = err
		return
	}

	vaultPath, err = storage.VaultRoot("")
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("%s: %v", configPath, err)
		r.Err = err
		return
	}

	r.Status = Pass
	r.Summary = configPath
	r.Details = []string{fmt.Sprintf("vault_path = %s", vaultPath)}
	return
}

// CheckVault verifies the vault directory exists.
func CheckVault(vaultPath string) Result {
	r := Result{Name: "Vault"}

	info, err := os.Stat(vaultPath)
	if err != nil {
		r.Status = Fail
		r.Summary = fmt.Sprintf("%s does not exist", vaultPath)
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
