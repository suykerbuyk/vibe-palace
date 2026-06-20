To the degree possible, I would like you to sequentially dispatch a subagent to complete each phase or task (your choice depending upon size), and for you to verify the deliverable matches the plan in form, fit, and intent.

This workflow is intended to preserve AI thread context for you, the orhestrating agent to run this epic all the way through to the finish line while still remaining within a sane boundary for your thread token context (200K tokens). Let sub agents do the work, you verify their work and the conformance to the planned deliverables.

If you encounter an issue during the subagent task completion review that might affect form, fit, function of the deliverable, pause and inform the human.  Explain the situation and offer options as to how to resolve it.  Do things right - don't guess, don't create "slop," don't hallucinate.  If in doubt, stop and ask for help from your pair programming partner, the human architect.

Note: when a subagent (or you) reads a task via `vp_get_task`, the body may
arrive as a `content_uri` (with `include_content=false`) rather than inline.
Read the resource (`resources/read`) or page it via `vp_read_resource` to get
the full body before acting on it.

No work is done until:
  - We achieve as close to 80% unit test coverage as is realistic.
  - We prove the function does what it is supposed to do in the context of the full stack with integration test.
  - Documentation has been updated.
