// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

// commandsReadmeStub is the README body written into scaffolded
// commands/ directories. Explains the 5-tier precedence and shows one
// example override. Kept short (≤40 lines) so users actually read it.
const commandsReadmeStub = `# Commands override directory

Files dropped here shadow the embedded command templates that ship
inside the ` + "`vp`" + ` binary. The resolver walks five tiers in this order
and returns the first match it finds:

  1. Room (Projects/<slug>/commands/<wing>/<room>/<name>.md)
  2. Wing (Projects/<slug>/commands/<wing>/<name>.md)
  3. Project (Projects/<slug>/commands/<name>.md — this directory)
  4. Vault (<vault>/Templates/commands/<name>.md)
  5. Embedded defaults (compiled into ` + "`vp`" + `)

When wing and room aren't set, the resolver collapses to the 3-tier
subset: Project > Vault > Embedded.

Override a built-in here for this project — ` + "`wrap.md`" + ` shadows the
built-in wrap command — or under <vault>/Templates/commands/ for every
project. No vp reconciler or upgrade command changes an override at
either tier. That is what "safe" means: vp_vault_write / edit / move /
delete and ` + "`vp vault commit --paths .`" + ` are direct edits and reach
any tier. An unedited copy of a built-in under <vault>/Templates/ is
vp's bytes and is pruned — edit it before syncing; a copy here is never
pruned. ` + "`vp commands reset <slug>`" + ` removes a vault override and keeps
a backup.

To start from a built-in, fetch it with the ` + "`vp_get_command`" + ` MCP
tool or copy internal/templates/templates/commands/<slug>.md from a
vibe-palace checkout.

Promotion back into the ` + "`vibe-palace`" + ` source tree is a manual git
operation — ` + "`vp`" + ` does not know where your source checkout lives at
runtime. See doc/ARCHITECTURE.md for the override → promote workflow.
`

// skillsReadmeStub is the README body written into scaffolded skills/
// directories. Same five-tier explanation as commandsReadmeStub; the
// two directory-specific sentences below differ.
const skillsReadmeStub = `# Skills override directory

Files dropped here shadow the embedded skill templates that ship
inside the ` + "`vp`" + ` binary. The resolver walks five tiers in this order
and returns the first match it finds:

  1. Room (Projects/<slug>/skills/<wing>/<room>/<name>.md)
  2. Wing (Projects/<slug>/skills/<wing>/<name>.md)
  3. Project (Projects/<slug>/skills/<name>.md — this directory)
  4. Vault (<vault>/Templates/skills/<name>.md)
  5. Embedded defaults (compiled into ` + "`vp`" + `)

When wing and room aren't set, the resolver collapses to the 3-tier
subset: Project > Vault > Embedded.

Skills are **directory-form**: drop a skill override here as
` + "`<slug>/SKILL.md`" + ` with optional ` + "`<slug>/references/*.md`" + ` files.
The ` + "`SKILL.md`" + ` is the thin persona entry-point (YAML frontmatter
plus body); the ` + "`references/`" + ` tree holds heavy material fetched
on demand. Overrides happen at the directory level: the tier that
supplies ` + "`SKILL.md`" + ` wins for the persona, while each reference
file falls through independently — if your project-level override
omits ` + "`references/capex-opex.md`" + `, the resolver will fetch that one
file from the next tier down. Flat-file skills (` + "`<slug>.md`" + `
without a directory) are not supported.

Override a built-in skill here for this project, or under
<vault>/Templates/skills/ for every project. No vp reconciler or
upgrade command changes an override at either tier. That is what
"safe" means: vp_vault_write / edit / move / delete and
` + "`vp vault commit --paths .`" + ` are direct edits and reach any tier. An
unedited copy of a built-in file under <vault>/Templates/ is vp's
bytes and is pruned — edit it before syncing; a copy here is never
pruned. ` + "`vp skills reset <slug>`" + ` removes a vault override and keeps
a backup. To start from a built-in, print it with
` + "`vp skills show <slug> [--section NAME]`" + `.

Promotion back into the ` + "`vibe-palace`" + ` source tree is a manual git
operation — ` + "`vp`" + ` does not know where your source checkout lives at
runtime. See doc/ARCHITECTURE.md for the override → promote workflow.
`

// RenderReadmeStub returns the canonical README body for a scaffolded
// override directory. kind must be "commands" or "skills"; any other
// value returns the empty string.
func RenderReadmeStub(kind string) string {
	switch kind {
	case "commands":
		return commandsReadmeStub
	case "skills":
		return skillsReadmeStub
	default:
		return ""
	}
}
