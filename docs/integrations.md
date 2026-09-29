# Integrations

`sesh` integrates with systemd user units, coding agents, and remote SSH peers.

## Systemd services (Linux)

Install user service units for automated background saves:

```sh
# Preview, then write the autosave timer, remote monitor and sleep/shutdown coordinator
sesh setup services --with-coordinator
sesh setup services --with-coordinator --apply

# Enable
systemctl --user enable --now sesh-autosave.timer
systemctl --user enable --now sesh-coordinator.service
```

| Unit | Purpose |
| --- | --- |
| `sesh-autosave.timer` | Triggers background save every 3 minutes |
| `sesh-autosave.service` | Runs `sesh action autosave-save-if-due` |
| `sesh-coordinator.service` | Runs `sesh daemon coordinator`, which holds logind delay inhibitors for sleep and shutdown |
| `sesh-remote-monitor.service` | Runs `sesh daemon remote-monitor` |

After upgrading sesh, run `sesh setup services --apply` again (with the same `--with-*` flags) so the units call the current commands.

Inspect status:

```sh
sesh autosave status --json
```

## Coding agents

`sesh` tracks live coding agent panes using the Agent Host Tool (`aht`) registry.

Install AHT v2.0.5 and enable the sesh integration:

```sh
go install github.com/zigai/aht/v2@v2.0.5
sesh setup agents
```

AHT v2 uses registry schema 3. `sesh` does not import older AHT registry files.

Navigate agent panes. These are actions meant for key bindings; `sesh setup tmux` prints the bindings:

```sh
# Focus next agent needing input
sesh action agent-next

# Focus previous agent
sesh action agent-back

# Jump to specific pane
sesh action agent-focus --target-pane '%4'
```

Supported agents: Claude, Codex, Cursor, Copilot, Cline, Kimi, Grok, Goose, Pi, OMP, OpenCode, Agy, Kilo, Droid, Hermes.

## Remote SSH peers

Configure remote tmux instances in `~/.config/sesh/config.toml`:

```toml
[remote]
enabled = true

[[remote.connections]]
id = "prod"
host = "prod.example.com"
sesh_path = "/usr/local/bin/sesh"
tmux_socket = "/tmp/tmux-1000/default"
```

Start the local inventory monitor (normally run by `sesh-remote-monitor.service`):

```sh
sesh daemon remote-monitor
```

Attach to a remote session:

```sh
sesh remote attach prod work
```
