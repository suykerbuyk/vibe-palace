// Package cli provides a lightweight CLI framework with structured command
// metadata, flag parsing, and help rendering.
package cli

// Exit codes for consistent CLI behavior.
const (
	ExitOK     = 0 // success
	ExitUser   = 1 // bad arguments, missing config, validation failure
	ExitSystem = 2 // I/O error, embedder failure, internal bug
)

// FlagDef describes a single command-line flag.
type FlagDef struct {
	Name    string // long form, e.g. "--project"
	Short   string // short form, e.g. "-p" (empty if none)
	Arg     string // argument placeholder, e.g. "PROJECT" (empty for booleans)
	Help    string // one-line description
	Default string // display default (empty if none)
}

// IsBool returns true if the flag takes no argument value.
func (f FlagDef) IsBool() bool { return f.Arg == "" }

// Example describes a command usage example.
type Example struct {
	Cmd     string // e.g. "vp search \"database\" --project myapp"
	Comment string // e.g. "Search myapp for database content"
}

// Command describes a CLI command with structured metadata.
//
// Run may be nil when Subcommands is non-empty: the dispatcher renders
// parent help for bare invocation (`vp <parent>`) and emits an
// unknown-subcommand error for `vp <parent> <bogus>`. Set
// BareInvocation to route `vp <parent>` back to Run (used by commands
// that are both a parent and a handler, e.g. `vp hook` which reads
// stdin from Claude Code).
type Command struct {
	Name        string    // e.g. "search" or "vault sync" (two-word)
	Synopsis    string    // e.g. "vp search <query> [flags]"
	Description string    // paragraph description
	Flags       []FlagDef // supported flags
	Examples    []Example // usage examples
	Subcommands []string  // registered names of child commands (e.g. "vault pull")
	Run         func(args []string) int
	Hidden      bool // exclude from top-level help
	// BareInvocation routes bare/flag-only invocations of a parent
	// command back to Run instead of rendering parent help.
	// Non-flag unknown subcommand tokens still produce the standard
	// unknown-subcommand error. Has no effect when Subcommands is
	// empty.
	BareInvocation bool
}
