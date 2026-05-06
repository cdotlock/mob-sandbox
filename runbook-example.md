# MoonShort Backend mob Sandbox Runbook Example

Last updated: 2026-05-06

This is a concrete handoff and operations example for continuing work on the
existing MoonShort backend deployment in a mob sandbox. Keep secrets out of this
file. Do not paste `.env.production`, GitHub tokens, Claude tokens, API keys, or
cloud provider credentials into runbooks, logs, commits, or issue comments.

## Current Inventory

- Local workspace: `/Users/Clock/mob-sandbox`
- mob CLI: `/Users/Clock/mob-sandbox/bin/mob`
- mob config: `/Users/Clock/.config/mob/config.yaml`
- Target sandbox: `65e43349-d0be-44ba-8147-0c987075e193`
- Target project path: `/home/daytona/moonshort-backend`
- Stable public route: `http://moonshort-backend.47.254.93.15.sslip.io:9876`
- User frontend entry: `http://moonshort-backend.47.254.93.15.sslip.io:9876/web/login`
- Admin frontend entry: `http://moonshort-backend.47.254.93.15.sslip.io:9876/web/admin/login`
- Old FastAPI sandbox, do not delete: `ca4cfdc5-a605-40be-ac1a-dc0df4fbe9f8`
- Old FastAPI route, do not overwrite: `http://moonshort.47.254.93.15.sslip.io:9876`

The MoonShort backend deployment is a Docker Compose production stack inside the
target sandbox. The stable route proxies to sandbox port 80, where the stack's
nginx service serves both `/api/*` and the bundled frontend under `/web/*`.

## Hard Rules

- Use the existing target sandbox. Do not create a new sandbox unless the user
  explicitly asks for one.
- Do not delete any sandbox. In particular, do not delete the old FastAPI
  sandbox.
- Do not use local SSH keys to log into the host server for normal app work.
  Enter the sandbox through `mob ssh <sandbox-id>`.
- Do not print `.env.production` or any secret-bearing environment file.
- Do not run `docker system prune -a`, `docker volume prune`, or any command that
  can remove database volumes.
- Do not treat a no-port URL as the source of truth. In current IP mode, the
  stable mob route includes `:9876`.
- Before changing code, run `git status --short --branch` in the target project
  and preserve any existing user changes.

## Expanded Handoff Prompt For Another Agent

Use this prompt when asking another agent to continue debugging or fixing the
MoonShort backend deployment. Fill in the current symptom and reproduction steps
before sending it.

