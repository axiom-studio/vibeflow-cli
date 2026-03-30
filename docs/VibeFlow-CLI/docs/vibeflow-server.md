# VibeFlow server & personas

## Connecting to VibeFlow

**VibeFlow mode** uses your **API token** and **server URL** to:

- List projects and link a session to a project.
- Drive **autonomous** workflows where the agent polls for todos/issues (via MCP / agent integration on the server side).
- Record **heartbeats** and session activity when integrated with your deployment.

Defaults target **`https://cloud.axiomstudio.ai`**. Self-hosted or enterprise URLs are supported via config or `VIBEFLOW_URL`.

## Personas

When creating a VibeFlow session, you assign a **persona** so multiple agents can work in parallel without sharing one session id. Typical built-in personas include:

| Key | Label |
|-----|--------|
| `developer` | Developer |
| `architect` | Architect |
| `ux_designer` | UX Designer |
| `qa_lead` | QA Lead |
| `security_lead` | Security Lead |
| `product_manager` | Product Manager |
| `project_manager` | Project Manager |
| `customer` | Customer |

Each persona uses its own **`.vibeflow-session-<persona>`** file when applicable.

## Autonomous operation

With **skip permissions** / provider-specific autonomous flags, agents can run long-running loops: poll work, implement, commit, and publish logs—according to your VibeFlow project rules and MCP configuration. The CLI supplies the process supervision (tmux, recovery, session files); the **server and MCP tools** define task workflow.

## Claude Code plugin

VibeFlow also ships a **Claude Code plugin** path (MCP config, skills, hooks) for users who prefer lightweight integration inside Claude Code instead of the full vibeflow-cli TUI. The CLI can **skip duplicating** some embedded docs when the plugin is detected—see [Advanced topics](advanced-topics.md).

## Next steps

- [Configuration](configuration.md)
- [Advanced topics](advanced-topics.md)
