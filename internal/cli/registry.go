package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Registry holds registered commands and dispatches to them.
type Registry struct {
	commands map[string]*Command
	order    []string
	info     BuildInfo
	out      io.Writer
	errOut   io.Writer
}

// NewRegistry creates a command registry with the given build info.
func NewRegistry(info BuildInfo) *Registry {
	return &Registry{
		commands: make(map[string]*Command),
		info:     info,
		out:      os.Stdout,
		errOut:   os.Stderr,
	}
}

// SetOutput overrides stdout and stderr writers (useful for testing).
func (r *Registry) SetOutput(out, errOut io.Writer) {
	r.out = out
	r.errOut = errOut
}

// Register adds a command to the registry.
func (r *Registry) Register(cmd *Command) {
	r.commands[cmd.Name] = cmd
	r.order = append(r.order, cmd.Name)
}

// Lookup finds a command by name.
func (r *Registry) Lookup(name string) (*Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// All returns all non-hidden commands sorted by name.
func (r *Registry) All() []*Command {
	var cmds []*Command
	for _, name := range r.order {
		cmd := r.commands[name]
		if !cmd.Hidden {
			cmds = append(cmds, cmd)
		}
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}

// Dispatch routes args to the appropriate command. Returns an exit code.
func (r *Registry) Dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(r.out, FormatUsage(r.All(), r.info))
		return ExitOK
	}

	// Check for top-level --help/-h.
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(r.out, FormatUsage(r.All(), r.info))
		return ExitOK
	}

	// Check for top-level --version/-v.
	if args[0] == "--version" || args[0] == "-v" {
		fmt.Fprintln(r.out, r.info)
		return ExitOK
	}

	// Try two-word lookup first (e.g. "vault sync").
	if len(args) >= 2 {
		twoWord := args[0] + " " + args[1]
		if cmd, ok := r.commands[twoWord]; ok {
			remaining := args[2:]
			if hasHelpFlag(remaining) {
				fmt.Fprint(r.out, FormatHelp(cmd))
				return ExitOK
			}
			return cmd.Run(remaining)
		}
	}

	// Single-word lookup.
	if cmd, ok := r.commands[args[0]]; ok {
		remaining := args[1:]
		if hasHelpFlag(remaining) {
			fmt.Fprint(r.out, FormatHelp(cmd))
			return ExitOK
		}
		return cmd.Run(remaining)
	}

	fmt.Fprintf(r.errOut, "vp: unknown command %q\nRun 'vp help' for usage.\n", args[0])
	return ExitUser
}

// RegisterHelp adds a "help" command that looks up and prints help for other commands.
func (r *Registry) RegisterHelp() {
	r.Register(&Command{
		Name:        "help",
		Synopsis:    "vp help [command]",
		Description: "Show help for a command.",
		Run: func(args []string) int {
			if len(args) == 0 {
				fmt.Fprint(r.out, FormatUsage(r.All(), r.info))
				return ExitOK
			}
			// Try two-word lookup first.
			if len(args) >= 2 {
				twoWord := args[0] + " " + args[1]
				if cmd, ok := r.commands[twoWord]; ok {
					fmt.Fprint(r.out, FormatHelp(cmd))
					return ExitOK
				}
			}
			// Single-word lookup.
			if cmd, ok := r.commands[args[0]]; ok {
				fmt.Fprint(r.out, FormatHelp(cmd))
				return ExitOK
			}
			fmt.Fprintf(r.errOut, "vp help: unknown command %q\n", strings.Join(args, " "))
			return ExitUser
		},
	})
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
		if a == "--" {
			return false
		}
	}
	return false
}
