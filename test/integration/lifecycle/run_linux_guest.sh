#!/usr/bin/env bash
# Explicitly opt-in disposable QEMU proof for real Linux logind reboot handling.
set -euo pipefail

if [[ ${SESH_LINUX_GUEST:-} != 1 ]]; then
  echo 'set SESH_LINUX_GUEST=1 to run the disposable QEMU lifecycle proof' >&2
  exit 2
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
image_name=ubuntu-24.04-server-cloudimg-amd64-20260911.img
image_url=https://cloud-images.ubuntu.com/releases/noble/release-20260911/ubuntu-24.04-server-cloudimg-amd64.img
image_sha256=612b2c0cc1bc413a6cb8c38fd611794caf0f2b436c50013d8b3794db12ad7354
cache_dir=${SESH_GUEST_CACHE:-/tmp/sesh-linux-guest-cache}
evidence_dir=${SESH_GUEST_EVIDENCE_DIR:-$repo_root/.agents/linux-guest/evidence}
work_dir=$(mktemp -d /tmp/sesh-linux-guest.XXXXXXXX)
qemu_pid=

cleanup() {
  local status=$?
  local cleanup_failed=0
  trap - EXIT HUP INT TERM
  set +e

  mkdir -p "$evidence_dir" || cleanup_failed=1
  if [[ -f $work_dir/console.log ]]; then
    cp "$work_dir/console.log" "$evidence_dir/console-last.log" 2>/dev/null || cleanup_failed=1
  fi
  if [[ -f $work_dir/qemu.log ]]; then
    cp "$work_dir/qemu.log" "$evidence_dir/qemu-last.log" 2>/dev/null || cleanup_failed=1
  fi
  if [[ $status -ne 0 ]] && [[ -n $qemu_pid ]] && kill -0 "$qemu_pid" 2>/dev/null && declare -p ssh_opts >/dev/null 2>&1; then
    timeout 30 ssh "${ssh_opts[@]}" sesh@127.0.0.1 \
      'sudo journalctl -u "sesh-*" -u systemd-logind.service --no-pager --output=short-iso' \
      >"$evidence_dir/guest-journal-failure.txt" 2>&1 || true
  fi
  rm -f "$evidence_dir/active-workdir" "$evidence_dir/continue" || cleanup_failed=1

  if [[ -n $qemu_pid ]] && kill -0 "$qemu_pid" 2>/dev/null; then
    kill "$qemu_pid" 2>/dev/null || true
    for _ in {1..50}; do
      kill -0 "$qemu_pid" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$qemu_pid" 2>/dev/null; then
      kill -KILL "$qemu_pid" 2>/dev/null || true
    fi
    wait "$qemu_pid" 2>/dev/null || true
    if kill -0 "$qemu_pid" 2>/dev/null; then
      cleanup_failed=1
    fi
  fi

  if [[ ${SESH_GUEST_KEEP_WORK:-} != 1 ]]; then
    rm -rf "$work_dir" || cleanup_failed=1
  else
    echo "kept guest work directory: $work_dir" >&2
  fi

  if [[ $cleanup_failed == 1 ]]; then
    echo 'guest cleanup was incomplete' >&2
    [[ $status == 0 ]] && status=1
  fi

  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'echo "guest lifecycle proof failed at line $LINENO" >&2' ERR

for command in curl sha256sum qemu-img qemu-system-x86_64 cloud-localds ssh-keygen ssh scp python3 go base64 jq timeout; do
  command -v "$command" >/dev/null || {
    echo "missing required command: $command" >&2
    exit 2
  }
done

mkdir -p "$cache_dir" "$evidence_dir"
image=$cache_dir/$image_name
if [[ ! -f $image ]]; then
  curl --connect-timeout 15 --max-time 1800 --fail --location --show-error --progress-bar "$image_url" -o "$image.part"
  mv "$image.part" "$image"
fi
printf '%s  %s\n' "$image_sha256" "$image" | sha256sum -c -

ssh-keygen -q -t ed25519 -N '' -C sesh-disposable -f "$work_dir/guest-user-key"
ssh-keygen -q -t ed25519 -N '' -C sesh-disposable -f "$work_dir/guest-host-key"
user_public_key=$(<"$work_dir/guest-user-key.pub")
host_public_key=$(<"$work_dir/guest-host-key.pub")
host_private_b64=$(base64 -w0 <"$work_dir/guest-host-key")
host_public_b64=$(base64 -w0 <"$work_dir/guest-host-key.pub")

cat >"$work_dir/meta-data" <<'EOF'
instance-id: sesh-linux-logind-proof
local-hostname: sesh-guest
EOF

cat >"$work_dir/user-data" <<EOF
#cloud-config
users:
  - name: sesh
    gecos: sesh disposable guest
    groups: [adm, systemd-journal]
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $user_public_key
ssh_deletekeys: false
write_files:
  - path: /etc/ssh/ssh_host_ed25519_key
    owner: root:root
    permissions: '0600'
    encoding: b64
    content: $host_private_b64
  - path: /etc/ssh/ssh_host_ed25519_key.pub
    owner: root:root
    permissions: '0644'
    encoding: b64
    content: $host_public_b64
  - path: /etc/systemd/logind.conf.d/sesh.conf
    owner: root:root
    permissions: '0644'
    content: |
      [Login]
      InhibitDelayMaxSec=3
package_update: true
packages:
  - bison
  - build-essential
  - ca-certificates
  - curl
  - jq
  - libevent-dev
  - libncurses-dev
  - neovim
  - pkg-config
  - openssh-server
runcmd:
  - [mkdir, -p, /var/log/journal]
  - [systemd-tmpfiles, --create, --prefix, /var/log/journal]
  - [systemctl, restart, systemd-logind]
  - [touch, /var/lib/cloud/sesh-ready]
EOF

cloud-localds "$work_dir/seed.img" "$work_dir/user-data" "$work_dir/meta-data"
qemu-img create -q -f qcow2 -F qcow2 -b "$image" "$work_dir/guest.qcow2" 8G

port=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
printf '[127.0.0.1]:%s %s\n' "$port" "$host_public_key" >"$work_dir/known_hosts"

qemu-system-x86_64 \
  -enable-kvm -cpu host -smp 2 -m 2048 \
  -display none -monitor none -serial "file:$work_dir/console.log" \
  -qmp "unix:$work_dir/qmp.sock,server=on,wait=off" \
  -drive "file=$work_dir/guest.qcow2,if=virtio,format=qcow2" \
  -drive "file=$work_dir/seed.img,if=virtio,format=raw,readonly=on" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22" \
  -device virtio-net-pci,netdev=net0 \
  >"$work_dir/qemu.log" 2>&1 &
qemu_pid=$!
printf '%s\n' "$qemu_pid" >"$work_dir/qemu.pid"

ssh_common_opts=(
  -F /dev/null
  -i "$work_dir/guest-user-key"
  -o BatchMode=yes
  -o ClearAllForwardings=yes
  -o ConnectTimeout=3
  -o ControlMaster=no
  -o ControlPath=none
  -o IdentitiesOnly=yes
  -o ProxyCommand=none
  -o RequestTTY=no
  -o ServerAliveCountMax=3
  -o ServerAliveInterval=5
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$work_dir/known_hosts"
)
ssh_opts=("${ssh_common_opts[@]}" -p "$port")
scp_opts=("${ssh_common_opts[@]}" -P "$port")

guest_ssh() {
  timeout 90 ssh "${ssh_opts[@]}" sesh@127.0.0.1 "$@"
}

guest_ssh_long() {
  timeout 900 ssh "${ssh_opts[@]}" sesh@127.0.0.1 "$@"
}

qmp_command() {
  python3 - "$work_dir/qmp.sock" "$1" <<'PY'
import json
import socket
import sys

with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
    client.settimeout(5)
    client.connect(sys.argv[1])
    stream = client.makefile("rwb", buffering=0)
    json.loads(stream.readline())
    stream.write(b'{"execute":"qmp_capabilities"}\r\n')
    while "return" not in json.loads(stream.readline()):
        pass
    command = json.dumps({"execute": sys.argv[2]}).encode()
    stream.write(command + b"\r\n")
    while True:
        response = json.loads(stream.readline())
        if "return" in response or "error" in response:
            print(json.dumps(response, sort_keys=True))
            break
PY
}

assert_daemon_inhibitors() {
  local unit=$1
  local output=$2
  local daemon_pid
  local inhibitors_ready=0

  daemon_pid=$(guest_ssh "systemctl show --property=MainPID --value $unit")
  [[ $daemon_pid =~ ^[1-9][0-9]*$ ]]

  for _ in {1..60}; do
    if guest_ssh 'systemd-inhibit --list --no-pager --no-legend' >"$output" 2>/dev/null &&
      awk -v pid="$daemon_pid" '$1 == "sesh" && $4 == pid && $6 == "shutdown" && $NF == "delay" { found = 1 } END { exit !found }' "$output" &&
      awk -v pid="$daemon_pid" '$1 == "sesh" && $4 == pid && $6 == "sleep" && $NF == "delay" { found = 1 } END { exit !found }' "$output"; then
      inhibitors_ready=1
      break
    fi
    sleep 1
  done

  [[ $inhibitors_ready == 1 ]]
  printf '%s\n' "$daemon_pid"
}

start_daemon() {
  local unit=$1
  local repository=$2
  local history=$3
  local status_file=$4
  local tmux_unit=${5:-sesh-tmux.service}

  guest_ssh "sudo systemd-run --unit=$unit --property=User=sesh --property=Group=sesh --property=After=$tmux_unit --property=Requires=$tmux_unit --property=RuntimeDirectory=sesh-guest --property=RuntimeDirectoryMode=0700 --property=RuntimeDirectoryPreserve=yes --property=TimeoutStopSec=30s --setenv=HOME=/home/sesh --setenv=PATH=/opt/tmux-3.7c/bin:/usr/local/bin:/usr/bin:/bin --setenv=XDG_RUNTIME_DIR=/run/sesh-guest --setenv=XDG_DATA_HOME=/var/lib/sesh-guest/data --setenv=XDG_STATE_HOME=/var/lib/sesh-guest/state /opt/sesh --no-config daemon coordinator --socket /run/sesh-guest/tmux.sock --repository $repository --lease-root /var/lib/sesh-guest/leases --history $history --status-file $status_file"
}

assert_daemon_runtime_status() {
  local repository=$1
  local output=$2

  set +e
  guest_ssh "XDG_RUNTIME_DIR=/run/sesh-guest /opt/sesh --no-config doctor --socket /run/sesh-guest/tmux.sock --repository $repository --json" >"$output"
  local doctor_exit=$?
  set -e
  [[ $doctor_exit == 0 || $doctor_exit == 1 ]]
  jq -e '.result.checks[] | select(.check == "daemon-runtime" and .status == "ok")' "$output" >/dev/null
}

ensure_tmux_ready() {
  if ! guest_ssh 'systemctl is-active --quiet sesh-tmux.service' >/dev/null 2>&1; then
    guest_ssh 'sudo systemd-run --unit=sesh-tmux --property=User=sesh --property=Group=sesh --property=RuntimeDirectory=sesh-guest --property=RuntimeDirectoryMode=0700 --property=RuntimeDirectoryPreserve=yes --setenv=HOME=/home/sesh --setenv=PATH=/opt/tmux-3.7c/bin:/usr/local/bin:/usr/bin:/bin --setenv=XDG_RUNTIME_DIR=/run/sesh-guest /opt/sesh --no-config daemon tmux --socket /run/sesh-guest/tmux.sock --tmux /opt/tmux-3.7c/bin/tmux'
    guest_ssh '/opt/sesh --no-config daemon wait-ready --socket /run/sesh-guest/tmux.sock --timeout 20s'
    guest_ssh '/opt/sesh-fixture /run/sesh-guest/tmux.sock'
  fi
}

for _ in {1..180}; do
  if guest_ssh true >/dev/null 2>&1; then
    break
  fi
  kill -0 "$qemu_pid" 2>/dev/null || {
    echo 'QEMU exited before SSH became ready' >&2
    exit 1
  }
  sleep 2
done
guest_ssh_long 'timeout 600 cloud-init status --wait'
guest_ssh 'test -f /var/lib/cloud/sesh-ready'

if [[ ${SESH_GUEST_PAUSE_AFTER_PROVISION:-} == 1 ]]; then
  rm -f "$evidence_dir/continue"
  printf '%s\n' "$work_dir" >"$evidence_dir/active-workdir"
  echo "guest provisioned; touch $evidence_dir/continue after the runtime freezes" >&2
  for _ in {1..450}; do
    [[ -f $evidence_dir/continue ]] && break
    kill -0 "$qemu_pid" 2>/dev/null || {
      echo 'QEMU exited while waiting for the runtime freeze' >&2
      exit 1
    }
    sleep 2
  done
  [[ -f $evidence_dir/continue ]] || {
    echo 'runtime freeze was not released within 15 minutes' >&2
    exit 1
  }
  rm -f "$evidence_dir/continue" "$evidence_dir/active-workdir"
fi

GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -o "$work_dir/sesh" "$repo_root/cmd/sesh"
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -o "$work_dir/fixture" "$repo_root/test/integration/lifecycle/fixture"
sha256sum "$work_dir/sesh" >"$work_dir/sesh-binary.sha256"
sha256sum "$work_dir/fixture" >"$work_dir/fixture-binary.sha256"
git rev-parse HEAD >"$work_dir/git-head.txt"
git status --porcelain >"$work_dir/git-status.txt"
timeout 180 scp "${scp_opts[@]}" \
  "$work_dir/sesh" \
  "$work_dir/fixture" \
  "$repo_root/.github/scripts/install-tmux.sh" \
  sesh@127.0.0.1:/tmp/

guest_ssh_long 'sudo install -m 0755 /tmp/sesh /opt/sesh && sudo install -m 0755 /tmp/fixture /opt/sesh-fixture && timeout 600 sudo /bin/sh /tmp/install-tmux.sh /opt/tmux-3.7c && sudo ln -sfn /opt/tmux-3.7c/bin/tmux /usr/local/bin/tmux && (which nvim >/dev/null 2>&1 || (sudo apt-get update && sudo apt-get install -y neovim))'
guest_ssh 'sudo rm -f /usr/lib/systemd/logind.conf.d/unattended* /lib/systemd/logind.conf.d/unattended* /etc/systemd/logind.conf.d/* && sudo mkdir -p /etc/systemd/logind.conf.d && printf "[Login]\nInhibitDelayMaxSec=3\n" | sudo tee /etc/systemd/logind.conf.d/zz-sesh.conf >/dev/null && sudo ln -sfn /dev/null /etc/systemd/logind.conf.d/unattended-upgrades-logind-maxdelay.conf && sudo sed -i "s/#\?InhibitDelayMaxSec=.*/InhibitDelayMaxSec=3/" /etc/systemd/logind.conf && sudo systemctl restart systemd-logind && timeout 10 systemctl is-active --quiet systemd-logind.service'
guest_ssh 'sudo install -d -o sesh -g sesh -m 0700 /run/sesh-guest /var/lib/sesh-guest /var/lib/sesh-guest/data /var/lib/sesh-guest/state /var/lib/sesh-guest/leases'
guest_ssh 'timeout 5 systemd-inhibit --what=shutdown --who=sesh --why=fixture-readiness --mode=delay true'
guest_ssh 'timeout 5 systemd-inhibit --what=sleep --who=sesh --why=fixture-readiness --mode=delay true'
guest_ssh 'sudo systemd-run --unit=sesh-tmux --property=User=sesh --property=Group=sesh --property=RuntimeDirectory=sesh-guest --property=RuntimeDirectoryMode=0700 --property=RuntimeDirectoryPreserve=yes --setenv=HOME=/home/sesh --setenv=PATH=/opt/tmux-3.7c/bin:/usr/local/bin:/usr/bin:/bin --setenv=XDG_RUNTIME_DIR=/run/sesh-guest /opt/sesh --no-config daemon tmux --socket /run/sesh-guest/tmux.sock --tmux /opt/tmux-3.7c/bin/tmux'
guest_ssh '/opt/sesh --no-config daemon wait-ready --socket /run/sesh-guest/tmux.sock --timeout 20s'
guest_ssh '/opt/sesh-fixture /run/sesh-guest/tmux.sock'

start_daemon \
  sesh-daemon \
  /var/lib/sesh-guest/repository \
  /var/lib/sesh-guest/history.json \
  /var/lib/sesh-guest/final-save-status.json

daemon_pid=$(assert_daemon_inhibitors sesh-daemon.service "$work_dir/inhibitor-before.txt")
guest_ssh 'systemctl is-active sesh-tmux.service sesh-daemon.service'
assert_daemon_runtime_status /var/lib/sesh-guest/repository "$work_dir/daemon-runtime-before.json"

if [[ ${SESH_GUEST_LIFECYCLE_SUITE:-} == extended ]]; then
  echo 'extended phase: suspend and resume'
  # Attempt a real guest suspend only when QEMU reports that it entered the
  # suspended state; QMP provides the bounded, owned wake channel.
  guest_ssh 'sudo systemd-run --unit=sesh-suspend-request --property=Type=oneshot /bin/systemctl suspend'
  suspend_supported=0
  for _ in {1..150}; do
    qmp_command query-status >"$work_dir/qmp-suspend-status.json"
    if jq -e '.return.status == "suspended"' "$work_dir/qmp-suspend-status.json" >/dev/null; then
      suspend_supported=1
      break
    fi
    if guest_ssh 'systemctl is-failed --quiet sesh-suspend-request.service' >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  if [[ $suspend_supported == 1 ]]; then
    qmp_command system_wakeup >"$work_dir/qmp-wakeup.json"
    resumed=0
    for _ in {1..180}; do
      if guest_ssh true >/dev/null 2>&1; then
        resumed=1
        break
      fi
      sleep 0.5
    done
    [[ $resumed == 1 ]]
    suspend_pid=$(assert_daemon_inhibitors sesh-daemon.service "$work_dir/inhibitor-after-resume.txt")
    test "$suspend_pid" = "$daemon_pid"
    printf 'supported\n' >"$work_dir/suspend-capability.txt"
  else
    printf 'unsupported_or_not_entered\n' >"$work_dir/suspend-capability.txt"
  fi
  guest_ssh 'sudo journalctl -u sesh-daemon.service -u sesh-suspend-request.service --no-pager --output=short-iso' >"$work_dir/suspend-journal.txt"

  # A requested unit stop must be clean and must not invent a shutdown receipt.
  echo 'extended phase: explicit daemon stop'
  guest_ssh 'sudo systemctl stop sesh-daemon.service'
  guest_ssh 'test "$(systemctl show --property=Result --value sesh-daemon.service)" = "success" && test "$(systemctl show --property=ExecMainStatus --value sesh-daemon.service)" = "0"'
  guest_ssh 'sudo journalctl -u sesh-daemon.service --no-pager --output=short-iso' >"$work_dir/explicit-stop-journal.txt"

  # Loss of logind's well-known-name owner is terminal and fail-closed.
  echo 'extended phase: logind owner loss'
  start_daemon \
    sesh-daemon-owner-loss \
    /var/lib/sesh-guest/owner-loss-repository \
    /var/lib/sesh-guest/owner-loss-history.json \
    /var/lib/sesh-guest/owner-loss-status.json
  owner_loss_pid=$(assert_daemon_inhibitors sesh-daemon-owner-loss.service "$work_dir/inhibitor-before-owner-loss.txt")
  guest_ssh 'sudo systemctl kill --kill-whom=main --signal=KILL systemd-logind.service'
  owner_loss_stopped=0
  for _ in {1..100}; do
    if ! guest_ssh 'systemctl is-active --quiet sesh-daemon-owner-loss.service' >/dev/null 2>&1; then
      owner_loss_stopped=1
      break
    fi
    sleep 0.1
  done
  [[ $owner_loss_stopped == 1 ]]
  guest_ssh 'test "$(systemctl show --property=Result --value sesh-daemon-owner-loss.service)" != "success"'
  guest_ssh 'sudo journalctl -u sesh-daemon-owner-loss.service -u systemd-logind.service --no-pager --output=short-iso' >"$work_dir/logind-owner-loss-journal.txt"
  printf '%s\n' "$owner_loss_pid" >"$work_dir/logind-owner-loss-daemon-pid.txt"
  guest_ssh 'sudo systemctl restart systemd-logind.service'
  guest_ssh 'timeout 10 systemctl is-active --quiet systemd-logind.service'

  # Seed a valid repository, make every repository inode read-only, then ask
  # real logind to reboot. The writable status path must retain the failure.
  echo 'extended phase: read-only repository reboot'
  if guest_ssh '/opt/sesh --no-config autosave final-save --socket /run/sesh-guest/tmux.sock --repository /var/lib/sesh-guest/readonly-repository --lease-root /var/lib/sesh-guest/leases --history /var/lib/sesh-guest/readonly-seed-history.json --status-file /var/lib/sesh-guest/readonly-seed-status.json --deadline 5s --json' >"$work_dir/readonly-seed.json"; then
    readonly_seed_exit=0
  else
    readonly_seed_exit=$?
  fi
  [[ $readonly_seed_exit == 0 || $readonly_seed_exit == 1 ]]
  readonly_generation=$(guest_ssh '/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/readonly-repository --json | jq -er .result.current')
  test -n "$readonly_generation"
  start_daemon \
    sesh-daemon-readonly \
    /var/lib/sesh-guest/readonly-repository \
    /var/lib/sesh-guest/readonly-history.json \
    /var/lib/sesh-guest/readonly-final-status.json
  assert_daemon_inhibitors sesh-daemon-readonly.service "$work_dir/inhibitor-before-readonly-reboot.txt" >"$work_dir/readonly-daemon-pid.txt"

  # Cancel a real future logind reboot before preparation. Noble logind does not
  # support canceling once PreparingForShutdown is true; this phase proves the
  # supported scheduled-cancellation behavior without claiming a false event.
  # Keep it adjacent to the intentional reboot so a distro-level stale schedule
  # cannot interfere with the independent suspend and owner-loss scenarios.
  readonly_boot_before=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id')
  guest_ssh 'sudo sh -c '\''shutdown_at=$(( $(date +%s%6N) + 60000000 )); busctl call org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager ScheduleShutdown st reboot "$shutdown_at"; sleep 1; busctl call org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager CancelScheduledShutdown'\''' >"$work_dir/canceled-shutdown-call.txt"
  grep -qx 'b true' "$work_dir/canceled-shutdown-call.txt"
  test "$(guest_ssh 'cat /proc/sys/kernel/random/boot_id')" = "$readonly_boot_before"
  canceled_pid=$(assert_daemon_inhibitors sesh-daemon-readonly.service "$work_dir/inhibitor-after-cancel.txt")
  test "$canceled_pid" = "$(cat "$work_dir/readonly-daemon-pid.txt")"
  guest_ssh 'sudo journalctl -u sesh-daemon-readonly.service -u systemd-logind.service --no-pager --output=short-iso' >"$work_dir/canceled-shutdown-journal.txt"

  guest_ssh 'sudo chmod -R a-w /var/lib/sesh-guest/readonly-repository'
  printf '%s\n' "$readonly_boot_before" >"$work_dir/readonly-boot-before.txt"
  printf '%s\n' "$readonly_generation" >"$work_dir/readonly-seed-generation.txt"

  set +e
  guest_ssh 'sudo systemctl reboot' >"$work_dir/readonly-reboot-request.txt" 2>&1
  set -e
  readonly_boot_after=
  for _ in {1..180}; do
    candidate=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null || true)
    if [[ -n $candidate && $candidate != "$readonly_boot_before" ]]; then
      readonly_boot_after=$candidate
      break
    fi
    sleep 2
  done
  test -n "$readonly_boot_after"
  printf '%s\n' "$readonly_boot_after" >"$work_dir/readonly-boot-after.txt"

  guest_ssh 'jq -e '\''(.result == "failed" or .result == "incomplete" or .result == "uncertain") and .incomplete == true and .result != "saved"'\'' /var/lib/sesh-guest/readonly-final-status.json'
  guest_ssh 'cat /var/lib/sesh-guest/readonly-final-status.json' >"$work_dir/readonly-final-status.json"
  guest_ssh 'sudo chmod -R u+w /var/lib/sesh-guest/readonly-repository'
  guest_ssh "/opt/sesh --no-config snapshot verify --repository /var/lib/sesh-guest/readonly-repository --generation $readonly_generation --json" >"$work_dir/readonly-seed-verify.json"
  jq -e --arg generation "$readonly_generation" '.ok == true and .result.generation == $generation and .result.byteIntegrity == true' "$work_dir/readonly-seed-verify.json" >/dev/null
  guest_ssh 'sudo journalctl -b -1 -u sesh-daemon-readonly.service -u sesh-tmux.service --no-pager --output=short-iso' >"$work_dir/readonly-journal.txt"

  # Recreate only the owned fixture after reboot, seed a known generation, then
  # stall the complete owned tmux service cgroup during another real reboot.
  echo 'extended phase: stalled tmux during reboot'
  guest_ssh 'sudo systemd-run --unit=sesh-tmux-budget --property=User=sesh --property=Group=sesh --property=RuntimeDirectory=sesh-guest --property=RuntimeDirectoryMode=0700 --property=RuntimeDirectoryPreserve=yes --setenv=HOME=/home/sesh --setenv=PATH=/opt/tmux-3.7c/bin:/usr/local/bin:/usr/bin:/bin --setenv=XDG_RUNTIME_DIR=/run/sesh-guest /opt/sesh --no-config daemon tmux --socket /run/sesh-guest/tmux.sock --tmux /opt/tmux-3.7c/bin/tmux'
  guest_ssh '/opt/sesh --no-config daemon wait-ready --socket /run/sesh-guest/tmux.sock --timeout 20s'
  guest_ssh '/opt/sesh-fixture /run/sesh-guest/tmux.sock'
  if guest_ssh '/opt/sesh --no-config autosave final-save --socket /run/sesh-guest/tmux.sock --repository /var/lib/sesh-guest/budget-repository --lease-root /var/lib/sesh-guest/leases --history /var/lib/sesh-guest/budget-seed-history.json --status-file /var/lib/sesh-guest/budget-seed-status.json --deadline 5s --json' >"$work_dir/budget-seed.json"; then
    budget_seed_exit=0
  else
    budget_seed_exit=$?
  fi
  [[ $budget_seed_exit == 0 || $budget_seed_exit == 1 ]]
  budget_generation=$(guest_ssh '/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/budget-repository --json | jq -er .result.current')
  printf '%s\n' "$budget_generation" >"$work_dir/budget-seed-generation.txt"
  echo 'extended phase: measure logind delay budget'
  guest_ssh 'sudo rm -f /usr/lib/systemd/logind.conf.d/unattended* /lib/systemd/logind.conf.d/unattended* /etc/systemd/logind.conf.d/* && sudo mkdir -p /etc/systemd/logind.conf.d && printf "[Login]\nInhibitDelayMaxSec=3\n" | sudo tee /etc/systemd/logind.conf.d/zz-sesh.conf >/dev/null && sudo ln -sfn /dev/null /etc/systemd/logind.conf.d/unattended-upgrades-logind-maxdelay.conf && sudo sed -i "s/#\?InhibitDelayMaxSec=.*/InhibitDelayMaxSec=3/" /etc/systemd/logind.conf && sudo systemctl restart systemd-logind'
  logind_delay_ok=0
  for _ in {1..30}; do
    if guest_ssh 'busctl get-property org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager InhibitDelayMaxUSec' >"$work_dir/budget-logind-delay.txt" 2>/dev/null &&
      awk '$1 == "t" && ($2 == 3000000 || $2 == 5000000) { found = 1 } END { exit !found }' "$work_dir/budget-logind-delay.txt"; then
      logind_delay_ok=1
      break
    fi
    sleep 0.5
  done
  [[ $logind_delay_ok == 1 ]]
  awk '$1 == "t" && $2 > 0 { valid = 1 } END { exit !valid }' "$work_dir/budget-logind-delay.txt"
  start_daemon \
    sesh-daemon-budget \
    /var/lib/sesh-guest/budget-repository \
    /var/lib/sesh-guest/budget-history.json \
    /var/lib/sesh-guest/budget-final-status.json \
    sesh-tmux-budget.service
  assert_daemon_inhibitors sesh-daemon-budget.service "$work_dir/inhibitor-before-budget-reboot.txt" >"$work_dir/budget-daemon-pid.txt"
  guest_ssh 'sudo systemctl stop unattended-upgrades.service'
  guest_ssh 'group=$(systemctl show --property=ControlGroup --value sesh-tmux-budget.service); test -n "$group"; systemctl show --property=MainPID --property=ControlGroup sesh-tmux-budget.service; sudo systemd-cgls --no-pager "$group"; sudo systemctl kill --kill-whom=all --signal=STOP sesh-tmux-budget.service' >"$work_dir/budget-stalled-cgroup.txt"
  budget_boot_before=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id')
  printf '%s\n' "$budget_boot_before" >"$work_dir/budget-boot-before.txt"
  set +e
  guest_ssh 'sudo systemctl reboot' >"$work_dir/budget-reboot-request.txt" 2>&1
  set -e
  budget_boot_after=
  for _ in {1..180}; do
    candidate=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null || true)
    if [[ -n $candidate && $candidate != "$budget_boot_before" ]]; then
      budget_boot_after=$candidate
      break
    fi
    sleep 2
  done
  test -n "$budget_boot_after"
  printf '%s\n' "$budget_boot_after" >"$work_dir/budget-boot-after.txt"
  guest_ssh 'test -f /var/lib/sesh-guest/budget-final-status.json'
  guest_ssh 'cat /var/lib/sesh-guest/budget-final-status.json' >"$work_dir/budget-final-status.json"
  cp "$work_dir"/{budget-boot-before.txt,budget-boot-after.txt,budget-daemon-pid.txt,budget-final-status.json,budget-logind-delay.txt,budget-seed-generation.txt,budget-stalled-cgroup.txt} "$evidence_dir/"
  jq -e '(.result == "failed" or .result == "incomplete" or .result == "uncertain") and .result != "saved" and (.reason | length > 0) and .incomplete == true and .bookkeepingComplete == false and .durationMs >= 2000 and .durationMs <= 9000' "$work_dir/budget-final-status.json" >/dev/null
  guest_ssh "/opt/sesh --no-config snapshot verify --repository /var/lib/sesh-guest/budget-repository --generation $budget_generation --json" >"$work_dir/budget-seed-verify.json"
  cp "$work_dir/budget-seed-verify.json" "$evidence_dir/"
  jq -e --arg generation "$budget_generation" '.ok == true and .result.generation == $generation and .result.byteIntegrity == true' "$work_dir/budget-seed-verify.json" >/dev/null
  guest_ssh 'sudo journalctl -b -1 -u sesh-daemon-budget.service -u sesh-tmux-budget.service -u systemd-logind.service --no-pager --output=short-iso' >"$work_dir/budget-journal.txt"
  grep -q 'Delay lock is active .* but inhibitor timeout is reached' "$work_dir/budget-journal.txt"

  # ENOSPC isolation: seed repository on a loopback mount, fill filesystem completely,
  # verify that save fails gracefully without destroying the committed prior generation.
  echo 'extended phase: enospc repository save'
  ensure_tmux_ready
  guest_ssh 'sudo dd if=/dev/zero of=/var/lib/sesh-guest/enospc.img bs=1M count=10 && sudo mkfs.ext4 -F /var/lib/sesh-guest/enospc.img && sudo mkdir -p /var/lib/sesh-guest/enospc-mnt && sudo mount -o loop /var/lib/sesh-guest/enospc.img /var/lib/sesh-guest/enospc-mnt && sudo chown -R sesh:sesh /var/lib/sesh-guest/enospc-mnt'
  if guest_ssh '/opt/sesh --no-config autosave final-save --socket /run/sesh-guest/tmux.sock --repository /var/lib/sesh-guest/enospc-mnt/repo --lease-root /var/lib/sesh-guest/leases --history /var/lib/sesh-guest/enospc-mnt/seed-history.json --status-file /var/lib/sesh-guest/enospc-mnt/seed-status.json --deadline 5s --json' >"$work_dir/enospc-seed.json"; then
    enospc_seed_exit=0
  else
    enospc_seed_exit=$?
  fi
  [[ $enospc_seed_exit == 0 || $enospc_seed_exit == 1 ]]
  enospc_prior_gen=$(guest_ssh '/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/enospc-mnt/repo --json | jq -er .result.current')
  test -n "$enospc_prior_gen"
  printf '%s\n' "$enospc_prior_gen" >"$work_dir/enospc-seed-generation.txt"

  guest_ssh "/opt/sesh --no-config snapshot verify --repository /var/lib/sesh-guest/enospc-mnt/repo --generation $enospc_prior_gen --json" >"$work_dir/enospc-prior-verify-before.json"
  jq -e --arg gen "$enospc_prior_gen" '.ok == true and .result.generation == $gen and .result.byteIntegrity == true' "$work_dir/enospc-prior-verify-before.json" >/dev/null

  # Fill the mount completely
  guest_ssh 'dd if=/dev/zero of=/var/lib/sesh-guest/enospc-mnt/filler bs=1M count=20 2>/dev/null || true; sync'
  guest_ssh 'df -h /var/lib/sesh-guest/enospc-mnt' >"$work_dir/enospc-df.txt"

  enospc_save_exit=0
  guest_ssh '/opt/sesh --no-config autosave final-save --socket /run/sesh-guest/tmux.sock --repository /var/lib/sesh-guest/enospc-mnt/repo --lease-root /var/lib/sesh-guest/leases --history /var/lib/sesh-guest/failed-history.json --status-file /var/lib/sesh-guest/enospc-failed-status.json --deadline 5s --json' >"$work_dir/enospc-failed-output.json" 2>&1 || enospc_save_exit=$?
  test $enospc_save_exit -ne 0 || jq -e '.ok == false or .result.status != "saved"' "$work_dir/enospc-failed-output.json" >/dev/null

  guest_ssh 'rm -f /var/lib/sesh-guest/enospc-mnt/filler; sync'

  guest_ssh "/opt/sesh --no-config snapshot verify --repository /var/lib/sesh-guest/enospc-mnt/repo --generation $enospc_prior_gen --json" >"$work_dir/enospc-prior-verify-after.json"
  jq -e --arg gen "$enospc_prior_gen" '.ok == true and .result.generation == $gen and .result.byteIntegrity == true' "$work_dir/enospc-prior-verify-after.json" >/dev/null

  guest_ssh "/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/enospc-mnt/repo --json" >"$work_dir/enospc-prior-history-after.json"
  jq -e --arg gen "$enospc_prior_gen" '.ok == true and .result.current == $gen' "$work_dir/enospc-prior-history-after.json" >/dev/null

  guest_ssh 'sudo umount /var/lib/sesh-guest/enospc-mnt && rm -rf /var/lib/sesh-guest/enospc-mnt /var/lib/sesh-guest/enospc.img'

  # User-manager teardown and linger: test user service ExecStop during user@1000 stop,
  # and verify linger enabled/disabled states.
  echo 'extended phase: user-manager teardown and linger'
  ensure_tmux_ready
  guest_ssh 'loginctl show-user sesh -p Linger' >"$work_dir/linger-before.txt"
  grep -q 'Linger=no' "$work_dir/linger-before.txt"

  guest_ssh 'mkdir -p /home/sesh/.config/systemd/user'
  guest_ssh 'cat >/home/sesh/.config/systemd/user/sesh-user-test.service <<EOF
