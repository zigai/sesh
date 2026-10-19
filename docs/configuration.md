# Configuration

sesh reads its configuration from `$XDG_CONFIG_HOME/sesh/config.toml`, which defaults to `~/.config/sesh/config.toml`. Every setting is optional.

To create the file, run:

```sh
sesh config init
```

The generated file lists every setting with its default, commented out. Uncomment a setting to change it.

## Overrides

- `--config <path>` reads a different file.
- `--no-config` ignores the file and uses the built-in defaults.
- `SESH_<TABLE>_<KEY>` environment variables override single settings, for example `SESH_SETTINGS_LOG_LEVEL=warn`.

## Services

sesh runs its background work as systemd user services. To write them, run:

```sh
sesh setup services --apply
```

| Service | Purpose |
| --- | --- |
| `sesh-autosave.timer` | Saves a snapshot every `autosave.interval`. |
| `sesh-coordinator.service` | Saves before sleep and shutdown. Written only with `--with-coordinator`. |
| `sesh-agent-status.service` | Tracks agents, colors windows that need attention, and finishes merged tasks. |
| `sesh-remote-monitor.service` | Keeps the list of remote sessions current. |

Enable the services you want:

```sh
systemctl --user enable --now sesh-autosave.timer sesh-agent-status.service
```