```text
You need to connect to and operate the existing mob sandbox. Do not create a new
sandbox, do not delete any sandbox, and do not delete Docker volumes.

Goal:
- Continue debugging/fixing: <specific bug or deployment task>.
- First produce a short situation report before editing code.
- If code changes are needed, keep them scoped, commit them to a branch, and push
  the branch. Do not push directly to main unless explicitly instructed.

Local machine:
- Workdir: /Users/Clock/mob-sandbox
- mob CLI: /Users/Clock/mob-sandbox/bin/mob
- mob config: /Users/Clock/.config/mob/config.yaml

Target sandbox:
- Sandbox id: 65e43349-d0be-44ba-8147-0c987075e193
- Project path: /home/daytona/moonshort-backend
- Public route: http://moonshort-backend.47.254.93.15.sslip.io:9876
- Frontend: http://moonshort-backend.47.254.93.15.sslip.io:9876/web/login
- Admin: http://moonshort-backend.47.254.93.15.sslip.io:9876/web/admin/login

Old FastAPI sandbox:
- Sandbox id: ca4cfdc5-a605-40be-ac1a-dc0df4fbe9f8
- Route: http://moonshort.47.254.93.15.sslip.io:9876
- Do not delete it and do not overwrite its route.

Enter the sandbox:
cd /Users/Clock/mob-sandbox
./bin/mob ssh 65e43349-d0be-44ba-8147-0c987075e193
cd /home/daytona/moonshort-backend

Current stack:
- Docker Compose prod stack
- Compose file: docker-compose.prod.yml
- Env file: .env.production
- Public proxy path: /api/* and /web/*
- Stable mob route uses port :9876 in IP mode.

Safety:
- Do not print .env.production.
- Do not print tokens, API keys, or bearer values.
- Do not run docker system prune -a.
- Do not run docker volume prune.
- Do not assume the no-port hostname is correct. Use the :9876 public URL.
- Do not use local SSH keys to log into the host; operate through mob ssh.

Initial report checklist:
1. Run: git status --short --branch
2. Run: git log --oneline -5
3. Run: sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps
4. Run: curl -s http://127.0.0.1/api/health
5. Run from local or inside sandbox if network allows:
   curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health
6. Check logs:
   sudo docker logs --tail=100 moonshort-backend-app-1
   sudo docker logs --tail=100 moonshort-backend-worker-1
   sudo docker logs --tail=100 moonshort-backend-dream-agent-1
   sudo docker logs --tail=100 moonshort-backend-nginx-1
7. Check DB connectivity:
   sudo docker exec moonshort-backend-db-1 psql -U postgres -d noval_demo -c 'select 1;'

For dream/admin bugs:
- Reproduce with the public :9876 URL, not the no-port URL.
- Capture the exact request path, status code, response body prefix, and relevant
  app/worker/dream-agent logs.
- Inspect DB schema before assuming table names:
  sudo docker exec -it moonshort-backend-db-1 psql -U postgres -d noval_demo
  \dt
- If querying jobs, first identify the actual table and columns, then list recent
  jobs by created/updated time.

Remote Claude Code:
- The sandbox has Claude Code installed. Run it only inside the sandbox.
- Use the operator-provided ANTHROPIC_* and CLAUDE_CODE_* environment variables.
- Do not write those values into files or commits.
- Start it with:
  claude --dangerously-skip-permissions

When deploying a user fix from main:
cd /home/daytona/moonshort-backend
git fetch origin
git checkout main
git pull --ff-only origin main
sudo docker compose -f docker-compose.prod.yml --env-file .env.production build
sudo docker compose -f docker-compose.prod.yml --env-file .env.production up -d
curl -s http://127.0.0.1/api/health
curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health

If the build fails or services stop:
- Check disk first: df -h and sudo docker system df
- Safe cleanup options are limited to dangling/unused build cache, for example:
  sudo docker image prune -f
  sudo docker builder prune -f
- Do not remove volumes.

Expected final output:
- Current status of the public route.
- Current git branch and commit deployed.
- Whether DB/Redis/queue are healthy.
- Whether dream-agent is running and why any job failed.
- Exact files changed, branch pushed, and validation commands run.
```

## Connect To The Target Sandbox

From the local workspace:

```bash
cd /Users/Clock/mob-sandbox
./bin/mob ssh 65e43349-d0be-44ba-8147-0c987075e193
```

Then inside the sandbox:

```bash
cd /home/daytona/moonshort-backend
git status --short --branch
```

If you need remote Claude Code, export the operator-provided Claude environment
variables in the sandbox shell, then run:

```bash
claude --dangerously-skip-permissions
```

Do not store the Claude auth token in this repository or in the product
repository.

## Deploy Or Redeploy The Product

Use this when the user says their fix has landed on `main` and asks you to
deploy it:

```bash
cd /home/daytona/moonshort-backend
git fetch origin
git checkout main
git pull --ff-only origin main
sudo docker compose -f docker-compose.prod.yml --env-file .env.production build
sudo docker compose -f docker-compose.prod.yml --env-file .env.production up -d
curl -s http://127.0.0.1/api/health
```

Validate from outside the sandbox through the mob route:

```bash
curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health
curl -sSI http://moonshort-backend.47.254.93.15.sslip.io:9876/web/login
curl -sSI http://moonshort-backend.47.254.93.15.sslip.io:9876/web/admin/login
```

For a first deployment or after a database reset, run seed data once:

```bash
sudo docker compose -f docker-compose.prod.yml --env-file .env.production exec app pnpm seed:all
```

Do not rerun seed blindly against production data. Check the app's seed behavior
or ask the user when data preservation matters.

## Docker And Health Checks

Inside the sandbox:

```bash
cd /home/daytona/moonshort-backend
sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps
curl -s http://127.0.0.1/api/health
sudo docker logs --tail=100 moonshort-backend-app-1
sudo docker logs --tail=100 moonshort-backend-worker-1
sudo docker logs --tail=100 moonshort-backend-dream-agent-1
sudo docker logs --tail=100 moonshort-backend-nginx-1
```

DB smoke test:

```bash
sudo docker exec moonshort-backend-db-1 psql -U postgres -d noval_demo -c 'select 1;'
```

If the public URL returns `502`, check in this order:

1. Disk space: `df -h`
2. Docker usage: `sudo docker system df`
3. Compose service status: `sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps`
4. Internal health: `curl -s http://127.0.0.1/api/health`
5. Public health: `curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health`
6. App, nginx, worker, and dream-agent logs

