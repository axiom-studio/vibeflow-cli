# Worktrees & session files

## Git worktrees

Worktrees let each agent work on a **separate checkout** (often a different branch) under a configurable base directory (default **`.claude/worktrees`**). Benefits:

- Fewer file conflicts when multiple sessions run at once.
- Clear mapping from tmux session → branch → directory.

Configuration (`cleanup_on_kill`, `auto_create`, `base_dir`) controls whether worktrees are created automatically and whether you are prompted to delete them when a session ends.

## Session files

VibeFlow-aware sessions write a small marker file in the working directory so the tool can detect **stale** vs **active** usage:

| File | When |
|------|------|
| `.vibeflow-session` | Legacy / non-persona vanilla path |
| `.vibeflow-session-<persona>` | VibeFlow sessions with a persona (e.g. `.vibeflow-session-developer`) |

The file holds the session id and metadata lines such as `provider=` and `persona=` so conflict checks are accurate.

## Conflict detection

Before launching, the CLI checks whether another **live** session already owns the same directory/persona combination. If so, you get a **resolution dialog**: attach to the existing session, use a worktree, clean up a stale file, or cancel.

## CLI check

```bash
vibeflow check
vibeflow check /path/to/repo
```

Use this in scripts or before automation to ensure a directory is safe for a new agent.

## Next steps

- [Configuration](configuration.md)
- [Troubleshooting](troubleshooting.md)
