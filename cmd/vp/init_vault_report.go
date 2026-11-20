// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// This file is `vp init`'s vault REPORTING layer. It owns no guard and writes
// nothing. Its job is attributability: a captured `vp init` transcript names
// the vault the project scaffold was written to and why, and never states a
// vault condition that this run did not compute.
//
// What it does NOT do, stated so nobody later reads it as more: it does not
// prevent a hand-run `vp init` from scaffolding and committing into the host
// default vault (that is `vp init`'s documented behaviour, and refusing
// without a flag was rejected by the operator), and it does not precede every
// vault write. The guarantee is exactly "named BEFORE PROJECT SCAFFOLDING AND
// ITS COMMIT". `vp init --vault-path <new dir>` creates and commits the new
// vault in initGlobal -> initVault first, by design, because the operator
// named that path.

// initVaultReport is where the immediate pre-scaffold line goes. It is a var
// so a test can observe the vault tree at the moment the line fires; stderr by
// default so a `> file` redirect of the status table does not hide it.
var initVaultReport = func(line string) { fmt.Fprintln(os.Stderr, line) }

// projectVault resolves the project's vault ONCE and hands the same answer to
// both the report and onboarding, so the vault named is the vault written.
// Two resolutions could disagree (cmd_status.go makes the same point), and a
// path printed for a vault that was not the one opened is worse than silence.
//
// 🔴 Resolution is lazy and MUST first happen after maybeBindProjectVault and
// maybeWriteLocalOverride: both change what the resolver returns.
type projectVault struct {
	dir     string
	resolve func(string) (storage.Resolution, error)

	once sync.Once
	res  storage.Resolution
	err  error
}

func newProjectVault(dir string) *projectVault {
	return &projectVault{dir: dir, resolve: storage.ResolveVaultBinding}
}

func (p *projectVault) get() (storage.Resolution, error) {
	p.once.Do(func() { p.res, p.err = p.resolve(p.dir) })
	return p.res, p.err
}

// Open is onboard.Request.OpenVault: the resolved root, resolved once.
func (p *projectVault) Open() (*storage.Vault, error) {
	res, err := p.get()
	if err != nil {
		return nil, err
	}
	return storage.NewVault(res.Path), nil
}

// Line is the one-line notice written just before project scaffolding.
func (p *projectVault) Line() string {
	res, err := p.get()
	if err != nil {
		return "vp init: project vault could not be resolved: " + err.Error()
	}
	return fmt.Sprintf("vp init: project vault = %s  (source: %s)", lineSafe(res.Path), lineSafe(res.Source))
}

// lineSafe Go-quotes a value that could forge or hide part of the one-line
// notice: any CR or LF, or leading/trailing whitespace. Ordinary paths are
// returned unchanged.
func lineSafe(v string) string {
	if strings.ContainsAny(v, "\r\n") || strings.TrimSpace(v) != v {
		return strconv.Quote(v)
	}
	return v
}

// Row is the status-table row built from the same resolution.
func (p *projectVault) Row() check.Result {
	res, err := p.get()
	if err != nil {
		return check.Result{Name: "Project vault", Status: check.Fail, Summary: err.Error()}
	}
	row := check.Result{Name: "Project vault", Status: check.Info, Summary: res.Path}
	row.Details = append(row.Details, "source: "+res.Source+" ("+tierGloss(res.Source)+")")
	if strings.HasPrefix(res.Source, "global:") {
		row.Details = append(row.Details,
			"no per-project binding applies: this project's scaffold is written and committed into the host default vault")
	}
	row.Details = append(row.Details, "reported before project scaffolding and its commit")
	row.Details = append(row.Details, res.Warnings...)
	return row
}

// tierGloss names the ADR-012 tier a Resolution.Source prefix denotes.
func tierGloss(source string) string {
	switch {
	case strings.HasPrefix(source, "cwd-local:"):
		return "tier 1, local override for this checkout"
	case strings.HasPrefix(source, "cwd:"):
		return "tier 1, legacy marker"
	case strings.HasPrefix(source, "binding:"):
		return "tier 2, [project_vaults] binding"
	case strings.HasPrefix(source, "global:"):
		return "tier 3, the host default vault"
	}
	return "unrecognised source"
}

// existingConfigVaultRow is the Vault row for an existing config when no new
// vault is being created. It states only what was computed here: no vault
// created, and what the host default resolves to. "Not set" is Info, not Fail:
// a host that only binds projects has no default; the Project vault row states
// the actual outcome.
func existingConfigVaultRow(fv *cli.FlagValues) check.Result {
	row := check.Result{Name: "Vault", Status: check.Info}
	root, src, err := storage.ResolveGlobalVaultPath()
	if err != nil {
		row.Summary = "no vault created; host default vault_path is not set or unreadable (" + err.Error() + ")"
	} else {
		row.Summary = "no vault created; existing config kept. Host default vault: " + root + " (" + src + ")"
		switch st, serr := os.Stat(root); {
		case errors.Is(serr, fs.ErrNotExist):
			row.Summary += " (directory does not exist)"
		case serr != nil:
			row.Summary += " (cannot be read: " + serr.Error() + ")"
		case !st.IsDir():
			row.Summary += " (not a directory)"
		}
	}
	if flag := fv.Get("--vault-path"); flag != "" {
		if abs, aerr := expandAndAbsPath(flag); aerr == nil {
			row.Details = append(row.Details, "--vault-path "+abs+" exists, so no vault is created there; "+
				"whether this project uses it is reported under Project binding / Project vault below")
		}
	}
	return row
}

// noGitInertRow reports --no-git on an existing config where no new vault is
// created. `--no-git` decides only whether a NEW vault gets a git repository;
// it never suppresses the project scaffold commit. Returns false when the flag
// is absent. Not called when a new vault is being created (the flag is honoured
// there).
func noGitInertRow(fv *cli.FlagValues) (check.Result, bool) {
	if !fv.Bool("--no-git") {
		return check.Result{}, false
	}
	const commit = "The project scaffold is still committed if the project vault is a git repository."
	if fv.Get("--vault-path") == "" {
		return check.Result{Name: "--no-git", Status: check.Skip,
			Summary: "ignored: it only decides whether a NEW vault gets a git repository, and none is being created. " + commit}, true
	}
	return check.Result{Name: "--no-git", Status: check.Info,
		Summary: "--vault-path is bound by a tier-1 local override (.vibe-palace/local.toml) rather than a " +
			"[project_vaults] git-vault binding; --no-git forces that choice. " + commit}, true
}
