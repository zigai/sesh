#!/bin/sh
# Build the reviewed terminal-test tool from an explicitly selected checkout.
set -eu

prefix=${1:?pass an absolute installation prefix}
source_dir=${2:?pass an absolute disposable Termtrace checkout}
revision=bbbb5513a9f928bf6157ec0f27f6590848578ef4

for directory in "$prefix" "$source_dir"; do
    case "$directory" in
        /*) ;;
        *) echo 'installation prefix and source directory must be absolute' >&2; exit 2 ;;
    esac
done

if [ "$(uname -s)" != Linux ]; then
    echo 'the required terminal-test tool currently builds on Linux only' >&2
    exit 2
fi

if [ "$(git -C "$source_dir" rev-parse HEAD)" != "$revision" ]; then
    echo 'Termtrace checkout does not match the pinned revision' >&2
    exit 2
fi
git -C "$source_dir" diff --exit-code --quiet HEAD --

cd "$source_dir"
git submodule update --init --recursive --depth 1
bash scripts/bootstrap.sh

TERMTRACE_BUILD_REVISION=$revision \
TERMTRACE_GHOSTTY_PREFIX="$source_dir/vendor/ghostty/zig-out" \
CARGO_TARGET_DIR="$source_dir/target" \
    cargo +1.97.1 build --locked --release --bin termtrace

install -d "$prefix/bin"
install -m 0755 target/release/termtrace "$prefix/bin/termtrace"
"$prefix/bin/termtrace" --version
