# MoonShort Backend mob 沙盒部署 Runbook 示例

最后更新：2026-05-06

这是一份给后续 Agent 接手 MoonShort backend 现有 mob sandbox 部署时使用的交接与运维示例。不要把任何密钥写进这份文档，包括 `.env.production`、GitHub token、Claude token、API key、云厂商凭证，或者其他 bearer/token 类配置。

## 当前资源清单

- 本地工作目录：`/Users/Clock/mob-sandbox`
- mob CLI：`/Users/Clock/mob-sandbox/bin/mob`
- mob 配置：`/Users/Clock/.config/mob/config.yaml`
- 当前目标 sandbox：`65e43349-d0be-44ba-8147-0c987075e193`
- 目标项目路径：`/home/daytona/moonshort-backend`
- 稳定公网入口：`http://moonshort-backend.47.254.93.15.sslip.io:9876`
- 用户前端入口：`http://moonshort-backend.47.254.93.15.sslip.io:9876/web/login`
- Admin 前端入口：`http://moonshort-backend.47.254.93.15.sslip.io:9876/web/admin/login`
- 旧 FastAPI sandbox，不要删除：`ca4cfdc5-a605-40be-ac1a-dc0df4fbe9f8`
- 旧 FastAPI 入口，不要覆盖：`http://moonshort.47.254.93.15.sslip.io:9876`

MoonShort backend 当前部署在目标 sandbox 内，是一套 Docker Compose production stack。稳定公网入口会代理到 sandbox 的 80 端口，stack 里的 nginx 同时服务 `/api/*` 和项目自带的 `/web/*` 前端。

## 硬性规则

- 使用现有目标 sandbox。除非用户明确要求，不要新建 sandbox。
- 不要删除任何 sandbox，尤其不要删除旧 FastAPI sandbox。
- 常规应用操作不要用本机 SSH key 登录宿主机；应该通过 `mob ssh <sandbox-id>` 进入 sandbox。
- 不要打印 `.env.production`，也不要打印任何带密钥的环境文件。
- 不要运行 `docker system prune -a`、`docker volume prune`，也不要运行任何可能删除数据库 volume 的命令。
- 不要把不带端口的 URL 当成事实入口。当前 IP 模式下，稳定 mob 入口包含 `:9876`。
- 改代码前，先在目标项目里执行 `git status --short --branch`，确认并保留已有用户改动。

## 给后续 Agent 的扩展交接 Prompt

需要让其他 Agent 接着排查或修复 MoonShort backend 时，可以直接把下面这段发给它。发送前，把当前症状和复现步骤补到对应位置。

