# mob-sandbox deployment and expose runbook

This runbook describes how to use a mob sandbox to run a remote service and expose it through either a real domain or an IP-mode stable URL.

## Concepts

- `mob forward` is local only. It opens `localhost:<random>` on your laptop and is not suitable for sharing.
- `mob url` creates a stable preview route. By default it lives for 30 days. Use `--permanent` for a non-expiring route.
- `mob expose` creates a permanent public route.
- `mob url` and `mob expose` can include `--health-path` and `--start-command`. The server guardian checks active routes and restarts the sandbox service when the health check fails.
- In domain mode, routes use `https://<name>.<domain>`.
- In IP mode, routes use `http://<name>.<server-ip>.sslip.io:<control-port>`.

The IP-mode URL is stable because `mob-server` owns the public route and proxies requests into the sandbox through the Daytona SSH gateway. It does not depend on Daytona's one-time signed preview URL.

## Start a service inside the sandbox

SSH into the prepared sandbox:

```bash
mob ssh <sandbox-id>
```

Start the API on the port you want to expose. Prefer `0.0.0.0`; the stable proxy also works with `127.0.0.1` because it enters through the sandbox SSH gateway.

```bash
cd ~/moonshort-script
go build -o bin/mss ./cmd/mss
python3 -m pip install -r requirements.txt
nohup python3 -m uvicorn api_server:app --host 0.0.0.0 --port 8888 > api_server.log 2>&1 &
curl -s http://127.0.0.1:8888/health
```

Expected response:

```json
{"status":"ok"}
```

## Expose in IP mode

IP mode requires no DNS provider. It uses `sslip.io` to resolve a name containing the server IP back to the server.

Create a 30-day preview route:

```bash
mob url <sandbox-id> 8888 --name moonshort \
  --health-path /health \
  --start-command 'cd /home/daytona/moonshort-script && nohup python3 -m uvicorn api_server:app --host 0.0.0.0 --port 8888 > api_server.log 2>&1 < /dev/null &'
```

Create a permanent route:

```bash
mob url <sandbox-id> 8888 --name moonshort --permanent \
  --health-path /health \
  --start-command 'cd /home/daytona/moonshort-script && nohup python3 -m uvicorn api_server:app --host 0.0.0.0 --port 8888 > api_server.log 2>&1 < /dev/null &'
```

Or use the explicit permanent expose command:

```bash
mob expose <sandbox-id> 8888 moonshort \
  --health-path /health \
  --start-command 'cd /home/daytona/moonshort-script && nohup python3 -m uvicorn api_server:app --host 0.0.0.0 --port 8888 > api_server.log 2>&1 < /dev/null &'
```

Example output in IP mode:

```json
{
  "name": "moonshort",
  "sandbox_id": "ca4cfdc5-a605-40be-ac1a-dc0df4fbe9f8",
  "port": 8888,
  "subdomain": "moonshort.47.254.93.15.sslip.io",
  "url": "http://moonshort.47.254.93.15.sslip.io:9876",
  "start_command": "cd /home/daytona/moonshort-script && nohup python3 -m uvicorn api_server:app --host 0.0.0.0 --port 8888 > api_server.log 2>&1 < /dev/null &",
  "health_path": "/health"
}
```

Validate from outside the sandbox:

```bash
BASE=http://moonshort.47.254.93.15.sslip.io:9876
curl -s "$BASE/health"
curl -s -X POST "$BASE/validate" -F "script=@testdata/minimal.md"
```

## Expose in domain mode

Domain mode requires the server to have:

- `mob-server init --domain <domain>` completed.
- DNS records for `*.<domain>` pointing to the server.
- Traefik running on ports 80 and 443.
- `mob-server daemon` running the control API.

Create a permanent domain route:

```bash
mob expose <sandbox-id> 8888 moonshort
```

Expected URL:

```text
https://moonshort.<domain>
```

The Traefik route forwards `moonshort.<domain>` to `mob-server`, and `mob-server` proxies the request into the sandbox through the Daytona SSH gateway.

## TUI workflow

Open the TUI:

```bash
mob
```

Select a sandbox, then:

- Press `u` for a 30-day preview URL.
- Press `e` for a permanent expose route.
- Press `f` only when you need a local `localhost` tunnel.

Preview and expose both work in IP mode and domain mode.

## Route management

List routes:

```bash
CONTROL=http://<server-ip>:9876
curl -s -H "Authorization: Bearer <mob-api-key>" \
  "$CONTROL/control/v1/expose"
```

Delete a route:

```bash
CONTROL=http://<server-ip>:9876
curl -s -X DELETE -H "Authorization: Bearer <mob-api-key>" \
  "$CONTROL/control/v1/expose/moonshort"
```

Routes are stored on the server at:

```text
/etc/mob-server/exposures.yml
```

Domain-mode Traefik routers are also written to:

```text
/etc/traefik/dynamic/routes.yml
```

## Troubleshooting

If the public route returns `502`, first check the service inside the sandbox:

```bash
mob ssh <sandbox-id>
curl -s -i http://127.0.0.1:8888/health
tail -100 ~/moonshort-script/api_server.log
```

If the route was created with `--health-path` and `--start-command`, `mob-server daemon` should repair it automatically. Wait for one guardian interval, then retry:

```bash
sleep 35
curl -s -i http://moonshort.<server-ip>.sslip.io:9876/health
```

If it still fails, check the server daemon logs:

```bash
ssh root@<server> journalctl -u mob-server -n 100 --no-pager
```

If the route returns `404`, confirm the route exists:

```bash
CONTROL=http://<server-ip>:9876
curl -s -H "Authorization: Bearer <mob-api-key>" \
  "$CONTROL/control/v1/expose"
```

If IP-mode DNS does not resolve, use the raw server IP with a Host header to confirm routing:

```bash
curl -s -H "Host: moonshort.<server-ip>.sslip.io" \
  "http://<server-ip>:9876/health"
```

If domain-mode HTTPS fails, check Traefik and DNS:

```bash
dig +short moonshort.<domain>
docker logs --tail=100 traefik
```

## Upgrade notes

Install the same release version on both the operator machine and the server:

```bash
make package
scp bin/mob-linux-amd64 root@<server>:/usr/local/bin/mob
scp bin/mob-server-linux-amd64 root@<server>:/usr/local/bin/mob-server
ssh root@<server> systemctl restart mob-server
```

After the server is upgraded, existing sandboxes do not need to be recreated. Re-run `mob url` or `mob expose` for the service port.
