# {{PROJECT}} — Workflow

<!-- THIN BY DESIGN (ADR-008). This file carries ONLY project-specific
     workflow patterns. The generic Vibe-Palace doctrine — pair programming,
     investigation-first workflow, task management, vault-accessor rules,
     commit discipline, core principles — is owned by the binary and served
     on demand; do not copy it back in here. -->

## Bootstrap Contract

Call `vp_bootstrap_context` at session start. The full Vibe-Palace operating
doctrine is served on demand, off the bootstrap payload: fetch it with
`vp_get_doctrine` (or read `vibe-palace://doctrine/{{PROJECT}}` via
`vp_read_resource`) before starting work, and follow it for the rest of the
session. Nothing is done until the human says it is done.

## Files

- **resume.md** — current project state, open threads, navigation (thin gateway)
- **iterations.md** — iteration narratives and project history (append-only archive)
- **tasks/** — active tasks; **tasks/done/** — completed (vault-resident,
  reached only via `vp_manage_task` / `vp_get_task` / `vp_list_tasks`)
- **commands/** — slash commands
- **doc/** — stable project reference: architecture, design decisions, testing

## Project-Specific Patterns

<!-- Record patterns that are true for THIS project only: branch/merge and
     wrap-timing rules, data workflow, build/test quirks, standing operator
     rules. Generic doctrine belongs in the served doctrine, not here. -->

- (none recorded yet)
