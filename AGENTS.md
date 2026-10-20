# AGENTS.md

sesh is a Go CLI and TUI that adds a picker, snapshots, tasks, agent tracking and remote access to stock tmux. It targets Linux, tmux 3.6+, Go 1.27.1+. Configuration is documented in `docs/configuration.md`.

## Commands

Tasks run through `just` (see `Justfile`):

- `just build` builds `./sesh`. `just install` builds to `~/.local/bin/sesh` and restarts running `sesh-*` systemd user services that still run the old binary.
- `just test` runs unit tests (`go test ./...`).
- `just integration` runs unit and integration tests (`-tags=integration`) with the `*_INTEGRATION_REQUIRED` variables set, so a missing tmux, application fixture or terminal tool fails instead of skipping.
- `just race` is the same with `-race`.
- `just lint`, `just fix`, `just format` run golangci-lint (config in `.golangci.yaml`, which also lints the `integration` build tag).
- `just check` runs everything CI-like: `mod-check`, `lint`, `test-isolation`, `race`, and a format diff. Run it locally before merging.
- `just guest` / `just guest-extended` run the QEMU lifecycle harness (needs KVM).

Single test: `go test ./internal/session -run TestName`. Integration tests need the tag: `go test -tags=integration ./test/integration/cli -run TestName`.

Agent row goldens in `internal/tui/testdata/agent_rows` are rewritten with `SESH_UPDATE_AGENT_GOLDENS=1 go test ./internal/tui -run Golden`.

## Tests and isolation

- Every test package must have a `TestMain` that calls `testenv.RunIsolated(m)` (`internal/testsupport/testenv`). It moves `$HOME`, XDG dirs and `$TMUX_TMPDIR` into a temp dir and clears `TMUX`, `TMUX_PANE`, `DBUS_SESSION_BUS_ADDRESS` and `SESH_*` variables, so tests never reach the user's live tmux sessions, systemd user manager or real snapshot repository. `just test-isolation` enforces this.
- Name tests for the behavior they exercise and place them with the owning feature. Do not use historical-origin prefixes or separate historical suites.
- When testing sesh by hand, never touch the live tmux server. Use a separate server (`tmux -L <name>`) or a scratch `TMUX_TMPDIR`.
- Integration tests use the `//go:build ... && integration` tag. They live next to the code (`*_integration_test.go`) and in `test/integration/{cli,snapshot,application,lifecycle}`, which drive the built binary against disposable tmux servers.
- Property tests use `pgregory.net/rapid`; its failure cases are saved under `testdata/rapid/`.
- `internal/testsupport/clitest` runs the real command pipeline for command tests.

## Architecture

Entry: `cmd/sesh/main.go` calls `cli.Execute`. `internal/cli/root.go` builds the cobra tree from `internal/cli/cmd/*`. `internal/cli/runtime.go` loads config, sets up logging, and stages stdout/stderr so a failed command does not publish partial output.

**Actions** are the core abstraction. `internal/action` registers typed action definitions (ID like `session.kill`, subject kind, params, remote access policy). Inputs and outputs are the JSON contract shared by the CLI, the TUI and the remote protocol. `internal/cli/actioncli` turns each action into a command whose path is its ID (`session.kill` becomes `sesh session kill`). `internal/app.Local` is the composition root that dispatches actions to the services that run them. `internal/remote` sends the same actions to peers over SSH.

**Services**, by package:
- `session`: live tmux session workflows (switch, attach, rename, kill), history, hooks.
- `snapshot/*`: save and restore. `model` holds snapshot values and the strict codec, `store` is the on-disk repository (generations, GC), `capture` reads tmux and processes, `restore` plans and executes restores with a journal, `engine` orchestrates save/restore, `apps` restores known applications.
- `automation`: autosave, startup restore, save locks.
- `agents`: tracks coding agents through aht (`github.com/zigai/aht/v2`), status, attention navigation.
- `task`: durable task records and their windows/worktrees. `work`: durable record of agent conversations for the inbox and resume. `inbox`: snooze and unseen state. `project`: project detection.
- `tui`: Bubble Tea v2 picker and sub-pickers (`taskpicker`, `projectpicker`, `snapshotpicker`, `logview`, `whichkey`).
- `setup`: renders tmux bindings, agent integration and systemd user units. `daemon`: long-running processes those units start (tmux supervisor, sleep/shutdown coordinator). `doctor`: health checks.
- `integration/{tmuxserver,process,lifecycle}`: low-level adapters for the tmux server, process observation, and logind/signals.
- `config`: TOML config with built-in defaults in `default.toml`; `SESH_<TABLE>_<KEY>` env overrides.

tmux access goes through `github.com/zigai/gotmux` (imported as `gotmux`).

**Import boundaries** are enforced by `internal/architecture_test.go`:
- Every production package has a layer in `packageLayers`: base, model (`snapshot/model`), storage (`snapshot/store`), adapter (`integration/*`), config, domain, contract (`action`), service, composition (`app`), surface (`cli`, `cmd/sesh`). A new package fails the test until it gets one.
- A package may import lower layers, and its own layer only inside the same directory tree. Other same-layer imports must be listed with a reason in `sameLayerImports`.
- gotmux may be imported only from the adapter layer up.
- Production code must not import `testing`, `internal/testsupport`, `test/`, `gotmux/tmuxtest`, `rapid` or `go-cmp`.

## Error handling

- Use `github.com/samber/oops` at application and domain error boundaries for structured operation details, stable codes, phases and stack traces (`oops.In("<domain>").Code(...).With("operation", ...)`).
- Sentinel errors are package-level `Err*` vars. Preserve standard sentinels and typed causes with `errors.Is`/`errors.As`. Preserve independent cleanup failures with `errors.Join` before enrichment. Low-level helpers may return ordinary errors for the domain to enrich.
- Keep error messages fixed and put variable data in `oops` context. Do not add a logger adapter.
- `oops` enrichment does not authorize retries. Reconcile uncertain mutations through durable state. Preserve uncertainty even when cancellation is joined.
- In the pinned `oops` release, `Code()`/`Domain()` resolve the deepest values and `Context()` merges layers. `internal/errorstep` reads the step a domain recorded on its own error.

## Logging

- Logging uses zerolog. The log file is private to the user (mode 0600) and exists to explain what went wrong and why.
- A record about a failure carries the error through `.Func(logging.Cause(err))`, which also records its `oops` domain, code and step. Error text, the diagnostics an error carries, paths, session names and IDs may appear. Credentials, environment values, terminal contents and command stdout may not. A failed hook logs the last 2 KB of its stderr.
- Log a failure once, at the boundary that handles it. A failure handled by skipping or falling back still gets a record when it hides a broken dependency.
- Levels: `error` when the operation failed, `warn` when it degraded, retried or skipped work, `info` for normal outcomes, including requested cancellation.
- Periodic loops log the first failure, a change of cause, and recovery, not every tick.
- Name the subject with stable IDs (action ID, task ID, connection ID, restore operation ID) so records can be correlated.

## Conventions

- golangci-lint is strict: declaration order const, var, type, func (`decorder`, `funcorder`); `exhaustruct`; `wrapcheck`; `wsl_v5`; `err113`; `nolint` needs a specific linter and an explanation. `internal/cli` and test files have some of these relaxed.
- Many packages are `//go:build linux || darwin`, with `_linux.go`, `_darwin.go` and `_other.go` variants.
- Edit `README.md` only to keep it accurate. When a command, flag or feature it describes changes, correct that part at about the same length. Do not add new sections, examples or long explanations.
