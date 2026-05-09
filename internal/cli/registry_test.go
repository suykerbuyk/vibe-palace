package cli

import (
	"bytes"
	"strings"
	"testing"
)

func testInfo() BuildInfo {
	return BuildInfo{Version: "test", Commit: "abc1234", BuildDate: "2026-01-01"}
}

func newTestRegistry() (*Registry, *bytes.Buffer, *bytes.Buffer) {
	reg := NewRegistry(testInfo())
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	reg.SetOutput(out, errOut)
	return reg, out, errOut
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	reg, _, _ := newTestRegistry()
	reg.Register(&Command{Name: "check", Description: "Run checks.", Run: func([]string) int { return 0 }})

	cmd, ok := reg.Lookup("check")
	if !ok || cmd.Name != "check" {
		t.Error("Lookup failed for registered command")
	}

	_, ok = reg.Lookup("nope")
	if ok {
		t.Error("Lookup should fail for unregistered command")
	}
}

func TestRegistryAll(t *testing.T) {
	reg, _, _ := newTestRegistry()
	reg.Register(&Command{Name: "check", Description: "Run checks."})
	reg.Register(&Command{Name: "version", Description: "Show version."})
	reg.Register(&Command{Name: "mcp", Description: "MCP server.", Hidden: true})

	cmds := reg.All()
	if len(cmds) != 2 {
		t.Fatalf("All() returned %d commands, want 2", len(cmds))
	}
	if cmds[0].Name != "check" || cmds[1].Name != "version" {
		t.Errorf("All() = [%s, %s], want [check, version]", cmds[0].Name, cmds[1].Name)
	}
}

func TestRegistryAllSorted(t *testing.T) {
	reg, _, _ := newTestRegistry()
	reg.Register(&Command{Name: "zebra", Description: "Z."})
	reg.Register(&Command{Name: "alpha", Description: "A."})

	cmds := reg.All()
	if cmds[0].Name != "alpha" || cmds[1].Name != "zebra" {
		t.Error("All() should be sorted by name")
	}
}

func TestDispatchNoArgs(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{Name: "check", Description: "Run checks."})

	code := reg.Dispatch(nil)
	if code != ExitOK {
		t.Errorf("Dispatch(nil) = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage output")
	}
}

func TestDispatchHelp(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{Name: "check", Description: "Run checks."})

	code := reg.Dispatch([]string{"--help"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage output")
	}
}

