# mob-sandbox CLI Design Spec

Date: 2026-04-29  
Repo: https://github.com/cdotlock/mob-sandbox  
Status: Final

---

## 核心理念

**mob-server** 运行在服务器上，是自治 daemon，自动维护沙盒平台 24/7。  
**mob** 运行在开发者笔记本上，轻量客户端，一键开沙盒。  
不绑定云商、域名商。有域名走 TLS，没域名裸 IP 直连。

---

## 三种端口暴露方案

沙盒里跑的服务怎么从外面访问，三种递进的方案：

| 方案 | 命令 | 时效 | 模式 | 适用场景 |
|------|------|------|------|----------|
| **A. SSH 隧道** | `mob forward` | 命令运行时 | 都支持 | 自己看效果 |
| **B. 签名 URL** | `mob url` | 1 小时 | 仅域名模式 | 临时分享给别人 |
| **C. 永久子域名** | `mob expose` | 永久 | 仅域名模式 | 长期暴露/对外 demo |

方案 A 最通用（裸 IP 也能用），方案 B/C 需要域名模式。

---

## mob-server — 服务端 daemon

运行在服务器上。自包含单二进制，compose/Dockerfile/配置 go:embed 内嵌。

### 命令

```
mob-server init [flags]             一次性引导安装，幂等
mob-server status                   服务健康状态 + 沙盒统计
mob-server key create <name>        创建团队 API key
mob-server key list                 列出 key
mob-server key revoke <name>        吊销 key
mob-server expose <sb> <port> [name]    管理永久子域名路由（仅域名模式）
mob-server daemon                   前台运行（systemd 调用，平时不手动跑）
```

### init flags

```
--domain <domain>           域名模式（自动 Traefik + TLS）
--dns-provider <name>       cloudflare / porkbun（自动配 DNS）
--dns-token <token>         DNS API token
--llm-key <key>             LLM API key（不给就不部署 OpenHands）
--llm-url <url>             默认 https://api.deepseek.com
--llm-model <model>         默认 deepseek-v4-pro
```

不给 `--domain` = IP-only 模式。

### init 流程（20 步）

```
 1. 检测系统（OS、内存、磁盘）
 2. 安装 Docker CE
 3. 配置 Docker daemon（insecure-registries: registry:6000）
 4. 配置 UFW 防火墙
 5. 释放内嵌文件到 /opt/mob-sandbox/
 6. 生成 Daytona SSH 密钥对（RSA 4096）
 7. 生成管理员 API key + SHA256
 8. 探测公网 IP
 9. [域名] 自动配 DNS  |  [IP] 跳过
10. 生成 compose override（端口/URL/WILDCARD_DOMAIN）
11. [域名] 部署 Traefik + ACME  |  [IP] 跳过
12. 启动 Daytona stack
13. 等待 daytona-api 健康（轮询，120s）
14. 提取 toolbox binary 到宿主机
15. 配置 registry /etc/hosts
16. API key 入 DB（keyHash/keyPrefix/keySuffix，已修复 bug）
17. 构建并 push mob-sandbox 镜像
18. 注册 Daytona snapshot（等待 state=active）
19. [有 LLM key] 部署 OpenHands
20. 注册 systemd service + 启动 daemon

最后打印连接信息（API URL、SSH 地址、管理员 key）
```

### daemon 职责（systemd 常驻）

- **保活**: 每 30s 检查 Docker 服务健康；连续 3 次失败重启
- **修复**: toolbox binary 丢失自动提取；registry hosts 容器重建后失效自动修复
- **清理**: error 状态沙盒超 1 小时删除；孤立 OpenHands runtime 容器清理
- **控制 API**: 监听本机端口（域名模式经 Traefik 暴露），处理 mob 客户端的 `expose` 等请求

### 控制 API（mob 客户端调用）

端点（全部需要 Daytona API key Bearer 认证）：
- `POST /control/v1/expose` — 创建永久路由
- `GET /control/v1/expose` — 列出当前用户的永久路由
- `DELETE /control/v1/expose/{name}` — 删除路由

实现：修改 `/etc/traefik/dynamic/routes.yml`，添加 router + service，热加载（Traefik 文件 provider 自动重载）。

### 配置：`/etc/mob-server/config.yaml`

```yaml
mode: "ip"                          # ip 或 domain
public_ip: "203.0.113.50"
domain: ""
daytona_api_key: "poc-..."
ports:
  api: 3986
  ssh: 2222
  openhands: 3000
  proxy: 4000
  control: 9876                     # mob-server 控制 API 端口
llm:
  api_key: ""
  base_url: "https://api.deepseek.com"
  model: "deepseek-v4-pro"
```

