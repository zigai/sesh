# Snapshots and recovery

`sesh` saves and restores tmux sessions on Linux.

Restoration recreates window splits, layouts, and supported foreground tools (`nvim`, interactive shells, `top`, `htop`, `btop`, `yazi`, `ranger`, `lazygit`). Unrecognized commands fall back to the default interactive shell.

## Commands

### `save`

Captures selected sessions into a snapshot generation:

```sh
sesh snapshot save \
  --repository ~/.local/share/sesh/snapshots \
  --session work,dev
```

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--repository` | string | `""` | Repository path (required) |
| `--session` | string | `""` | Target session names or IDs |
| `--socket` | string | `""` | Target tmux socket |
| `--json` | bool | `false` | Output as JSON |

### `restore`

Restores a snapshot generation:

```sh
# Restore specific generation
sesh snapshot restore \
  --repository ~/.local/share/sesh/snapshots \
  --generation 2026-09-22T14-00-00Z

# Interactive generation picker
sesh snapshot restore \
  --repository ~/.local/share/sesh/snapshots \
  --pick
```

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--repository` | string | `""` | Repository path (required) |
| `--generation` | string | `""` | Generation identifier |
| `--pick` | bool | `false` | Open generation picker |
| `--socket` | string | `""` | Target tmux socket |
| `--no-processes` | bool | `false` | Reconstruct layout without launching programs |

### `prune`

Removes unpinned generations matching retention limits:

```sh
sesh snapshot prune \
  --repository ~/.local/share/sesh/snapshots \
  --retention-days 14
```

### `gc`

Purges unlinked generation artifacts from disk:

```sh
sesh snapshot gc \
  --repository ~/.local/share/sesh/snapshots \
  --max-generations 20
```

## Journal reconciliation

If a restore operation is interrupted, inspect its recorded journal:

```sh
sesh snapshot reconcile \
  --repository ~/.local/share/sesh/snapshots \
  --operation <OPERATION_ID>
```

Resume the operation:

```sh
sesh snapshot restore \
  --repository ~/.local/share/sesh/snapshots \
  --operation <OPERATION_ID>
```

Release the recovery pin without rolling back tmux state:

```sh
sesh snapshot abandon \
  --repository ~/.local/share/sesh/snapshots \
  --operation <OPERATION_ID>
```