The previous long outage was caused by the sandbox disk filling up. Rebuilds can
fail or containers can stop when Docker cannot write layers/logs. Increase disk
or prune only safe cache/dangling images; never remove volumes unless the user
explicitly approves data loss.

## Public Route Notes

Current mob server is in IP mode. In IP mode the stable route format is:

```text
http://<route-name>.<server-ip>.sslip.io:<control-port>
```

For this deployment that is:

```text
http://moonshort-backend.47.254.93.15.sslip.io:9876
```

The no-port hostname is not the authoritative route for this deployment. During
this deployment it appeared to be served by a separate host-level nginx on port
80, with different behavior from the mob route. Fixing that requires host-level
proxy access or operator action, not changes inside the sandbox. Use the `:9876`
URL for debugging and user handoff unless host port 80 has been explicitly
updated.

To list registered mob routes without printing the API key:

```bash
cd /Users/Clock/mob-sandbox
CONTROL=http://47.254.93.15:9876
API_KEY="$(awk '/^api_key:/{print $2}' /Users/Clock/.config/mob/config.yaml)"
curl --noproxy '*' -sS -H "Authorization: Bearer ${API_KEY}" \
  "${CONTROL}/control/v1/expose"
unset API_KEY
```

If the route needs to be recreated, prefer `mob expose` and keep the health path
and start command attached:

```bash
cd /Users/Clock/mob-sandbox
./bin/mob expose 65e43349-d0be-44ba-8147-0c987075e193 80 moonshort-backend \
  --health-path /api/health \
  --start-command 'cd /home/daytona/moonshort-backend && sudo docker compose -f docker-compose.prod.yml --env-file .env.production up -d'
```

## Debugging Dream/Admin Issues

Use the public `:9876` URL for browser reproduction. The app includes its own
frontend; do not look for a separate Cocos deployment unless the user explicitly
changes the target.

Recommended flow:

```bash
cd /home/daytona/moonshort-backend
git status --short --branch
sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps
curl -s http://127.0.0.1/api/health
sudo docker logs --tail=200 moonshort-backend-app-1
sudo docker logs --tail=200 moonshort-backend-worker-1
sudo docker logs --tail=200 moonshort-backend-dream-agent-1
```

For DB investigation, first inspect schema:

```bash
sudo docker exec -it moonshort-backend-db-1 psql -U postgres -d noval_demo
\dt
```

Only after confirming actual table names should you query recent dream jobs,
events, traces, or errors. Do not assume table names from memory.

When the UI says a dream job is running but opening it returns JSON parse errors,
capture:

- Browser path and status code
- First 200 bytes of the response body
- App logs around the request timestamp
- Worker logs around the job timestamp
- Dream-agent logs around the same timestamp
- DB row status/error for the specific job id, after confirming schema

## Local Build Cleanup

The user asked to remove stale local builds, not to delete sandboxes. Safe local
cleanup depends on where the build happened:

- In this repo, remove generated `bin/` or `dist/` artifacts only after checking
  `git status`.
- In the sandbox, use Docker cache cleanup conservatively:

```bash
df -h
sudo docker system df
sudo docker image prune -f
sudo docker builder prune -f
```

Avoid broad cleanup commands. Database state is in Docker volumes and must be
preserved unless the user explicitly approves a reset.

## Lessons Learned

- A working sandbox-local health check is not enough. Always validate the public
  mob route as well.
- In mob IP mode, the stable URL includes `:9876`. A same-host no-port URL can be
  a different host-level reverse proxy and can show stale or inconsistent
  behavior.
- `mob forward` is local only. Use `mob expose` for permanent public service
  routes and attach a health path/start command for recovery.
- The MoonShort project ships its own frontend under `/web/*`; there is no
  separate frontend needed for the current deployment.
- Disk pressure can present as `502 Bad Gateway` or stopped Docker services.
  Check disk before repeatedly rebuilding.
- Remote Claude Code should run inside the target sandbox through `mob ssh`; it
  is not a reason to SSH into the host with local private keys.
- Do not debug dream jobs from only the UI. Correlate UI status, app logs,
  worker logs, dream-agent logs, and DB state by timestamp/job id.
- Keep deployment, route, and bug-fix work separate in git branches. A branch
  that documents findings is useful, but production deploy should still verify
  the exact commit and public URL after `docker compose up -d`.