### status 输出

```
mob-sandbox · 203.0.113.50 · IP mode · up 3d 14h

  daytona-api     ✓    daytona-db     ✓    openhands     ✓
  daytona-runner  ✓    daytona-redis  ✓    toolbox       ✓
  daytona-proxy   ✓    daytona-minio  ✓
  ssh-gateway     ✓    dex            ✓

  sandboxes: 5 running · 2 stopped
  keys: 3 active · routes: 2 permanent
```

---

## mob — 客户端 CLI

运行在开发者笔记本上。只需要 server URL + API key。

### 命令（9 条）

```
mob init                            连接服务器，自动探测并保存配置
mob create                          创建沙盒，打印 ID
mob ssh [id]                        SSH 进沙盒（不给 id = 自动创建新的再进）
mob ps                              列出我的沙盒
mob rm <id>                         删除沙盒
mob forward <id> <port>             SSH 隧道转发到 localhost（方案 A）
mob url <id> <port>                 拿一个 1 小时签名 URL（方案 B，域名模式）
mob expose <id> <port> [name]       永久子域名路由（方案 C，域名模式）
mob openhands                       打开 OpenHands 浏览器页
```

### 配置：`~/.config/mob/config.yaml`

```yaml
server: "http://203.0.113.50:3986"
api_key: "poc-..."
ssh_host: "203.0.113.50"
ssh_port: 2222
openhands: "http://203.0.113.50:3000"
control: "http://203.0.113.50:9876"      # mob-server 控制 API
mode: "ip"                                # ip 或 domain
```

`mode` 通过 init 时探测自动写入，决定 url/expose 是否可用。

### mob init

```
$ mob init
? Server: http://203.0.113.50:3986
? API Key: poc-a3f7b2e19c...
  ✓ 连接成功 (mode: ip)
  ✓ SSH 203.0.113.50:2222
  ✓ OpenHands 203.0.113.50:3000
  Saved → ~/.config/mob/config.yaml
```

探测逻辑：
1. `GET {server}/api/health` 验证连通 + 取版本
2. 探测 SSH gateway 端口（默认 2222）
3. 探测 OpenHands 端口（默认 3000，可能 12000）
4. `GET {server}/api/info` 探测 mode（ip / domain）

### mob ssh

```
$ mob ssh
  ● 创建沙盒...
  ✓ a1b2c3d4 就绪 (3s)

daytona@a1b2c3d4:~$ claude
```

不传 id：创建新沙盒 → SSH 进入。  
传 id：直接 SSH 进入已有沙盒。

### mob forward / url / expose

```
$ mob forward a1b2c3d4 3000
  ✓ http://localhost:38291 → sandbox:3000 (Ctrl+C 断开)

$ mob url a1b2c3d4 3000
  https://3000-poxvlfmcexwvi69b.node.proxy.example.com  (有效期 1h)

$ mob expose a1b2c3d4 3000 mydemo
  ✓ https://mydemo.example.com → sandbox a1b2c3d4:3000  (永久)
```

`url` 在 IP 模式下会报错：`Use mob forward instead (IP mode)`。  
`expose` 在 IP 模式下报错：`Requires domain mode`。

### mob openhands

```
$ mob openhands
  ✓ Opened http://203.0.113.50:3000
```

直接打开浏览器。在 OpenHands UI 里建对话、下任务，OpenHands 自己管理它的 runtime 容器。

---

## 仓库结构

```
mob-sandbox/
├── cmd/
│   ├── mob-server/main.go           
│   └── mob/main.go
├── pkg/
│   ├── daytona/          Daytona REST API client
│   ├── deploy/           init 安装编排（20 步）
│   ├── guardian/         daemon 保活逻辑
│   ├── control/          mob-server HTTP 控制 API
│   ├── dns/              DNS provider（cloudflare、porkbun、manual）
│   ├── remote/           SSH 连接、端口转发
│   ├── config/           配置读写
│   ├── embedded/         go:embed 内嵌资源
│   └── ui/               spinner、表格、颜色
├── embed/
│   ├── docker-compose.daytona.yml
│   ├── docker-compose.traefik.yml
│   ├── docker-compose.openhands.yml
│   ├── Dockerfile.sandbox
│   ├── dex-config.yaml.tmpl
│   └── traefik-routes.yml.tmpl
├── install.sh
├── poc/                  原有脚本保留
├── go.mod
└── Makefile
```

