# mob-sandbox 平台实现报告

日期: 2026-04-29  
版本: PoC v1  
VM: Vultr Tokyo, 45.32.25.73 (Ubuntu 24.04, 4 vCPU / 8GB RAM / 160GB NVMe)  
域名: mobai.beauty (Porkbun)

本报告是 PoC 全过程的完整记录，覆盖每一步的具体做法、踩过的坑、以及最终的解决方案。后续 Go CLI（mob-server / mob）的实现以本报告为根据。

---

## 目录

1. [平台架构总览](#1-平台架构总览)
2. [基础设施准备](#2-基础设施准备)
3. [Docker 与网络配置](#3-docker-与网络配置)
4. [Traefik 反向代理](#4-traefik-反向代理)
5. [Daytona 9 服务栈](#5-daytona-9-服务栈)
6. [SSH Gateway 配置](#6-ssh-gateway-配置)
7. [Toolbox Binary 提取](#7-toolbox-binary-提取)
8. [API Key 管理](#8-api-key-管理)
9. [Sandbox 镜像构建](#9-sandbox-镜像构建)
10. [Snapshot 注册](#10-snapshot-注册)
11. [OpenHands 部署](#11-openhands-部署)
12. [Dex OIDC 认证](#12-dex-oidc-认证)
13. [Preview URL 机制](#13-preview-url-机制)
14. [全部踩坑清单](#14-全部踩坑清单)
15. [CLI 实现注意事项](#15-cli-实现注意事项)

---

## 1. 平台架构总览

```
Internet
  └─ Traefik v3.3 (TLS termination, DNS-01 ACME via Porkbun)
       ├─ daytona.mobai.beauty   → daytona-api:3000
       ├─ openhands.mobai.beauty → openhands:3000
       └─ *.node.proxy.mobai.beauty → daytona-proxy:4000

Docker networks:
  edge (172.18.0.0/16)        ← Traefik + 所有需要外部暴露的容器
  daytona-network (172.20.0.0/16) ← Daytona 内部 9 服务通信
  runner-bridge (10.100.0.0/24)   ← sandbox 容器所在网络

Daytona 9 services:
  api · proxy · runner · ssh-gateway · dex · db(postgres) · redis · registry · minio
```

关键概念：
- **runner-bridge** 是 sandbox 容器的宿主网络。Daytona runner 在这个网络上创建 sandbox 容器，每个 sandbox 分配 10.100.0.x 的 IP。
- **SSH gateway** 和 **runner** 必须同时在 runner-bridge 上，否则无法路由到 sandbox。
- **daytona-proxy** 处理 Preview URL → sandbox 端口的转发，通过 signed token 鉴权。

---

## 2. 基础设施准备

### 2.1 Vultr VM 创建

- 选 Tokyo 机房（vc2-4c-8gb），Ubuntu 24.04
- SSH key 事先上传到 Vultr，记住 SSH_KEY_ID
- 创建后拿到 VM_IP

### 2.2 DNS 配置（Porkbun API）

需要 4 条 A 记录，全部指向 VM_IP：

```
daytona.mobai.beauty    → 45.32.25.73
openhands.mobai.beauty  → 45.32.25.73
*.proxy.mobai.beauty    → 45.32.25.73
*.node.proxy.mobai.beauty → 45.32.25.73  (泛域名)
```

Porkbun API 调用：
```bash
curl -s -X POST "https://api.porkbun.com/api/json/v3/dns/create/$DOMAIN" \
  -H "Content-Type: application/json" \
  -d '{"apikey":"pk1_...","secretapikey":"sk1_...","type":"A","name":"daytona","content":"45.32.25.73","ttl":"300"}'
```

**坑：** Porkbun 的泛域名记录 `*.node.proxy` 需要把 name 写成 `*.node.proxy`，不是 `node.proxy.*`。

### 2.3 VM Bootstrap

SSH 进 VM（注意需要特殊 KEX 算法）：
```bash
ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@45.32.25.73
```

**坑：Vultr 默认 SSH 不支持标准 KEX。** 必须加 `-o KexAlgorithms=curve25519-sha256`，否则直接 connection refused。这个参数在所有后续 SSH 操作中都必须带上。

Bootstrap 步骤（对应 bootstrap.sh）：
1. apt-get update + upgrade
2. 安装 Docker CE（添加 Docker 官方 APT 源）
3. UFW 防火墙：开放 22, 80, 443, 2222（SSH gateway）
4. 创建 Docker 网络：`edge`（Traefik 用）和 `daytona`

---

## 3. Docker 与网络配置

### 3.1 三个 Docker 网络

```yaml
# 在 docker-compose 中定义
networks:
  edge:
    external: true          # Traefik 所在，需要提前创建
  daytona-network:
    driver: bridge          # Daytona 服务间通信
  runner-bridge:
    driver: bridge
    ipam:
      config:
        - subnet: 10.100.0.0/24   # sandbox 容器分配 IP 的网段
```

### 3.2 Docker daemon 配置

sandbox 镜像存储在 Daytona 内部 registry（`registry:6000`），走 HTTP 不走 HTTPS：

```json
// /etc/docker/daemon.json
{
  "insecure-registries": ["registry:6000"]
}
```

改完之后 `systemctl reload docker`。

**坑：** registry 的主机名 `registry` 不是标准 DNS 可解析的。需要在 VM 的 /etc/hosts 中添加 registry 容器的 IP（见 §7 registry hosts 配置）。

### 3.3 registry /etc/hosts

Daytona runner 通过 `registry:6000` 拉取 sandbox 镜像。这个主机名只在 Docker 网络内有效，但 VM 本身（Docker daemon）在 push 镜像时也需要解析它。

做法：查询 registry 容器 IP，写入 /etc/hosts：
```bash
REGISTRY_IP=$(docker inspect daytona-registry \
  --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' \
  | awk '{print $1}')
sed -i '/[[:space:]]registry$/d' /etc/hosts
echo "$REGISTRY_IP registry" >> /etc/hosts
```

**坑：** 每次 daytona-registry 容器重建（compose down/up），IP 可能变。deploy 和 upgrade 流程都要重新查询并更新 /etc/hosts。

---

## 4. Traefik 反向代理

### 4.1 基本配置

Traefik v3.3，使用 Docker provider（自身）+ File provider（路由规则）。

```yaml
# docker-compose.traefik.yml
services:
  traefik:
    image: traefik:v3.3
    command:
      - --entrypoints.web.address=:80
      - --entrypoints.websecure.address=:443
      - --providers.file.directory=/etc/traefik/dynamic
      - --certificatesresolvers.le.acme.dnschallenge.provider=porkbun
      - --certificatesresolvers.le.acme.email=${ACME_EMAIL}
    environment:
      - PORKBUN_API_KEY=${PORKBUN_API_KEY}
      - PORKBUN_SECRET_KEY=${PORKBUN_SECRET_KEY}
    ports:
      - "80:80"
      - "443:443"
    networks:
      - edge
    volumes:
      - /etc/traefik/dynamic:/etc/traefik/dynamic:ro
      - traefik-acme:/acme
```

### 4.2 路由规则（File provider）

```yaml
# /etc/traefik/dynamic/routes.yml
http:
  routers:
    daytona-dex:
      rule: "Host(`daytona.mobai.beauty`) && PathPrefix(`/dex`)"
      service: dex
      tls: { certResolver: le }

    daytona:
      rule: "Host(`daytona.mobai.beauty`)"
      service: daytona-api
      tls:
        certResolver: le
        domains:
          - main: "mobai.beauty"
            sans: ["*.mobai.beauty"]

    daytona-proxy:
      rule: "HostRegexp(`^.+\\.node\\.proxy\\.mobai\\.beauty$`)"
      service: daytona-proxy
      tls:
        certResolver: le
        domains:
          - main: "proxy.mobai.beauty"
            sans: ["*.proxy.mobai.beauty", "*.node.proxy.mobai.beauty"]

    openhands:
      rule: "Host(`openhands.mobai.beauty`)"
      service: openhands
      tls: { certResolver: le }

  services:
    daytona-api:
      loadBalancer:
        servers: [{ url: "http://daytona-api:3000" }]
    dex:
      loadBalancer:
        servers: [{ url: "http://daytona-dex:5556" }]
    daytona-proxy:
      loadBalancer:
        servers: [{ url: "http://daytona-proxy:4000" }]
    openhands:
      loadBalancer:
        servers: [{ url: "http://openhands:3000" }]
```

**坑：DNS-01 ACME 证书签发需要 1-3 分钟。** deploy 后不能立即访问 HTTPS，需要等 Traefik 完成 ACME challenge。Smoke test 之前要 sleep 或轮询。

**坑：Dex 路由必须在 Daytona 路由前面。** 因为 Dex 的 `/dex` 路径是 Daytona 域名下的子路径。如果 Daytona 路由先匹配了，Dex 就收不到请求。Traefik 按 rule 长度优先，但用 PathPrefix 显式分流更可靠。

**坑：泛域名 TLS 证书。** `*.node.proxy.mobai.beauty` 是二级泛域名，需要在 Traefik 的 `domains.sans` 中显式声明。如果只声明 `*.mobai.beauty`，不会覆盖 `*.node.proxy.mobai.beauty`。

---

## 5. Daytona 9 服务栈

### 5.1 docker-compose.daytona.yml 关键配置

```yaml
services:
  api:
    image: daytonaio/daytona-api:v0.170.0
    container_name: daytona-api
    environment:
      - DATABASE_URL=postgres://daytona:daytona@db:5432/daytona
      - REDIS_URL=redis://redis:6379
      - SERVER_URL=https://daytona.mobai.beauty
      - OIDC_ISSUER_URL=https://daytona.mobai.beauty/dex
      - WILDCARD_DOMAIN=node.proxy.mobai.beauty
    networks: [daytona-network, edge]
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:3000/api/health"]
      interval: 10s
      timeout: 5s
      retries: 12

  runner:
    image: daytonaio/daytona-runner:v0.170.0
    container_name: daytona-runner
    privileged: true
    environment:
      - API_URL=http://api:3000
      - RUNNER_ID=main-runner
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /usr/local/bin/.tmp/binaries:/usr/local/bin/.tmp/binaries  # toolbox binary 挂载
    networks: [daytona-network, runner-bridge]
    ports:
      - "3003:3003"  # runner API（toolbox 转发）

  ssh-gateway:
    image: daytonaio/daytona-ssh-gateway:v0.170.0
    container_name: daytona-ssh-gateway
    environment:
      - API_URL=http://api:3000
      - SSH_PRIVATE_KEY=<base64>
      - SSH_HOST_KEY=<base64>
      - SSH_PUBLIC_KEY=<base64>
      - SSH_GATEWAY_PUBLIC_KEY=<base64>
    networks: [daytona-network, runner-bridge]  # 关键：必须同时在两个网络
    ports:
      - "2222:2222"

  proxy:
    image: daytonaio/daytona-proxy:v0.170.0
    container_name: daytona-proxy
    environment:
      - API_URL=http://api:3000
    networks: [daytona-network, edge, runner-bridge]
    ports:
      - "4000:4000"
```

### 5.2 SSH 密钥注入

Daytona SSH gateway 需要 4 个密钥环境变量（base64 编码的 RSA 4096 密钥）：
- `SSH_PRIVATE_KEY` — gateway 私钥
- `SSH_PUBLIC_KEY` — gateway 公钥
- `SSH_HOST_KEY` — host 私钥（给 sandbox 内部的 SSH daemon 用）
- `SSH_GATEWAY_PUBLIC_KEY` — gateway 公钥的副本

生成方式：
```bash
ssh-keygen -t rsa -b 4096 -f /tmp/daytona-gateway -N ""
ssh-keygen -t rsa -b 4096 -f /tmp/daytona-host -N ""
# base64 编码后用 Python 正则替换到 compose 文件中
```

**坑：密钥必须是 RSA 4096。** Ed25519 不行，Daytona 的 SSH gateway 不支持。

---

## 6. SSH Gateway 配置

### 6.1 网络拓扑

```
外部 SSH 客户端
  → 45.32.25.73:2222
  → daytona-ssh-gateway 容器
  → runner-bridge (10.100.0.x)
  → sandbox 容器内的 toolbox daemon（端口 22102/22103）
  → 用户 shell
```

### 6.2 SSH 使用流程

```bash
# 1. 获取 SSH token
TOKEN=$(curl -s -X POST \
  -H "Authorization: Bearer $API_KEY" \
  https://daytona.mobai.beauty/api/sandbox/$SANDBOX_ID/ssh-access \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')

# 2. SSH 连接
ssh -p 2222 $TOKEN@45.32.25.73
```

**坑（严重）：SSH Gateway 必须在 runner-bridge 网络上。** 原始的 docker-compose 中 ssh-gateway 只在 `daytona-network` 上，没有 `runner-bridge`。导致 ssh-gateway 无法路由到 10.100.0.x 的 sandbox 容器。修复方法：在 ssh-gateway 的 networks 中加上 `runner-bridge`。

这是整个 PoC 中最难发现的 bug 之一，因为错误信息只是"connection refused"，不会告诉你是网络不通。

---

## 7. Toolbox Binary 提取

### 7.1 问题背景

Daytona sandbox 容器启动后，内部需要运行一个 `toolbox daemon`（路径 `/usr/local/bin/.tmp/binaries/daemon-amd64`，55MB）。这个 binary 不在 sandbox 镜像里——它由 runner 通过 volume mount 注入。

但 Daytona 的 runner 容器内有这个 binary，我们需要把它 "提取" 到 VM 宿主机的对应路径上。

### 7.2 提取方法

```bash
BINDIR=/usr/local/bin/.tmp/binaries
mkdir -p "$BINDIR"
docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daemon-amd64 > "$BINDIR/daemon-amd64"
chmod +x "$BINDIR/daemon-amd64"
```

### 7.3 持久化：systemd 服务

每次 VM 重启后 runner 容器可能还没 ready，需要等待。创建 oneshot systemd 服务：

```ini
[Unit]
Description=Extract Daytona toolbox binary from runner container
After=docker.service network.target
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/poc/ensure-toolbox.sh

[Install]
WantedBy=multi-user.target
```

ensure-toolbox.sh 逻辑：循环 30 次，每次 sleep 10s，检测 runner 是否 ready，然后提取。

**坑（严重）：Text file busy。** 如果 sandbox 正在运行（toolbox daemon 正在被执行），直接覆盖这个 binary 会报 `ETXTBSY`。修复方法：先检查文件是否存在且 >10MB（已经是正确的 binary），如果是就跳过。否则写到 `.tmp` 文件再 `mv -f` 原子替换。

**坑：`/usr/local/bin/daytona` is a directory。** 早期测试中有 3 个 sandbox 创建失败，错误是 `exec "/usr/local/bin/daytona": is a directory: permission denied`。原因是 toolbox binary 没有正确提取，`/usr/local/bin/daytona` 路径上存在一个空目录（Daytona runner 创建的占位符）。确保 toolbox binary 正确提取后问题解决。

---

## 8. API Key 管理

### 8.1 Key 格式

生成：`poc-$(openssl rand -hex 20)` → 例如 `poc-adm3f7b2e...041a`（44 字符）

### 8.2 数据库结构

```sql
-- Daytona v0.170.0 的 api_key 表
CREATE TABLE api_key (
  "userId"         varchar NOT NULL,
  name             varchar NOT NULL,
  "createdAt"      timestamp NOT NULL,
  "organizationId" uuid NOT NULL,
  permissions      api_key_permissions_enum[] NOT NULL,
  "keyHash"        varchar NOT NULL DEFAULT '',   -- SHA256 hex
  "keyPrefix"      varchar NOT NULL DEFAULT '',   -- key 的前 7 个字符
  "keySuffix"      varchar NOT NULL DEFAULT '',   -- key 的后 4 个字符
  "lastUsedAt"     timestamp,
  "expiresAt"      timestamp,
  PRIMARY KEY ("userId", name, "organizationId")
);
```

### 8.3 插入方法（正确版）

```sql
DO $$
DECLARE
  org_id uuid;
  user_id text;
BEGIN
  SELECT id INTO org_id FROM organization LIMIT 1;
  SELECT id INTO user_id FROM "user" LIMIT 1;

  INSERT INTO api_key (
    "userId", name, "createdAt", "organizationId",
    permissions, "keyHash", "keyPrefix", "keySuffix"
  ) VALUES (
    user_id,
    'poc-admin',
    NOW(),
    org_id,
    '{write:sandboxes,delete:sandboxes,write:snapshots,delete:snapshots,read:volumes,write:volumes,delete:volumes,read:runners,write:runners,read:audit_logs}',
    '<sha256 hex>',
    '<前7字符>',
    '<后4字符>'
  ) ON CONFLICT DO NOTHING;
END $$;
```

**坑（严重，deploy.sh 中的 bug）：deploy.sh 使用了错误的列名。** 脚本写的是 `INSERT INTO api_key (key, hash, ...)` ，但实际表结构是 `"keyHash"/"keyPrefix"/"keySuffix"`。没有 `key` 和 `hash` 列。这导致 INSERT 静默失败（被 PL/pgSQL 的 ON CONFLICT 吞掉了错误），key 没有入库。

PoC 测试期间实际使用的 key 是通过 Daytona Web UI 手动创建的（不是 deploy.sh 插入的），所以一开始没发现这个 bug。直到 session 恢复后才发现 key 不匹配。

**修复方案：** Go CLI 中用正确的列名，并在 INSERT 后立即 SELECT 验证 key 确实存在。

### 8.4 permissions 枚举值

Daytona v0.170.0 使用 PostgreSQL 枚举数组类型 `api_key_permissions_enum[]`。有效值：

```
write:sandboxes, delete:sandboxes,
write:snapshots, delete:snapshots,
read:volumes, write:volumes, delete:volumes,
read:runners, write:runners,
read:audit_logs
```

注意没有 `read:sandboxes`——读取权限似乎是默认的。

---

## 9. Sandbox 镜像构建

### 9.1 Dockerfile

```dockerfile
FROM daytonaio/sandbox:0.5.0-slim

USER root

RUN apt-get update && apt-get install -y --no-install-recommends \
    git curl wget \
    && rm -rf /var/lib/apt/lists/*

# ttyd 1.7.7 — web terminal
RUN ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then TTYD_ARCH="x86_64"; else TTYD_ARCH="aarch64"; fi && \
    curl -fsSL "https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.${TTYD_ARCH}" \
    -o /usr/local/bin/ttyd && chmod +x /usr/local/bin/ttyd

# Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code --loglevel=error

USER daytona
```

基础镜像 `daytonaio/sandbox:0.5.0-slim` 自带：
- Python 3.11.14
- Node.js 22.14.0（via NVM）
- 基本 Linux 工具

### 9.2 构建 & push 到内部 registry

```bash
cd /opt/poc/sandbox-image
docker build -t mob-sandbox:1.0 -t registry:6000/mob-sandbox:1.0 .
docker push registry:6000/mob-sandbox:1.0
```

**坑：push 到 `registry:6000` 需要 /etc/hosts 和 insecure-registries 都配好。** 两者缺一不可。/etc/hosts 让 `registry` 解析到容器 IP，`insecure-registries` 让 Docker daemon 接受 HTTP push。

---

## 10. Snapshot 注册

Daytona 用 "snapshot" 概念管理沙盒镜像。push 完镜像后要通过 API 注册：

```bash
curl -X POST \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"imageName":"registry:6000/mob-sandbox:1.0","autoStart":true}' \
  https://daytona.mobai.beauty/api/snapshot
```

响应：`{"id":"<snapshot_id>","state":"pulling",...}`

需要轮询直到 `state=active`：
```bash
for i in $(seq 1 60); do
  STATE=$(curl -s -H "Authorization: Bearer $KEY" \
    https://daytona.mobai.beauty/api/snapshot/$SNAP_ID \
    | python3 -c 'import sys,json; print(json.load(sys.stdin).get("state",""))')
  [[ "$STATE" == "active" ]] && break
  sleep 5
done
```

**坑：不要用 `:latest` tag。** Daytona 明确拒绝 `Images with tag ":latest" are not allowed`。必须用带版本号的 tag（如 `mob-sandbox:1.0`）。

---

## 11. OpenHands 部署

### 11.1 架构

OpenHands V1 (`docker.openhands.dev/openhands/openhands:1.6`) 自己管理容器生命周期：
- `openhands` — 主容器（Web UI + API）
- `oh-agent-server-*` — LLM 编排容器（per worker）
- `openhands-runtime-{conv_id}` — 每个对话的独立 runtime 容器

**重要：OpenHands 不使用 Daytona sandbox。** 它使用自己的 Docker-in-Docker runtime。两者是独立的隔离体系。

### 11.2 docker-compose.openhands.yml

```yaml
services:
  openhands:
    image: docker.openhands.dev/openhands/openhands:1.6
    container_name: openhands
    environment:
      - SANDBOX_RUNTIME_CONTAINER_IMAGE=ghcr.io/openhands/runtime:oh_v1.6.0
      - LLM_API_KEY=${LLM_API_KEY}
      - LLM_BASE_URL=https://api.deepseek.com
      - LLM_MODEL=deepseek-v4-pro
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    networks:
      - edge
```

### 11.3 对话 API

```bash
# 创建对话
POST /api/conversations
{"initial_user_msg": "Write hello.py that prints Hello World, run it."}
→ {"status":"ok","conversation_id":"87523c1d...","conversation_status":"STARTING"}

# 查询对话列表
GET /api/conversations
→ {"conversations": [...]}
```

**坑：API 字段名是 `initial_user_msg` 不是 `initial_message`。** 发错字段会返回 422：`Extra inputs are not permitted`。正确的 schema 通过 `GET /openapi.json` 查看 `InitSessionRequest`。

**坑：conversations list 有时返回空。** 创建后立即查询可能返回 0 条，因为对话初始化是异步的。runtime 容器需要几秒才能启动。

---

## 12. Dex OIDC 认证

### 12.1 配置

```yaml
# daytona/dex/config.yaml
issuer: https://daytona.mobai.beauty/dex

staticClients:
  - id: daytona
    redirectURIs: ['https://daytona.mobai.beauty/callback']
    name: 'Daytona'
    secret: daytona-secret

staticPasswords:
  - email: 'admin@mobai.beauty'
    hash: '$2b$10$esalWDfCK3zmYuIfDDffQuU.RypaGZld84QkClst7.eIJsSaMvl76'
    username: 'admin'
    userID: '0001'
```

### 12.2 密码 bcrypt hash 生成

```python
import bcrypt
print(bcrypt.hashpw(b'Mobai2026!', bcrypt.gensalt(rounds=10)).decode())
# → $2b$10$esalWDfCK3zmYuIfDDffQuU.RypaGZld84QkClst7.eIJsSaMvl76
```

**坑（严重）：初始 bcrypt hash 无效。** deploy.sh 中硬编码的 hash `$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W` 对密码 `Mobai2026!` 验证失败。Dex 日志：`failed login attempt: Invalid credentials`。

原因：这个 hash 是手工拼凑的，不是真正用 bcrypt 生成的。必须用正确的工具生成。修复：用 Python bcrypt 重新生成，更新 dex config，重启 dex。

**坑：Dex 重启后所有 session 失效。** Dex 的 signing keys 存储在内存中，重启后轮换。所有浏览器 session 的 JWT token 立即失效。用户需要重新登录。

生产修复方案（v2）：配置 Dex 使用持久化 storage backend（PostgreSQL），或者配置固定 signing key。

---

## 13. Preview URL 机制

### 13.1 获取 Signed Preview URL

```bash
# API 调用
GET /api/sandbox/{sandbox_id}/ports/{port}/signed-preview-url
Authorization: Bearer <api_key>

# 响应
{
  "sandboxId": "592b8065-...",
  "port": 3000,
  "token": "poxvlfmcexwvi69b",
  "url": "https://3000-poxvlfmcexwvi69b.node.proxy.mobai.beauty"
}
```

### 13.2 URL 格式

```
https://{PORT}-{TOKEN}.node.proxy.mobai.beauty
```

- `PORT` — sandbox 内部监听端口
- `TOKEN` — signed token，约 1 小时有效期
- 域名解析到 Traefik → daytona-proxy → runner-bridge → sandbox 容器

### 13.3 完整链路

```
Browser GET https://3000-poxvlfmcexwvi69b.node.proxy.mobai.beauty
  → DNS → 45.32.25.73
  → Traefik (TLS termination, route by HostRegexp)
  → daytona-proxy:4000 (验证 signed token, 查找 sandbox IP)
  → 10.100.0.x:3000 (sandbox 容器内的 HTTP server)
  → HTTP/2 200 ✓
```

**坑：Preview URL 有效期约 1 小时。** 过期后访问返回 307 redirect 到 Dex 登录页。需要重新获取 signed URL。

**坑：新启动的 HTTP server 需要几秒才能被 proxy 感知。** 启动 Node.js server 后立即请求 preview URL 可能返回 502。等 2-3 秒后重试即可。

---

## 14. 全部踩坑清单

按严重程度排序：

### P0 — 阻断性 bug

| # | 问题 | 现象 | 根因 | 修复 |
|---|------|------|------|------|
| 1 | SSH Gateway 网络隔离 | SSH 到 sandbox "connection refused" | ssh-gateway 不在 runner-bridge 网络上 | compose 中 ssh-gateway 加 `runner-bridge` 网络 |
| 2 | API Key DB 字段名错误 | API 返回 401 Unauthorized | deploy.sh 用 `key/hash` 列，实际是 `keyHash/keyPrefix/keySuffix` | 用正确字段名 INSERT |
| 3 | Bcrypt hash 无效 | Dex 登录 "Invalid credentials" | 硬编码的 hash 不是真正 bcrypt 生成 | Python bcrypt 重新生成 |
| 4 | Toolbox binary 缺失 | sandbox 创建失败 "is a directory: permission denied" | /usr/local/bin/daytona 路径是空目录 | 从 runner 容器正确提取 daemon-amd64 |

### P1 — 需要 workaround

| # | 问题 | 现象 | 根因 | Workaround |
|---|------|------|------|------------|
| 5 | Text file busy | 覆盖 toolbox binary 报 ETXTBSY | sandbox 正在使用该 binary | 检查存在则跳过，否则写 .tmp 再 mv |
| 6 | git clone via Toolbox API 失败 | "could not read Username: No such device or address" | Toolbox process executor 无 stdin/tty | 改用 docker exec 直接克隆 |
| 7 | Dex session 重启失效 | 每次 Dex 重启后浏览器要重新登录 | signing keys 存内存 | 文档记录，v2 配持久化 |
| 8 | 创建 sandbox 带资源参数报错 | "Cannot specify resources when using snapshot" | Daytona snapshot 锁定了 CPU/RAM/Disk 配置 | 只发 `{"snapshot":"mob-sandbox:1.0"}` |

### P2 — 易错但有明确解法

| # | 问题 | 现象 | 根因 | 解法 |
|---|------|------|------|------|
| 9 | Vultr SSH KEX 不兼容 | SSH 连接被拒 | Vultr 默认 SSH 不支持标准 KEX | 加 `-o KexAlgorithms=curve25519-sha256` |
| 10 | registry:6000 不可解析 | docker push 失败 | VM hosts 文件没有 registry 条目 | 查容器 IP 写 /etc/hosts |
| 11 | Preview URL 过期 | 浏览器 307 跳转到登录页 | signed token 约 1 小时有效 | 重新获取 signed URL |
| 12 | :latest tag 被拒绝 | snapshot 创建失败 | Daytona 禁止 latest tag | 使用带版本号的 tag |
| 13 | Toolbox response 字段 | 取不到命令输出 | 返回值在 `result` 不是 `output` | 解析 `result` 字段 |
| 14 | OpenHands API 字段名 | 422 Unprocessable Content | 字段是 `initial_user_msg` 不是 `initial_message` | 对照 /openapi.json schema |

---

## 15. CLI 实现注意事项

以下是从 PoC 中提炼的、Go CLI 实现时必须注意的技术要点：

### SSH 连接

- 所有 SSH 操作必须指定 `KexAlgorithms: curve25519-sha256`
- 使用 `golang.org/x/crypto/ssh`，在 `ClientConfig` 中设置 `KeyExchanges: []string{"curve25519-sha256"}`
- SSH 命令执行结果要同时捕获 stdout 和 stderr

### API 调用

- Daytona API base URL 是 `https://daytona.{domain}`（不是 `api.{domain}`）
- Auth header 格式：`Authorization: Bearer {api_key}`
- 沙盒创建只发 `{"snapshot":"mob-sandbox:1.0"}`，不带资源字段
- Signed preview URL endpoint: `GET /api/sandbox/{id}/ports/{port}/signed-preview-url`
- SSH token endpoint: `POST /api/sandbox/{id}/ssh-access` → 响应中取 `token` 字段

### Vultr API

- 使用官方 Go SDK `github.com/vultr/govultr/v3`
- VM halt: `POST /v2/instances/{id}/halt` — 停机但保留实例
- VM start: `POST /v2/instances/{id}/start` — 恢复运行
- 停机后 VM IP 不变，但 Docker 服务全停，需要 stack start 拉起

### Deploy 流程

- 全部 18 步必须顺序执行，任何一步失败要清晰报告是第几步
- 健康检查要有 timeout（API 120s，其他 60s）
- registry /etc/hosts 在每次 compose up 后都要更新
- API key 插入后要 SELECT 验证确实存在
- snapshot 注册后要轮询到 state=active（最多 5 分钟）

### mob code 用户体验

- 创建 sandbox → 等待 started → 获取 SSH token → exec ssh（替换进程）
- 用 `syscall.Exec` 替换当前进程，用户感知是"直接进入了 sandbox"
- 退出 SSH 后回到 mob，提示是否删除 sandbox

### mob openhands 用户体验

- 创建 conversation → 等待 container 启动 → 打开浏览器
- 用 `github.com/pkg/browser` 或 `exec.Command("open", url)` 打开
- 打印 URL 后立即退出，不阻塞终端
