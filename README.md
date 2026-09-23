# sesh

Native session manager and snapshot persistence daemon for [tmux](https://github.com/tmux/tmux).

## Install

```sh
go install github.com/zigai/sesh/cmd/sesh@latest
```

Requirements: Linux, tmux 3.6+, Go 1.27.1+.

## Usage

```sh
# Open session picker
sesh popup

# Create or switch session
sesh new work --dir ~/Projects/work

# Switch session
sesh switch work

# Restore snapshots interactively
sesh snapshot restore --pick
```

## Documentation

- [Configuration](docs/configuration.md)
- [Snapshots and recovery](docs/snapshots.md)
- [Integrations](docs/integrations.md)
- [Development](docs/development.md)