[Unit]
Description=sesh user teardown test
[Service]
Type=exec
ExecStart=/bin/sleep 300
ExecStop=/opt/sesh --no-config autosave final-save --socket /run/sesh-guest/tmux.sock --repository /var/lib/sesh-guest/user-teardown-repo --lease-root /var/lib/sesh-guest/leases --history /var/lib/sesh-guest/user-teardown-history.json --status-file /var/lib/sesh-guest/user-teardown-status.json --deadline 5s --json
TimeoutStopSec=10s
EOF'
  guest_ssh 'systemctl --user daemon-reload'
  guest_ssh 'systemctl --user start sesh-user-test.service'
  guest_ssh 'systemctl --user is-active sesh-user-test.service' >"$work_dir/user-service-active.txt"
  grep -qx 'active' "$work_dir/user-service-active.txt"

  guest_ssh 'sudo systemctl stop user@1000.service'
  guest_ssh 'sudo journalctl -u user@1000.service --no-pager --output=short-iso' >"$work_dir/user-manager-teardown-journal.txt"
  guest_ssh 'test -f /var/lib/sesh-guest/user-teardown-status.json'
  guest_ssh 'cat /var/lib/sesh-guest/user-teardown-status.json' >"$work_dir/user-teardown-status.json"

  guest_ssh 'sudo loginctl enable-linger sesh'
  guest_ssh 'loginctl show-user sesh -p Linger' >"$work_dir/linger-enabled.txt"
  grep -q 'Linger=yes' "$work_dir/linger-enabled.txt"
  guest_ssh 'sudo systemctl start user@1000.service'
  guest_ssh 'systemctl is-active user@1000.service' >"$work_dir/user-manager-linger-active.txt"
  grep -qx 'active' "$work_dir/user-manager-linger-active.txt"
  guest_ssh 'sudo loginctl disable-linger sesh'
  guest_ssh 'loginctl show-user sesh -p Linger' >"$work_dir/linger-after.txt"
  grep -q 'Linger=no' "$work_dir/linger-after.txt"
  guest_ssh 'rm -f /home/sesh/.config/systemd/user/sesh-user-test.service'

  # Abrupt guest power cut: commit a generation to disk, flush/sync, then kill QEMU abruptly
  # with SIGKILL (power cut). Restart QEMU with the same drive, verify filesystem recovery
  # and verify that the committed generation survived intact.
  echo 'extended phase: abrupt guest power cut'
  ensure_tmux_ready
  if guest_ssh '/opt/sesh --no-config autosave final-save --socket /run/sesh-guest/tmux.sock --repository /var/lib/sesh-guest/powercut-repository --lease-root /var/lib/sesh-guest/leases --history /var/lib/sesh-guest/powercut-history.json --status-file /var/lib/sesh-guest/powercut-status.json --deadline 5s --json' >"$work_dir/powercut-seed.json"; then
    powercut_seed_exit=0
  else
    powercut_seed_exit=$?
  fi
  [[ $powercut_seed_exit == 0 || $powercut_seed_exit == 1 ]]
  powercut_gen=$(guest_ssh '/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/powercut-repository --json | jq -er .result.current')
  test -n "$powercut_gen"
  printf '%s\n' "$powercut_gen" >"$work_dir/powercut-generation.txt"

  guest_ssh 'sync'
  powercut_boot_before=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id')
  printf '%s\n' "$powercut_boot_before" >"$work_dir/powercut-boot-before.txt"

  # Host-side immediate power cut
  kill -KILL "$qemu_pid"
  for _ in {1..50}; do
    kill -0 "$qemu_pid" 2>/dev/null || break
    sleep 0.1
  done
  wait "$qemu_pid" 2>/dev/null || true
  qemu_pid=
  rm -f "$work_dir/qmp.sock"

  # Restart guest with identical drive
  qemu-system-x86_64 \
    -enable-kvm -cpu host -smp 2 -m 2048 \
    -display none -monitor none -serial "file:$work_dir/console.log" \
    -qmp "unix:$work_dir/qmp.sock,server=on,wait=off" \
    -drive "file=$work_dir/guest.qcow2,if=virtio,format=qcow2" \
    -drive "file=$work_dir/seed.img,if=virtio,format=raw,readonly=on" \
    -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22" \
    -device virtio-net-pci,netdev=net0 \
    >>"$work_dir/qemu.log" 2>&1 &
  qemu_pid=$!
  printf '%s\n' "$qemu_pid" >"$work_dir/qemu.pid"

  powercut_boot_after=
  for _ in {1..180}; do
    candidate=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null || true)
    if [[ -n $candidate && $candidate != "$powercut_boot_before" ]]; then
      powercut_boot_after=$candidate
      break
    fi
    sleep 2
  done
  test -n "$powercut_boot_after"
  printf '%s\n' "$powercut_boot_after" >"$work_dir/powercut-boot-after.txt"

  guest_ssh 'sudo dmesg | grep -iE "ext4|recovery|journal"' >"$work_dir/powercut-dmesg.txt" || true
  guest_ssh "/opt/sesh --no-config snapshot verify --repository /var/lib/sesh-guest/powercut-repository --generation $powercut_gen --json" >"$work_dir/powercut-verify.json"
  jq -e --arg gen "$powercut_gen" '.ok == true and .result.generation == $gen and .result.byteIntegrity == true' "$work_dir/powercut-verify.json" >/dev/null
  guest_ssh '/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/powercut-repository --json' >"$work_dir/powercut-history.json"
  jq -e --arg gen "$powercut_gen" '.ok == true and .result.current == $gen' "$work_dir/powercut-history.json" >/dev/null
  guest_ssh "/opt/sesh --no-config snapshot list --repository /var/lib/sesh-guest/powercut-repository --generation $powercut_gen --json" >"$work_dir/powercut-list.json"
  jq -e --arg gen "$powercut_gen" '.ok == true and .result.generation == $gen and (.result.sessions | length) >= 1' "$work_dir/powercut-list.json" >/dev/null
