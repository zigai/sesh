# Configuration

Location: `$XDG_CONFIG_HOME/sesh/config.toml` (defaults to `~/.config/sesh/config.toml`).

Initialize template:

```sh
sesh config init
```

Override via flag: `--config /path/to/config.toml` or `--no-config`.

Override via environment: `SESH_<SECTION>_<KEY>`, for example `SESH_SETTINGS_LOG_LEVEL=warn`.

## Storage paths

| Component | Default path |
| --- | --- |
| Config | `$XDG_CONFIG_HOME/sesh/config.toml` |
| Data | `$XDG_DATA_HOME/sesh` |
| Snapshots | `$XDG_DATA_HOME/sesh/snapshots` |
| Logs | `$XDG_STATE_HOME/sesh/sesh.log` |
| History | `$XDG_STATE_HOME/sesh/history.json` |

## Default configuration

```toml
[settings]
show_footer = true
directory_suggestions = 5
default_session_path_suggestion = "~/Projects/{session}"
display_modes = ["sesh", "agents"]
log_level = "info" # trace, debug, info, warn, error, disabled
shortcut_style = "none" # none, indent, right

[popup]
sesh_width = "40%"
sesh_height = "45%"
agents_width = "40%"
agents_height = "80%"

[display]
# Row templates: {field} substitutions; {right} and agent columns align across filtered rows.
sesh_format = "{source_prefix}{session_name}{right}{session_agent_context}"
agents_session_format = "{source_prefix}{session_name}"
agents_agent_format = "{tree}{status_glyph} {status_age:<7}   {agent_cwd_name:<23.23}  {agent_session}{right}{harness:<6.6}   {agent_git_label:<6.22} {agent_git_added:<5} {agent_git_deleted:<5} {agent_git_ahead:<2} {agent_git_behind:<2}"

[agent_sessions]
enabled = true
popup_sort = "position" # position, name, attention
popup_counts = true
refresh_interval = "250ms"
status_style = "glyph" # glyph, text
status_time_format = "relative" # relative, relative-seconds, clock, hh:mm

[agent_status]
enabled = true
store_path = "" # defaults to ~/.local/state/aht/state.json
tmux_binary = "tmux"
style_waiting = "fg=#1e1e2e,bg=#ffdd57,bold"
style_failed = "fg=#1e1e2e,bg=#ff5555,bold"
style_unread = "fg=#1e1e2e,bg=#6c7086,bold"
set_window_status_style = true
set_window_status_bell_style = true
active_bypass = true
clear_failed_on_visit = true

[remote]
enabled = false
ssh_path = "ssh"
tmux_poll_interval = "1s"
connect_timeout = "5s"
action_timeout = "2m"
max_parallel_connects = 4
enable_reverse_bridge = true
return_to_origin = true
connections = []
# Example peer connection:
# connections = [
#   { id = "prod", label = "Production", host = "prod", sesh_path = "/usr/local/bin/sesh", tmux_socket = "/tmp/tmux-1000/default" }
# ]

[autosave]
enabled = true
interval = "3m"
restore_last_on_start = true # restore the newest snapshot when the managed tmux service starts (see docs/integrations.md)

[snapshot]
retention_days = 14 # 0 disables age limit
max_count = 0       # 0 disables count limit
max_bytes = 0       # 0 disables size limit
process_environment = ["PATH", "VIRTUAL_ENV", "CONDA_PREFIX", "CONDA_DEFAULT_ENV", "PYTHONPATH", "NODE_PATH"]

[keys]
help = "?"
cycle_mode = "ctrl+f"
agent_sort = "ctrl+s"

[theme]
preset = "dracula" # dracula, catppuccin, catppuccin-latte, tokyo-night, gruvbox, nord, terminal
primary = "#24c8ff"
secondary = "#ffdd57"
muted = "#6c7086"
error = "#ff5555"
warning = "#ff9e3b"
success = "#50fa7b"
border = "#3a3d45"
selected = "#2a2a2a"
background = "" # empty for black popup, "default" for transparent terminal background
foreground = "" # empty/default for light text, or custom hex/ANSI

[theme.agent_status]
running = "#50fa7b"
waiting = "#ffdd57"
failed = "#ff5555"
interrupted = "#ff9e3b"
idle = "#6c7086"

[paths]
data_dir = "" # defaults to $XDG_DATA_HOME/sesh
log_file = "" # defaults to $XDG_STATE_HOME/sesh/sesh.log

[hooks]
timeout = "30s"
session_switch_pre = []
session_switch_post = []
session_attach_pre = []
session_attach_post = []
tmux_startup_post = []
```

## Actions

Per-action policy is keyed by action ID. `confirm` asks before the popup runs the action; `allow_remote` lets remote peers run it on this machine. `session.kill` and `session.delete` confirm and refuse remote use by default; `session.switch` never runs remotely.

```toml
[actions.session.kill]
confirm = false
allow_remote = true
```

## Key bindings

`[[keys.bind]]` binds a key to an action in the picker (`in = "picker"`) or in a tmux key table (`in = "tmux"`, rendered by `sesh setup tmux`). An entry replaces the default binding of the same key, and an entry without an action removes it. `params` sets action parameters. Tmux bindings pass the current session or pane as the action's subject. Picker bindings cannot use the picker's own navigation keys or keys that type into its search field.

Defaults: in the picker `alt+c` session.create, `alt+r` session.rename, `alt+x` session.kill, `alt+X` session.delete, `alt+s` snapshot.save; in tmux table `root` `S-F12` agent.next, `M-S-F12` agent.next with `local = true`, `S-F1` agent.back, `C-o` pick.popup.

```toml
[[keys.bind]]
key = "ctrl+g"
action = "session.create"
in = "picker"

[[keys.bind]]
key = "alt+c"
in = "picker"

[[keys.bind]]
key = "M-s"
action = "snapshot.save"
in = "tmux"
table = "prefix"
```
