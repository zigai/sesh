_:
    @just help

binary := "sesh"
prefix := env_var_or_default("SESH_INSTALL_PREFIX", env_var("HOME") + "/.local/bin")
integration_env := "TMUX_INTEGRATION_REQUIRED=1 SESH_APPLICATION_INTEGRATION_REQUIRED=1 SESH_LIFECYCLE_INTEGRATION_REQUIRED=1 SESH_TERMINAL_INTEGRATION_REQUIRED=1"

# List available commands
help:
    @just --list

# Run all tests
test:
    go test ./...

# Run unit and integration tests against disposable tmux servers, applications, terminals and buses
integration:
    {{ integration_env }} go test -tags=integration ./...

# Run application integration workflow tests against disposable tmux
integration-app:
    SESH_APPLICATION_INTEGRATION_REQUIRED=1 TMUX_INTEGRATION_REQUIRED=1 go test -tags=integration ./test/integration/application/...

# Run unit and integration tests with the race detector
race:
    {{ integration_env }} go test -race -tags=integration ./...

# Require every test package to run in an isolated home and tmux environment
test-isolation:
    #!/usr/bin/env bash
    set -euo pipefail
    missing=$(for tags in "" integration; do go list -tags="$tags" -f '{{{{if or .TestGoFiles .XTestGoFiles}}{{{{.Dir}}{{{{end}}' ./...; done | sort -u | while read -r dir; do grep -q 'RunIsolated(m)' "$dir"/*_test.go || echo "$dir"; done)
    if [ -n "$missing" ]; then
        printf 'Test packages without testenv.RunIsolated:\n%s\n' "$missing" >&2
        exit 1
    fi

# Check strict native codec properties and repository state transitions
model:
    go test ./internal/snapshot/model ./internal/snapshot/store

# Run the optional disposable Linux QEMU guest lifecycle harness (requires KVM)
guest suite="standard":
    #!/usr/bin/env sh
    set -eu
    guest_suite={{ quote(suite) }}
    case "$guest_suite" in
        standard|extended)
            ;;
        *)
            echo "Error: invalid guest lifecycle suite '$guest_suite'; accepted suites: standard, extended" >&2
            exit 2
            ;;
    esac
    SESH_LINUX_GUEST=1 SESH_GUEST_LIFECYCLE_SUITE="$guest_suite" bash test/integration/lifecycle/run_linux_guest.sh

# Run the extended optional disposable Linux QEMU guest lifecycle harness (requires KVM)
guest-extended:
    @just guest extended

# Run tests and display coverage
coverage:
    #!/usr/bin/env sh
    set -e
    coverage_file=$(mktemp)
    trap 'rm -f "$coverage_file"' EXIT
    go test -coverprofile="$coverage_file" ./...
    go tool cover -func="$coverage_file"

# Update Go module files
tidy:
    go mod tidy

# Check Go module files without modifying them
mod-check:
    go mod tidy -diff

# Format Go source files
format:
    golangci-lint fmt

# Apply automatic fixes and format code
fix:
    golangci-lint run --fix
    golangci-lint fmt

# Run golangci-lint without --fix
lint:
    golangci-lint run

# Run all non-mutating quality checks and every local test suite
check: mod-check lint test-isolation race
    golangci-lint fmt --diff

# Build the binary
build:
    go build -o {{ binary }} ./cmd/sesh

build-release:
    #!/usr/bin/env bash
    set -euo pipefail
    commit=$(git rev-parse --short HEAD 2>/dev/null || printf none)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/zigai/sesh/internal/cli.version=dev -X github.com/zigai/sesh/internal/cli.commit=$commit -X github.com/zigai/sesh/internal/cli.date=$build_date" -o {{ binary }} ./cmd/sesh

install: install-binary restart-services
    @printf 'Installed %s\n' {{ quote(prefix + "/" + binary) }}
    @{{ quote(prefix + "/" + binary) }} --version

# Restart running sesh daemons that still execute a replaced binary
restart-services:
    #!/usr/bin/env bash
    set -euo pipefail
    [ "$(uname -s)" = Linux ] || exit 0
    installed_path={{ quote(prefix + "/" + binary) }}
    for unit in $(systemctl --user list-units 'sesh-*.service' --state=running --no-legend --plain | awk '{print $1}'); do
        [ "$(systemctl --user show -p Type --value "$unit")" != oneshot ] || continue
        pid=$(systemctl --user show -p MainPID --value "$unit")
        if [ "$(readlink "/proc/$pid/exe")" = "$installed_path (deleted)" ]; then
            systemctl --user restart "$unit"
            printf 'Restarted %s\n' "$unit"
        fi
    done

refresh-services:
    {{ quote(prefix + "/" + binary) }} setup services --apply

install-binary:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{ quote(prefix) }}
    installed_tmp=$(mktemp {{ quote(prefix + "/." + binary + ".XXXXXX") }})
    link_dir=""
    trap 'rm -f "$installed_tmp"; if [ -n "$link_dir" ]; then rm -f "$link_dir/{{ binary }}"; rmdir "$link_dir"; fi' EXIT
    commit=$(git rev-parse --short HEAD 2>/dev/null || printf none)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/zigai/sesh/internal/cli.version=dev -X github.com/zigai/sesh/internal/cli.commit=$commit -X github.com/zigai/sesh/internal/cli.date=$build_date" -o "$installed_tmp" ./cmd/sesh
    chmod 755 "$installed_tmp"
    "$installed_tmp" --version >/dev/null
    installed_path={{ quote(prefix + "/" + binary) }}
    if [ -d "$installed_path" ] && [ ! -L "$installed_path" ]; then
        printf 'Cannot install sesh command: %s is a directory\n' "$installed_path" >&2
        exit 1
    fi
    if [ -f "$installed_path" ] && [ ! -L "$installed_path" ] && [ ! -e "$installed_path.previous" ]; then
        ln "$installed_path" "$installed_path.previous"
    fi
    mv -f "$installed_tmp" "$installed_path"
    if [ -z "${SESH_INSTALL_PREFIX:-}" ]; then
        go_bin_dir=$(go env GOBIN)
        if [ -z "$go_bin_dir" ]; then
            go_path=$(go env GOPATH)
            go_bin_dir="${go_path%%:*}/bin"
        fi
        go_binary="$go_bin_dir/{{ binary }}"
        if [ "$go_binary" != "$installed_path" ] && { [ -e "$go_binary" ] || [ -L "$go_binary" ]; }; then
            if [ -d "$go_binary" ] && [ ! -L "$go_binary" ]; then
                printf 'Cannot link %s: path is a directory\n' "$go_binary" >&2
                exit 1
            fi
            link_dir=$(mktemp -d "$go_bin_dir/.{{ binary }}-link.XXXXXX")
            ln -s "$installed_path" "$link_dir/{{ binary }}"
            mv -f "$link_dir/{{ binary }}" "$go_binary"
            rmdir "$link_dir"
            link_dir=""
            printf 'Linked %s -> %s\n' "$go_binary" "$installed_path"
        fi
    fi

# Remove build artifacts
clean:
    rm -rf sesh sesh.exe dist/

# Build with version info (local dev)
build-dev:
    #!/usr/bin/env sh
    set -e
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo none)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    go build -ldflags "-X github.com/zigai/sesh/internal/cli.version=dev -X github.com/zigai/sesh/internal/cli.commit=${commit} -X github.com/zigai/sesh/internal/cli.date=${build_date}" -o {{ binary }} ./cmd/sesh

# Validate local packaging configuration without publishing
package-check:
    goreleaser check

# Test goreleaser locally
release-dry-run:
    goreleaser release --snapshot --clean

# Pre-release safety checks
_release-check:
    #!/usr/bin/env sh
    set -e
    if [ -n "$(git status --porcelain)" ]; then
        echo "Error: uncommitted changes. Commit or stash first." >&2
        exit 1
    fi
    default_branch=$(git ls-remote --symref origin HEAD | awk '$1 == "ref:" { sub("refs/heads/", "", $2); print $2; exit }')
    if [ -z "$default_branch" ]; then
        echo "Error: could not determine the default branch for origin." >&2
        exit 1
    fi
    branch=$(git branch --show-current)
    if [ "$branch" != "$default_branch" ]; then
        echo "Error: not on $default_branch branch (on $branch)" >&2
        exit 1
    fi
    git fetch origin "$default_branch" --tags
    local_head=$(git rev-parse HEAD)
    remote_head=$(git rev-parse "origin/$default_branch")
    if [ "$local_head" != "$remote_head" ]; then
        echo "Error: local $default_branch differs from origin/$default_branch. Pull or push first." >&2
        exit 1
    fi
    latest_tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
    if [ -n "$latest_tag" ]; then
        tag_commit=$(git rev-parse "$latest_tag"^{})
        if [ "$local_head" = "$tag_commit" ]; then
            echo "Error: HEAD is already tagged as $latest_tag. Make new commits first." >&2
            exit 1
        fi
    fi

# Release a new patch version (eg. v1.0.0 -> v1.0.1)
release-patch: _release-check
    #!/usr/bin/env sh
    set -e
    latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    major=$(echo "$latest" | sed 's/v//' | cut -d. -f1)
    minor=$(echo "$latest" | sed 's/v//' | cut -d. -f2)
    patch=$(echo "$latest" | sed 's/v//' | cut -d. -f3)
    new="v${major}.${minor}.$((patch + 1))"
    echo "Releasing $new (was $latest)"
    git tag "$new"
    git push origin "$new"

# Release a new minor version (eg. v1.0.0 -> v1.1.0)
release-minor: _release-check
    #!/usr/bin/env sh
    set -e
    latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    major=$(echo "$latest" | sed 's/v//' | cut -d. -f1)
    minor=$(echo "$latest" | sed 's/v//' | cut -d. -f2)
    new="v${major}.$((minor + 1)).0"
    echo "Releasing $new (was $latest)"
    git tag "$new"
    git push origin "$new"

# Release a new major version (eg. v1.0.0 -> v2.0.0)
release-major: _release-check
    #!/usr/bin/env sh
    set -e
    latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    major=$(echo "$latest" | sed 's/v//' | cut -d. -f1)
    new="v$((major + 1)).0.0"
    echo "Releasing $new (was $latest)"
    git tag "$new"
    git push origin "$new"

alias release := release-patch

alias cov := coverage
alias fmt := format