fi

echo 'phase: clean reboot and practical application restore'
ensure_tmux_ready
guest_ssh 'rm -f /var/lib/sesh-guest/final-save-status.json'
start_daemon \
  sesh-daemon \
  /var/lib/sesh-guest/repository \
  /var/lib/sesh-guest/history.json \
  /var/lib/sesh-guest/final-save-status.json
daemon_pid=$(assert_daemon_inhibitors sesh-daemon.service "$work_dir/inhibitor-before.txt")
guest_ssh 'systemctl is-active sesh-tmux.service sesh-daemon.service'

boot_before=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id')
printf '%s\n' "$boot_before" >"$work_dir/boot-before.txt"

set +e
guest_ssh 'sudo systemctl reboot' >"$work_dir/reboot-request.txt" 2>&1
set -e

boot_after=
for _ in {1..180}; do
  candidate=$(guest_ssh 'cat /proc/sys/kernel/random/boot_id' 2>/dev/null || true)
  if [[ -n $candidate && $candidate != "$boot_before" ]]; then
    boot_after=$candidate
    break
  fi
  sleep 2
done
if [[ -z $boot_after ]]; then
  echo 'guest did not complete an independently observed reboot' >&2
  exit 1
fi
printf '%s\n' "$boot_after" >"$work_dir/boot-after.txt"

