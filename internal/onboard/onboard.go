// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package onboard is the single definition of what "onboarding a project"
// means, shared by `vp init` and (in a later commit) the vp_init MCP tool.
//
// # Why a package rather than a helper in cmd/vp
//
// `vp init` grew eight distinct writes across three SURFACES that do not belong
// to the same machine:
//
//   - the VAULT (Projects/<slug>/config.toml, the scaffold tree),
//   - the WORKING TREE (.vibe-palace.toml, AGENTS.md, .claude/commands/,
//     .gitignore, .git/hooks/post-commit),
//   - the RUNNING HOST'S GLOBALS (~/.claude/settings.json).
//
// On the CLI all three are the same machine, so the distinction is invisible
// and the code never had to draw it. Over MCP they are not: the server's home
// directory is the OPERATOR's, and the project directory may not even exist on
// the server. A surface therefore has to be able to say "I may not write that"
// — and, critically, to say so OUT LOUD rather than by silently doing less.
// That is what Side, Scope and Omission are for.
//
// # What this package does NOT own
//
// The installation bootstrap — global config, vault creation, Templates/
// materialize — stays in cmd/vp. Those are "does this machine have a
// vibe-palace at all", not "is this project onboarded", and the MCP tool
// answers the first question by refusing rather than by creating.
//
// It also owns no writer of its own: every step drives a reconciler or helper
// that already exists in a shared package. A step body here is orchestration
// and row vocabulary, never file I/O.
package onboard

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Side names the surface a step writes to. It is the unit Scope filters on.
type Side int

const (
	// SideVault writes under the vault root. Every surface may do this: the
	// vault is the thing both the CLI and the MCP server are bound to.
	SideVault Side = iota
	// SideWorkingTree writes under the project directory. A surface may only
	// do this for a directory it can prove is a project root.
	SideWorkingTree
	// SideHostGlobal writes the RUNNING host's user-global state
	// (~/.claude/settings.json). Over MCP that is the server operator's
	// machine, never the caller's, so no remote surface may ever admit it.
	SideHostGlobal
)

func (s Side) String() string {
	switch s {
	case SideVault:
		return "vault"
	case SideWorkingTree:
		return "working-tree"
	case SideHostGlobal:
		return "host-global"
	}
	return fmt.Sprintf("Side(%d)", int(s))
}

// Status values are check's, re-exported so callers and tests can spell
// onboard.Fail without importing check for a constant. Sharing the type is
// what keeps the renderer a straight field copy.
const (
	Pass = check.Pass
	Fail = check.Fail
	Skip = check.Skip
	Info = check.Info
)

// Step is one unit of onboarding.
type Step struct {
	// Name is the stable identity used by Needs, by Omitted, and by the
	// golden table. It is not user-facing text; see rowName.
	Name string
	// Side is the surface this step WRITES.
	Side Side
	// ReadsHostGlobal marks a step that consults the RUNNING host's ~/.claude
	// or ~/.grok to decide what to write. It is orthogonal to Side: a step can
	// write the working tree and still read the host's home, which over MCP
	// would consult the SERVER OPERATOR's surface to decide the CALLER's
	// files. Such a step is excluded from any scope that may not write
	// SideHostGlobal.
	ReadsHostGlobal bool
	// Needs names steps that must have SUCCEEDED first. An unmet prerequisite
	// is a Skip outcome, never an Omission: the step was in scope, it simply
	// did not get to run.
	Needs []string
	// Run drives the writer. It must return at least one Outcome — a step
	// that writes nothing still owes the operator a row saying so.
	Run func(ctx context.Context, req Request) []Outcome
}

// Request is everything a step needs that it cannot derive itself.
type Request struct {
	// OpenVault resolves the vault. Run memoizes it — vault AND error both —
	// and it is first called on the first SideVault step, i.e. AFTER
	// cwd-project has had its chance to write the .vibe-palace.toml whose
	// vault_path override the resolution honours. Resolving eagerly would
	// read the world one write too early.
	OpenVault func() (*storage.Vault, error)

	Slug              string
	ProjectDir        string
	Domain            string
	Tags              []string
	VaultPathOverride string
}

// Outcome is one rendered row produced by a step that RAN.
type Outcome struct {
	// Step is stamped by Run from the step table; a body need not set it.
	Step    string
	Name    string
	Status  check.Status
	Summary string
	Details []string
}

