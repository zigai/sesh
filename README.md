# sesh

[![tests](https://img.shields.io/github/actions/workflow/status/zigai/sesh/test.yml?branch=master&label=tests&labelColor=30363d)](https://github.com/zigai/sesh/actions/workflows/test.yml)
[![lint](https://img.shields.io/github/actions/workflow/status/zigai/sesh/lint.yml?branch=master&label=lint&labelColor=30363d)](https://github.com/zigai/sesh/actions/workflows/lint.yml)
[![go](https://img.shields.io/github/go-mod/go-version/zigai/sesh?label=&logo=go&logoColor=white&labelColor=30363d)](go.mod)
[![tmux](https://img.shields.io/badge/tmux-3.6+-blue?logo=tmux&logoColor=white&labelColor=30363d)](https://github.com/tmux/tmux)

---

**everything stock [tmux](https://github.com/tmux/tmux) is missing, without replacing it.**

- **still just tmux** - your sessions, windows, and keys work as before.
- **one picker for everything** - switch between sessions, windows, and agents from one popup.
- **survives reboots** - sessions are saved automatically and restored after a restart, with supported agents resumed.
- **a window per task** - each task gets its own worktree and agent, and closes when its PR merges.
- **manage your coding agents** - track every agent's status, jump to the one that needs you, and resume past conversations.
- **several machines, one picker** - reach tmux on other machines over SSH.
- **make it yours** - custom keys, menus, fields, and hooks.

---

## Installation

```sh
go install github.com/zigai/sesh/cmd/sesh@latest
```

Requires Linux, tmux 3.6+, Go 1.27.4+, and [AHT](https://github.com/zigai/aht) for agent features.

## Quick start

Register the key bindings on the running tmux server and connect the coding agents you use:

```sh
sesh setup tmux --apply
sesh setup agents claude codex
```

Press `Ctrl+o` in tmux to open the picker. Add the output of `sesh setup tmux` to your `tmux.conf` to keep the bindings after tmux restarts.

Save sessions and track agents in the background:

```sh
sesh setup services --apply
systemctl --user enable --now sesh-autosave.timer sesh-agent-status.service
```

After a reboot, start tmux and restore the saved sessions:

```sh
sesh snapshot restore
```

## Commands

```text
$ sesh --help
Session, task, and agent manager for tmux

Usage:
  sesh [flags]
  sesh [command]

Available Commands:
  pick        Open the interactive session picker
  session     Manage live tmux sessions
  snapshot    Manage native tmux session snapshots
  agent       Navigate between coding agents
  project     Open projects in their own sessions
  task        Start tasks in their own windows and worktrees
  autosave    Manage periodic and lifecycle snapshot automation
  remote      Browse and operate configured remote tmux peers
  keys        List the session picker key bindings
  logs        Display diagnostic logs
  hooks       Inspect and run configured lifecycle hooks
  config      Manage native sesh configuration
  setup       Render or apply tmux bindings, agent integrations and services
  daemon      Run the long-running processes started by service managers
  doctor      Check tmux, configuration, snapshots and services
  help        Help about any command

Flags:
      --config <path>   Explicit native TOML configuration file
      --help            Show help for this command
      --no-config       Use built-in settings without reading configuration files
  -V, --version         Print version

Use "sesh [command] --help" for more information about a command.
```

## Configuration

Configuration is optional. Create `~/.config/sesh/config.toml` with every setting and its default, commented out:

```sh
sesh config init
```

A minimal example that adds a second project folder and saves less often:

```toml
[projects]
roots = ["~/Projects", "~/work"]

[autosave]
interval = "10m"
```

`SESH_<TABLE>_<KEY>` environment variables override single settings, such as `SESH_SETTINGS_LOG_LEVEL=warn`.

## Documentation

- [Configuration](docs/configuration.md)
