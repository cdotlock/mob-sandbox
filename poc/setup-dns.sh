#!/bin/bash
# Run this FROM the VM (api.porkbun.com requires authenticated API access)
set -euo pipefail

DOMAIN="${DOMAIN:-mobai.beauty}"
VM_IP=$(curl -s https://ipv4.icanhazip.com)
PB_KEY="${PORKBUN_API_KEY}"
PB_SECRET="${PORKBUN_SECRET_KEY}"
API="https://api.porkbun.com/api/json/v3"

echo "=== Setting up DNS for $DOMAIN → $VM_IP ==="

pb_create() {
  local name="$1" type="${2:-A}" content="${3:-$VM_IP}"
  curl -s -X POST "$API/dns/create/$DOMAIN" \
    -H "Content-Type: application/json" \
    --data "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\",\"name\":\"$name\",\"type\":\"$type\",\"content\":\"$content\",\"ttl\":\"300\"}"
}

pb_delete() {
  local id="$1"
  curl -s -X POST "$API/dns/delete/$DOMAIN/$id" \
    -H "Content-Type: application/json" \
    --data "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\"}"
}

# Fetch and delete existing A records to avoid duplicates
echo "Fetching existing records..."
EXISTING=$(curl -s -X POST "$API/dns/retrieve/$DOMAIN" \
  -H "Content-Type: application/json" \
  --data "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\"}")

echo "$EXISTING" | python3 -c "
import json, sys, subprocess
data = json.load(sys.stdin)
for r in data.get('records', []):
    if r['type'] in ('A', 'CNAME'):
        print(f\"  Deleting {r['type']} {r['name']} → {r['content']} (id={r['id']})\")
        sys.stdout.flush()
        subprocess.run([
            'curl', '-s', '-X', 'POST',
            f\"https://api.porkbun.com/api/json/v3/dns/delete/$DOMAIN/{r['id']}\",
            '-H', 'Content-Type: application/json',
            '--data', json.dumps({'apikey': '$PB_KEY', 'secretapikey': '$PB_SECRET'})
        ])
" DOMAIN="$DOMAIN" PB_KEY="$PB_KEY" PB_SECRET="$PB_SECRET" || true

echo ""
echo "Adding A records..."

declare -A RECORDS=(
  ["*"]="wildcard *.mobai.beauty"
  [""]="root mobai.beauty"
  ["proxy"]="proxy.mobai.beauty"
  ["node.proxy"]="node.proxy.mobai.beauty"
  ["*.node.proxy"]="*.node.proxy.mobai.beauty (Daytona preview)"
)

for name in "*" "" "proxy" "node.proxy" "*.node.proxy"; do
  desc="${RECORDS[$name]}"
  echo -n "  $desc: "
  result=$(pb_create "$name" A "$VM_IP")
  echo "$result"
done

echo ""
echo "=== DNS setup complete. Propagation: ~1-2 min ==="
echo "Verify:"
echo "  dig +short openhands.$DOMAIN @8.8.8.8"
echo "  dig +short daytona.$DOMAIN @8.8.8.8"
echo "  dig +short abc123.node.proxy.$DOMAIN @8.8.8.8"
