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

Drop a command override here as ` + "`<slug>.md`" + ` — for example,
` + "`wrap.md`" + ` shadows the vault-level wrap command with whatever
markdown you author here. The filename (minus the extension) is the
slug the CLI looks up.

To see what an embedded default looks like before you edit it, check
the materialized copy at ` + "`<vault>/Templates/commands/<slug>.md`" + `.

Promotion back into the ` + "`vibe-palace`" + ` source tree is a manual git
operation — ` + "`vp`" + ` does not know where your source checkout lives at
runtime. See doc/ARCHITECTURE.md for the materialize → edit → promote
workflow.
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

To see what an embedded default looks like before you edit it, check
the materialized copy at ` + "`<vault>/Templates/skills/<slug>/`" + `.

Promotion back into the ` + "`vibe-palace`" + ` source tree is a manual git
operation — ` + "`vp`" + ` does not know where your source checkout lives at
runtime. See doc/ARCHITECTURE.md for the materialize → edit → promote
workflow.
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
