#!/bin/sh
# Print the packages assigned to one shard of the test matrix.
#
#   usage: test-shard.sh OS INDEX COUNT < packages.txt
#
# OS is linux or macos. INDEX is 1-based. Packages are assigned longest first
# to the least loaded shard, using the measured durations in test-timings.txt.
set -eu

os=${1:?pass linux or macos}
index=${2:?pass the 1-based shard index}
count=${3:?pass the shard count}
timings=$(dirname "$0")/../test-timings.txt

awk -v os="$os" -v want="$index" -v shards="$count" '
    FNR == NR {
        if ($0 ~ /^#/ || NF < 3) { next }
        weight[$1] = (os == "macos") ? $3 : $2
        next
    }
    {
        package = $1
        seconds = ($1 in weight) ? weight[$1] : 60
        print seconds, package
    }
' "$timings" - | sort -rn | awk -v want="$index" -v shards="$count" '
    {
        seconds = $1
        package = $2
        target = 1
        for (shard = 2; shard <= shards; shard++) {
            if (load[shard] < load[target]) { target = shard }
        }
        load[target] += seconds
        if (target == want) { print package }
    }
'