主要依赖：
- `github.com/spf13/cobra` — CLI
- `github.com/spf13/viper` — 配置
- `golang.org/x/crypto/ssh` — SSH
- `github.com/briandowns/spinner` — 终端进度

---

## 已知坑（实现时必须处理）

| 坑 | 处理位置 |
|----|----------|
| SSH KEX 必须 curve25519-sha256 | pkg/remote 固定 KeyExchanges |
| DB 字段 keyHash/keyPrefix/keySuffix（不是 key/hash） | deploy step 16 |
| SSH gateway 必须在 runner-bridge 网络上 | embed/docker-compose 已修复 |
| toolbox binary ETXTBSY | guardian 原子替换（写 .tmp 再 mv） |
| registry hosts 容器重建失效 | guardian 自动修复 |
| snapshot 不能用 :latest | 固定 mob-sandbox:1.0 |
| sandbox 创建不能带 cpu/memory/disk | 只发 {"snapshot":"..."} |
| OpenHands API 字段是 initial_user_msg | pkg/daytona 正确字段 |
| Daytona response 字段是 result（非 output） | pkg/daytona 解析正确字段 |
| Dex 重启丢 session | guardian 避免不必要的 Dex 重启 |

---

## 极简实现计划

按顺序实现，每步可独立验证：

### Phase 1 — 仓库骨架（30min）
1. `git clone https://github.com/cdotlock/mob-sandbox.git`，初始化 go.mod、Makefile、目录结构
2. `embed/` 下放好 compose 文件、Dockerfile（从 poc/ 复制并修复已知 bug）
3. 写 install.sh（下载二进制 → /usr/local/bin/）

### Phase 2 — 共享基础包（2-3h）
4. **pkg/config**: 读写 yaml 配置（mob-server 用 /etc/，mob 用 ~/.config/）
5. **pkg/ui**: spinner、彩色输出、表格（基于 fatih/color + briandowns/spinner）
6. **pkg/remote**: SSH 客户端 + 命令执行 + 端口转发（KEX 固定 curve25519-sha256）
7. **pkg/daytona**: Daytona REST client（sandbox CRUD、ssh-access、signed URL、snapshot、API key DB 操作）

### Phase 3 — 服务端核心（4-5h）
8. **pkg/embedded**: go:embed 加载 compose/Dockerfile，写入磁盘
9. **pkg/dns**: DNS provider 接口 + cloudflare/porkbun 实现
10. **pkg/deploy**: 20 步 init 流程，每步幂等 + 可恢复
11. **pkg/guardian**: 健康检查循环、自动修复、清理逻辑
12. **pkg/control**: HTTP API（expose 路由管理、Traefik routes.yml 修改）

### Phase 4 — 命令绑定（2-3h）
13. **cmd/mob-server**: cobra 绑定 init/status/key/expose/daemon
14. **cmd/mob**: cobra 绑定 init/create/ssh/ps/rm/forward/url/expose/openhands

### Phase 5 — 验证（1-2h）
15. 在测试 VM 上 `mob-server init` 全流程跑通
16. 从笔记本 `mob init` → `mob ssh` → 沙盒里 `claude --version` 通
17. `mob forward` 端口转发通
18. 域名模式下 `mob url` 和 `mob expose` 通
19. 推送到 GitHub

总预计 10-13h 工作量。

### 验收标准

每条命令必须能在测试 VM 上跑通：

**mob-server:**
- [ ] `mob-server init` 在裸 Ubuntu 服务器上一次性跑完，幂等可重跑
- [ ] `mob-server status` 输出准确反映服务状态
- [ ] `mob-server key create alice` 输出可用 key
- [ ] daemon 自动恢复：手动 `docker stop daytona-api`，30s 内自愈

**mob:**
- [ ] `mob init` + `mob ssh` 能在 5s 内进入沙盒
- [ ] 沙盒里 `claude --version` 输出 2.1.123
- [ ] `mob forward` 转发的端口可在浏览器访问
- [ ] `mob openhands` 打开浏览器到 OpenHands 页面
- [ ] `mob rm` 后 `mob ps` 不再显示

---

## 不在范围内（v1）

- 沙盒持久化卷
- 多节点集群
- Web 管理 UI
- Claude Code 自动登录
- OpenHands 任务结果回传
- Webhook/告警通知
- 沙盒资源限制配额
