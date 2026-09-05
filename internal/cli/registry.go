package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
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
	preRun   func(*Command) int
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

// SetPreRun installs a hook invoked just before a leaf command's Run, with the
// matched command. A non-zero return aborts dispatch with that exit code; zero
// proceeds. cmd/vp uses this as the single choke-point for the MCP surface gate
// (fail-stop on MutatesVault commands, warn-only otherwise) without the cli
// framework depending on storage/surface. nil hook = no gating.
func (r *Registry) SetPreRun(fn func(*Command) int) {
	r.preRun = fn
}

// runCmd is the single execution choke-point: it runs the pre-run hook (if any)
// before delegating to cmd.Run, so every leaf invocation is gated uniformly.
func (r *Registry) runCmd(cmd *Command, args []string) int {
	if r.preRun != nil {
		if code := r.preRun(cmd); code != ExitOK {
			return code
		}
	}
	return cmd.Run(args)
}

// Register adds a command to the registry and maintains the parent/child link
// that Command.Subcommands expresses.
func (r *Registry) Register(cmd *Command) {
	r.commands[cmd.Name] = cmd
	r.order = append(r.order, cmd.Name)
	r.linkSubcommands(cmd)
}

// linkSubcommands makes Command.Subcommands a DERIVED view of what is registered
// rather than a hand-maintained copy of it.
//
// # Why the field is populated and not replaced by an accessor
//
// 🔴 Subcommands IS LOAD-BEARING FOR DISPATCH, NOT ONLY FOR HELP.
// dispatchCommand branches on len(cmd.Subcommands) == 0 to decide leaf-versus-
// parent, and BareInvocation is documented as having no effect when it is empty.
// So the registry fills the field in and every existing reader — dispatch, help,
// the man-page generator, the registry invariant tests — keeps working unchanged.
// Swapping it for a Children() accessor would change dispatch, which is a
// different and much larger change than deleting fifteen literals.
//
// # Why this runs inside Register and not in a finalize pass
//
// A finalize pass would be a call site, and a call site can be forgotten. The
// cost of forgetting it is not a missing help entry: with Subcommands empty a
// parent looks like a LEAF, so dispatch calls Run — and 12 of the 15 parents
// have a nil Run. Measured: `vp migrate` with an empty Subcommands panics with a
// nil pointer dereference. Doing the work here means there is no step to omit.
//
// # Why it is order-independent rather than relying on registration order
//
// Registration order happens to put every parent before its children today
// (measured: 0 of 51 children registered first), so a one-directional append
// would work. It is not pinned anywhere, though, and a future reordering of
// registerAll would silently drop a child — reintroducing the exact defect this
// replaces, with no literal left for a reader to notice was short. So both
// directions are handled: a child joins an already-registered parent, and a
// parent adopts children registered before it. The invariant is removed rather
// than asserted, and TestRegisterIsOrderIndependent pins that.
//
// Entries are appended in REGISTRATION order, which reproduces all fifteen
// hand-written literals byte for byte (measured 15/15) — so this introduces no
// ordering decision and changes no help output. Appending is additive and never
// clears what a caller supplied: an out-of-tree caller that still declares a
// literal keeps it, and a duplicate is not added.
func (r *Registry) linkSubcommands(cmd *Command) {
	if parent, _, ok := strings.Cut(cmd.Name, " "); ok {
		if p, registered := r.commands[parent]; registered {
			p.Subcommands = appendUnique(p.Subcommands, cmd.Name)
		}
		return
	}
	for _, name := range r.order {
		if parent, _, ok := strings.Cut(name, " "); ok && parent == cmd.Name {
			cmd.Subcommands = appendUnique(cmd.Subcommands, name)
		}
	}
}

// appendUnique appends name unless it is already present, so re-registering a
// command cannot double-list it under its parent.
func appendUnique(list []string, name string) []string {
	if slices.Contains(list, name) {
		return list
	}
	return append(list, name)
}

