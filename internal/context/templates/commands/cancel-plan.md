# Cancel Plan — Task Cancellation

Cancel a planned task that was investigated and found not worth implementing.

## Steps

1. Document why the task is being cancelled (out of scope, superseded,
   not feasible, or requirements changed).

2. Cancel the task using `vp_manage_task` with `action: cancel`.

3. If the cancellation reveals follow-up work, create a new task for it.

4. Update the resume if the cancellation affects open threads or project state.