`sesh setup tmux --apply` registers the tmux key bindings. `sesh setup agents` connects sesh to [aht](https://github.com/zigai/aht) v2, which all agent features need.

## Example

```toml
[settings]
display_modes = ["sesh", "inbox"]

[projects]
roots = ["~/Projects", "~/work"]

[[tasks.harness]]
name = "claude"
prompt_args = ["{title}"]

[inbox]
sections = ["done", "input", "running", "closed", "finished"]

[merge]
check = ["just", "check"]

[[keys.bind]]
key = "M-s"
action = "snapshot.save"
in = "tmux"
table = "prefix"
```

## General

### `[settings]`

- `show_footer` `(bool: true)` - Show key hints at the bottom of the popup.
- `display_modes` `(list: ["sesh", "agents", "inbox"])` - Popup modes, in cycle order. One entry locks the popup to that mode.
- `directory_suggestions` `(int: 5)` - Number of directory completions shown when creating a session.
- `default_session_path_suggestion` `(string: "~/Projects/{session}")` - Initial directory for a new session. `{session}` becomes the session name.
- `shortcut_style` `(string: "none")` - Where session keys are shown: `none`, `indent` (left of each row), or `right`.
- `session_key_action` `(string: "focus")` - What a session key does in the agents and inbox modes: `focus` waits for an agent key, `open` opens the session.
- `log_level` `(string: "info")` - One of `debug`, `info`, `warn`, `error`, or `disabled`.

### `[paths]`

- `data_dir` `(string: "$XDG_DATA_HOME/sesh")` - Directory for snapshots.
- `log_file` `(string: "$XDG_STATE_HOME/sesh/sesh.log")` - Log file.

Other files sesh writes:

| Data | Path |
| --- | --- |
| Snapshots | `<data_dir>/snapshots` |
| Session history | `$XDG_STATE_HOME/sesh/history.json` |
| Tasks | `$XDG_STATE_HOME/sesh/tasks.json` |
| Agent conversations | `$XDG_STATE_HOME/sesh/work.json` |
| Task worktrees | `$WORKTREE_DIR/<project>/`, else `~/Worktrees/<project>/` |
| Custom field cache | `$XDG_CACHE_HOME/sesh/fields` |

## Picker

### `[popup]`

Sizes are terminal cells (`"80"`), percentages (`"50%"`), or `"auto"`.

- `sesh_width` `(string: "40%")` - Width in sesh mode.
- `sesh_height` `(string: "45%")` - Height in sesh mode.
- `agents_width` `(string: "40%")` - Width in the agents and inbox modes.
- `agents_height` `(string: "80%")` - Height in the agents and inbox modes.

### `[display]`

Row templates. `{field}` inserts a value, and `{right}` starts a right-aligned column. Widths use `{field:<20}`, `{field:>20}`, `{field:^20}`, and `{field:<20.20}`. `{{` and `}}` produce literal braces.

- `sesh_format` `(string)` - Rows in sesh mode.
- `agents_session_format` `(string)` - Session headings in agents mode.
- `agents_agent_format` `(string)` - Agent rows in agents mode.
- `inbox_group_format` `(string)` - Group headings in inbox mode.
- `inbox_agent_format` `(string)` - Rows in inbox mode.

The defaults are in the file `sesh config init` writes.

### `[display.fields]`

Custom fields. `{custom.NAME}` in a row template shows the output of the command named `NAME`.

```toml
[display]
sesh_format = "{source_prefix}{session_name}{right}{custom.git_sha}"

[display.fields]
git_sha = "git rev-parse --short HEAD"
weather = { command = "/usr/local/bin/weather", args = ["--short"], refresh = "10m" }
```

A field is either a string, run with `/bin/sh -c`, or a table:

- `command` `(string: <required>)` - Program to run.
- `args` `(list: [])` - Arguments.
- `refresh` `(duration: "")` - Rerun interval while the popup is open. By default a field runs when the popup opens and when its row changes.

Field names use `a-z`, `0-9`, and `_`. The command starts in the row's directory when the row is local. The value is everything it writes to stdout; line breaks become spaces, and only color and style escapes are kept. Empty output hides the field and any `[...]` group around it. A failed run keeps the previous value.

The command gets the row as one JSON line on stdin, and these environment variables:

| Variable | Value |
| --- | --- |
| `SESH_FIELD` | Field name |
| `SESH_ROW` | `session` or `agent` |
| `SESH_SESSION`, `SESH_SESSION_ID` | Session name and ID |
| `SESH_SESSION_KIND` | `live` or `saved` |
| `SESH_SESSION_PATH` | Session directory |
| `SESH_ORIGIN`, `SESH_CONNECTION_ID` | `local`, `ssh`, or `bridge`, and the remote connection |
| `SESH_PANE_ID` | Agent pane on agent rows, active pane on session rows |
| `SESH_AGENT_HARNESS`, `SESH_AGENT_STATUS`, `SESH_AGENT_CWD`, `SESH_AGENT_PROJECT_ROOT`, `SESH_AGENT_GIT_BRANCH`, `SESH_AGENT_PID` | Agent details, empty on session rows |

### `[keys]`

Keys handled while the popup is focused.

- `help` `(string: "?")` - Show help.
- `cycle_mode` `(string: "ctrl+f")` - Switch to the next mode.
- `agent_sort` `(string: "ctrl+s")` - Change the agent sort order.
- `inbox_group` `(string: "ctrl+g")` - Group the inbox by state or by project.
- `menu` `(string: "alt+m")` - Open the selected row's menu. A right-click also opens it.
- `page_up`, `page_down`, `top`, `bottom` `(string: "pgup", "pgdown", "home", "end")` - Scroll the list.
- `session_prefix` `(string: "tab")` - Key pressed before a session letter. `"alt"` holds alt with the letter instead.
- `agent_keys` `(string: "asdfghjklqwertyuiopzxcvbnm")` - Letters given in order to the agents of a focused session.

### `[keys.sessions]`

Fixed letters for local sessions, as `<session> = "<letter>"`. Other sessions get a free letter once and keep it.

```toml
[keys.sessions]
dotfiles = "d"
```

### `[[keys.bind]]`

Binds a key to an action in the popup or in a tmux key table. An entry replaces the default binding of the same key. An entry without `action` removes it.

- `key` `(string: <required>)` - The key.
- `in` `(string: <required>)` - `picker` for the popup, or `tmux` for a tmux key table.
- `action` `(string: "")` - Action ID to run.
- `table` `(string: "root")` - tmux key table. Only with `in = "tmux"`.
- `params` `(table: {})` - Action parameters.

Picker bindings cannot use the popup's navigation keys or keys that type into its search field. A tmux binding gets the current session or pane as its subject. `sesh setup tmux` applies tmux bindings, and `sesh keys` lists the picker bindings.

Default bindings:

| Key | In | Action |
| --- | --- | --- |
| `alt+c` | picker | `session.create` |
| `alt+r` | picker | `session.rename` |
| `alt+x` | picker | `session.kill` |
| `alt+X` | picker | `session.delete` |
| `alt+s` | picker | `snapshot.save` |
| `alt+z` | picker | `task.sleep` |
| `alt+Z` | picker | `task.wake` |
| `alt+f` | picker | `inbox.finish` |
| `alt+o` | picker | `task.open` |
| `alt+t` | picker | `inbox.task` |
| `C-o` | tmux | `pick.popup` |
| `M-w` | tmux | `pick.popup` with `mode = "inbox"` |
| `C-t` | tmux | `task.menu` |
| `M-n` | tmux | `project.pick` |
| `S-F12` | tmux | `agent.next` |
| `M-S-F12` | tmux | `agent.next` with `local = true` |
| `S-F1` | tmux | `agent.back` |

### `[theme]`

Colors accept hex values and terminal color names.

- `preset` `(string: "dracula")` - One of `dracula`, `catppuccin`, `catppuccin-mocha`, `catppuccin-latte`, `tokyo-night`, `gruvbox`, `nord`, or `terminal`. Other settings override single colors of the preset.
- `primary`, `secondary`, `muted`, `error`, `warning`, `success`, `border`, `selected` `(string)` - Interface colors.
- `background` `(string: "")` - Empty for a black popup, `"default"` for the terminal's background.
- `foreground` `(string: "")` - Empty for light text.

### `[theme.agent_status]`

Agent status colors. Each defaults to a theme color.

- `running` `(string: success)`
- `waiting` `(string: secondary)`
- `failed` `(string: error)`
- `interrupted` `(string: warning)`
- `idle` `(string: muted)`

## Snapshots

### `[autosave]`

- `enabled` `(bool: true)` - Save snapshots automatically.
- `interval` `(duration: "3m")` - Time between saves.
- `restore_last_on_start` `(bool: true)` - Restore the newest snapshot when tmux starts.

After a crash, start tmux on the same socket and run `sesh snapshot restore`.

### `[snapshot]`

The newest five snapshots are always kept.

- `retention_days` `(int: 14)` - Delete snapshots older than this. `0` disables the limit.
- `max_count` `(int: 0)` - Keep at most this many snapshots. `0` disables the limit.
- `max_bytes` `(int: 0)` - Keep at most this many bytes of snapshots. `0` disables the limit.
- `process_environment` `(list: ["PATH", "VIRTUAL_ENV", "CONDA_PREFIX", "CONDA_DEFAULT_ENV", "PYTHONPATH", "NODE_PATH"])` - Environment variables saved for running programs. An empty list saves none.

## Agents

### `[agent_sessions]`

Agent rows in the popup.

- `enabled` `(bool: true)` - Show agents and enable agent navigation.
- `popup_sort` `(string: "position")` - Agent order within a session: `position`, `name`, or `attention`.
- `popup_counts` `(bool: false)` - Show agent counts on sesh mode rows.
- `refresh_interval` `(duration: "250ms")` - Refresh rate when live updates are unavailable.
- `status_style` `(string: "glyph")` - `glyph` for colored circles, `text` for words such as `WORKING`.
- `status_time_format` `(string: "relative-seconds")` - `relative` (`12m`), `relative-seconds` (`40s`, `12m`), `clock` (`00:01:23`), or `hh:mm` (`00:01`).

### `[agent_status]`

tmux window styles for agents that need attention. Applied by `sesh-agent-status.service`.

- `enabled` `(bool: true)` - Style windows by agent state.
- `style_waiting` `(string: "fg=#1e1e2e,bg=#ffdd57,bold")` - Style of a window whose agent waits for input.
- `style_failed` `(string: "fg=#1e1e2e,bg=#ff5555,bold")` - Style of a window whose agent failed.
- `style_unread` `(string: "fg=#1e1e2e,bg=#6c7086,bold")` - Style of a window whose agent finished while you were away.
- `set_window_status_style` `(bool: true)` - Set `window-status-style`.
- `set_window_status_bell_style` `(bool: true)` - Set `window-status-bell-style`.
- `active_bypass` `(bool: true)` - Skip the unread style when the window was active as the agent finished.
- `clear_failed_on_visit` `(bool: true)` - Clear the failed style when you visit the window.
- `clear_on_shutdown` `(bool: true)` - Clear all styles when the service stops.
- `store_path` `(string: "~/.local/state/aht/state.json")` - aht state file.
- `tmux_binary` `(string: "tmux")` - tmux executable.
- `initial_backoff` `(duration: "500ms")` - First reconnect delay after losing aht.
- `max_backoff` `(duration: "5s")` - Longest reconnect delay.
- `backoff_factor` `(float: 2.0)` - Reconnect delay multiplier.
- `quiet` `(bool: false)` - Drop informational log records.

## Projects and tasks

### `[projects]`

A project is a git repository or registered folder with its own tmux session.

- `roots` `(list: ["~/Projects"])` - Directories whose direct child repositories are projects.
- `extra` `(list: [])` - More folders to treat as projects.

### `[projects.overrides.<name>]`

Settings for one project. `<name>` is the project's directory name, or a path such as `"~/work/api"`, which wins over the name.

- `task_panes` `(list)` - Replaces or adds `[[tasks.panes]]` entries by name.
- `task_layouts` `(list)` - Replaces `[[tasks.layouts]]`. An empty list keeps the defaults.
- `menu_items.<item>` `(table)` - Replaces fields of a `[menu.items]` entry. A prompt or prompt file replaces both.
- `merge` `(table)` - Replaces `[merge]`.
- `finish` `(table)` - Replaces `[finish]`.

### `[tasks]`

A task is a tmux window with its own worktree, agent, and panes. `ctrl+t` starts one.

- `wake_on_restore` `(string: "previous")` - Task agents to resume after a restore: `previous` for the ones that were running, `none`, or a count such as `"3"` for the newest ones.
- `popup_socket` `(string: "sesh-popup")` - tmux server for task popup terminals.
- `popup_config` `(string: "")` - Config file for that server. Without one, sesh hides its status line and binds `C-p` to close the popup.
- `show_footer` `(bool: true)` - Show key hints in the new task popup.

Commands in task settings run in your interactive shell, so aliases and functions work. They can use the variables `{task}`, `{title}`, `{branch}`, `{base}`, `{project}`, and `{worktree}`.

### `[[tasks.harness]]`

Agents offered for a new task, in order. Without entries, every supported agent found on `PATH` is offered.

- `name` `(string: <required>)` - Agent name. Uses `a-z`, `0-9`, `_`, and `-`.
- `command` `(list: [])` - Command to start the agent. Required for agents aht does not support.
- `prompt_args` `(list: [])` - Arguments added when the task has a title.

### `[[tasks.panes]]`

Panes opened beside the agent. Default: one `editor` pane running `nvim`.

- `name` `(string: <required>)` - Pane name, stored in the pane's `@sesh_role` option. The agent pane is `agent`.
- `command` `(list: [])` - Command to run. Without one, the pane is a shell.
- `dir` `(string: "")` - Directory relative to the task directory.
- `restart` `(bool: false)` - Run the command again after a restore.

### `[[tasks.layouts]]`

How a task window is arranged. The first layout whose `when` matches the window is used. Without layouts, the agent sits beside the panes in a landscape window and above them otherwise.

- `name` `(string: "")` - Layout name, for `sesh task new --layout <name>`.
- `when` `(table: {})` - Conditions: `orientation` (`landscape` or `portrait`), `min_width`, `max_width`, `min_height`, `max_height`, and `harness`. A landscape window has at least twice as many columns as rows.
- `arrange` `(string: "")` - tmux layout with the agent as the main pane: `main-vertical`, `main-horizontal`, `main-vertical-mirrored`, `main-horizontal-mirrored`, `even-horizontal`, `even-vertical`, or `tiled`.
- `main_size` `(string: "50%")` - Size of the agent pane with `arrange`.
- `panes` `(list: [])` - Panes `arrange` includes.
- `splits` `(list: [])` - Places panes one by one instead of `arrange`. Each split has `pane`, `of` (the agent or an earlier pane), `direction` (`right`, `below`, `left`, or `above`), and `size` (cells or a percentage).
- `focus` `(string: "")` - Pane selected when the window opens.

Set either `arrange` or `splits`.

```toml
[[tasks.panes]]
name = "editor"
command = ["nvim", "."]

[[tasks.panes]]
name = "shell"

[[tasks.layouts]]
name = "wide"
when = { orientation = "landscape" }
splits = [
  { pane = "editor", of = "agent", direction = "right", size = "45%" },
  { pane = "shell", of = "editor", direction = "below", size = "30%" },
]

[[tasks.layouts]]
name = "tall"
arrange = "main-horizontal"
main_size = "60%"
```

The `task.relayout` action arranges the current task window again for its current size.

## Inbox

### `[inbox]`

The inbox lists agent conversations by state. The agent status service records them.

- `group` `(string: "state")` - Group rows by `state` or `project`.
- `sections` `(list: ["done", "approve", "input", "waiting", "limited", "unmerged", "running", "snoozed", "asleep", "idle", "closed"])` - Sections shown, in order. Add `finished` to show finished conversations.
- `labels` `(table: {})` - Section headings, as in `labels = { done = "new" }`.
- `dim` `(list: ["running", "snoozed", "asleep", "closed", "finished"])` - Sections with dimmed rows.
- `snooze` `(list: ["next message", "1h", "4h", "tomorrow 09:00"])` - Snooze choices: a duration, a time such as `"18:00"`, `"tomorrow 09:00"`, or `"next message"`.
- `closed_shown` `(int: 3)` - Closed rows per project before a row that shows the rest. `0` shows all.

`approve` and `input` hold agents waiting for permission or an answer, and `limited` holds agents stopped by a usage limit. A conversation finishes when you finish it or when all its linked pull requests end. The agent status service also finishes a task when its pull request merges, once its agent is idle and no client shows its window. Enter on a closed or finished conversation resumes it.

### `[menu]`

Row menus, opened with `keys.menu` or a right-click. A menu shows only the entries that apply to the row. `-` is a divider, and `items` lists the `[menu.items]` entries not named elsewhere.

- `agent` `(list: ["go", "snooze", "unsnooze", "seen", "unseen", "pin", "unpin", "evidence", "pr", "link", "unlink", "merge", "sleep", "wake", "open", "finish", "-", "items"])` - Menu of agent rows.
- `session` `(list: ["go", "rename", "snapshot", "kill", "delete"])` - Menu of session rows.

### `[menu.items.<name>]`

Custom task actions. An item runs headless in the task worktree, in a window of the task's popup terminal.

- `run` `(list: <required>)` - Command to run.
- `prompt` `(string: "")` - Last argument of the command. Can use the task variables.
- `prompt_file` `(string: "")` - File in the worktree to read the prompt from.
- `key` `(string: "")` - Picker key that runs the item.
- `label` `(string: <name>)` - Name shown in the menu.
- `link_pr` `(bool: false)` - Link the branch's pull request afterwards.

```toml
[menu.items.ship]
key = "alt+p"
run = ["claude", "-p", "--model", "sonnet"]
prompt = "Commit, push and open a PR for {branch} against {base}."
link_pr = true
```

### `[merge]`

The merge action rebases a branch onto its base and pushes it to the base on `origin`.

- `check` `(list: [])` - Command run after the rebase and before the push. A failure stops the merge.

### `[finish]`

- `run` `(list: [])` - Command run in a task worktree before finishing removes it, never in a main checkout. A failure stops the finish; `--skip-run` skips it.

## Remote machines

### `[remote]`

Remote access uses SSH. Users, ports, keys, and proxies come from your OpenSSH configuration, and hosts must allow noninteractive login. The remote machine needs the same sesh version.

- `enabled` `(bool: false)` - Turn on remote sessions.
- `ssh_path` `(string: "ssh")` - SSH executable.
- `tmux_poll_interval` `(duration: "1s")` - How often remote tmux state is read.
- `connect_timeout` `(duration: "5s")` - Connection timeout.
- `action_timeout` `(duration: "2m")` - Timeout for a remote action.
- `heartbeat_interval` `(duration: "10s")` - Time between connection checks.
- `stale_after` `(duration: "30s")` - Age at which remote sessions show as stale. Must exceed `heartbeat_interval`.
- `hide_after` `(duration: "10m")` - Age at which remote sessions are hidden. Must exceed `stale_after`.
- `max_parallel_connects` `(int: 4)` - Connections opened at once, from 1 to 128.
- `enable_reverse_bridge` `(bool: true)` - While you are attached to a remote session, list this machine's sessions in the remote popup so you can switch back.

### `[[remote.connections]]`

- `id` `(string: <required>)` - Connection name. Uses letters, digits, `_`, `-`, and `.`.
- `host` `(string: <required>)` - SSH host or alias.
- `label` `(string: "")` - Name shown in the popup.
- `sesh_path` `(string: "")` - Path to sesh on the remote machine.
- `tmux_socket` `(string: "")` - tmux socket on the remote machine.

```toml
[remote]
enabled = true

[[remote.connections]]
id = "prod"
host = "prod.example.com"
sesh_path = "/usr/local/bin/sesh"
tmux_socket = "/tmp/tmux-1000/default"
```

Attach with `sesh remote attach prod work`.

## Actions

### `[actions.<id>]`

Policy for one action.

- `confirm` `(bool)` - Ask before the popup runs the action.
- `allow_remote` `(bool)` - Let remote machines run the action here.

By default, `session.kill`, `session.delete`, and `task.finish` ask first and refuse remote use. `task.new`, `task.offer`, `task.sleep`, `task.wake`, and `task.run` allow remote use. `session.switch` never runs remotely.

```toml
[actions.session.kill]
confirm = false
allow_remote = true
```

## Hooks

### `[hooks]`

Commands run on lifecycle events. Each entry is a shell string, or a table with `command`, `args`, and `timeout`.

- `timeout` `(duration: "30s")` - Default timeout for each command.
- `session_switch_pre`, `session_switch_post` `(list: [])` - Before and after switching sessions.
- `session_attach_pre`, `session_attach_post` `(list: [])` - Before and after attaching to a session.
- `tmux_startup_post` `(list: [])` - After tmux starts.

```toml
[hooks]
session_switch_post = [
  "notify-send 'sesh' \"$SESH_SESSION\"",
  { command = "logger", args = ["sesh switched"], timeout = "2s" },
]
```

`sesh hooks list` shows the configured hooks, and `sesh hooks validate` checks them.
