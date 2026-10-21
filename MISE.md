# Development with mise

Toolchain: Go 1.25.14.
Versions are pinned in `mise.toml`.

## Quick start

```sh
mise trust
mise install
mise tasks ls
mise run versions
```

Tasks: `dev`, `build`, `test`, `lint`.

The build task calls the existing Makefile, including its embedded agent-document synchronization and version metadata.
Pass CLI arguments after the task, for example `mise run dev -- --help`.
Existing VibeFlow credentials remain in the calling environment or CLI configuration.

## Local settings

Commit `mise.toml` and this guide to share tool versions and tasks with the team.
Keep personal overrides in ignored `mise.local.toml` and private helper scripts in `.mise-local/`.
Install mise separately on each machine and run `mise install` inside this repository.
macOS and Linux shells are supported; Windows developers should use WSL for the shell-based tasks.
Native mobile builds additionally need the appropriate platform toolchain.
Keep credentials in existing local secret files or environment variables, never in committed `mise.toml`.
Task commands run without sourcing ~/.zshrc.
Project runtime versions are selected by `mise run` even without shell activation.
