package cli

import (
	"fmt"
	"strings"
)

// FormatManPage generates a troff-formatted man page from Command metadata.
// children should contain resolved child commands for parent commands.
// section is the man section (typically 1). date and version are displayed
// in the page header/footer.
func FormatManPage(cmd *Command, children []*Command, section int, date, version string) string {
	var b strings.Builder

	// Page name: "vp-vault-pull" for "vault pull".
	pageName := "vp"
	if cmd.Name != "" {
		pageName = "vp-" + strings.ReplaceAll(cmd.Name, " ", "-")
	}
	upperName := strings.ToUpper(pageName)

	// .TH
	fmt.Fprintf(&b, ".TH %s %d %q %q %q\n",
		troffEscape(upperName), section,
		troffEscape(date), troffEscape("vp "+version),
		"Vibe Palace Manual")

	// NAME
	b.WriteString(".SH NAME\n")
	desc := cmd.Description
	if idx := strings.IndexByte(desc, '.'); idx >= 0 {
		desc = desc[:idx]
	}
	// Use the user-facing command name with spaces (e.g. "vp vault pull").
	fmt.Fprintf(&b, "%s \\- %s\n", troffEscape("vp "+cmd.Name), troffEscape(strings.ToLower(desc)))

	// SYNOPSIS
	b.WriteString(".SH SYNOPSIS\n")
	synopsis := cmd.Synopsis
	if synopsis == "" {
		synopsis = "vp " + cmd.Name
	}
	fmt.Fprintf(&b, ".B %s\n", troffEscape(synopsis))

	// DESCRIPTION
	b.WriteString(".SH DESCRIPTION\n")
	fmt.Fprintf(&b, "%s\n", troffEscape(cmd.Description))

	// OPTIONS (from Flags)
	if len(cmd.Flags) > 0 {
		b.WriteString(".SH OPTIONS\n")
		for _, f := range cmd.Flags {
			var opt string
			if f.Short != "" {
				opt = fmt.Sprintf("\\fB%s\\fR, \\fB%s\\fR", troffEscape(f.Short), troffEscape(f.Name))
			} else {
				opt = fmt.Sprintf("\\fB%s\\fR", troffEscape(f.Name))
			}
			if f.Arg != "" {
				opt += " \\fI" + troffEscape(f.Arg) + "\\fR"
			}
			fmt.Fprintf(&b, ".TP\n%s\n", opt)
			help := f.Help
			if f.Default != "" {
				help += fmt.Sprintf(" (default: %s)", f.Default)
			}
			fmt.Fprintf(&b, "%s\n", troffEscape(help))
		}
	}

	// COMMANDS (for parents)
	if len(children) > 0 {
		b.WriteString(".SH COMMANDS\n")
		for _, child := range children {
			synopsis := child.Synopsis
			if synopsis == "" {
				synopsis = "vp " + child.Name
			}
			fmt.Fprintf(&b, ".TP\n\\fB%s\\fR\n", troffEscape(synopsis))
			childDesc := child.Description
			if idx := strings.IndexByte(childDesc, '.'); idx >= 0 {
				childDesc = childDesc[:idx+1]
			}
			fmt.Fprintf(&b, "%s\n", troffEscape(childDesc))
		}
	}

	// EXAMPLES
	examples := cmd.Examples
	for _, child := range children {
		examples = append(examples, child.Examples...)
	}
	if len(examples) > 0 {
		b.WriteString(".SH EXAMPLES\n")
		for _, e := range examples {
			fmt.Fprintf(&b, ".TP\n\\fB%s\\fR\n", troffEscape(e.Cmd))
			if e.Comment != "" {
				fmt.Fprintf(&b, "%s\n", troffEscape(e.Comment))
			}
		}
	}

	// SEE ALSO
	b.WriteString(".SH SEE ALSO\n")
	var refs []string
	if cmd.Name != "" {
		// Two-word commands: reference parent and siblings.
		if parent, _, ok := strings.Cut(cmd.Name, " "); ok {
			refs = append(refs, fmt.Sprintf("\\fBvp\\-%s\\fR(1)", troffEscape(parent)))
			for _, child := range children {
				if child.Name != cmd.Name {
					ref := strings.ReplaceAll(child.Name, " ", "-")
					refs = append(refs, fmt.Sprintf("\\fBvp\\-%s\\fR(1)", troffEscape(ref)))
				}
			}
		} else if len(children) > 0 {
			// Parent commands: reference all children.
			for _, child := range children {
				ref := strings.ReplaceAll(child.Name, " ", "-")
				refs = append(refs, fmt.Sprintf("\\fBvp\\-%s\\fR(1)", troffEscape(ref)))
			}
		}
	}
	// Always reference vp(1) for standalone commands.
	if len(refs) == 0 {
		refs = append(refs, "\\fBvp\\fR(1)")
	}
	fmt.Fprintf(&b, "%s\n", strings.Join(refs, ", "))

	return b.String()
}

// troffEscape escapes special characters for troff output.
func troffEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	// Escape hyphens in flag names to prevent troff interpretation.
	s = strings.ReplaceAll(s, "-", `\-`)
	// Protect leading dots and apostrophes.
	if strings.HasPrefix(s, ".") {
		s = `\&` + s
	}
	if strings.HasPrefix(s, "'") {
		s = `\&` + s
	}
	return s
}
