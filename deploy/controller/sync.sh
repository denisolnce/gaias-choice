#!/usr/bin/env bash
# BOOTSTRAP / REPAIR of the doco-cd controller (the daemon itself) on the VM.
# Run via `task doco:sync`. Since 2026-10-08 the daemon manages its own stack from
# git (the `doco-cd` document in .doco-cd.yml + SELF_UPDATE_ENABLED), so this is
# for a box with NO controller or a broken one. While the git-managed daemon runs
# it REFUSES: scp + `up -d` would recreate the container from the LOCAL tree and
# the next poll would hand over again from git. FORCE=1 overrides, knowing that.
# Non-secret files only — secrets.env stays VM-only. The app stack needs NO sync.
#
# Overridable via env: VM_HOST, VM_PORT, VM_KEY.
set -euo pipefail

VM_PORT="${VM_PORT:-13337}"
VM_HOST="${VM_HOST:-root@gaias-choice.gardenofatlantis.com}"
VM_KEY="${VM_KEY:-$HOME/.ssh/gaia}"
dir="$(cd "$(dirname "$0")" && pwd)"

if [ "${FORCE:-0}" != "1" ]; then
  managed=$(ssh -p "$VM_PORT" -i "$VM_KEY" "$VM_HOST" \
    'docker ps -q --filter label=com.docker.compose.project=doco-cd --filter label=com.docker.compose.service=doco-cd --filter label=cd.doco.deployment.name=doco-cd' 2>/dev/null || true)
  if [ -n "$managed" ]; then
    echo "✗ $VM_HOST runs a GIT-MANAGED doco-cd ($managed): change deploy/controller/compose.yaml on main instead. FORCE=1 to override." >&2
    exit 1
  fi
fi

echo "→ scp controller config to $VM_HOST:/opt/doco-cd/"
scp -P "$VM_PORT" -i "$VM_KEY" "$dir/compose.yaml" "$dir/poll.yaml" "$VM_HOST:/opt/doco-cd/"

echo "→ reload daemon (docker compose up -d)"
ssh -p "$VM_PORT" -i "$VM_KEY" "$VM_HOST" 'cd /opt/doco-cd && docker compose up -d'

echo "✓ controller synced + reloaded (secrets.env untouched, VM-only)"