// Lookup finds a command by name.
func (r *Registry) Lookup(name string) (*Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// Each invokes fn for every registered command, including two-word
// subcommands and Hidden entries. Iteration order is registration
// order. Use All for the filtered, sorted view shown in top-level
// help.
func (r *Registry) Each(fn func(*Command)) {
	for _, name := range r.order {
		fn(r.commands[name])
	}
}

// All returns all non-hidden commands sorted by name. Two-word commands
// (e.g. "vault pull") are excluded when their parent (e.g. "vault") is
// also registered, since the parent's help now lists them.
func (r *Registry) All() []*Command {
	var cmds []*Command
	for _, name := range r.order {
		cmd := r.commands[name]
		if cmd.Hidden {
			continue
		}
		// Skip subcommands whose parent is registered.
		if parent, _, ok := strings.Cut(name, " "); ok {
			if _, hasParent := r.commands[parent]; hasParent {
				continue
			}
		}
		cmds = append(cmds, cmd)
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
				fmt.Fprint(r.out, r.formatHelp(cmd))
				return ExitOK
			}
			return r.runCmd(cmd, remaining)
		}
	}

	// Single-word lookup.
	if cmd, ok := r.commands[args[0]]; ok {
		remaining := args[1:]
		if hasHelpFlag(remaining) {
			fmt.Fprint(r.out, r.formatHelp(cmd))
			return ExitOK
		}
		return r.dispatchCommand(cmd, remaining)
	}

	fmt.Fprintf(r.errOut, "vp: unknown command %q\nRun 'vp help' for usage.\n", args[0])
	return ExitUser
}

// dispatchCommand routes a matched single-word command to Run, or to
// the parent-help / unknown-subcommand paths when the command has
// registered subcommands. See Command.BareInvocation for the opt-out.
func (r *Registry) dispatchCommand(cmd *Command, remaining []string) int {
	// Leaf command (no subcommands) — delegate to Run.
	if len(cmd.Subcommands) == 0 {
		return r.runCmd(cmd, remaining)
	}

	// Parent command. A two-word lookup already ran in Dispatch and
	// missed, so anything in remaining is either a flag, an unknown
	// subcommand token, or empty.
	if cmd.BareInvocation {
		// Empty or flag-only args → delegate to Run (stdin handler
		// or flag-only invocation). A non-flag first token is
		// treated as an unknown subcommand — preserves typo
		// detection.
		if len(remaining) == 0 || strings.HasPrefix(remaining[0], "-") {
			return r.runCmd(cmd, remaining)
		}
		return r.unknownSubcommand(cmd, remaining[0])
	}

	// Pure parent (no Run): bare invocation → help; unknown token → error.
	if len(remaining) == 0 {
		fmt.Fprint(r.out, r.formatHelp(cmd))
		return ExitOK
	}
	return r.unknownSubcommand(cmd, remaining[0])
}

// unknownSubcommand writes "vp <parent>: unknown subcommand ..." plus
// parent help to stderr and returns ExitUser.
func (r *Registry) unknownSubcommand(cmd *Command, token string) int {
	fmt.Fprintf(r.errOut, "vp %s: unknown subcommand %q\n\n", cmd.Name, token)
	fmt.Fprint(r.errOut, r.formatHelp(cmd))
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
					fmt.Fprint(r.out, r.formatHelp(cmd))
					return ExitOK
				}
			}
			// Single-word lookup.
			if cmd, ok := r.commands[args[0]]; ok {
				fmt.Fprint(r.out, r.formatHelp(cmd))
				return ExitOK
			}
			fmt.Fprintf(r.errOut, "vp help: unknown command %q\n", strings.Join(args, " "))
			return ExitUser
		},
	})
}

// formatHelp renders help for a command, using parent-aware rendering
// when the command has subcommands.
func (r *Registry) formatHelp(cmd *Command) string {
	return FormatHelpWithSubs(cmd, func(name string) *Command {
		c, _ := r.commands[name]
		return c
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