// Omission is a step a SURFACE MAY NOT RUN. It is not a failure and not a
// deferral — it is the boundary of what this caller is allowed to do, stated
// explicitly so the operator learns what is still missing and where to go do
// it.
//
// Remedy is mandatory and must name three things: the verbatim command, the
// HOST it has to run on, and the artifact that is missing. "Run vp init" is
// useless to someone whose MCP server lives on another machine.
type Omission struct {
	Step   string
	Side   Side
	Reason string
	Remedy string
}

// Result is the whole run.
type Result struct {
	Slug     string
	Outcomes []Outcome
	Omitted  []Omission
	// Complete is true only when every step was in scope AND none failed.
	// A surface that reports Complete is saying "there is nothing left for a
	// human to do on another machine".
	Complete bool
}

// Scope decides which sides a surface may write.
//
// It also, indirectly, decides which steps may READ the running host's
// globals: a scope that does not admit SideHostGlobal excludes every step with
// ReadsHostGlobal set. The implication is not a shortcut — a surface that is
// not allowed to WRITE the operator's ~/.claude has no business READING it to
// decide what to write into somebody else's project either.
type Scope map[Side]bool

// ScopeCLI is the local operator: one machine, all three surfaces.
var ScopeCLI = Scope{SideVault: true, SideWorkingTree: true, SideHostGlobal: true}

// ScopeForMCP is the scope a vp_init MCP call gets.
//
//   - SideVault: always. The server is bound to the vault; that binding is the
//     entire premise of the tool.
//   - SideWorkingTree: only when req.ProjectDir passes project.HasRootedSignal
//     — the directory itself must carry .vibe-palace.toml, .git or a known
//     manifest, and must not be $HOME or /. Without that proof the server is
//     being asked to scaffold into an arbitrary path on its own disk.
//   - SideHostGlobal: never. See Side's doc.
func ScopeForMCP(req Request) Scope {
	return Scope{
		SideVault:       true,
		SideWorkingTree: project.HasRootedSignal(req.ProjectDir),
		SideHostGlobal:  false,
	}
}

// Steps returns the canonical step table, in the order `vp init` has always
// run them. The returned slice is a copy: a caller cannot retag the table.
func Steps() []Step { return slices.Clone(stepTable) }

