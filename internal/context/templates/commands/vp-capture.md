# Capture — Mid-Session Checkpoint

Record a checkpoint of current work for {{PROJECT}} without ending the session.
Use this when you want to save progress, capture a decision, or recover from
a tool failure — without doing the full wrap-up sequence.

## Steps

1. Summarize what was accomplished or attempted since the last capture.

2. Call `vp_capture_session` with:
   - **project**: `{{PROJECT}}`
   - **summary** (required): What was accomplished or attempted
   - **tag**: implementation | debugging | refactor | exploration | review | docs | planning
   - **decisions**: Key technical decisions made (if any)
   - **files_changed**: Files created or modified (if any)
   - **open_threads**: Unresolved items or next steps

3. Confirm the capture succeeded and report the session ID.

Unlike `/wrap`, this does NOT update the resume, retire tasks, or signal
end-of-session. Use it freely throughout a work session.
