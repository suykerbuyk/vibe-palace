package cli

import (
	"fmt"
	"strings"
)

// FormatHelp renders the --help output for a single command.
func FormatHelp(cmd *Command) string {
	var b strings.Builder

	if cmd.Synopsis != "" {
		fmt.Fprintf(&b, "Usage: %s\n", cmd.Synopsis)
	} else {
		fmt.Fprintf(&b, "Usage: vp %s\n", cmd.Name)
	}

	if cmd.Description != "" {
		b.WriteString("\n")
		b.WriteString(cmd.Description)
		if !strings.HasSuffix(cmd.Description, "\n") {
			b.WriteString("\n")
		}
	}

	if len(cmd.Flags) > 0 {
		b.WriteString("\nFlags:\n")
		for _, f := range cmd.Flags {
			b.WriteString(formatFlag(f))
		}
	}

	if len(cmd.Examples) > 0 {
		b.WriteString("\nExamples:\n")
		for _, e := range cmd.Examples {
			fmt.Fprintf(&b, "  %s\n", e.Cmd)
			if e.Comment != "" {
				fmt.Fprintf(&b, "      %s\n", e.Comment)
			}
		}
	}

	return b.String()
}

func formatFlag(f FlagDef) string {
	var left string
	if f.Short != "" {
		left = fmt.Sprintf("  %s, %s", f.Short, f.Name)
	} else {
		left = fmt.Sprintf("      %s", f.Name)
	}
	if f.Arg != "" {
		left += " " + f.Arg
	}

	line := left
	if f.Help != "" {
		if len(left) < 24 {
			line += strings.Repeat(" ", 24-len(left))
		} else {
			line += "  "
		}
		line += f.Help
	}
	if f.Default != "" {
		line += fmt.Sprintf(" (default: %s)", f.Default)
	}
	return line + "\n"
}

// FormatHelpWithSubs renders help for a parent command, showing its
// subcommands in a compact layout. The resolver looks up child commands
// by name. If cmd has no Subcommands, it delegates to FormatHelp.
func FormatHelpWithSubs(cmd *Command, resolver func(string) *Command) string {
	if len(cmd.Subcommands) == 0 || resolver == nil {
		return FormatHelp(cmd)
	}

	var b strings.Builder

	if cmd.Synopsis != "" {
		fmt.Fprintf(&b, "Usage: %s\n", cmd.Synopsis)
	} else {
		fmt.Fprintf(&b, "Usage: vp %s\n", cmd.Name)
	}

	if cmd.Description != "" {
		b.WriteString("\n")
		b.WriteString(cmd.Description)
		if !strings.HasSuffix(cmd.Description, "\n") {
			b.WriteString("\n")
		}
	}

	b.WriteString("\nCommands:\n")
	for _, name := range cmd.Subcommands {
		child := resolver(name)
		if child == nil {
			continue
		}
		// Show synopsis with flags inline.
		synopsis := child.Synopsis
		if synopsis == "" {
			synopsis = "vp " + child.Name
		}
		fmt.Fprintf(&b, "  %s\n", synopsis)
		if child.Description != "" {
			// First sentence only for the compact listing.
			desc := child.Description
			if idx := strings.IndexByte(desc, '.'); idx >= 0 {
				desc = desc[:idx+1]
			}
			fmt.Fprintf(&b, "      %s\n", desc)
		}
		b.WriteString("\n")
	}

	// Collect examples: parent first, then children.
	var examples []Example
	examples = append(examples, cmd.Examples...)
	for _, name := range cmd.Subcommands {
		child := resolver(name)
		if child != nil {
			examples = append(examples, child.Examples...)
		}
	}
	if len(examples) > 0 {
		b.WriteString("Examples:\n")
		for _, e := range examples {
			fmt.Fprintf(&b, "  %s\n", e.Cmd)
			if e.Comment != "" {
				fmt.Fprintf(&b, "      %s\n", e.Comment)
			}
		}
	}

	return b.String()
}

// FormatUsage renders the top-level usage listing all commands.
func FormatUsage(cmds []*Command, info BuildInfo) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s — vibe-palace AI context engine\n\n", info.Short())
	b.WriteString("Usage: vp <command> [flags]\n\nCommands:\n")

	// Find max command name length for alignment.
	maxLen := 0
	for _, cmd := range cmds {
		if len(cmd.Name) > maxLen {
			maxLen = len(cmd.Name)
		}
	}

	for _, cmd := range cmds {
		desc := cmd.Description
		// Use only first sentence for the listing.
		if idx := strings.IndexByte(desc, '.'); idx >= 0 {
			desc = desc[:idx+1]
		}
		// Truncate long descriptions.
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		pad := strings.Repeat(" ", maxLen-len(cmd.Name)+2)
		fmt.Fprintf(&b, "  %s%s%s\n", cmd.Name, pad, desc)
	}

	b.WriteString("\nRun 'vp help <command>' for details on a specific command.\n")
	return b.String()
}