// Run executes the step table under scope.
//
// preOmitted lets the CALLER declare steps it has already accounted for and
// that must therefore not run. It has exactly one user today: `vp init`'s
// "the project already carries a .vibe-palace.toml" early return, which has
// never re-touched the project-config artifacts on a re-init. When that gate
// is deleted, this argument goes with it. Passing an Omission for an unknown
// step is a programming error and is reported as one.
//
// The returned error is an ACCOUNTING failure, not a step failure: step
// failures are rows. Run refuses to hand back a Result in which some step is
// neither in Outcomes nor in Omitted, because that is exactly the shape of the
// bug this package exists to prevent — a surface that quietly did less than it
// was asked and reported success.
func Run(ctx context.Context, req Request, scope Scope, preOmitted ...Omission) (Result, error) {
	steps := Steps()
	known := make(map[string]Step, len(steps))
	for _, s := range steps {
		known[s.Name] = s
	}

	pre := make(map[string]Omission, len(preOmitted))
	for _, om := range preOmitted {
		s, ok := known[om.Step]
		if !ok {
			return Result{}, fmt.Errorf("onboard: pre-omitted step %q is not in the step table", om.Step)
		}
		om.Side = s.Side
		pre[om.Step] = om
	}

	res := Result{Slug: req.Slug}

	// Memoize the vault AND the error. A failing resolution re-derived per
	// step would report the same fault three times and, worse, could report it
	// DIFFERENTLY on a flapping filesystem.
	var (
		vault       *storage.Vault
		vaultErr    error
		vaultOpened bool
	)
	openVault := func() (*storage.Vault, error) {
		if !vaultOpened {
			vaultOpened = true
			if req.OpenVault == nil {
				vaultErr = fmt.Errorf("no vault resolver supplied")
			} else {
				vault, vaultErr = req.OpenVault()
			}
		}
		return vault, vaultErr
	}
	stepReq := req
	stepReq.OpenVault = openVault

	// vaultErrReported keeps the resolution failure to ONE Fail row; every
	// later SideVault step names it as the cause of its own Skip.
	vaultErrReported := false
	// succeeded records which steps a Needs clause may depend on.
	succeeded := map[string]bool{}

	for _, step := range steps {
		if om, ok := pre[step.Name]; ok {
			res.Omitted = append(res.Omitted, om)
			continue
		}
		if !scope[step.Side] {
			res.Omitted = append(res.Omitted, omitForSide(step, req))
			continue
		}
		if step.ReadsHostGlobal && !scope[SideHostGlobal] {
			res.Omitted = append(res.Omitted, omitForHostRead(step, req))
			continue
		}

		// The vault gate runs BEFORE the Needs gate deliberately. When the
		// vault cannot be opened at all, "the vault could not be resolved" is
		// the root cause and "a prerequisite failed" is its shadow; naming the
		// shadow sends the operator to the wrong repair.
		if step.Side == SideVault {
			if _, err := openVault(); err != nil {
				if !vaultErrReported {
					vaultErrReported = true
					res.Outcomes = append(res.Outcomes, Outcome{
						Step:    step.Name,
						Name:    rowNameFor(step.Name),
						Status:  Fail,
						Summary: "open vault: " + err.Error(),
					})
				} else {
					res.Outcomes = append(res.Outcomes, Outcome{
						Step:    step.Name,
						Name:    rowNameFor(step.Name),
						Status:  Skip,
						Summary: "skipped — open vault: " + err.Error(),
					})
				}
				continue
			}
		}

		if unmet := firstUnmet(step.Needs, succeeded); unmet != "" {
			res.Outcomes = append(res.Outcomes, Outcome{
				Step:    step.Name,
				Name:    rowNameFor(step.Name),
				Status:  Skip,
				Summary: "skipped — prerequisite " + unmet + " did not succeed",
			})
			continue
		}

		out := step.Run(ctx, stepReq)
		stepOK := len(out) > 0
		for i := range out {
			out[i].Step = step.Name
			if out[i].Name == "" {
				out[i].Name = rowNameFor(step.Name)
			}
			if out[i].Status == Fail {
				stepOK = false
			}
		}
		succeeded[step.Name] = stepOK
		res.Outcomes = append(res.Outcomes, out...)
	}

	if err := account(steps, res); err != nil {
		return Result{}, err
	}

	res.Complete = len(res.Omitted) == 0
	for _, oc := range res.Outcomes {
		if oc.Status == Fail {
			res.Complete = false
			break
		}
	}
	return res, nil
}

// firstUnmet returns the first prerequisite that did not succeed, or "".
func firstUnmet(needs []string, succeeded map[string]bool) string {
	for _, n := range needs {
		if !succeeded[n] {
			return n
		}
	}
	return ""
}

// account is the invariant: every step appears EXACTLY ONCE across the set of
// steps that produced outcomes and the set of omissions.
//
// It is an assertion and not a test-only helper on purpose. The failure mode it
// catches — a step that fell through every branch and produced nothing — is
// invisible in a status table (a missing row looks like a short table, and a
// short table looks fine) and is precisely how "vp_init reported success" and
// "the project is onboarded" come apart.
func account(steps []Step, res Result) error {
	produced := map[string]bool{}
	for _, oc := range res.Outcomes {
		produced[oc.Step] = true
	}
	omitted := map[string]bool{}
	for _, om := range res.Omitted {
		omitted[om.Step] = true
	}

	known := map[string]bool{}
	var missing, doubled []string
	for _, s := range steps {
		known[s.Name] = true
		switch {
		case produced[s.Name] && omitted[s.Name]:
			doubled = append(doubled, s.Name)
		case !produced[s.Name] && !omitted[s.Name]:
			missing = append(missing, s.Name)
		}
	}
	var stray []string
	for name := range produced {
		if !known[name] {
			stray = append(stray, name)
		}
	}
	for name := range omitted {
		if !known[name] {
			stray = append(stray, name)
		}
	}
	slices.Sort(stray)

	if len(missing) == 0 && len(doubled) == 0 && len(stray) == 0 {
		return nil
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "produced neither an outcome nor an omission: "+strings.Join(missing, ", "))
	}
	if len(doubled) > 0 {
		parts = append(parts, "both ran and was omitted: "+strings.Join(doubled, ", "))
	}
	if len(stray) > 0 {
		parts = append(parts, "named a step outside the table: "+strings.Join(stray, ", "))
	}
	return fmt.Errorf("onboard: step accounting failed — %s", strings.Join(parts, "; "))
}
