// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// These tests pin `vp init`'s vault REPORTING (init_vault_report.go): the
// project vault is named, with its source, before project scaffolding and its
// commit; the "Vault" row states only what was computed; and an inert
// --no-git is reported.
//
// Every assertion that a printed line is RIGHT is paired with a fixture whose
// resolver answer differs, so a hardcoded or re-derived string cannot pass:
// the line must CHANGE when the resolver's answer changes. All fixtures use the
// real resolver and real config files under t.TempDir(); none touches an
// operator vault.

// reportProject is a project directory under HOME (resolution walks are bounded
// at $HOME) carrying a go.mod.
func reportProject(t *testing.T, home, name string) string {
	t.Helper()
	p := filepath.Join(home, "code", name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, p)
	return p
}

// recordReport swaps initVaultReport for a recorder and returns the captured
// lines. probe, when non-nil, runs at the moment each line is emitted.
func recordReport(t *testing.T, probe func(line string)) *[]string {
	t.Helper()
	var lines []string
	prev := initVaultReport
	t.Cleanup(func() { initVaultReport = prev })
	initVaultReport = func(l string) {
		lines = append(lines, l)
		if probe != nil {
			probe(l)
		}
	}
	return &lines
}

// unsetDefault rewrites the host config so it has NO default vault_path.
func unsetDefault(t *testing.T, cfg string) {
	t.Helper()
	writeTestFile(t, cfg, "# vault_path = \"x\"\ngit_enabled = true\n")
}

func TestInitReportsTheResolvedVaultAndItsSource(t *testing.T) {
	// Tier 3: two fixtures with DIFFERENT host defaults. A row that prints a
	// fixed or re-derived path cannot match both.
	for _, name := range []string{"first", "second"} {
		t.Run("tier3/"+name, func(t *testing.T) {
			home, cfg, def := existingHostEnv(t)
			proj := reportProject(t, home, "alpha")
			lines := recordReport(t, nil)

			code, out := runInit(t, proj, "--name", "alpha")
			if code != cli.ExitOK {
				t.Fatalf("exit = %d\n%s", code, out)
			}
			f := flat(out)
			if !strings.Contains(f, "[info] Project vault: "+def) {
				t.Errorf("Project vault row must name %s:\n%s", def, out)
			}
			if !strings.Contains(f, "source: global:"+cfg+" (tier 3, the host default vault)") {
				t.Errorf("row must carry source global:%s and the tier gloss:\n%s", cfg, out)
			}
			if len(*lines) != 1 || !strings.Contains((*lines)[0], def) || !strings.Contains((*lines)[0], "global:"+cfg) {
				t.Errorf("pre-scaffold line = %q, want one line naming %s and global:%s", *lines, def, cfg)
			}
			// The reported vault is the one written.
			if _, err := os.Stat(filepath.Join(def, "Projects", "alpha")); err != nil {
				t.Errorf("the reported vault %s was not the one scaffolded: %v", def, err)
			}
		})
	}

	t.Run("tier1/local-override", func(t *testing.T) {
		home, cfg, def := existingHostEnv(t)
		proj := reportProject(t, home, "beta")
		x := t.TempDir() // existing, not a git vault: init writes a tier-1 override
		lines := recordReport(t, nil)

		code, out := runInit(t, proj, "--name", "beta", "--vault-path", x)
		if code != cli.ExitOK {
			t.Fatalf("exit = %d\n%s", code, out)
		}
		f := flat(out)
		want := "source: cwd-local:" + filepath.Join(proj, ".vibe-palace", "local.toml") + " (tier 1, local override for this checkout)"
		if !strings.Contains(f, "[info] Project vault: "+x) || !strings.Contains(f, want) {
			t.Errorf("row must name %s with %q:\n%s", x, want, out)
		}
		if strings.Contains(f, "[info] Project vault: "+def) {
			t.Errorf("row names the host default %s though the override won", def)
		}
		if len(*lines) != 1 || !strings.Contains((*lines)[0], x) {
			t.Errorf("pre-scaffold line = %q, want it to name %s", *lines, x)
		}
		if _, err := os.Stat(filepath.Join(x, "Projects", "beta")); err != nil {
			t.Errorf("reported vault %s was not scaffolded: %v", x, err)
		}
		if _, err := os.Stat(filepath.Join(def, "Projects", "beta")); err == nil {
			t.Errorf("project leaked into the host default %s", def)
		}
		_ = cfg
	})

	t.Run("tier2/binding", func(t *testing.T) {
		home, cfg, _ := existingHostEnv(t)
		proj := reportProject(t, home, "gamma")
		x := filepath.Join(t.TempDir(), "quantum") // absent: init creates it, then binds tier 2
		recordReport(t, nil)

		code, out := runInit(t, proj, "--name", "gamma", "--vault-path", x)
		if code != cli.ExitOK {
			t.Fatalf("exit = %d\n%s", code, out)
		}
		f := flat(out)
		if !strings.Contains(f, "[info] Project vault: "+x) ||
			!strings.Contains(f, "source: binding:"+cfg+"#gamma (tier 2, [project_vaults] binding)") {
			t.Errorf("row must name %s with a binding source:\n%s", x, out)
		}
	})
}