guest_ssh 'test -f /var/lib/sesh-guest/final-save-status.json'
guest_ssh "jq -e '(.result == \"saved\" or .result == \"incomplete\") and .recorded == true and (.snapshotStatus == \"best_effort\" or .snapshotStatus == \"partial\") and (.generation | length > 0)' /var/lib/sesh-guest/final-save-status.json"
guest_ssh 'test "$(stat -c "%a %U %G" /var/lib/sesh-guest/final-save-status.json)" = "600 sesh sesh"'
guest_ssh 'stat -c "%a %U %G %s" /var/lib/sesh-guest/final-save-status.json' >"$work_dir/status-stat.txt"
guest_ssh 'cat /var/lib/sesh-guest/final-save-status.json' >"$work_dir/final-save-status.json"
guest_ssh '/opt/sesh --no-config snapshot verify --repository /var/lib/sesh-guest/repository --generation "$(jq -r .generation /var/lib/sesh-guest/final-save-status.json)" --json' >"$work_dir/snapshot-verify.json"
guest_ssh '/opt/sesh --no-config snapshot history --repository /var/lib/sesh-guest/repository --json' >"$work_dir/snapshot-history.json"
guest_ssh '/opt/sesh --no-config snapshot list --repository /var/lib/sesh-guest/repository --generation "$(jq -r .generation /var/lib/sesh-guest/final-save-status.json)" --json' >"$work_dir/snapshot-list.json"
expected_generation=$(jq -r '.generation' "$work_dir/final-save-status.json")
jq -e --arg generation "$expected_generation" '.ok == true and .result.generation == $generation and .result.byteIntegrity == true' "$work_dir/snapshot-verify.json" >/dev/null
jq -e --arg generation "$expected_generation" '.ok == true and ([.result.generations[].id] | index($generation) != null)' "$work_dir/snapshot-history.json" >/dev/null
jq -e --arg generation "$expected_generation" '.ok == true and .result.generation == $generation and (.result.sessions | length) == 1 and .result.sessions[0].name == "guest-lifecycle-fixture"' "$work_dir/snapshot-list.json" >/dev/null
guest_ssh 'sudo journalctl -b -1 -u sesh-daemon.service -u sesh-tmux.service --no-pager --output=short-iso' >"$work_dir/guest-journal.txt"

