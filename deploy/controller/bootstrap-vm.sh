#!/usr/bin/env bash
# Provision a fresh VM to run the Gaia's Choice backend via doco-cd GitOps.
# Idempotent — safe to re-run. Designed to double as OpenTofu/Terraform server
# userdata (runs as root on first boot). Mirrors deploy/infra-log.md "VM base
# setup"; the Hetzner edge firewall + SSH hardening are NOT here (they'll be
# codified in OpenTofu). AlmaLinux/RHEL family (dnf) — adjust for other distros.
#
# It does NOT hold secrets: it seeds /opt/doco-cd/secrets.env from the committed
# example (empty values). Fill that in (or have userdata write it) before the
# daemon can deploy the app stack.
#
#   Usage (manual):   curl -fsSL <raw>/deploy/controller/bootstrap-vm.sh | bash
#   Override:         REF=<branch> REPO_RAW=<host> bash bootstrap-vm.sh
set -euo pipefail

REPO_RAW="${REPO_RAW:-https://raw.githubusercontent.com/denisolnce/gaias-choice}"
REF="${REF:-main}"
CTRL_DIR=/opt/doco-cd
CTRL_URL="$REPO_RAW/$REF/deploy/controller"

echo "==> 1/4  Docker CE + compose plugin"
if command -v docker >/dev/null 2>&1; then
  echo "    docker present — skipping install"
else
  dnf -y install dnf-plugins-core
  dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
  dnf -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
fi

echo "==> 2/4  host bind-mount dirs (/srv — boring backups, decision D9)"
mkdir -p /srv/gaias-choice/{data,caddy,backups}

echo "==> 3/4  doco-cd controller config → $CTRL_DIR"
mkdir -p "$CTRL_DIR"
for f in compose.yaml poll.yaml; do
  curl -fsSL "$CTRL_URL/$f" -o "$CTRL_DIR/$f"
done

echo "==> 4/4  secrets + daemon"
if [ ! -f "$CTRL_DIR/secrets.env" ]; then
  curl -fsSL "$CTRL_URL/secrets.env.example" -o "$CTRL_DIR/secrets.env"
  chmod 600 "$CTRL_DIR/secrets.env"
  echo
  echo "    !! wrote $CTRL_DIR/secrets.env from the template — FILL IN real values,"
  echo "       then: cd $CTRL_DIR && docker compose up -d"
  echo "       (skipping 'up' now: empty secrets would crash-loop the api)"
  exit 0
fi
chmod 600 "$CTRL_DIR/secrets.env"
cd "$CTRL_DIR" && docker compose up -d
echo "==> done — doco-cd is polling; it deploys the app stack from git within ~30s."