// F1: the guarantee is "before project scaffolding and its commit", and the
// row says exactly that rather than "before any vault write".
func TestInitVaultReportFiresBeforeProjectScaffolding(t *testing.T) {
	home, _, def := existingHostEnv(t)
	proj := reportProject(t, home, "delta")
	var existedAtReport = true
	recordReport(t, func(string) {
		_, err := os.Stat(filepath.Join(def, "Projects", "delta"))
		existedAtReport = err == nil
	})

	code, out := runInit(t, proj, "--name", "delta")
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if existedAtReport {
		t.Errorf("Projects/delta already existed in %s when the vault was reported", def)
	}
	if _, err := os.Stat(filepath.Join(def, "Projects", "delta")); err != nil {
		t.Errorf("Projects/delta was never scaffolded: %v", err)
	}
	f := flat(out)
	if !strings.Contains(f, "reported before project scaffolding and its commit") {
		t.Errorf("row must state the precise guarantee:\n%s", out)
	}
	if strings.Contains(f, "before any vault write") {
		t.Errorf("row over-claims (initGlobal can create and commit a new vault first):\n%s", out)
	}
}

func TestProjectVaultResolvesOnce(t *testing.T) {
	var calls atomic.Int32
	pv := newProjectVault(t.TempDir())
	pv.resolve = func(string) (storage.Resolution, error) {
		calls.Add(1)
		return storage.Resolution{Path: "/the/vault", Source: "global:/c.toml"}, nil
	}
	_ = pv.Line()
	row := pv.Row()
	v1, _ := pv.Open()
	v2, _ := pv.Open()
	if n := calls.Load(); n != 1 {
		t.Errorf("resolver called %d times, want exactly 1", n)
	}
	if row.Summary != "/the/vault" || v1.Root != "/the/vault" || v2.Root != v1.Root {
		t.Errorf("row %q and opened roots %q, %q must be one answer", row.Summary, v1.Root, v2.Root)
	}
}

// F2: the legacy tier-1 source `cwd:` (an untracked marker carrying vault_path)
// through the REAL resolver, with its gloss and its migration warning.
func TestProjectVaultRowForTheLegacyMarkerSource(t *testing.T) {
	home, _, _ := existingHostEnv(t)
	proj := reportProject(t, home, "legacy")
	vault := t.TempDir()
	marker := filepath.Join(proj, ".vibe-palace.toml")
	writeTestFile(t, marker, "vault_path = \""+vault+"\"\n\n[project]\nname = \"legacy\"\n")

	row := newProjectVault(proj).Row()
	if row.Summary != vault {
		t.Fatalf("row path = %q, want %q (status %v, details %v)", row.Summary, vault, row.Status, row.Details)
	}
	d := flat(strings.Join(row.Details, "\n"))
	if !strings.Contains(d, "source: cwd:"+marker+" (tier 1, legacy marker)") {
		t.Errorf("legacy source and gloss missing: %v", row.Details)
	}
	if !strings.Contains(d, "DEPRECATED") {
		t.Errorf("the resolver's migration warning must reach the row: %v", row.Details)
	}
}

func TestInitDoesNotClaimAnUnverifiedVault(t *testing.T) {
	t.Run("host with no default vault_path", func(t *testing.T) {
		home, cfg, _ := existingHostEnv(t)
		unsetDefault(t, cfg)
		proj := reportProject(t, home, "epsilon")
		lines := recordReport(t, nil)

		_, out := runInit(t, proj, "--name", "epsilon")
		f := flat(out)
		if strings.Contains(f, "already configured") {
			t.Errorf("the unchecked line is back:\n%s", out)
		}
		if !strings.Contains(f, "[info] Vault: no vault created; host default vault_path is not set or unreadable") {
			t.Errorf("Vault row must say the default is not set:\n%s", out)
		}
		if !strings.Contains(f, "[FAIL] Project vault: ") || !strings.Contains(f, "vault_path not set") {
			t.Errorf("resolution failure must be its own FAIL row:\n%s", out)
		}
		if len(*lines) != 1 || !strings.Contains((*lines)[0], "could not be resolved") {
			t.Errorf("pre-scaffold line = %q, want a could-not-resolve notice", *lines)
		}
	})

	t.Run("no contradiction above a failed binding", func(t *testing.T) {
		home, cfg, _ := existingHostEnv(t)
		unsetDefault(t, cfg)
		proj := reportProject(t, home, "zeta")
		x := t.TempDir()
		makeGitVaultWithOrigin(t, x) // stamped git vault: tier-2 bind is attempted and refused

		_, out := runInit(t, proj, "--name", "zeta", "--vault-path", x)
		f := flat(out)
		iv, ib := strings.Index(f, "] Vault:"), strings.Index(f, "[FAIL] Project binding:")
		if iv < 0 || ib < 0 || iv > ib {
			t.Fatalf("want the Vault row above a failed Project binding row (Vault@%d, binding@%d):\n%s", iv, ib, out)
		}
		if strings.Contains(f, "already configured") || !strings.Contains(f, "host default vault_path is not set or unreadable") {
			t.Errorf("the Vault row above the failed binding must not claim a configured vault:\n%s", out)
		}
	})
}

