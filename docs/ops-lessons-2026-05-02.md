# Ops Lessons: Vultr + Daytona + Claude Code

Date: 2026-05-02

These notes capture reusable lessons from bringing up the Vultr-backed Daytona sandbox and Claude Code flow. Do not place API keys, Claude tokens, DNS secrets, or SSH private keys in this file.

## Cloud Host Bring-up

- Treat `.env` as the only local secret source. Keep it gitignored and mode `0600`.
- Write non-secret instance metadata back into `.env` as soon as the VPS is created: `VM_ID`, `VM_IP`, `DOMAIN`, and `VULTR_SSH_KEY_ID`.
- Verify the server from the outside before debugging application state: SSH first, then Docker, then `https://daytona.<domain>/api/health`, then `mob ps`, then create/delete a sandbox.
- Do not bake LLM tokens into Docker images or registries. Inject them at sandbox creation time or through root-only runtime files.

## Traefik And DNS

- Traefik v3.3 with Porkbun DNS-01 can fail ACME record creation because of a provider response decoding issue. For explicit hostnames such as `daytona.<domain>`, HTTP-01 is simpler and avoids Porkbun API fragility.
- HTTP-01 does not issue wildcard certificates. If wildcard preview domains require trusted TLS, keep DNS-01 or route previews through explicit hostnames. For API health and dashboard, explicit host certificates are enough.
- Use single-quoted YAML for `HostRegexp` rules that contain backslashes:

```yaml
rule: 'HostRegexp(`^.+\\.node\\.proxy\\.example\\.com$`)'
```

- Avoid deleting cloud DNS records unless the operator explicitly approves it. Prefer adding explicit records or changing routing first.

## Daytona v0.171 Details

- The snapshot endpoint is plural: `POST /api/snapshots`, `GET /api/snapshots/{id}`. Older `/api/snapshot` calls return 404.
- `CreateSnapshot` requires a `name`; use `mob-sandbox:1.0` so existing CLI calls can create sandboxes by that snapshot name.
- A fresh organization may initialize with per-sandbox CPU, memory, disk, and volume limits at `0`. Insert/update both:
  - `organization.max_cpu_per_sandbox`, `max_memory_per_sandbox`, `max_disk_per_sandbox`, `volume_quota`
  - `region_quota` for the default region
- API keys need the current `api_key` schema fields: `keyHash`, `keyPrefix`, and `keySuffix`. The cleartext key must remain local.
- If Daytona API was started before fixing quota rows, restart `daytona-api` so quota checks reload cleanly.

## Claude Code In Sandboxes

- Claude Code should be launched inside the Daytona sandbox, not on the local machine.
- Store Claude Code env in the local `mob` client config under `claude_code_env`; `mob create`, `mob ssh`, and `mob claude` then inject those variables into new sandboxes through the Daytona API.
- Injecting sandbox env can override the image `PATH`. Keep Node and Claude reachable via `/usr/local/bin` symlinks in the sandbox image.
- `mob claude <sandbox-id>` should use the absolute command `/usr/local/bin/claude`; relying on PATH through the SSH gateway can fail with exit code 127.
- Do not print LLM auth tokens during verification. Check token presence with length or prefix-only diagnostics.

## Terminal UX

- Full-screen TUI programs need the local terminal in raw mode. Without raw mode, arrow keys can appear as escape sequences like `^[[B`.
- Request the PTY with the actual local terminal size and propagate `SIGWINCH` resize events to the remote session.
- Print the remote sandbox id before opening an SSH session. It prevents confusing a local shell for a remote sandbox shell.

## Verification Checklist

```bash
go test ./...
./bin/mob ps
./bin/mob create
./bin/mob claude <sandbox-id>
```

Inside the sandbox or through the toolbox API:

```bash
/usr/local/bin/claude --version
printf '%s\n' "$ANTHROPIC_BASE_URL"
printf '%s\n' "${#ANTHROPIC_AUTH_TOKEN}"
```