echo 'phase: postboot practical application restore'
guest_ssh 'sudo systemd-run --unit=sesh-tmux-restore --property=User=sesh --property=Group=sesh --property=RuntimeDirectory=sesh-guest --property=RuntimeDirectoryMode=0700 --property=RuntimeDirectoryPreserve=yes --setenv=HOME=/home/sesh --setenv=PATH=/opt/tmux-3.7c/bin:/usr/local/bin:/usr/bin:/bin --setenv=XDG_RUNTIME_DIR=/run/sesh-guest /opt/sesh --no-config daemon tmux --socket /run/sesh-guest/restore-tmux.sock --tmux /opt/tmux-3.7c/bin/tmux'
guest_ssh '/opt/sesh --no-config daemon wait-ready --socket /run/sesh-guest/restore-tmux.sock --timeout 20s'

guest_ssh "/opt/sesh --no-config snapshot restore --socket /run/sesh-guest/restore-tmux.sock --repository /var/lib/sesh-guest/repository --lease-root /var/lib/sesh-guest/leases --generation $expected_generation --json" >"$work_dir/postboot-restore.json"
jq -e '.ok == true and (.result.status == "best_effort" or .result.status == "exact")' "$work_dir/postboot-restore.json" >/dev/null

  # Verify restored windows and panes through gotmux in fixture (zero direct tmux subprocess calls)
  guest_ssh "/opt/sesh-fixture verify /run/sesh-guest/restore-tmux.sock guest-lifecycle-fixture /home/sesh/fixture-alpha.txt /home/sesh/fixture-beta.txt" >"$work_dir/postboot-fixture-verify.txt"
  awk '/--- WINDOWS ---/{flag=1;next}/--- PANES ---/{flag=0}flag' "$work_dir/postboot-fixture-verify.txt" >"$work_dir/postboot-windows.txt"
  awk '/--- PANES ---/{flag=1;next}flag' "$work_dir/postboot-fixture-verify.txt" >"$work_dir/postboot-panes.txt"
  guest_ssh 'ps -u sesh -o pid,ppid,comm,args' >"$work_dir/postboot-processes.txt"
  guest_ssh 'pgrep -a nvim' >"$work_dir/postboot-nvim.txt" || true
  guest_ssh 'pgrep -a top' >"$work_dir/postboot-top.txt" || true

  grep -q 'fixture-alpha.txt' "$work_dir/postboot-nvim.txt"
  grep -q 'fixture-beta.txt' "$work_dir/postboot-nvim.txt"
  grep -q 'top' "$work_dir/postboot-top.txt"
  grep -q 'editor' "$work_dir/postboot-windows.txt"
  grep -q 'monitor' "$work_dir/postboot-windows.txt"