func TestInitReportsAnInertNoGit(t *testing.T) {
	t.Run("ignored: reported, and the scaffold commit still happens", func(t *testing.T) {
		home, _, def := existingHostEnv(t)
		proj := reportProject(t, home, "eta")
		recordReport(t, nil)

		code, out := runInit(t, proj, "--name", "eta", "--no-git")
		if code != cli.ExitOK {
			t.Fatalf("exit = %d\n%s", code, out)
		}
		f := flat(out)
		if !strings.Contains(f, "[skip] --no-git: ignored: it only decides whether a NEW vault gets a git repository") ||
			!strings.Contains(f, "The project scaffold is still committed if the project vault is a git repository.") {
			t.Errorf("inert --no-git must be reported with the commit caveat:\n%s", out)
		}
		// The row's claim is TRUE, not just printed.
		if log := gitIn(t, def, "log", "--format=%s"); !strings.Contains(log, "Scaffold Projects/eta/{commands,skills}") {
			t.Errorf("the row says the scaffold is committed but the vault log has no such commit:\n%s", log)
		}
	})

	t.Run("honoured on a new vault: no ignored row", func(t *testing.T) {
		home, _, _ := existingHostEnv(t)
		proj := reportProject(t, home, "theta")
		x := filepath.Join(t.TempDir(), "fresh")
		recordReport(t, nil)

		_, out := runInit(t, proj, "--name", "theta", "--vault-path", x, "--no-git")
		if strings.Contains(flat(out), "] --no-git:") {
			t.Errorf("--no-git was honoured (new vault); it must not be reported ignored:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(x, ".git")); err == nil {
			t.Errorf("--no-git created a git repository in the new vault")
		}
	})

	t.Run("existing path: reported as the tier-1 override choice", func(t *testing.T) {
		home, _, _ := existingHostEnv(t)
		proj := reportProject(t, home, "iota")
		x := t.TempDir()
		recordReport(t, nil)

		_, out := runInit(t, proj, "--name", "iota", "--vault-path", x, "--no-git")
		f := flat(out)
		if !strings.Contains(f, "[info] --no-git: --vault-path is bound by a tier-1 local override") {
			t.Errorf("want the tier-1 override note:\n%s", out)
		}
		if strings.Contains(f, "[skip] --no-git:") {
			t.Errorf("--no-git took effect here (forced tier 1); it must not be reported ignored:\n%s", out)
		}
	})
}

// N1: a path or source carrying a newline cannot forge a second notice line, and
// leading/trailing whitespace cannot be hidden; ordinary values stay unquoted.
func TestProjectVaultLineCannotBeForged(t *testing.T) {
	line := func(path, source string) string {
		pv := newProjectVault(t.TempDir())
		pv.resolve = func(string) (storage.Resolution, error) {
			return storage.Resolution{Path: path, Source: source}, nil
		}
		return pv.Line()
	}
	for name, c := range map[string][2]string{
		"newline in path":       {"/v\nvp init: project vault = /elsewhere", "global:/c.toml"},
		"carriage return":       {"/v\r/x", "global:/c.toml"},
		"newline in source":     {"/v", "global:/c\nforged"},
		"leading space":         {" /v", "global:/c.toml"},
		"trailing space":        {"/v ", "global:/c.toml"},
		"trailing space in src": {"/v", "global:/c.toml "},
	} {
		got := line(c[0], c[1])
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%s: notice spans more than one line: %q", name, got)
		}
		if !strings.Contains(got, `"`) {
			t.Errorf("%s: unsafe value was not quoted: %q", name, got)
		}
	}
	if got := line("/plain/vault", "global:/c.toml"); got != "vp init: project vault = /plain/vault  (source: global:/c.toml)" {
		t.Errorf("an ordinary path must stay unquoted, got %q", got)
	}
}
