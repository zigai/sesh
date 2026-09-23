# Integrations

`sesh` integrates with systemd user units, coding agents, and remote SSH peers.

## Systemd services (Linux)

Install user service units for automated background saves:

```sh
# Generate and install autosave timer and coordinator
sesh setup services --kind autosave --apply
sesh setup services --kind coordinator --apply

# Reload and enable
systemctl --user daemon-reload
systemctl --user enable --now sesh-autosave.timer
systemctl --user enable --now sesh-coordinator.service
```

| Unit | Purpose |
| --- | --- |
| `sesh-autosave.timer` | Triggers background save every 3 minutes |
| `sesh-autosave.service` | Runs `sesh autosave save-if-due` |
| `sesh-coordinator.service` | Holds logind delay inhibitors for sleep and shutdown |

Inspect status:

```sh
sesh autosave status --json
```

## Coding agents

`sesh` tracks live coding agent panes using the Agent Host Tool (`aht`) registry.

Install agent integration:

```sh
sesh setup agents
```

Navigate agent panes:

```sh
# Focus next agent needing input
sesh agent next

# Focus previous agent
sesh agent back

# Jump to specific pane
sesh agent focus --target-pane '%4'
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

Start the local inventory monitor:

```sh
sesh remote monitor run
```

Attach to a remote session:

```sh
sesh remote attach prod work
```
