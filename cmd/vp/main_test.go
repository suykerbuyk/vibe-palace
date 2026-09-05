// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func testRegistry() (*cli.Registry, *bytes.Buffer, *bytes.Buffer) {
	info := cli.BuildInfo{Version: "test", Commit: "abc1234", BuildDate: "2026-01-01"}
	reg := cli.NewRegistry(info)
	registerAll(reg, info)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	reg.SetOutput(out, errOut)
	return reg, out, errOut
}

func TestDispatchNoArgs(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch(nil)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage listing")
	}
}

func TestDispatchHelp(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"--help"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage listing")
	}
}

func TestDispatchVersion(t *testing.T) {
	reg, _, _ := testRegistry()
	// version command writes to os.Stdout directly; just verify exit code.
	code := reg.Dispatch([]string{"version"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

func TestDispatchVersionFlag(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"--version"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp test") {
		t.Errorf("expected version, got %q", out.String())
	}
}

func TestDispatchUnknown(t *testing.T) {
	reg, _, errOut := testRegistry()
	code := reg.Dispatch([]string{"nope"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Error("expected error message")
	}
}

func TestDispatchHelpSearch(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"help", "search"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp search <query>") {
		t.Errorf("expected search help, got %q", out.String())
	}
}

func TestDispatchHelpMigrateVibevault(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"help", "migrate", "vibevault"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "--vault-path") {
		t.Errorf("expected migrate vibevault help, got %q", out.String())
	}
}

func TestDispatchHelpVaultSync(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"help", "vault", "sync"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vault sync") {
		t.Errorf("expected vault sync help, got %q", out.String())
	}
}

func TestDispatchPerCommandHelp(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"search", "--help"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp search") {
		t.Errorf("expected search help, got %q", out.String())
	}
}

func TestDispatchTwoWordPerCommandHelp(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"vault", "push", "--help"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vault push") {
		t.Errorf("expected vault push help, got %q", out.String())
	}
}

