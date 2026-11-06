INTEGRATION_TESTS := "^(TestApplicationSaveOwnsOneOutcome|TestDoctorApplicationWarningOnlyJSONSucceeds|TestJSONDoctorWarningReportSuccessContract|TestPickerCatalogKeepsLiveRowsWhenRepositoryCannotBeOpened|TestPickerServiceRejectsReplacementSessionBeforeMutation|TestQueryRuntimeStatusHonorsReadDeadline|TestQueryRuntimeStatusRejectsInvalidBudget)$"
BINARY := "sesh"
PREFIX := env_var_or_default("SESH_INSTALL_PREFIX", env_var("HOME") + "/.local/bin")
INTEGRATION_ENV := "TMUX_INTEGRATION_REQUIRED=1 SESH_APPLICATION_INTEGRATION_REQUIRED=1 SESH_LIFECYCLE_INTEGRATION_REQUIRED=1 SESH_TERMINAL_INTEGRATION_REQUIRED=1"

# Start a scratch tmux server with this checkout's sesh, isolated from the live server
sandbox:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -f .cache/sandbox/root ]; then
        printf 'A sandbox is already running; source %s/env, or run just sandbox-stop\n' "$(cat .cache/sandbox/root)" >&2
        exit 1
    fi
    mkdir -p .cache/sandbox "$HOME/.cache"
    # Under ~/.cache rather than the checkout: tmux and sesh sockets fail past 108 bytes.
    root=$(mktemp -d "$HOME/.cache/ssb.XXXXXX")
    mkdir -p "$root"/{bin,home,config,data,state,cache,tmux}
    go build -o "$root/bin/{{ BINARY }}" ./cmd/sesh
    cat > "$root/env" <<EOF
    export HOME="$root/home" XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data"
    export XDG_STATE_HOME="$root/state" XDG_CACHE_HOME="$root/cache" TMUX_TMPDIR="$root/tmux"
    export PATH="$root/bin:\$PATH" SHELL=/bin/bash
    unset TMUX TMUX_PANE DBUS_SESSION_BUS_ADDRESS CLAUDE_PID
    for name in \${!SESH_@}; do unset "\$name"; done
    EOF
    printf '%s\n' "$root" > .cache/sandbox/root
    (source "$root/env"; tmux new-session -d -s sandbox -c "$HOME")
    printf 'Run this in each shell: source %s/env\nThen sesh and tmux use only the sandbox. Stop it with just sandbox-stop.\n' "$root"

