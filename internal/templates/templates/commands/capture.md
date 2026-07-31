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
   - **model**: the model identifier you are running as — **always pass it.** The session note's DoD requires `model`, and hook-less hosts (Grok, Zed pane, HTTP serve) cannot derive it from the transcript, so your self-report is the only source there; on Claude Code the SessionEnd hook also fills it in, so supplying it is harmless
   - **transcript**: when the host can supply the session transcript text, pass it (required for friction scoring and durable archive content on hook-less hosts)
   - **archive_transcript**: `true` — for hook-less hosts (Grok, Zed pane, HTTP serve) / when not relying on SessionEnd; Claude Code no-ops this when the server can derive the host session id

3. Confirm the capture succeeded and report the session ID.

Unlike `/wrap`, this does NOT update the resume, retire tasks, or signal
end-of-session. Use it freely throughout a work session.