func TestDispatchMigrateParent(t *testing.T) {
	reg, out, errOut := testRegistry()
	code := reg.Dispatch([]string{"migrate"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Errorf("bare `migrate` should render help on stdout, got:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("bare `migrate` should not write to stderr, got: %q", errOut.String())
	}
}

func TestDispatchVaultParent(t *testing.T) {
	reg, out, errOut := testRegistry()
	code := reg.Dispatch([]string{"vault"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Errorf("bare `vault` should render help on stdout, got:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("bare `vault` should not write to stderr, got: %q", errOut.String())
	}
}

func TestDispatchParentUnknownSubcommand(t *testing.T) {
	// End-to-end validation of the unknown-subcommand error path
	// against the real command registration.
	reg, out, errOut := testRegistry()
	code := reg.Dispatch([]string{"config", "bogus"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", out.String())
	}
	if !strings.Contains(errOut.String(), `unknown subcommand "bogus"`) {
		t.Errorf("stderr should mention the bad token, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "vp config") {
		t.Errorf("stderr should name the parent, got:\n%s", errOut.String())
	}
}

func TestAllCommandsRegistered(t *testing.T) {
	reg, _, _ := testRegistry()
	expected := []string{
		"check", "help", "init", "inject", "mcp", "mcp serve",
		"migrate", "migrate mempalace", "migrate vibevault",
		"search", "sessions", "status", "tasks", "tasks epics", "tasks edit",
		"vault", "vault pull", "vault push", "vault sync",
		"version",
	}
	for _, name := range expected {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("command %q not registered", name)
		}
	}
}

// TestAllCommandsRegisterValidly enforces the Command invariant at CI
// time: every registered command must have a Run, Subcommands, or
// both; BareInvocation implies Run != nil. Adding a new parent with
// neither Run nor Subcommands (or with BareInvocation but no Run)
// will fail here.
func TestAllCommandsRegisterValidly(t *testing.T) {
	reg, _, _ := testRegistry()
	reg.Each(func(cmd *cli.Command) {
		if cmd.Run == nil && len(cmd.Subcommands) == 0 {
			t.Errorf("command %q has neither Run nor Subcommands", cmd.Name)
		}
		if cmd.BareInvocation && cmd.Run == nil {
			t.Errorf("command %q has BareInvocation=true but Run is nil", cmd.Name)
		}
	})
}

// TestMutatingCommandsAreGated pins the class invariant behind the surface
// gate: a command that can write the local vault MUST be marked MutatesVault,
// so surfaceGate fail-stops it against a newer-format vault instead of taking
// the warn-only path. It exists because audit rooms once opted out silently
// (audit-rooms-apply-bypasses-the-surface-gate) and nothing checked the
// property, so the gate did not actually gate that write.
//
// It uses reg.Each, NOT reg.All: All() skips a two-word subcommand whose parent
// is registered, so a test built on All() would never see "audit rooms" — the
// exact command that regressed — and would pass while the bug was live.
//
// wantMutating is the complete set of local-vault-writing command Names. When
// you add a command that writes the vault, add it here and wrap its
// registration in mutates(); the test fails both ways (a writer left unwrapped,
// or a non-writer wrapped) so the table cannot silently drift from reality.
//
// Deliberately EXCLUDED: `vault pull`, `vault push` and `vault sync` mutate
// on-disk vault content, but that content is remote-sourced (git fetch/merge) or
// already on disk (stage/commit) — not this binary writing through the stamped
// local write path. A different category than the fail-stop guards, so they stay
// unwrapped by design. `vault sync` joined them once it was measured that
// SyncVault CONTAINS the pull (vaultsyncflow.go:120), so gating the command
// gated the escape hatch pull exists to be.
//
// `vault commit` and `vault tidy` do not stamp either and are kept wrapped as a
// DEFERRAL, not a verdict: they carry no lockout, and
// move-the-surface-gate-to-the-write-chokepoint would dissolve the question
// rather than answer it. The reasoning lives at the registration site.
func TestMutatingCommandsAreGated(t *testing.T) {
	wantMutating := map[string]bool{
		"absorb":         true,
		"archive create": true,
		"archive link":   true,
		"audit rooms":    true,
		"audit vault":    true,
		"discover rooms": true,
		"tune rooms":     true,
		"config upgrade": true,
		"config sync":    true,
		// Both reach commands.applyWithPolicy, which writes Change.VaultPath —
		// a template under the vault's Templates/ tree — through the stamped
		// local write path. Left unwrapped until 2026-08-19, which meant the
		// command that WRITES template mirrors was ungated while `config sync`,
		// the command that prunes them, was gated.
		"commands upgrade": true,
		"skills upgrade":   true,
		"init":             true,
		"tasks edit":       true,
		// "vault sync" is deliberately ABSENT: it contains the pull
		// (storage.SyncVault, vaultsyncflow.go:120), so gating it gated the very
		// operation `vault pull` is left ungated to protect. See the rationale at
		// its registration site in commands.go.
		"vault commit":         true,
		"vault tidy":           true,
		"vault write":          true,
		"vault edit":           true,
		"vault delete":         true,
		"vault move":           true,
		"memory harvest":       true,
		"migrate":              true,
		"migrate vibevault":    true,
		"migrate mempalace":    true,
		"migrate kg-filenames": true,
		// Both rewrite Projects/<slug>/iterations.md through the stamped local
		// write path. iterations.md is the project's narrative history and the
		// one vault file with no second copy, so a write from a binary older
		// than the vault's surface must fail-stop, not warn.
		"migrate iteration-headings":  true,
		"migrate iterations-preamble": true,
		// Writes active task files through storage.OverwriteTaskFile — the
		// locked, surface-stamping task writer — under --apply.
		"migrate task-preamble": true,
		// Same writer, opposite scope: the only caller that deliberately writes
		// ARCHIVED task files. OverwriteTaskFile resolves all three directories
		// and leaves the archived question to its caller, so scope is enforced
		// here rather than in storage.
		"migrate task-status": true,
		// Same writer again, and the only caller that reaches ACTIVE, done/ and
		// cancelled/ alike: the header classes it reports span all three, and a
		// legacy header is not made harmless by the directory it sits in.
		"migrate task-header": true,
	}

	reg, _, _ := testRegistry()
	reg.Each(func(cmd *cli.Command) {
		want := wantMutating[cmd.Name]
		if cmd.MutatesVault != want {
			if want {
				t.Errorf("command %q must be registered mutates() (it writes the vault) but MutatesVault=false", cmd.Name)
			} else {
				t.Errorf("command %q is marked MutatesVault=true but is not in the known-vault-writer table; if it writes the vault add it to wantMutating, otherwise drop the mutates() wrapper", cmd.Name)
			}
		}
	})
}

// Command-specific tests are in cmd_*_test.go files.

// subcommandOmissions reports, per parent, the registered children missing from
// that parent's Subcommands list.
//
// It is the completeness check this task's option 2 describes, extracted so it
// can be driven over a SYNTHETIC registry as well as the real one. The
// expectation is derived from what is registered — "record the query, never the
// answer" applied to a Go literal.
func subcommandOmissions(reg *cli.Registry) map[string][]string {
	listed := map[string]map[string]bool{}
	registered := map[string]bool{}
	var order []string
	reg.Each(func(c *cli.Command) {
		registered[c.Name] = true
		order = append(order, c.Name)
		set := map[string]bool{}
		for _, s := range c.Subcommands {
			set[s] = true
		}
		listed[c.Name] = set
	})

	omissions := map[string][]string{}
	for _, name := range order {
		parent, _, ok := strings.Cut(name, " ")
		if !ok || !registered[parent] {
			continue
		}
		if !listed[parent][name] {
			omissions[parent] = append(omissions[parent], name)
		}
	}
	return omissions
}

// TestEveryRegisteredChildIsListedByItsParent is the invariant `vp migrate
// task-status` broke: a command registered under a parent must be discoverable
// from that parent.
//
// Under the registry-derived field this holds by construction, which is the
// point — the check is kept as a REGRESSION pin, so that anything which stops
// populating the field (a reverted derivation, a one-directional append, a
// reintroduced hand-maintained literal that goes short) fails here rather than
// shipping a command nobody can find. TestSubcommandOmissionsDetectsAnOmission
// is what proves this check can actually fail.
func TestEveryRegisteredChildIsListedByItsParent(t *testing.T) {
	reg, _, _ := testRegistry()
	omissions := subcommandOmissions(reg)
	for parent, missing := range omissions {
		t.Errorf("parent %q omits registered child(ren) %v — the command runs but is "+
			"undiscoverable from its own parent's help", parent, missing)
	}
	// Guard against the check silently examining nothing.
	kids := 0
	reg.Each(func(c *cli.Command) {
		if strings.Contains(c.Name, " ") {
			kids++
		}
	})
	if kids == 0 {
		t.Fatal("no two-word commands found; the completeness check examined nothing")
	}
}

// TestSubcommandOmissionsDetectsAnOmission is the acceptance criterion this
// task's Acceptance section asks for, met the only way it still can be.
//
// 🔴 THE ORIGINAL CRITERION CANNOT BE SATISFIED. It required the check to be shown
// red "against the current omission before the omission is repaired", and the
// repair (6fdf569) already consumed the only live specimen. Worse, a check that
// could only ever go red against one file in one moment of history would go green
// forever afterwards and prove nothing about the next omission.
//
// So the omission is SYNTHETIC and deliberate: a parent whose list is short by one
// registered child, which is exactly the shape `vp migrate` had. Built by taking
// the field out of the registry's hands after registration, because the derivation
// makes the defect unconstructible through Register — which is the whole benefit,
// and also why the check needs a fixture to be provable at all.
func TestSubcommandOmissionsDetectsAnOmission(t *testing.T) {
	reg := cli.NewRegistry(cli.BuildInfo{})
	parent := &cli.Command{Name: "migrate", Synopsis: "vp migrate <command>"}
	reg.Register(parent)
	reg.Register(&cli.Command{Name: "migrate task-preamble", Run: func([]string) int { return 0 }})
	reg.Register(&cli.Command{Name: "migrate task-status", Run: func([]string) int { return 0 }})

	if got := subcommandOmissions(reg); len(got) != 0 {
		t.Fatalf("a freshly registered set already reports omissions %v; the fixture is not "+
			"isolating the defect", got)
	}

	// The defect: the field no longer says what the registry knows.
	parent.Subcommands = []string{"migrate task-preamble"}

	got := subcommandOmissions(reg)
	missing, ok := got["migrate"]
	if !ok {
		t.Fatalf("the completeness check did not notice a parent short by one child; it reports %v", got)
	}
	if len(missing) != 1 || missing[0] != "migrate task-status" {
		t.Errorf("omissions = %v, want exactly [migrate task-status]", missing)
	}
}

// registeredCommand returns a command AS THE REGISTRY SEES IT.
//
// 🔴 Command constructors no longer declare Subcommands — the registry derives it
// at registration (see cli.Registry.linkSubcommands). A test that calls a
// constructor directly and reads that field is reading a command that has never
// been registered, so the field is empty and the assertion is meaningless. Ask
// the registry instead.
func registeredCommand(t *testing.T, name string) *cli.Command {
	t.Helper()
	reg, _, _ := testRegistry()
	cmd, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("command %q is not registered", name)
	}
	return cmd
}

// registeredChildren returns the two-word commands registered under parent, in
// registration order — the expectation DERIVED rather than restated.
func registeredChildren(t *testing.T, parent string) []string {
	t.Helper()
	reg, _, _ := testRegistry()
	var kids []string
	reg.Each(func(c *cli.Command) {
		if p, _, ok := strings.Cut(c.Name, " "); ok && p == parent {
			kids = append(kids, c.Name)
		}
	})
	return kids
}
