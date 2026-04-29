#!/usr/bin/env bash
# deploy.sh — full one-shot deploy of mob-sandbox-platform
# Run from the repo root: bash poc/deploy.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POC="$REPO_ROOT/poc"
ENV_FILE="$REPO_ROOT/.env"

# ── helpers ───────────────────────────────────────────────────────────────────
die()  { echo "ERROR: $*" >&2; exit 1; }
info() { echo ">>> $*"; }
ok()   { echo "    ✓ $*"; }

ssh_vm() {
  ssh -o KexAlgorithms=curve25519-sha256 \
      -o StrictHostKeyChecking=no \
      -o ConnectTimeout=10 \
      -i "$SSH_KEY" \
      "root@$VM_IP" "$@"
}

scp_to_vm() {
  scp -q -r \
      -o KexAlgorithms=curve25519-sha256 \
      -o StrictHostKeyChecking=no \
      -i "$SSH_KEY" \
      "$1" "root@$VM_IP:$2"
}

wait_http() {
  local url="$1" max="${2:-120}" elapsed=0
  info "Waiting for $url ..."
  until curl -sf --max-time 5 -o /dev/null "$url" 2>/dev/null; do
    sleep 5; elapsed=$((elapsed + 5))
    [[ $elapsed -ge $max ]] && die "Timed out waiting for $url"
    echo -n "."
  done
  echo " up"
}

# ── env & validation ─────────────────────────────────────────────────────────
[[ -f "$ENV_FILE" ]] || die ".env not found at $ENV_FILE. Copy .env.example and fill it in."
# shellcheck source=/dev/null
source "$ENV_FILE"

for v in VM_IP DOMAIN ACME_EMAIL PORKBUN_API_KEY PORKBUN_SECRET_KEY LLM_API_KEY; do
  [[ -n "${!v:-}" ]] || die "$v is not set in .env"
done

SSH_KEY="${SSH_KEY_PATH:-$HOME/.ssh/poc_ed25519}"
[[ -f "$SSH_KEY" ]] || {
  info "Generating deploy SSH key at $SSH_KEY ..."
  ssh-keygen -t ed25519 -f "$SSH_KEY" -N "" -C "poc-deploy"
  ok "SSH key generated. Add the public key to your Vultr instance before proceeding."
  echo "  Public key: $(cat "${SSH_KEY}.pub")"
  exit 0
}

SANDBOX_VERSION="${SANDBOX_VERSION:-1.0}"
SANDBOX_IMAGE="mob-sandbox:$SANDBOX_VERSION"
REGISTRY_IMAGE="registry:6000/mob-sandbox:$SANDBOX_VERSION"

# ── step 0: DNS ───────────────────────────────────────────────────────────────
info "[0/8] Setting up DNS ..."
PORKBUN_API_KEY="$PORKBUN_API_KEY" \
PORKBUN_SECRET_KEY="$PORKBUN_SECRET_KEY" \
DOMAIN="$DOMAIN" \
VM_IP="$VM_IP" \
  bash "$POC/setup-dns.sh"
ok "DNS records created"

# ── step 1: bootstrap VM ─────────────────────────────────────────────────────
info "[1/8] Bootstrapping VM ..."
ssh_vm "bash -s" < "$POC/bootstrap.sh"
ok "VM bootstrapped"

# ── step 2: upload project files ─────────────────────────────────────────────
info "[2/8] Uploading project files ..."
ssh_vm "rm -rf /opt/poc && mkdir -p /opt/poc"
scp_to_vm "$POC/." "/opt/poc/"
ok "Files uploaded to /opt/poc"

# ── step 3: generate SSH keys + API key, write compose ───────────────────────
info "[3/8] Generating Daytona SSH keys and API key ..."

# Generate everything on the VM, then substitute into compose
ssh_vm bash <<'REMOTE'
set -euo pipefail

cd /opt/poc

# Generate gateway and host keypairs
ssh-keygen -t rsa -b 4096 -f /tmp/daytona-gateway -N "" -C "daytona-gateway" -q
ssh-keygen -t rsa -b 4096 -f /tmp/daytona-host    -N "" -C "daytona-host"    -q

GW_PRIV_B64=$(base64 -w0 /tmp/daytona-gateway)
GW_PUB_B64=$(base64 -w0 /tmp/daytona-gateway.pub)
HOST_KEY_B64=$(base64 -w0 /tmp/daytona-host)

rm -f /tmp/daytona-gateway /tmp/daytona-gateway.pub /tmp/daytona-host /tmp/daytona-host.pub

