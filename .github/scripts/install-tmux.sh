#!/bin/sh
# Build the pinned upstream tmux release into an explicitly selected CI prefix.
set -eu

prefix=${1:?pass an absolute installation prefix}
case "$prefix" in
    /*) ;;
    *)
        echo 'installation prefix must be absolute' >&2
        exit 2
        ;;
esac

build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM
cd "$build_dir"
curl --fail --location --silent --show-error \
    https://github.com/tmux/tmux/releases/download/3.8/tmux-3.8.tar.gz \
    --output tmux.tar.gz
printf '%s\n' 'e79c699c7e949dccd0a4a125e17b8d1261e311b16979dbc8eb34542f3966d82e  tmux.tar.gz' | shasum -a 256 -c -
tar -xzf tmux.tar.gz
cd tmux-3.8
if [ "$(uname -s)" = Darwin ]; then
    ./configure --prefix="$prefix" --enable-utf8proc --enable-jemalloc
else
    ./configure --prefix="$prefix"
fi
make -j2
make install