```text
你需要连接并操作现有 mob sandbox。不要新建 sandbox，不要删除任何 sandbox，也不要删除 Docker volume。

目标：
- 继续排查/修复：<具体 bug 或部署任务>。
- 动手改代码前，先输出一份简短现状报告。
- 如果确实需要改代码，改动要保持小范围，提交到一个分支并 push。除非用户明确要求，不要直接 push 到 main。

本地机器：
- 工作目录：/Users/Clock/mob-sandbox
- mob CLI：/Users/Clock/mob-sandbox/bin/mob
- mob 配置：/Users/Clock/.config/mob/config.yaml

目标 sandbox：
- Sandbox id：65e43349-d0be-44ba-8147-0c987075e193
- 项目路径：/home/daytona/moonshort-backend
- 公网入口：http://moonshort-backend.47.254.93.15.sslip.io:9876
- 前端入口：http://moonshort-backend.47.254.93.15.sslip.io:9876/web/login
- Admin 入口：http://moonshort-backend.47.254.93.15.sslip.io:9876/web/admin/login

旧 FastAPI sandbox：
- Sandbox id：ca4cfdc5-a605-40be-ac1a-dc0df4fbe9f8
- 入口：http://moonshort.47.254.93.15.sslip.io:9876
- 不要删除它，也不要覆盖它的 route。

进入 sandbox：
cd /Users/Clock/mob-sandbox
./bin/mob ssh 65e43349-d0be-44ba-8147-0c987075e193
cd /home/daytona/moonshort-backend

当前 stack：
- Docker Compose prod stack
- Compose 文件：docker-compose.prod.yml
- Env 文件：.env.production
- 公网代理路径：/api/* 和 /web/*
- 当前 mob 是 IP 模式，稳定入口必须带 :9876。

安全要求：
- 不要打印 .env.production。
- 不要打印 token、API key、bearer value。
- 不要运行 docker system prune -a。
- 不要运行 docker volume prune。
- 不要假设不带端口的 hostname 是正确入口；使用带 :9876 的公网 URL。
- 不要用本机 SSH key 登录宿主机；通过 mob ssh 进入 sandbox 操作。

初始现状报告清单：
1. 运行：git status --short --branch
2. 运行：git log --oneline -5
3. 运行：sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps
4. 运行：curl -s http://127.0.0.1/api/health
5. 从本机或 sandbox 内网络可达处运行：
   curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health
6. 检查日志：
   sudo docker logs --tail=100 moonshort-backend-app-1
   sudo docker logs --tail=100 moonshort-backend-worker-1
   sudo docker logs --tail=100 moonshort-backend-dream-agent-1
   sudo docker logs --tail=100 moonshort-backend-nginx-1
7. 检查 DB 连通性：
   sudo docker exec moonshort-backend-db-1 psql -U postgres -d noval_demo -c 'select 1;'

排查 dream/admin 相关 bug 时：
- 用带 :9876 的公网 URL 复现，不要用不带端口的 URL。
- 记录精确请求路径、状态码、response body 前缀，以及相关 app/worker/dream-agent 日志。
- 查询 DB 前先看 schema，不要凭记忆猜表名：
  sudo docker exec -it moonshort-backend-db-1 psql -U postgres -d noval_demo
  \dt
- 如果要查 job，先确认真实表名和字段，再按 created/updated 时间列最近的任务。

远端 Claude Code：
- sandbox 内已经安装 Claude Code，只能在 sandbox 内运行。
- 使用 operator 提供的 ANTHROPIC_* 和 CLAUDE_CODE_* 环境变量。
- 不要把这些值写进文件或 commit。
- 启动命令：
  claude --dangerously-skip-permissions

部署用户已经合入 main 的修复时：
cd /home/daytona/moonshort-backend
git fetch origin
git checkout main
git pull --ff-only origin main
sudo docker compose -f docker-compose.prod.yml --env-file .env.production build
sudo docker compose -f docker-compose.prod.yml --env-file .env.production up -d
curl -s http://127.0.0.1/api/health
curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health

如果 build 失败或服务停止：
- 先检查磁盘：df -h 和 sudo docker system df
- 只做保守清理，例如 dangling image/build cache：
  sudo docker image prune -f
  sudo docker builder prune -f
- 不要删除 volume。

期望最终输出：
- 当前公网入口状态。
- 当前部署的 git branch 和 commit。
- DB/Redis/queue 是否健康。
- dream-agent 是否运行；如果 job 失败，说明失败原因。
- 改了哪些文件，push 到哪个分支，跑了哪些验证命令。
```

## 连接目标 Sandbox

从本地工作目录进入目标 sandbox：

```bash
cd /Users/Clock/mob-sandbox
./bin/mob ssh 65e43349-d0be-44ba-8147-0c987075e193
```

进入 sandbox 后：

```bash
cd /home/daytona/moonshort-backend
git status --short --branch
```

如果需要运行远端 Claude Code，在 sandbox shell 里 export operator 提供的 Claude 环境变量，然后运行：

```bash
claude --dangerously-skip-permissions
```

不要把 Claude auth token 存进这个仓库，也不要存进业务项目仓库。

## 部署或重新部署业务项目

用户说明修复已经合入 `main` 并要求部署时，使用下面流程：

```bash
cd /home/daytona/moonshort-backend
git fetch origin
git checkout main
git pull --ff-only origin main
sudo docker compose -f docker-compose.prod.yml --env-file .env.production build
sudo docker compose -f docker-compose.prod.yml --env-file .env.production up -d
curl -s http://127.0.0.1/api/health
```

然后从 sandbox 外部通过 mob 稳定入口验证：

```bash
curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health
curl -sSI http://moonshort-backend.47.254.93.15.sslip.io:9876/web/login
curl -sSI http://moonshort-backend.47.254.93.15.sslip.io:9876/web/admin/login
```

首次部署或数据库重置后，才需要 seed 一次：

```bash
sudo docker compose -f docker-compose.prod.yml --env-file .env.production exec app pnpm seed:all
```

不要在已有生产数据上盲目重复 seed。涉及数据保留时，先确认项目的 seed 行为，或者直接问用户。

## Docker 与健康检查

在 sandbox 内执行：

```bash
cd /home/daytona/moonshort-backend
sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps
curl -s http://127.0.0.1/api/health
sudo docker logs --tail=100 moonshort-backend-app-1
sudo docker logs --tail=100 moonshort-backend-worker-1
sudo docker logs --tail=100 moonshort-backend-dream-agent-1
sudo docker logs --tail=100 moonshort-backend-nginx-1
```

DB smoke test：

```bash
sudo docker exec moonshort-backend-db-1 psql -U postgres -d noval_demo -c 'select 1;'
```

如果公网 URL 返回 `502`，按这个顺序查：

1. 磁盘空间：`df -h`
2. Docker 占用：`sudo docker system df`
3. Compose 服务状态：`sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps`
4. sandbox 内部健康检查：`curl -s http://127.0.0.1/api/health`
5. 公网健康检查：`curl -s http://moonshort-backend.47.254.93.15.sslip.io:9876/api/health`
6. app、nginx、worker、dream-agent 日志

