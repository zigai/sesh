# Integrations

`sesh` integrates with systemd user units, coding agents, and remote SSH peers.

## Systemd services (Linux)

Enable autosave for your existing tmux server:

```sh
sesh setup services --apply
systemctl --user enable --now sesh-autosave.timer
```

Saves run every 3 minutes by default. Use `--socket` for a custom server. Add `--with-coordinator` and enable `sesh-coordinator.service` for saves before sleep and shutdown.

The command ends by saying whether automatic saves are on, or which `systemctl --user enable --now` command still starts them. Rerunning it restarts running units on the new definition and starts failed ones again.

Use `--with-tmux-service` to manage a foreground tmux server. Its daemon creates missing socket parent directories with private `0700` permissions before starting tmux, including when run directly with `sesh daemon tmux --socket /absolute/path/to/tmux.sock`. Existing parent permissions and unrelated runtime state are left unchanged; stopping the service does not remove the socket directory.

To recover, start tmux on the same socket and run:

```sh
sesh snapshot restore
```

## Coding agents

`sesh` tracks live coding agent panes using the Agent Host Tool (`aht`) registry.

Install AHT v2.0.5 and enable the sesh integration:

```sh
go install github.com/zigai/aht/v2@v2.0.5
sesh setup agents
```

AHT v2 uses registry schema 3. `sesh` does not import older AHT registry files.

`sesh setup services` also writes `sesh-agent-status.service`, which styles tmux windows whose agent needs attention. Enable it with `systemctl --user enable --now sesh-agent-status.service`.

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
