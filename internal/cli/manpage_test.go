package cli

import (
	"strings"
	"testing"
)

func TestFormatManPageSimple(t *testing.T) {
	cmd := &Command{
		Name:        "version",
		Synopsis:    "vp version",
		Description: "Print version, commit, and build date.",
		Examples: []Example{
			{Cmd: "vp version", Comment: "Show version information"},
		},
	}

	out := FormatManPage(cmd, nil, 1, "2026-04-10", "0.1.0")

	checks := []string{
		".TH VP\\-VERSION 1",
		".SH NAME",
		"vp version",
		"print version, commit, and build date",
		".SH SYNOPSIS",
		".SH DESCRIPTION",
		".SH EXAMPLES",
		"vp version",
		"Show version information",
		".SH SEE ALSO",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("FormatManPage missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestFormatManPageWithFlags(t *testing.T) {
	cmd := &Command{
		Name:        "search",
		Synopsis:    "vp search <query> [flags]",
		Description: "Semantic search across palace content.",
		Flags: []FlagDef{
			{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name"},
			{Name: "--limit", Short: "-n", Arg: "N", Help: "Max results", Default: "10"},
			{Name: "--json", Help: "Output JSON"},
		},
		Examples: []Example{
			{Cmd: "vp search \"database\"", Comment: "Search current project"},
		},
	}

	out := FormatManPage(cmd, nil, 1, "2026-04-10", "0.1.0")

	checks := []string{
		".SH OPTIONS",
		"\\fB\\-p\\fR, \\fB\\-\\-project\\fR \\fIPROJECT\\fR",
		"Project name",
		"\\fB\\-n\\fR, \\fB\\-\\-limit\\fR \\fIN\\fR",
		"(default: 10)",
		"\\fB\\-\\-json\\fR",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("FormatManPage missing %q\nGot:\n%s", c, out)
		}
	}

	// Should not have COMMANDS section.
	if strings.Contains(out, ".SH COMMANDS") {
		t.Error("unexpected COMMANDS section for command without children")
	}
}

func TestFormatManPageParentWithChildren(t *testing.T) {
	parent := &Command{
		Name:        "vault",
		Synopsis:    "vp vault <command> [flags]",
		Description: "Manage the vault git repository.",
		Subcommands: []string{"vault pull", "vault push"},
	}
	children := []*Command{
		{
			Name:        "vault pull",
			Synopsis:    "vp vault pull [--dry-run]",
			Description: "Pull from all configured vault remotes.",
			Examples: []Example{
				{Cmd: "vp vault pull", Comment: "Pull from all remotes"},
			},
		},
		{
			Name:        "vault push",
			Synopsis:    "vp vault push [--dry-run]",
			Description: "Push to all configured vault remotes.",
			Examples: []Example{
				{Cmd: "vp vault push", Comment: "Push to all remotes"},
			},
		},
	}

	out := FormatManPage(parent, children, 1, "2026-04-10", "0.1.0")

	checks := []string{
		".SH COMMANDS",
		"vp vault pull",
		"Pull from all configured vault remotes.",
		"vp vault push",
		"Push to all configured vault remotes.",
		".SH EXAMPLES",
		".SH SEE ALSO",
		"vp\\-vault\\-pull",
		"vp\\-vault\\-push",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("FormatManPage missing %q\nGot:\n%s", c, out)
		}
	}
}

func TestTroffEscape(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"--flag", `\-\-flag`},
		{`a\b`, `a\\b`},
		{".TH", `\&.TH`},
		{"'quote", `\&'quote`},
		{"normal", "normal"},
	}
	for _, tt := range tests {
		got := troffEscape(tt.in)
		if got != tt.want {
			t.Errorf("troffEscape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
