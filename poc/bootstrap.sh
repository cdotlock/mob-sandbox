#!/bin/bash
# Phase 1: Bootstrap VM — run once after first SSH
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "=== [1/4] System update ==="
apt-get update -qq
apt-get upgrade -y -qq

echo "=== [2/4] Install Docker CE ==="
apt-get install -y -qq ca-certificates curl gnupg lsb-release ufw
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update -qq
apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin
systemctl enable --now docker

echo "=== [3/4] Configure firewall ==="
ufw --force reset
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment 'SSH'
ufw allow 80/tcp comment 'HTTP (LE challenge + redirect)'
ufw allow 443/tcp comment 'HTTPS'
ufw allow 2222/tcp comment 'Daytona SSH Gateway'
ufw --force enable

echo "=== [4/4] Create Docker networks ==="
docker network create edge 2>/dev/null || true
docker network create daytona 2>/dev/null || true

echo ""
echo "Bootstrap complete."
docker --version
docker compose version
ufw status verbose