# Stop the scratch tmux server and delete its files
sandbox-stop:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f .cache/sandbox/root ]; then
        printf 'No sandbox is running\n'
        exit 0
    fi
    root=$(cat .cache/sandbox/root)
    (
        source "$root/env"
        shopt -s nullglob
        for socket in "$TMUX_TMPDIR"/tmux-*/*; do
            # The server exits with its last session.
            tmux -S "$socket" list-sessions -F '#{session_id}' 2>/dev/null \
                | while read -r session; do tmux -S "$socket" kill-session -t "$session"; done
        done
    )
    rm -rf "$root" .cache/sandbox/root
    printf 'Stopped the sandbox\n'

# Run fast Go unit tests without live fixtures
[positional-arguments]
test *args:
    #!/usr/bin/env bash
    set -euo pipefail
    if (( $# )); then go test -skip '{{ INTEGRATION_TESTS }}' "$@"; else go test -skip '{{ INTEGRATION_TESTS }}' ./...; fi

# Run unit and integration tests against disposable tmux servers, applications, terminals and buses
integration:
    {{ INTEGRATION_ENV }} go test -tags=integration ./...

# Run application integration workflow tests against disposable tmux
integration-app:
    SESH_APPLICATION_INTEGRATION_REQUIRED=1 TMUX_INTEGRATION_REQUIRED=1 go test -tags=integration ./test/integration/application/...

# Run unit and integration tests with the race detector
race:
    {{ INTEGRATION_ENV }} go test -race -tags=integration ./...

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

# Measure all local test suites and print coverage
coverage:
    #!/usr/bin/env bash
    set -euo pipefail
    rm -rf coverage
    mkdir -p coverage
    coverage_package=$(awk '$1 == "module" {gsub(/"/, "", $2); print $2; exit}' go.mod)
    go test -coverpkg=./... ./... -args -test.gocoverdir="$PWD/coverage"
    {{ INTEGRATION_ENV }} go test -coverpkg=./... -tags=integration ./... -args -test.gocoverdir="$PWD/coverage"
    SESH_APPLICATION_INTEGRATION_REQUIRED=1 TMUX_INTEGRATION_REQUIRED=1 go test -coverpkg=./... -tags=integration ./test/integration/application/... -args -test.gocoverdir="$PWD/coverage"
    go tool covdata percent -i coverage -pkg "$coverage_package/..."
    go tool covdata textfmt -i coverage -o coverage/profile.txt -pkg "$coverage_package/..."
    go tool cover -func coverage/profile.txt | awk '$1 == "total:"'

# Format every supported first-party file type
format:
    @golangci-lint run --fix --enable-only whitespace --issues-exit-code=0
    @golangci-lint fmt
    @git ls-files -co --exclude-standard -z | while IFS= read -r -d '' file; do if [[ -f $file ]]; then printf '%s\0' "$file"; fi; done | xargs -0 -r shfmt -f | xargs -r -d '\n' shfmt -w
    @git ls-files -co --exclude-standard -- Justfile justfile "*.just" | while IFS= read -r file; do just --justfile "$file" --fmt; done
    @just markdown fmt

# Apply safe lint autofixes, then format every supported file type
fix: && format
    golangci-lint run --fix

# Run every configured linter on the whole project, stopping on failure
lint:
    @golangci-lint run

# Run the commit checks
check: (run-checks "formatcheck" "lockcheck" "test-isolation" "lint" "test")

# Build the binary with version info
build:
    #!/usr/bin/env sh
    set -e
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo none)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    go build -ldflags "-X github.com/zigai/sesh/internal/cli.version=dev -X github.com/zigai/sesh/internal/cli.commit=${commit} -X github.com/zigai/sesh/internal/cli.date=${build_date}" -o bin/{{ BINARY }} ./cmd/sesh

# Install the command and restart running services using the old binary
install: install-binary && (report-install PREFIX / BINARY) restart-services

# Install only the executable to the configured prefix
install-binary:
    #!/usr/bin/env bash
    set -euo pipefail
    commit=$(git rev-parse --short HEAD 2>/dev/null || printf none)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    CGO_ENABLED=0 GOBIN={{ quote(PREFIX) }} go install -trimpath -ldflags "-s -w -X github.com/zigai/sesh/internal/cli.version=dev -X github.com/zigai/sesh/internal/cli.commit=$commit -X github.com/zigai/sesh/internal/cli.date=$build_date" ./cmd/sesh

# Remove generated output and caches
clean: (remove "bin" "sesh" "sesh.exe" "dist" "coverage.out" "*.test" "*.prof" "coverage")

# Validate local packaging configuration without publishing
package-check:
    goreleaser check

# Check the same files and tools without writing
formatcheck:
    @golangci-lint run --enable-only whitespace
    @golangci-lint fmt --diff
    @git ls-files -co --exclude-standard -z | while IFS= read -r -d '' file; do if [[ -f $file ]]; then printf '%s\0' "$file"; fi; done | xargs -0 -r shfmt -f | xargs -r -d '\n' shfmt -d
    @git ls-files -co --exclude-standard -- Justfile justfile "*.just" | while IFS= read -r file; do just --justfile "$file" --fmt --check; done
    @just markdown check

# Run the commit checks and slow suites
checkfull: (run-checks "formatcheck" "lockcheck" "test-isolation" "lint" "test" "race" "integration" "integration-app" "guest" "guest-extended")

# Synchronize dependency locks without upgrading dependencies
[no-exit-message]
lock:
    @go mod tidy

# Verify dependency manifests and locks without changing files
[no-exit-message]
lockcheck:
    @go mod tidy -diff

# Install repository dependencies
setup:
    @go mod download

# List available recipes
[default]
help:
    @just --list

alias cov := coverage
alias fmt := format

[private]
restart-services:
    #!/usr/bin/env bash
    set -euo pipefail
    [ "$(uname -s)" = Linux ] || exit 0
    installed_path={{ quote(PREFIX + "/" + BINARY) }}
    for unit in $(systemctl --user list-units 'sesh-*.service' --state=running --no-legend --plain | awk '{print $1}'); do
        [ "$(systemctl --user show -p Type --value "$unit")" != oneshot ] || continue
        pid=$(systemctl --user show -p MainPID --value "$unit")
        if [ "$(readlink "/proc/$pid/exe")" = "$installed_path (deleted)" ]; then
            systemctl --user restart "$unit"
            printf 'restarted  %s\n' "$unit"
        fi
    done

[positional-arguments]
[private]
remove +paths:
    #!/usr/bin/env bash
    set -euo pipefail
    shopt -s nullglob globstar
    size() { LC_ALL=C numfmt --from-unit=1024 --to=iec-i --suffix=B --format=%.1f "$1"; }
    rows=() total=0
    for pattern in "$@"; do
        found=()
        for path in $pattern; do
            if [[ -e $path || -L $path ]]; then found+=("$path"); fi
        done
        if (( ${#found[@]} == 0 )); then continue; fi
        kib=$(du -sck -- "${found[@]}" | tail -n1 | cut -f1)
        rm -rf -- "${found[@]}"
        label=$pattern
        if (( ${#found[@]} > 1 )); then label+=" (${#found[@]})"; fi
        rows+=("removed  $label|$(size "$kib")")
        total=$(( total + kib ))
    done
    if [[ -n ${CLEAN_TOTAL_FILE:-} ]]; then echo "$total" >> "$CLEAN_TOTAL_FILE"; fi
    if (( ${#rows[@]} == 0 )); then echo "nothing to clean"; exit 0; fi
    printf '%s\n' "${rows[@]}" "total freed|$(size "$total")" | column -t -s '|' -R 2

[positional-arguments]
[private]
report-install +paths:
    #!/usr/bin/env bash
    set -euo pipefail
    rows=() warnings=() status=0
    for path in "$@"; do
        name=$(basename "$path") real=$(realpath "$path")
        sized=$real venv=$(dirname "$(dirname "$real")")
        [[ -f $venv/pyvenv.cfg ]] && sized=$venv
        if ! version=$("$path" --version 2>&1); then
            "$path" --help >/dev/null 2>&1 || { echo "failed     $path does not start" >&2; status=1; continue; }
            version=-
        fi
        rows+=("installed|$name|${path/#$HOME/\~}|$(du -sk "$sized" | cut -f1 | LC_ALL=C numfmt --from-unit=1024 --to=iec-i --suffix=B --format=%.1f)|${version%%$'\n'*}")
        first=$(command -v "$name")
        [[ $(realpath "$first") == "$real" ]] || warnings+=("warning    $name: PATH finds ${first/#$HOME/\~} first")
    done
    if (( ${#rows[@]} )); then printf '%s\n' "${rows[@]}" | column -t -s '|' -R 4; fi
    if (( ${#warnings[@]} )); then printf '%s\n' "${warnings[@]}"; fi
    exit "$status"

[positional-arguments]
[private]
run-checks +steps:
    #!/usr/bin/env bash
    set -uo pipefail
    LC_NUMERIC=C
    log_dir="${XDG_CACHE_HOME:-$HOME/.cache}/just-check"
    mkdir -p "$log_dir"
    log=$(mktemp "$log_dir/step.XXXXXX")
    trap 'rm -f "$log"' EXIT
    width=0 failed=()
    for step in "$@"; do (( ${#step} > width )) && width=${#step}; done
    for step in "$@"; do
        read -ra command <<< "$step"
        printf 'check  %-*s  ' "$width" "$step"
        start=$EPOCHREALTIME
        run=(just --justfile {{ quote(justfile()) }} "${command[@]}")
        if [[ -t 1 ]]; then run=(script -qefc "${run[*]@Q}" /dev/null); fi
        if "${run[@]}" < /dev/null > "$log" 2>&1; then status=ok; else status=FAIL; failed+=("$step"); fi
        printf '%-4s  %7.1fs\n' "$status" "$(LC_NUMERIC=C awk -v a="$start" -v b="$EPOCHREALTIME" 'BEGIN { print b - a }')"
        if [[ "$status" == FAIL ]]; then sed 's/\r$//; s/^/    /' "$log"; fi
    done
    if (( ${#failed[@]} )); then printf '%d of %d failed: %s\n' "${#failed[@]}" "$#" "${failed[*]}"; exit 1; fi
    printf 'all %d passed\n' "$#"

[private]
markdown *args:
    rumdl {{ args }} --disable MD001,MD013,MD024,MD025,MD026,MD031,MD032,MD033,MD034,MD036,MD041,MD058 --config 'MD060.enabled=true' --config 'MD060.style="aligned"' --config 'MD003.style="atx"' --config 'MD004.style="dash"' --config 'MD029.style="ordered"' --config 'MD046.style="fenced"' --config 'MD048.style="backtick"' --config 'MD049.style="asterisk"' --config 'MD050.style="asterisk"' --config 'MD055.style="leading_and_trailing"'