之前长时间不可用的根因是 sandbox 磁盘打满。Docker 无法写 layer 或日志时，rebuild 会失败，容器也可能停止。遇到这种情况要先扩容或只清理安全的 cache/dangling image；除非用户明确批准数据丢失，不要删除 volume。

## 公网 Route 注意事项

当前 mob server 是 IP 模式。IP 模式的稳定入口格式是：

```text
http://<route-name>.<server-ip>.sslip.io:<control-port>
```

这次部署对应：

```text
http://moonshort-backend.47.254.93.15.sslip.io:9876
```

不带端口的 hostname 不是这次部署的权威入口。排查时发现，不带端口的 hostname 由宿主机 80 端口上的另一套 host-level nginx 提供服务，行为和 mob route 不一致。修它需要宿主机级别的反向代理权限或 operator 介入，不是 sandbox 内部改代码能解决的。除非宿主机 80 端口配置已经明确更新，否则调试和交付都使用带 `:9876` 的 URL。

列出已注册 mob routes 时，不要打印 API key：

```bash
cd /Users/Clock/mob-sandbox
CONTROL=http://47.254.93.15:9876
API_KEY="$(awk '/^api_key:/{print $2}' /Users/Clock/.config/mob/config.yaml)"
curl --noproxy '*' -sS -H "Authorization: Bearer ${API_KEY}" \
  "${CONTROL}/control/v1/expose"
unset API_KEY
```

如果需要重建 route，优先用 `mob expose`，并带上 health path 和 start command：

```bash
cd /Users/Clock/mob-sandbox
./bin/mob expose 65e43349-d0be-44ba-8147-0c987075e193 80 moonshort-backend \
  --health-path /api/health \
  --start-command 'cd /home/daytona/moonshort-backend && sudo docker compose -f docker-compose.prod.yml --env-file .env.production up -d'
```

## Dream/Admin 问题排查

浏览器复现使用带 `:9876` 的公网 URL。这个项目自带前端，不要再找独立 Cocos 部署，除非用户明确改变目标。

推荐排查流程：

```bash
cd /home/daytona/moonshort-backend
git status --short --branch
sudo docker compose -f docker-compose.prod.yml --env-file .env.production ps
curl -s http://127.0.0.1/api/health
sudo docker logs --tail=200 moonshort-backend-app-1
sudo docker logs --tail=200 moonshort-backend-worker-1
sudo docker logs --tail=200 moonshort-backend-dream-agent-1
```

DB 排查先看 schema：

```bash
sudo docker exec -it moonshort-backend-db-1 psql -U postgres -d noval_demo
\dt
```

确认真实表名后，再查询最近的 dream jobs、events、traces 或 errors。不要凭记忆假设表名。

如果 UI 显示 dream job 在跑，但打开详情时报 JSON parse error，至少要记录：

- 浏览器请求路径和状态码
- response body 前 200 字节
- 同一时间段 app 日志
- 同一时间段 worker 日志
- 同一时间段 dream-agent 日志
- 确认 schema 后，对应 job id 的 DB 状态和错误字段

## 本地过时构建清理

用户之前要求删的是本地过时构建，不是删除 sandbox。安全清理取决于构建发生的位置：

- 在这个 repo 里，只在检查 `git status` 后清理生成的 `bin/` 或 `dist/` artifact。
- 在 sandbox 里，只做保守 Docker cache 清理：

```bash
df -h
sudo docker system df
sudo docker image prune -f
sudo docker builder prune -f
```

避免使用大范围清理命令。数据库状态在 Docker volumes 里，除非用户明确允许重置，否则必须保留。

## 这次学到的经验

- sandbox 内部健康检查通过还不够，必须同时验证公网 mob route。
- mob IP 模式下稳定入口包含 `:9876`。同 host 不带端口的 URL 可能是另一套宿主机反代，可能出现陈旧或不一致行为。
- `mob forward` 只适合本地调试。长期公网服务用 `mob expose`，并绑定 health path 和 start command。
- MoonShort 项目自带 `/web/*` 前端；当前部署不需要单独找前端项目。
- 磁盘打满可能表现为 `502 Bad Gateway` 或 Docker 服务停止。不要在没查磁盘的情况下反复 rebuild。
- 远端 Claude Code 应该通过 `mob ssh` 进入目标 sandbox 后运行；这不是用本机私钥 SSH 到宿主机的理由。
- dream job 不能只看 UI。要按时间或 job id 关联 UI 状态、app 日志、worker 日志、dream-agent 日志和 DB 状态。
- 部署、route 修复、bug 修复要分支隔离。记录问题的文档分支有价值，但生产部署后仍要确认实际 commit 和公网 URL。
