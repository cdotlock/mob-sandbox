# mob-power Cloudflare Worker

Brokers Vultr instance power control (`start` / `stop` / `reboot` / `status`)
for mob operators authenticated by SSH ed25519 signature. The Vultr API key
lives in the Worker; operators only need their local SSH private key.

## Why this exists

Vultr's API is bearer-token authenticated. Whoever calls the API has to hold
the token. A pubkey-only operator model needs an always-on broker that:

1. Holds the Vultr API key (single trust boundary).
2. Verifies operator requests via SSH-pubkey signatures (no shared secret).
3. Forwards approved requests to Vultr.

A Cloudflare Worker is a clean fit: free tier covers ~3000 ops/day,
sub-100ms cold start, secrets are first-class, Web Crypto supports
`Ed25519` natively (since compat date `2024-09-23`).

## Auth protocol

Operator builds a request body:

```json
{
  "action":    "start",
  "operator":  "<name>",
  "timestamp": 1714521600,
  "signature": "<base64 ed25519 signature>"
}
```

The signature is computed over the canonical text:

```
<action>|<operator>|<timestamp>
```

Worker:

1. Parses JSON, checks all fields present.
2. Validates `timestamp` is within `CLOCK_SKEW_SECONDS` (default 300s) of now.
3. Looks up `operator` in `AUTHORIZED_PUBKEYS` to find their `pubkey_b64`.
4. Verifies the signature against the canonical text using Web Crypto
   `Ed25519`.
5. If valid, calls Vultr API with `VULTR_API_KEY` and returns the result.

## Deploy (one-time, by admin)

Prerequisites: Cloudflare account, `wrangler` CLI installed
(`npm i -g wrangler` or `npx wrangler ...`).

```bash
cd infra/power-worker
npm install              # pulls wrangler

# Authenticate wrangler (opens browser)
npx wrangler login

# Set the Vultr API key as a secret (never goes to git)
npx wrangler secret put VULTR_API_KEY
# (paste the key, hit enter)

# Edit wrangler.toml: set VM_ID and AUTHORIZED_PUBKEYS
$EDITOR wrangler.toml

# Deploy
npx wrangler deploy
# → Worker URL printed, e.g. https://mob-power.<your-subdomain>.workers.dev
```

Save the Worker URL — operators need it for `mob power init`.

## Add or remove operators

The Worker reads `AUTHORIZED_PUBKEYS` from `wrangler.toml`. To update:

1. SSH to the mob server and add the operator's SSH pubkey to `authorized_keys`:

   ```bash
   mob-server operator add <name> --pubkey-file <name>.pub
   ```

   Output includes a `{"name":"...","pubkey_b64":"..."}` line.

2. Print the full Worker config:

   ```bash
   mob-server operator worker-config
   ```

3. Paste that JSON array into `AUTHORIZED_PUBKEYS` in `wrangler.toml`.

4. Redeploy:

   ```bash
   cd infra/power-worker && npx wrangler deploy
   ```

To revoke: `mob-server operator revoke <name>` on the server, then refresh
`AUTHORIZED_PUBKEYS` and redeploy.

## Health check

```bash
curl https://mob-power.<your-subdomain>.workers.dev/health
# → {"ok":true,"vm_id":"..."}
```

## Test from the CLI

```bash
mob power status        # GET instance info via Worker
mob power start         # power on
mob power stop          # power off
mob power reboot        # reboot
```

See [`cmd/mob/power.go`](../../cmd/mob/power.go) for the client.

## Cost

Free tier (Cloudflare Workers free plan):
- 100,000 requests/day
- 10ms CPU per request

Power control is far below this. Free indefinitely for normal use.

## Security notes

- The Vultr API key is a Worker secret (encrypted at rest, never logged).
- Operator pubkeys are public; storing them in `AUTHORIZED_PUBKEYS` is fine.
- A captured request can be replayed within the `CLOCK_SKEW_SECONDS` window
  (default 5 min). For power control on a single VM this is acceptable; if
  it's not, add nonce tracking via Workers KV.
- `wrangler.toml` should never contain secrets. Anything sensitive goes
  through `wrangler secret put`.

## Troubleshooting

- `bad signature` → operator's `pubkey_b64` in Worker doesn't match their
  local SSH key. Re-run `mob-server operator add` and refresh
  `AUTHORIZED_PUBKEYS`.
- `unknown operator` → `mob` CLI's `operator_name` config doesn't match any
  entry in `AUTHORIZED_PUBKEYS`. Check `~/.config/mob/config.yaml`.
- `timestamp out of window` → operator's clock is off by more than 5 min.
  Run `ntpdate` or sync time.
- Vultr 401 → `VULTR_API_KEY` secret is wrong, or has IP allowlist that
  excludes Cloudflare's egress IPs (lift the allowlist on this key, or use a
  separate non-restricted key dedicated to the Worker).
