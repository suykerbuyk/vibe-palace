package cli

import (
	"strings"
	"testing"
)

func TestFormatHelp(t *testing.T) {
	cmd := &Command{
		Name:        "search",
		Synopsis:    "vp search <query> [flags]",
		Description: "Search the palace for matching content.",
		Flags: []FlagDef{
			{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name"},
			{Name: "--json", Help: "Output JSON"},
			{Name: "--limit", Short: "-n", Arg: "N", Help: "Max results", Default: "10"},
		},
		Examples: []Example{
			{Cmd: "vp search \"database\"", Comment: "Search current project"},
			{Cmd: "vp search \"auth\" -p myapp"},
		},
	}

	out := FormatHelp(cmd)

	checks := []string{
		"Usage: vp search <query> [flags]",
		"Search the palace",
		"-p, --project PROJECT",
		"Project name",
		"--json",
		"Output JSON",
		"-n, --limit N",
		"(default: 10)",
		"Examples:",
		"vp search \"database\"",
		"Search current project",
		"vp search \"auth\" -p myapp",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("FormatHelp missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestFormatHelpMinimal(t *testing.T) {
	cmd := &Command{Name: "version"}
	out := FormatHelp(cmd)
	if !strings.Contains(out, "Usage: vp version") {
		t.Errorf("expected fallback synopsis, got %q", out)
	}
}

func TestFormatUsage(t *testing.T) {
	cmds := []*Command{
		{Name: "check", Description: "Verify installation."},
		{Name: "search", Description: "Search the palace."},
		{Name: "version", Description: "Show version info."},
	}
	info := BuildInfo{Version: "1.0.0"}
	out := FormatUsage(cmds, info)

	checks := []string{
		"vp 1.0.0",
		"Usage: vp <command> [flags]",
		"Commands:",
		"check",
		"Verify installation.",
		"search",
		"Search the palace.",
		"version",
		"Show version info.",
		"vp help <command>",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("FormatUsage missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestFormatUsageTruncatesLongDescriptions(t *testing.T) {
	cmds := []*Command{
		{Name: "x", Description: strings.Repeat("a", 100)},
	}
	out := FormatUsage(cmds, BuildInfo{Version: "dev"})
	// Description truncated at first sentence boundary or 60 chars.
	// No period → truncates at 57 + "..."
	if !strings.Contains(out, "...") {
		t.Error("expected truncated description")
	}
}

func TestFormatHelpWithSubsNil(t *testing.T) {
	cmd := &Command{
		Name:        "search",
		Synopsis:    "vp search <query>",
		Description: "Search the palace.",
	}
	// No subcommands → should match FormatHelp exactly.
	withSubs := FormatHelpWithSubs(cmd, nil)
	plain := FormatHelp(cmd)
	if withSubs != plain {
		t.Errorf("FormatHelpWithSubs with no subs should match FormatHelp\nGot:\n%s\nWant:\n%s", withSubs, plain)
	}
}

func TestFormatHelpWithSubsParent(t *testing.T) {
	parent := &Command{
		Name:        "vault",
		Synopsis:    "vp vault <command> [flags]",
		Description: "Manage the vault git repository.",
		Subcommands: []string{"vault pull", "vault push"},
	}
	children := map[string]*Command{
		"vault pull": {
			Name:        "vault pull",
			Synopsis:    "vp vault pull [--dry-run]",
			Description: "Pull from all configured vault remotes.",
			Examples: []Example{
				{Cmd: "vp vault pull", Comment: "Pull from all remotes"},
			},
		},
		"vault push": {
			Name:        "vault push",
			Synopsis:    "vp vault push [--dry-run]",
			Description: "Push to all configured vault remotes.",
			Examples: []Example{
				{Cmd: "vp vault push", Comment: "Push to all remotes"},
			},
		},
	}

	out := FormatHelpWithSubs(parent, func(name string) *Command {
		return children[name]
	})

	checks := []string{
		"Usage: vp vault <command> [flags]",
		"Manage the vault",
		"Commands:",
		"vp vault pull [--dry-run]",
		"Pull from all configured vault remotes.",
		"vp vault push [--dry-run]",
		"Push to all configured vault remotes.",
		"Examples:",
		"vp vault pull",
		"Pull from all remotes",
		"vp vault push",
		"Push to all remotes",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("FormatHelpWithSubs missing %q\nGot:\n%s", c, out)
		}
	}

	// Should NOT have a Flags section (parent has no flags).
	if strings.Contains(out, "Flags:") {
		t.Error("unexpected Flags section in parent help")
	}
}

func TestFormatHelpWithSubsMissingChild(t *testing.T) {
	parent := &Command{
		Name:        "vault",
		Synopsis:    "vp vault <command>",
		Description: "Manage vault.",
		Subcommands: []string{"vault pull", "vault missing"},
	}

	out := FormatHelpWithSubs(parent, func(name string) *Command {
		if name == "vault pull" {
			return &Command{Name: "vault pull", Synopsis: "vp vault pull", Description: "Pull."}
		}
		return nil // missing child
	})

	if !strings.Contains(out, "vault pull") {
		t.Error("expected vault pull in output")
	}
	if strings.Contains(out, "vault missing") {
		t.Error("missing child should be skipped, not shown")
	}
}

func TestFormatFlagAlignment(t *testing.T) {
	cmd := &Command{
		Name: "test",
		Flags: []FlagDef{
			{Name: "--short", Help: "A short flag"},
			{Name: "--a-very-long-flag-name", Arg: "VALUE", Help: "A long flag"},
		},
	}
	out := FormatHelp(cmd)
	// Both flags should have help text on the same line.
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "--short") && !strings.Contains(line, "A short flag") {
			t.Error("short flag and help should be on same line")
		}
		if strings.Contains(line, "--a-very-long-flag-name") && !strings.Contains(line, "A long flag") {
			t.Error("long flag and help should be on same line")
		}
	}
}
