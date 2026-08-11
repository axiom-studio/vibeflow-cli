# @axiom-studio/vibeflow-setup

One-command setup for VibeFlow. Replaces the manual Setup-page steps with:

```bash
npx @axiom-studio/vibeflow-setup --api-key <API_KEY>
```

It will:

1. Download the matching `vibeflow-cli` release binary for your OS/arch into
   `~/.vibeflow/bin`, after verifying its SHA-256 against the release's published
   `checksums.txt`. A mismatch deletes the download and aborts before the binary
   is executed in step 3.

   When the release also publishes `checksums.txt.sig`, its **Ed25519 signature**
   is verified against a public key pinned in `index.js` — that is what proves the
   checksum list came from us, not merely from the same server as the archive.
   Verified with Node's built-in `crypto`, so there is nothing extra to install.

   Once signing is live, a **missing** signature is refused by default for any
   release in the signed era — withholding a `.sig` would otherwise be a free way
   to downgrade the check back to same-origin trust. `--allow-unsigned` overrides
   that; a **bad** signature always aborts and cannot be overridden.

   **Current state:** signing is wired up but **inert until a release key is
   generated and the signed-era floor is set** (see [SIGNING.md](SIGNING.md)).
   Until then, and for genuinely older releases, the installer verifies checksums
   and says plainly that authenticity is unverified rather than implying a check
   it is not performing. Pass `--require-signature` to refuse anything unsigned
   even now.
2. Install **tmux** if it is missing (apt / dnf / yum / apk / brew). Skipped on
   Windows. If tmux is still missing afterwards the installer says so explicitly,
   because `vibeflow launch` cannot work without it.
3. Run `vibeflow bootstrap --all --api-key <key>`, which writes **7 targets**: the
   MCP server config for all six supported agents — Claude CLI, Claude Desktop,
   Gemini CLI, Cursor, Codex CLI, **Kiro CLI** — plus the `vibeflow-cli` `config.yaml`
   that stores your API key.

   Each agent config carries the endpoint and a `${MCP_TOKEN}` reference rather
   than the key itself. Four of the five JSON agents also get the 300000 ms
   client timeout; **Claude CLI deliberately gets none**, because it honors the
   `MCP_TIMEOUT` environment variable instead (bootstrap prints a note about
   this). Codex uses TOML with `bearer_token_env_var`. Claude Desktop is the one
   exception that stores the real key, in its own `env` block at mode 0600 —
   it is a GUI app, so it cannot receive a variable injected at launch.

## Options

| Flag | Default | Meaning |
|---|---|---|
| `--api-key <key>` | (required) | VibeFlow API key, from Account > API Keys |
| `--base-url <url>` | `https://cloud.axiomstudio.ai` | VibeFlow base URL |
| `--agents <csv>` | all | Configure only these agents |
| `--all` | on | Configure all supported agents |
| `--version <tag>` | latest | Pin a specific `vibeflow-cli` release |
| `--skip-checksum` | off | Skip SHA-256 verification of the download (not recommended) |
| `--require-signature` | off | Refuse to install unless the release checksums carry a valid signature |

## Verify

The configs reference the token as `${MCP_TOKEN}`, so the variable must be set in
whatever shell you verify from:

```bash
MCP_TOKEN=<your-api-key> claude mcp list   # should show: vibeflow ... ✓ Connected
```

Without it you get `[Warning] mcpServers.vibeflow: Missing environment variables:
MCP_TOKEN` and no connection. That is expected, not a failed setup — `vibeflow
launch` and the TUI inject `MCP_TOKEN` into every agent they start, so **only
hand-started agents need the export**. To verify through the supported path
instead, run `vibeflow launch` and let the agent call `list_projects`.

## Notes

- Requires Node >= 18. No npm dependencies (built-in modules only).
- Linux, macOS, and Windows (amd64/arm64) are supported.
- On Windows the MCP config is written normally, so Claude Desktop and the other
  agents connect — but `vibeflow launch` needs **tmux**, which has no Windows
  port, so run session launching under WSL. Step 2 (tmux install) is skipped.
- Model selection from the Setup UI is handled at agent-launch time, not by
  `bootstrap`, so it is not a flag here.
