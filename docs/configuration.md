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
| Custom field cache | `$XDG_CACHE_HOME/sesh/fields` |

## Default configuration

```toml
[settings]
confirm_kill = true
confirm_delete = true
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
next_idle_key = "F24"
next_idle_local_key = "M-F24"
navigation_back_key = "F13"

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
permissions = { create = true, rename = true, kill = false, delete = false, snapshot = true, restore = true }
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
create = "alt+c"
rename = "alt+r"
kill = "alt+x"
delete = "alt+shift+x"
snapshot = "alt+s"
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

## Custom fields

`{custom.NAME}` in a row format shows the output of the command defined under `[display.fields]`:

```toml
[display]
sesh_format = "{source_prefix}{session_name}{right}{custom.git_sha}"

[display.fields]
git_sha = "git rev-parse --short HEAD"
weather = { command = "/usr/local/bin/weather", args = ["--short"], refresh = "10m" }
```

A string runs with `/bin/sh -c`. A table runs `command` with `args`, for example `{ command = "zsh", args = ["-c", "..."] }` for another shell. Names use `a-z`, `0-9` and `_`.

The popup runs a field for each row when it opens and again when that row changes. `refresh` also reruns it on an interval. Rows run at the same time. Closing the popup stops running commands.

Each run gets the row as one JSON line on stdin, with `schemaVersion`, `field`, `row`, `session`, and `agent` on agent rows. It also gets these environment variables:

| Variable | Value |
| --- | --- |
| `SESH_FIELD` | Field name |
| `SESH_ROW` | `session` or `agent` |
| `SESH_SESSION`, `SESH_SESSION_ID` | Session name and ID |
| `SESH_SESSION_KIND` | `live` or `saved` |
| `SESH_SESSION_PATH` | Session directory |
| `SESH_ORIGIN`, `SESH_CONNECTION_ID` | `local`, `ssh` or `bridge`, and the remote connection |
| `SESH_PANE_ID` | Agent pane on agent rows, active pane on session rows |
| `SESH_AGENT_HARNESS`, `SESH_AGENT_STATUS`, `SESH_AGENT_CWD`, `SESH_AGENT_PROJECT_ROOT`, `SESH_AGENT_GIT_BRANCH`, `SESH_AGENT_PID` | Agent details, empty on session rows |

A command starts in the row's directory when the row is local and the directory exists.

The value is everything the command writes to stdout until stdout closes, so redirect the output of background jobs. Line breaks become spaces. Color and style escape sequences are kept; other escape sequences are removed. Empty output hides the field and any `[...]` group around it. When a command fails, the field keeps its previous value and the failure goes to the log with fixed reason and error codes and the exit code, without command output.
