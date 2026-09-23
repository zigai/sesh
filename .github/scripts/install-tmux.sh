#!/bin/sh
# Build the pinned upstream tmux release into an explicitly selected CI prefix.
set -eu

prefix=${1:?pass an absolute installation prefix}
case "$prefix" in
    /*) ;;
    *) echo 'installation prefix must be absolute' >&2; exit 2 ;;
esac

build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM
cd "$build_dir"
curl --fail --location --silent --show-error \
    https://github.com/tmux/tmux/releases/download/3.7c/tmux-3.7c.tar.gz \
    --output tmux.tar.gz
printf '%s\n' '7c60cae9a0e25288e2e24750aafc9e8800fc7fd4555e447e1b29ee4201cfb3bf  tmux.tar.gz' | shasum -a 256 -c -
tar -xzf tmux.tar.gz
cd tmux-3.7c
./configure --prefix="$prefix"
make -j2
make install
