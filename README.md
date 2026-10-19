# sesh

<p align="center">
  <a href="https://github.com/zigai/sesh/actions/workflows/test.yml"><img src="https://img.shields.io/github/actions/workflow/status/zigai/sesh/test.yml?branch=master&label=tests&labelColor=333333" alt="tests" /></a>
  <a href="https://github.com/zigai/sesh/blob/master/go.mod"><img src="https://img.shields.io/github/go-mod/go-version/zigai/sesh?label=go&labelColor=333333&color=666666" alt="Go version" /></a>
</p>

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

## Install

```sh
go install github.com/zigai/sesh/cmd/sesh@latest
```

Requirements: Linux, tmux 3.6+, Go 1.27.1+.

## Usage

```sh
sesh --help
```

```txt
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

## Documentation

- [Configuration](docs/configuration.md)
