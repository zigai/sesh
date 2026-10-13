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
| Tasks | `$XDG_STATE_HOME/sesh/tasks.json` |
| Task worktrees | `$WORKTREE_DIR/<project>/` |
| Custom field cache | `$XDG_CACHE_HOME/sesh/fields` |

## Default configuration

```toml
[settings]
show_footer = true
directory_suggestions = 5
default_session_path_suggestion = "~/Projects/{session}"
display_modes = ["sesh", "agents", "work"]
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
work_section_format = "{session_name}"
work_task_format = "{tree}{status_glyph} {status_age:<7}   {agent_project_name:<16.16}  {agent_session}{right}{harness:<6.6}   {agent_git_label:<6.22} {agent_git_added:<5} {agent_git_deleted:<5} {agent_pr:<6}"

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
restore_last_on_start = true # restore the newest snapshot when tmux starts, or at the next autosave if that start skipped it

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

[projects]
roots = ["~/Projects"]
extra = []

[tasks]
editor = ["nvim"]
popup_socket = "sesh-popup"
popup_config = "" # without it, the popup server hides its status line and C-p closes it
wake_on_restore = "previous" # after a restore: "previous" resumes the agents that were awake, "none" brings every task back asleep, "3" resumes only the 3 newest awake tasks
harness = [] # the agents ctrl+t offers, in order; defaults to every supported harness found on PATH

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

## Tasks

`ctrl+t` opens a popup that starts a task: a title (optional), the agent, and in a project session or a repository where it works. Left/right pick the agent, starting from the first; up/down pick where it works; typing goes to the title; enter starts the task. The title keeps its usual editing keys. The title names the task window and a new branch (`wt/<title>`), and is passed to the agent through the harness's `prompt_args`. The places are a new worktree on a new branch from the default branch, whose name the popup shows, the pane's checkout, and `existing branch…`, where enter expands the popup to list every location:

- new worktrees on a new branch, from the default branch or the branch the pane is on; filtering also finds the other local branches
- the main checkout and existing worktrees
- local branches no worktree has checked out, and branches only a remote has, which get a local branch tracking them

A checkout another task works in is shown with that task, and enter goes to it. Outside a repository the task works in the pane's directory, and the agent row also offers a plain tab. The task window opens in the pane's session. Finishing a task removes only the worktree and branch sesh created. A failed start is shown in the popup. Key hints show at its bottom; `tasks.show_footer = false` hides them.

`[[tasks.harness]]` entries are the agents the popup offers, in order. A harness AHT supports needs only its name; any other entry needs `command`. Task commands run in your interactive shell (tmux's `default-shell`), so your aliases, functions and options apply as when you type them. `prompt_args` are appended to the command when the task has a title, with the task variables of task action prompts filled in.

```toml
[[tasks.harness]]
name = "pi"

[[tasks.harness]]
name = "claude"
prompt_args = ["{title}"]
```

## Task actions

`[task_actions.<name>]` defines an action on a task. It runs `run` headless in the task worktree, in a window of the task's popup terminal named after the action, with `prompt` (or the contents of `prompt_file`, relative to the worktree) as its last argument. Prompts can use `{task}`, `{title}`, `{branch}`, `{base}`, `{project}` and `{worktree}`. `key` binds it in the picker; `link_pr` records the branch's pull request afterwards.

```toml
[task_actions.ship]
key = "alt+p"
run = ["claude", "-p", "--model", "sonnet"]
prompt = "Commit, push and open a PR for {branch} against {base}."
link_pr = true

[projects.overrides.sesh.task_actions.ship]
prompt_file = ".agents/ship.md"
```

`[projects.overrides.<name>.task_actions.<action>]` replaces fields of an action for the project with that directory name; a prompt or prompt file replaces both.

## Actions

Per-action policy is keyed by action ID. `confirm` asks before the popup runs the action; `allow_remote` lets remote peers run it on this machine. `session.kill`, `session.delete` and `task.finish` confirm and refuse remote use by default; `task.sleep`, `task.wake` and `task.run` run remotely by default; `session.switch` never runs remotely.

```toml
[actions.session.kill]
confirm = false
allow_remote = true
```

## Key bindings

`[[keys.bind]]` binds a key to an action in the picker (`in = "picker"`) or in a tmux key table (`in = "tmux"`, rendered by `sesh setup tmux`). An entry replaces the default binding of the same key, and an entry without an action removes it. `params` sets action parameters. Tmux bindings pass the current session or pane as the action's subject; a root-table key pressed in a pane that shows a remote attachment goes to the remote tmux instead. Picker bindings cannot use the picker's own navigation keys or keys that type into its search field.

Defaults: in the picker `alt+c` session.create, `alt+r` session.rename, `alt+x` session.kill, `alt+X` session.delete, `alt+s` snapshot.save, `alt+z` task.sleep, `alt+Z` task.wake, `alt+f` task.finish; in tmux table `root` `S-F12` agent.next, `M-S-F12` agent.next with `local = true`, `S-F1` agent.back, `C-o` pick.popup, `M-w` pick.popup with `mode = "work"`, `C-t` task.menu, `M-n` project.pick.

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