python3 - "$GW_PRIV_B64" "$GW_PUB_B64" "$HOST_KEY_B64" <<'PY'
import sys, re

gw_priv, gw_pub, host_key = sys.argv[1], sys.argv[2], sys.argv[3]

with open('/opt/poc/docker-compose.daytona.yml') as f:
    c = f.read()

# Replace placeholder base64 blobs — any run of base64 chars after the key name
def sub(name, value, text):
    return re.sub(
        rf'(- {name}=)[A-Za-z0-9+/=]+',
        rf'\g<1>{value}',
        text
    )

c = sub('SSH_PRIVATE_KEY', gw_priv, c)
c = sub('SSH_HOST_KEY',    host_key, c)
c = sub('SSH_PUBLIC_KEY',  gw_pub,   c)
c = sub('SSH_GATEWAY_PUBLIC_KEY', gw_pub, c)

with open('/opt/poc/docker-compose.daytona.yml', 'w') as f:
    f.write(c)
PY

echo "SSH keys injected."
REMOTE

# Generate API key locally, store in .env, will be inserted into DB in step 6
if [[ -z "${DAYTONA_API_KEY:-}" ]]; then
  DAYTONA_API_KEY="poc-$(openssl rand -hex 20)"
  # Persist it
  if grep -q '^DAYTONA_API_KEY=' "$ENV_FILE"; then
    sed -i.bak "s|^DAYTONA_API_KEY=.*|DAYTONA_API_KEY=$DAYTONA_API_KEY|" "$ENV_FILE"
  else
    echo "DAYTONA_API_KEY=$DAYTONA_API_KEY" >> "$ENV_FILE"
  fi
fi
DAYTONA_API_KEY_HASH=$(echo -n "$DAYTONA_API_KEY" | sha256sum | awk '{print $1}')
ok "API key ready: ${DAYTONA_API_KEY:0:20}..."

# ── step 4: configure Docker daemon ──────────────────────────────────────────
info "[4/8] Configuring Docker daemon for insecure registry ..."
ssh_vm bash <<'REMOTE'
cat > /etc/docker/daemon.json <<'JSON'
{
  "insecure-registries": ["registry:6000"]
}
JSON
systemctl reload docker || systemctl restart docker
sleep 3
REMOTE
ok "Docker daemon configured"

# ── step 5: deploy Traefik ────────────────────────────────────────────────────
info "[5/8] Deploying Traefik ..."
ssh_vm bash <<REMOTE
set -euo pipefail
cd /opt/poc
mkdir -p /etc/traefik/dynamic
cp traefik-dynamic/routes.yml /etc/traefik/dynamic/routes.yml

# Replace domain placeholders
sed -i 's/mobai\.beauty/${DOMAIN}/g' /etc/traefik/dynamic/routes.yml

ACME_EMAIL="${ACME_EMAIL}" \
PORKBUN_API_KEY="${PORKBUN_API_KEY}" \
PORKBUN_SECRET_KEY="${PORKBUN_SECRET_KEY}" \
  docker compose -f docker-compose.traefik.yml up -d
REMOTE
ok "Traefik deployed"

# Wait for Traefik to be routing (HTTPS might take ~60s for cert)
sleep 10

# ── step 6: deploy Daytona ────────────────────────────────────────────────────
info "[6/8] Deploying Daytona stack ..."
ssh_vm bash <<REMOTE
set -euo pipefail
cd /opt/poc

# Ensure runner-bridge network exists
docker network create runner-bridge 2>/dev/null || true

# Substitute the correct domain into compose files
sed -i 's/mobai\.beauty/${DOMAIN}/g' docker-compose.daytona.yml
sed -i 's/45\.32\.25\.73/${VM_IP}/g'  docker-compose.daytona.yml

# Update dex config domain
sed -i 's/mobai\.beauty/${DOMAIN}/g' daytona/dex/config.yaml

docker compose -f docker-compose.daytona.yml up -d
REMOTE
ok "Daytona stack started"

# ── step 6b: wait for Daytona API health ─────────────────────────────────────
info "Waiting for Daytona API to be healthy ..."
ssh_vm bash <<'REMOTE'
for i in $(seq 1 60); do
  STATUS=$(docker inspect --format='{{.State.Health.Status}}' daytona-api 2>/dev/null || echo "missing")
  echo "  attempt $i: $STATUS"
  if [[ "$STATUS" == "healthy" ]]; then
    echo "Daytona API healthy"
    exit 0
  fi
  sleep 5
