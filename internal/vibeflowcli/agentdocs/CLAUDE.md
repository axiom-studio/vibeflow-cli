# Claude Code Configuration

## Permissions

Allow: Bash(sleep *)
Allow: Bash(git:*)

## vibeflow Agent Session Rules

**CRITICAL — When a vibeflow session_init prompt is active (autonomous agent mode), these rules apply to ALL work, including ad-hoc user requests:**

1. **NEVER write code, enter plan mode, or use EnterPlanMode before creating a tracked work item in vibeflow.** If the user asks you to build, fix, add, or modify anything, your FIRST action must be to classify it (feature todo or issue) and create it in vibeflow via the MCP tools. No exceptions.

2. **The ad-hoc request workflow in the agent prompt takes ABSOLUTE PRIORITY over Claude Code's built-in planning tools.** Do not use EnterPlanMode until after the vibeflow work item exists and has been transitioned to `implementing` status.

3. **Every piece of work must flow through vibeflow status transitions** (planning → implementing → done), with execution logs published, git commits tracked, and line counts passed — even for "small" or "quick" changes.

4. **When polling for work, always drill into features to check todos.** `list_features` returns containers, not work items. For each feature returned with `ready_to_implement` or `implementing` status, call `list_todos(feature_id, status: "ready_to_implement,implementing")` to find actual work items. Never treat an empty `list_issues` result as "no work" without also checking todos inside returned features.

5. **YOU MUST use filters for tool calls to optimize data fetch.** Example: when listing_features to find items ready for work, filter by status so you only get features that are ready.

6. **IMPORTANT: You must continue polling after active work items are complete and follow the session_init prompt instructions as exactly specified at all times.**

7. **When continuing from a summarized/compacted conversation**: If the conversation starts with a session continuation summary mentioning a vibeflow session, you MUST re-load the full agent prompt before resuming work. Do this by:
   a. Read `.vibeflow-session` from the working directory to get the existing session_id
   b. Call `session_init(project_name, session_id)` to get the full agent prompt
   c. Re-read the returned `prompt` field to reload Phase 1-4 instructions
   d. Skip Phase 1 steps already done (project lookup, etc.) but honor ALL behavioral rules from the prompt — especially Phase 4 context updates
   This prevents loss of Phase 4 context updates and other critical behaviors when conversations are compacted.