func TestDispatchVersion(t *testing.T) {
	reg, out, _ := newTestRegistry()

	code := reg.Dispatch([]string{"--version"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp test") {
		t.Errorf("expected version in output, got %q", out.String())
	}
}

func TestDispatchSingleWord(t *testing.T) {
	reg, _, _ := newTestRegistry()
	var called bool
	reg.Register(&Command{
		Name: "check",
		Run:  func(args []string) int { called = true; return ExitOK },
	})

	code := reg.Dispatch([]string{"check"})
	if code != ExitOK || !called {
		t.Error("command not dispatched")
	}
}

func TestDispatchTwoWord(t *testing.T) {
	reg, _, _ := newTestRegistry()
	var gotArgs []string
	reg.Register(&Command{
		Name: "vault sync",
		Run:  func(args []string) int { gotArgs = args; return ExitOK },
	})

	code := reg.Dispatch([]string{"vault", "sync", "--dry-run"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--dry-run" {
		t.Errorf("args = %v, want [--dry-run]", gotArgs)
	}
}

func TestDispatchTwoWordOverridesSingle(t *testing.T) {
	reg, _, _ := newTestRegistry()
	var singleCalled, twoCalled bool
	reg.Register(&Command{
		Name: "vault",
		Run:  func([]string) int { singleCalled = true; return ExitOK },
	})
	reg.Register(&Command{
		Name: "vault sync",
		Run:  func([]string) int { twoCalled = true; return ExitOK },
	})

	reg.Dispatch([]string{"vault", "sync"})
	if singleCalled {
		t.Error("single-word command should not be called when two-word matches")
	}
	if !twoCalled {
		t.Error("two-word command should be called")
	}
}

func TestDispatchFallbackToSingle(t *testing.T) {
	reg, _, _ := newTestRegistry()
	var gotArgs []string
	reg.Register(&Command{
		Name: "vault",
		Run:  func(args []string) int { gotArgs = args; return ExitOK },
	})

	reg.Dispatch([]string{"vault", "unknown"})
	if len(gotArgs) != 1 || gotArgs[0] != "unknown" {
		t.Errorf("args = %v, want [unknown]", gotArgs)
	}
}

func TestDispatchUnknown(t *testing.T) {
	reg, _, errOut := newTestRegistry()

	code := reg.Dispatch([]string{"nope"})
	if code != ExitUser {
		t.Errorf("exit code = %d, want %d", code, ExitUser)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("expected error message, got %q", errOut.String())
	}
}

func TestDispatchPerCommandHelp(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{
		Name:        "search",
		Synopsis:    "vp search <query>",
		Description: "Search the palace.",
		Run:         func([]string) int { return ExitOK },
	})

	code := reg.Dispatch([]string{"search", "--help"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp search <query>") {
		t.Errorf("expected command help, got %q", out.String())
	}
}

func TestDispatchTwoWordHelp(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{
		Name:        "vault sync",
		Synopsis:    "vp vault sync",
		Description: "Sync the vault.",
		Run:         func([]string) int { return ExitOK },
	})

	code := reg.Dispatch([]string{"vault", "sync", "--help"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp vault sync") {
		t.Errorf("expected two-word help, got %q", out.String())
	}
}

func TestRegisterHelp(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{
		Name:        "search",
		Synopsis:    "vp search <query>",
		Description: "Search the palace.",
	})
	reg.Register(&Command{
		Name:        "vault sync",
		Synopsis:    "vp vault sync",
		Description: "Sync the vault.",
	})
	reg.RegisterHelp()

	// vp help search
	code := reg.Dispatch([]string{"help", "search"})
	if code != ExitOK {
		t.Errorf("help search: exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp search <query>") {
		t.Errorf("expected search help, got %q", out.String())
	}

	// vp help vault sync
	out.Reset()
	code = reg.Dispatch([]string{"help", "vault", "sync"})
	if code != ExitOK {
		t.Errorf("help vault sync: exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp vault sync") {
		t.Errorf("expected vault sync help, got %q", out.String())
	}
}

func TestRegisterHelpUnknown(t *testing.T) {
	reg, _, errOut := newTestRegistry()
	reg.RegisterHelp()

	code := reg.Dispatch([]string{"help", "nope"})
	if code != ExitUser {
		t.Errorf("exit code = %d, want %d", code, ExitUser)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Error("expected unknown command error")
	}
}

func TestRegisterHelpNoArgs(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{Name: "check", Description: "Run checks."})
	reg.RegisterHelp()

	code := reg.Dispatch([]string{"help"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage output")
	}
}

func TestAllFiltersSubcommands(t *testing.T) {
	reg, _, _ := newTestRegistry()
	reg.Register(&Command{Name: "vault", Description: "Manage vault.", Subcommands: []string{"vault pull", "vault push"}})
	reg.Register(&Command{Name: "vault pull", Description: "Pull."})
	reg.Register(&Command{Name: "vault push", Description: "Push."})
	reg.Register(&Command{Name: "check", Description: "Check."})

	cmds := reg.All()
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}

	// vault pull and vault push should be filtered out.
	for _, name := range names {
		if name == "vault pull" || name == "vault push" {
			t.Errorf("All() should not include subcommand %q when parent is registered", name)
		}
	}
	// vault and check should remain.
	if len(cmds) != 2 {
		t.Errorf("All() returned %d commands, want 2: %v", len(cmds), names)
	}
}

func TestAllKeepsOrphans(t *testing.T) {
	reg, _, _ := newTestRegistry()
	// "vault pull" without a "vault" parent should NOT be filtered.
	reg.Register(&Command{Name: "vault pull", Description: "Pull."})

	cmds := reg.All()
	if len(cmds) != 1 || cmds[0].Name != "vault pull" {
		t.Errorf("All() should keep two-word command when parent is not registered, got %v", cmds)
	}
}

func TestDispatchParentHelp(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{
		Name:        "vault",
		Synopsis:    "vp vault <command>",
		Description: "Manage vault.",
		Subcommands: []string{"vault pull"},
		Run:         func([]string) int { return ExitOK },
	})
	reg.Register(&Command{
		Name:        "vault pull",
		Synopsis:    "vp vault pull [--dry-run]",
		Description: "Pull from remotes.",
	})

	code := reg.Dispatch([]string{"vault", "--help"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	output := out.String()
	if !strings.Contains(output, "Commands:") {
		t.Errorf("parent --help should include Commands section\nGot:\n%s", output)
	}
	if !strings.Contains(output, "vault pull") {
		t.Errorf("parent --help should list subcommands\nGot:\n%s", output)
	}
}

func TestRegisterHelpParent(t *testing.T) {
	reg, out, _ := newTestRegistry()
	reg.Register(&Command{
		Name:        "vault",
		Synopsis:    "vp vault <command>",
		Description: "Manage vault.",
		Subcommands: []string{"vault pull"},
		Run:         func([]string) int { return ExitOK },
	})
	reg.Register(&Command{
		Name:        "vault pull",
		Synopsis:    "vp vault pull",
		Description: "Pull from remotes.",
	})
	reg.RegisterHelp()

	code := reg.Dispatch([]string{"help", "vault"})
	if code != ExitOK {
		t.Errorf("exit code = %d", code)
	}
	output := out.String()
	if !strings.Contains(output, "Commands:") {
		t.Errorf("help vault should include Commands section\nGot:\n%s", output)
	}
}

// parentRegistry builds a registry with a "svc" parent that has two
// subcommands and a leaf "ping" — shared fixture for the parent
// bare-invocation behavior tests.
func parentRegistry() (*Registry, *bytes.Buffer, *bytes.Buffer, *int) {
	reg, out, errOut := newTestRegistry()
	runCalls := 0
	reg.Register(&Command{
		Name:        "svc",
		Synopsis:    "vp svc <command>",
		Description: "Manage the service.",
		Subcommands: []string{"svc start", "svc stop"},
	})
	reg.Register(&Command{
		Name:        "svc start",
		Synopsis:    "vp svc start",
		Description: "Start the service.",
		Run:         func([]string) int { return ExitOK },
	})
	reg.Register(&Command{
		Name:        "svc stop",
		Synopsis:    "vp svc stop",
		Description: "Stop the service.",
		Run:         func([]string) int { return ExitOK },
	})
	reg.Register(&Command{
		Name:        "ping",
		Description: "Leaf command.",
		Run: func([]string) int {
			runCalls++
			return ExitOK
		},
	})
	return reg, out, errOut, &runCalls
}

func TestDispatchParentBareShowsHelpOnStdout(t *testing.T) {
	reg, out, errOut, _ := parentRegistry()

	code := reg.Dispatch([]string{"svc"})
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Errorf("bare parent should render help on stdout, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "svc start") {
		t.Errorf("help should list subcommands, got:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr should be empty on bare parent invocation, got: %q", errOut.String())
	}
}

func TestDispatchParentUnknownSubcommandIsExitUser(t *testing.T) {
	reg, out, errOut, _ := parentRegistry()

	code := reg.Dispatch([]string{"svc", "bogus"})
	if code != ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty on unknown-subcommand error, got: %q", out.String())
	}
	errText := errOut.String()
	if !strings.Contains(errText, `unknown subcommand "bogus"`) {
		t.Errorf("stderr should mention unknown subcommand, got:\n%s", errText)
	}
	if !strings.Contains(errText, "vp svc") {
		t.Errorf("stderr should name the parent command, got:\n%s", errText)
	}
	if !strings.Contains(errText, "Commands:") {
		t.Errorf("stderr should include parent help, got:\n%s", errText)
	}
}

func TestDispatchParentKnownSubcommandUnchanged(t *testing.T) {
	reg, _, _, _ := parentRegistry()
	if code := reg.Dispatch([]string{"svc", "start"}); code != ExitOK {
		t.Errorf("known subcommand should dispatch normally, got exit %d", code)
	}
}

func TestDispatchParentHelpFlagUnchanged(t *testing.T) {
	// Regression guard: --help must still route through Dispatch's
	// early hasHelpFlag branch and land on stdout with ExitOK.
	reg, out, _, _ := parentRegistry()
	if code := reg.Dispatch([]string{"svc", "--help"}); code != ExitOK {
		t.Errorf("--help exit = %d, want ExitOK", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Errorf("--help should render parent help on stdout")
	}
}

func TestDispatchLeafCommandUnaffected(t *testing.T) {
	// Regression guard: the parent gate must not intercept leaf
	// commands (no Subcommands).
	reg, _, _, runCalls := parentRegistry()
	if code := reg.Dispatch([]string{"ping"}); code != ExitOK {
		t.Errorf("leaf dispatch exit = %d, want ExitOK", code)
	}
	if *runCalls != 1 {
		t.Errorf("leaf Run call count = %d, want 1", *runCalls)
	}
}

// bareInvocationRegistry wraps a BareInvocation=true "hook" parent
// with a "hook install" subcommand. Used to verify that hook-style
// parents still receive empty / flag-only args via Run, while
// unknown non-flag tokens produce the same error path as non-bare
// parents.
func bareInvocationRegistry() (*Registry, *bytes.Buffer, *bytes.Buffer, *[][]string) {
	reg, out, errOut := newTestRegistry()
	var runCalls [][]string
	reg.Register(&Command{
		Name:           "hook",
		Synopsis:       "vp hook",
		Description:    "Hook handler; also a parent.",
		Subcommands:    []string{"hook install"},
		BareInvocation: true,
		Run: func(args []string) int {
			runCalls = append(runCalls, args)
			return ExitOK
		},
	})
	reg.Register(&Command{
		Name:        "hook install",
		Synopsis:    "vp hook install",
		Description: "Install hook.",
		Run:         func([]string) int { return ExitOK },
	})
	return reg, out, errOut, &runCalls
}

func TestBareInvocationEmptyArgsRunsHandler(t *testing.T) {
	reg, _, errOut, calls := bareInvocationRegistry()

	code := reg.Dispatch([]string{"hook"})
	if code != ExitOK {
		t.Errorf("exit = %d, want ExitOK", code)
	}
	if len(*calls) != 1 {
		t.Fatalf("Run calls = %d, want 1", len(*calls))
	}
	if len((*calls)[0]) != 0 {
		t.Errorf("Run args = %v, want empty", (*calls)[0])
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", errOut.String())
	}
}

func TestBareInvocationFlagOnlyRunsHandler(t *testing.T) {
	reg, _, _, calls := bareInvocationRegistry()

	code := reg.Dispatch([]string{"hook", "--verbose"})
	if code != ExitOK {
		t.Errorf("exit = %d, want ExitOK", code)
	}
	if len(*calls) != 1 || len((*calls)[0]) != 1 || (*calls)[0][0] != "--verbose" {
		t.Errorf("Run calls = %v, want [[--verbose]]", *calls)
	}
}

func TestBareInvocationUnknownNonFlagIsExitUser(t *testing.T) {
	// Regression lock for review finding H1: `vp hook bogus` must
	// not silently dispatch to Run just because hook sets
	// BareInvocation.
	reg, _, errOut, calls := bareInvocationRegistry()

	code := reg.Dispatch([]string{"hook", "bogus"})
	if code != ExitUser {
		t.Errorf("exit = %d, want ExitUser", code)
	}
	if len(*calls) != 0 {
		t.Errorf("Run should NOT be called for unknown subcommand, called %d times with %v", len(*calls), *calls)
	}
	if !strings.Contains(errOut.String(), `unknown subcommand "bogus"`) {
		t.Errorf("stderr should mention unknown subcommand, got:\n%s", errOut.String())
	}
}

func TestBareInvocationKnownSubcommandUnchanged(t *testing.T) {
	reg, _, _, calls := bareInvocationRegistry()
	if code := reg.Dispatch([]string{"hook", "install"}); code != ExitOK {
		t.Errorf("known subcommand exit = %d, want ExitOK", code)
	}
	if len(*calls) != 0 {
		t.Errorf("parent Run should not be called when two-word lookup hits, got %d calls", len(*calls))
	}
}
