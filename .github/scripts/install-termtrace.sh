#!/bin/sh
# Install the checksum-pinned terminal-test tool release.
set -eu

prefix=${1:?pass an absolute installation prefix}
version=v0.1.0
archive=termtrace-x86_64-unknown-linux-gnu
sha256=5fd039202d9076ccf38eb09290229793bf4836209f5da1c97bcb0476af021931

case "$prefix" in
    /*) ;;
    *)
        echo 'installation prefix must be absolute' >&2
        exit 2
        ;;
esac

if [ "$(uname -s)-$(uname -m)" != Linux-x86_64 ]; then
    echo 'the pinned terminal-test tool release is Linux x86_64 only' >&2
    exit 2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL -o "$work/$archive.tar.gz" "https://github.com/zigai/termtrace/releases/download/$version/$archive.tar.gz"
echo "$sha256  $work/$archive.tar.gz" | sha256sum -c -
tar -xzf "$work/$archive.tar.gz" -C "$work"

install -d "$prefix/bin"
install -m 0755 "$work/$archive/termtrace" "$prefix/bin/termtrace"
"$prefix/bin/termtrace" --version
