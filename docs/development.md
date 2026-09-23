# Development

Development workflows and testing targets for `sesh`.

## Prerequisites

- Go 1.27.1+
- [just](https://github.com/casey/just)
- [golangci-lint](https://golangci-lint.run/)
- [tmux](https://github.com/tmux/tmux) 3.6+

## Quality checks

Run module checks, tests, linter, and formatting diffs:

```sh
just check
```

## Common recipes

```sh
# Build binary
just build

# Format source
just format

# Apply linter auto-fixes
just fix

# Run unit tests
just test

# Run integration tests against disposable tmux instances
just integration

# Run data race detector
just race

# Run performance benchmarks
just bench
```

## Linux guest lifecycle harness

Test real systemd lifecycle events (sleep, shutdown inhibitors, power loss) inside an automated QEMU guest:

```sh
SESH_LINUX_GUEST=1 just guest
```