done
echo "ERROR: Daytona API never became healthy" >&2
docker logs daytona-api --tail 30 >&2
exit 1
REMOTE
ok "Daytona API healthy"

# ── step 6c: extract toolbox binary ──────────────────────────────────────────
info "Extracting Daytona toolbox binary from runner ..."
ssh_vm bash <<'REMOTE'
set -euo pipefail
BINDIR=/usr/local/bin/.tmp/binaries
mkdir -p "$BINDIR"

# Extract daemon (toolbox) binary from inside runner container
docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daemon-amd64 \
  > "$BINDIR/daemon-amd64"
chmod +x "$BINDIR/daemon-amd64"

# Extract daytona-computer-use if present
docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daytona-computer-use \
  > "$BINDIR/daytona-computer-use" 2>/dev/null || true
[[ -f "$BINDIR/daytona-computer-use" ]] && chmod +x "$BINDIR/daytona-computer-use" || true

ls -lh "$BINDIR/"
REMOTE
ok "Toolbox binary extracted"

# Write a systemd oneshot to re-extract on reboots
ssh_vm bash <<'REMOTE'
cat > /etc/systemd/system/daytona-toolbox-extract.service <<'SVC'
[Unit]
Description=Extract Daytona toolbox binary from runner container
After=docker.service network.target
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/poc/ensure-toolbox.sh
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
SVC

cat > /opt/poc/ensure-toolbox.sh <<'SCRIPT'
#!/bin/bash
set -euo pipefail
BINDIR=/usr/local/bin/.tmp/binaries
mkdir -p "$BINDIR"

for attempt in $(seq 1 30); do
  if docker exec daytona-runner test -f /usr/local/bin/.tmp/binaries/daemon-amd64 2>/dev/null; then
    docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daemon-amd64 \
      > "$BINDIR/daemon-amd64"
    chmod +x "$BINDIR/daemon-amd64"
    docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daytona-computer-use \
      > "$BINDIR/daytona-computer-use" 2>/dev/null || true
    [[ -f "$BINDIR/daytona-computer-use" ]] && chmod +x "$BINDIR/daytona-computer-use" || true
    echo "Toolbox binary extracted successfully"
    exit 0
  fi
  echo "Attempt $attempt: runner not ready, waiting..."
  sleep 10
done
echo "Failed to extract toolbox binary" >&2
exit 1
SCRIPT
chmod +x /opt/poc/ensure-toolbox.sh

systemctl daemon-reload
systemctl enable daytona-toolbox-extract.service
REMOTE
ok "Toolbox extraction service installed"

# ── step 6d: configure registry /etc/hosts ───────────────────────────────────
info "Configuring registry DNS ..."
ssh_vm bash <<'REMOTE'
# Wait for registry to be up
for i in $(seq 1 30); do
  if docker inspect daytona-registry &>/dev/null; then break; fi
  sleep 2
done