cp "$work_dir"/{boot-before.txt,boot-after.txt,inhibitor-before.txt,final-save-status.json,status-stat.txt,snapshot-verify.json,snapshot-history.json,snapshot-list.json,guest-journal.txt,console.log,sesh-binary.sha256,fixture-binary.sha256,git-head.txt,git-status.txt} "$evidence_dir/"
[[ -f $work_dir/postboot-restore.json ]] && cp "$work_dir"/postboot-* "$evidence_dir/"
if [[ ${SESH_GUEST_LIFECYCLE_SUITE:-} == extended ]]; then
  cp "$work_dir"/{daemon-runtime-before.json,inhibitor-after-cancel.txt,canceled-shutdown-call.txt,canceled-shutdown-journal.txt,suspend-capability.txt,suspend-journal.txt,explicit-stop-journal.txt,inhibitor-before-owner-loss.txt,logind-owner-loss-journal.txt,logind-owner-loss-daemon-pid.txt,readonly-boot-before.txt,readonly-boot-after.txt,readonly-seed-generation.txt,readonly-final-status.json,readonly-seed-verify.json,readonly-journal.txt,budget-boot-before.txt,budget-boot-after.txt,budget-daemon-pid.txt,budget-logind-delay.txt,budget-stalled-cgroup.txt,budget-seed-generation.txt,budget-seed-verify.json,budget-journal.txt,budget-final-status.json} "$evidence_dir/" 2>/dev/null || true
  [[ -f $work_dir/inhibitor-after-resume.txt ]] && cp "$work_dir/inhibitor-after-resume.txt" "$evidence_dir/"
  [[ -f $work_dir/qmp-wakeup.json ]] && cp "$work_dir/qmp-wakeup.json" "$evidence_dir/"
  [[ -f $work_dir/enospc-df.txt ]] && cp "$work_dir"/enospc-* "$evidence_dir/" 2>/dev/null || true
  [[ -f $work_dir/linger-before.txt ]] && cp "$work_dir"/{linger-*,user-*} "$evidence_dir/" 2>/dev/null || true
  [[ -f $work_dir/powercut-boot-before.txt ]] && cp "$work_dir"/powercut-* "$evidence_dir/" 2>/dev/null || true
fi

guest_ssh 'sudo systemctl poweroff' >/dev/null 2>&1 || true
for _ in {1..100}; do
  kill -0 "$qemu_pid" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$qemu_pid" 2>/dev/null; then
  echo 'guest did not power off within the cleanup budget' >&2
  exit 1
fi
if ! wait "$qemu_pid"; then
  echo 'QEMU exited abnormally after the guest poweroff request' >&2
  exit 1
fi
qemu_pid=

echo "guest lifecycle evidence: $evidence_dir"
