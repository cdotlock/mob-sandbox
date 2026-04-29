#!/usr/bin/env bash
# destroy.sh — tear down mob-sandbox-platform from the VM
# Run from the repo root: bash poc/destroy.sh
# WARNING: destroys all data including volumes.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

[[ -f "$ENV_FILE" ]] || { echo "ERROR: .env not found" >&2; exit 1; }
# shellcheck source=/dev/null
source "$ENV_FILE"

SSH_KEY="${SSH_KEY_PATH:-$HOME/.ssh/poc_ed25519}"

ssh_vm() {
  ssh -o KexAlgorithms=curve25519-sha256 \
      -o StrictHostKeyChecking=no \
      -i "$SSH_KEY" \
      "root@$VM_IP" "$@"
}

echo "WARNING: This will destroy ALL containers and volumes on $VM_IP."
read -r -p "Type 'destroy' to confirm: " CONFIRM
[[ "$CONFIRM" == "destroy" ]] || { echo "Aborted."; exit 0; }

echo ">>> Stopping and removing all containers ..."
ssh_vm bash <<'REMOTE'
cd /opt/poc

docker compose -f docker-compose.openhands.yml down -v 2>/dev/null || true
docker compose -f docker-compose.daytona.yml   down -v 2>/dev/null || true
docker compose -f docker-compose.traefik.yml   down -v 2>/dev/null || true

# Remove sandbox containers left by Daytona runner
docker ps -aq | xargs docker rm -f 2>/dev/null || true

# Remove images built locally
docker rmi mob-sandbox:1.0 2>/dev/null || true
docker rmi registry:6000/mob-sandbox:1.0 2>/dev/null || true

# Disable toolbox extraction service
systemctl disable --now daytona-toolbox-extract.service 2>/dev/null || true
rm -f /etc/systemd/system/daytona-toolbox-extract.service

# Clean up hosts entry
sed -i '/[[:space:]]registry$/d' /etc/hosts

echo "All services stopped and volumes removed."
REMOTE

echo ""
echo "Done. VM services destroyed."
echo "DNS records and the VM itself are not deleted — do those manually if needed."