REGISTRY_IP=$(docker inspect daytona-registry \
  --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' \
  | awk '{print $1}')

# Remove stale registry entry, add fresh
sed -i '/[[:space:]]registry$/d' /etc/hosts
echo "$REGISTRY_IP registry" >> /etc/hosts
echo "Registry: $REGISTRY_IP"
REMOTE
ok "Registry /etc/hosts configured"

# ── step 6e: insert Daytona API key into DB ───────────────────────────────────
info "Inserting Daytona API key ..."
ssh_vm bash <<REMOTE
set -euo pipefail
for i in \$(seq 1 30); do
  if docker exec daytona-db psql -U daytona -c '\q' &>/dev/null; then break; fi
  sleep 3
done

# Wait for migrations (user table must exist)
for i in \$(seq 1 30); do
  if docker exec daytona-db psql -U daytona -d daytona \
       -c 'SELECT 1 FROM "user" LIMIT 1' &>/dev/null; then
    break
  fi
  sleep 5
done

docker exec daytona-db psql -U daytona -d daytona <<'SQL'
DO \$\$
DECLARE
  org_id text;
  user_id text;
BEGIN
  SELECT id INTO org_id FROM organization LIMIT 1;
  SELECT id INTO user_id FROM "user" WHERE email = 'admin@mobai.beauty' LIMIT 1;

  IF org_id IS NULL OR user_id IS NULL THEN
    RAISE EXCEPTION 'org or user not found';
  END IF;

  INSERT INTO api_key (key, hash, name, "createdAt", "organizationId", "userId", type, permissions)
  VALUES (
    '${DAYTONA_API_KEY}',
    '${DAYTONA_API_KEY_HASH}',
    'poc-admin',
    NOW(),
    org_id,
    user_id,
    'personal',
    '{"read":true,"write":true,"delete":true,"exec":true}'::jsonb
  )
  ON CONFLICT (key) DO NOTHING;
END
\$\$;
SQL

echo "API key inserted."
REMOTE
ok "API key inserted"

# ── step 7: build and push sandbox image ─────────────────────────────────────
info "[7/8] Building sandbox image ..."
ssh_vm bash <<REMOTE
set -euo pipefail
cd /opt/poc/sandbox-image
docker build -t "${SANDBOX_IMAGE}" -t "${REGISTRY_IMAGE}" .
docker push "${REGISTRY_IMAGE}"
echo "Image pushed to internal registry."
REMOTE
ok "Sandbox image built and pushed"

# Register snapshot
info "Registering Daytona snapshot ..."
ssh_vm bash <<REMOTE
set -euo pipefail
API_KEY="${DAYTONA_API_KEY}"

# Wait for API to accept requests
for i in \$(seq 1 20); do
  RESP=\$(curl -sf -H "Authorization: Bearer \$API_KEY" \
    https://daytona.${DOMAIN}/api/sandbox 2>&1) && break
  sleep 5
done

SNAP=\$(curl -sf -X POST \
  -H "Authorization: Bearer \$API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"imageName":"${REGISTRY_IMAGE}","autoStart":true}' \
  https://daytona.${DOMAIN}/api/snapshot 2>&1)
echo "Snapshot create response: \$SNAP"

SNAP_ID=\$(echo "\$SNAP" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("id",""))' 2>/dev/null || true)
echo "Snapshot ID: \$SNAP_ID"

# Poll until active
if [[ -n "\$SNAP_ID" ]]; then
  for i in \$(seq 1 60); do
    STATE=\$(curl -sf -H "Authorization: Bearer \$API_KEY" \
      https://daytona.${DOMAIN}/api/snapshot/\$SNAP_ID \
      | python3 -c 'import sys,json; print(json.load(sys.stdin).get("state",""))' 2>/dev/null || echo "unknown")
    echo "  snapshot state: \$STATE"
    [[ "\$STATE" == "active" ]] && break
    [[ "\$STATE" == "error" ]] && { echo "Snapshot error!" >&2; exit 1; }
    sleep 5
  done
fi
REMOTE
ok "Snapshot registered"

# ── step 8: deploy OpenHands ─────────────────────────────────────────────────
info "[8/8] Deploying OpenHands ..."
ssh_vm bash <<REMOTE
set -euo pipefail
cd /opt/poc

# Substitute domain and LLM key
sed -i 's/mobai\.beauty/${DOMAIN}/g'   docker-compose.openhands.yml
sed -i 's|api\.deepseek\.com|${LLM_BASE_URL#https://}|' docker-compose.openhands.yml

LLM_API_KEY="${LLM_API_KEY}" \
  docker compose -f docker-compose.openhands.yml up -d
REMOTE
ok "OpenHands deployed"

# ── smoke test ────────────────────────────────────────────────────────────────
echo ""
info "=== SMOKE TEST ==="

for url in \
    "https://daytona.${DOMAIN}" \
    "https://openhands.${DOMAIN}"; do
  CODE=$(curl -sf -o /dev/null -w '%{http_code}' --max-time 10 "$url" 2>/dev/null || echo "000")
  printf "  %-50s %s\n" "$url" "$CODE"
done

echo ""
echo "══════════════════════════════════════════════════════"
echo "  DEPLOYMENT COMPLETE"
echo "══════════════════════════════════════════════════════"
echo ""
echo "  Daytona:      https://daytona.${DOMAIN}"
echo "  OpenHands:    https://openhands.${DOMAIN}"
echo ""
echo "  Login:        admin@${DOMAIN} / Mobai2026!"
echo "  API key:      ${DAYTONA_API_KEY}"
echo ""
echo "  SSH to sandbox:"
echo "    TOKEN=\$(curl -s -X POST -H 'Authorization: Bearer ${DAYTONA_API_KEY}' \\"
echo "      https://daytona.${DOMAIN}/api/sandbox/SANDBOX_ID/ssh-access \\"
echo "      | python3 -c 'import sys,json; print(json.load(sys.stdin)[\"token\"])')"
echo "    ssh -p 2222 \$TOKEN@${VM_IP}"
echo "══════════════════════════════════════════════════════"
