# mob-sandbox

自托管的 AI 编程沙盒平台。基于 Daytona 提供一键式沙盒环境，内置 Claude Code + OpenHands。

## 架构

```
开发者笔记本                           服务器 (Ubuntu)
┌──────────┐                    ┌──────────────────────────┐
│  mob CLI │───── API/SSH ─────▶│  mob-server daemon       │
│          │                    │  ├─ Daytona (9 services)  │
│  mob init│                    │  ├─ Traefik (TLS)        │
│  mob ssh │                    │  ├─ OpenHands            │
│  mob ps  │                    │  └─ Guardian (保活)       │
└──────────┘                    └──────────────────────────┘
```

## 两个 CLI

### mob-server — 服务端 daemon

运行在服务器上，自动部署和维护沙盒平台。

```bash
mob-server init --domain example.com --dns-provider porkbun --dns-token xxx --llm-key sk-xxx
mob-server status
mob-server key create alice                       # Daytona API key
mob-server key list
mob-server key revoke alice
mob-server operator add alice -f alice.pub        # SSH operator (root 登录)
mob-server operator list
mob-server operator revoke alice
mob-server operator worker-config                 # 输出 CF Worker 用的 JSON
mob-server daemon
```

### mob — 客户端 CLI

运行在开发者笔记本上，一键开沙盒 + VPS 开关机。无参数运行 `mob` 会进入交互式 TUI；也可以用下面的单次命令直接操作。

```bash
mob                               # 打开交互式 TUI
mob tui                           # 显式打开交互式 TUI
mob init                          # 连接服务器
mob create                        # 创建沙盒
mob ssh [id]                      # SSH 进沙盒（不给 id 自动创建）
mob ps                            # 列出沙盒
mob rm <id>                       # 删除沙盒
mob forward <id> <port>           # SSH 隧道转发到 localhost
mob url <id> <port>               # 稳定预览 URL
mob expose <id> <port> [name]     # 永久稳定 URL
mob openhands                     # 打开 OpenHands 浏览器

mob power init                    # 配置 Cloudflare Worker URL + operator 名
mob power start                   # 开机（Vultr API 通过 Worker 调用）
mob power stop                    # 关机
mob power reboot                  # 重启
mob power status                  # 查看 VPS 状态
```

TUI 基于 Charmbracelet Bubble Tea 构建，支持在同一个界面里刷新沙盒、创建沙盒、进入 SSH/Claude Code、保持 localhost 隧道、生成预览 URL、创建永久路由、删除沙盒、打开 OpenHands 和调用 VPS power 控制。SSH/Claude Code 会临时接管终端，退出远程会话后自动回到 TUI。

## 两种模式

- **域名模式**: 自动 TLS、预览 URL、永久子域名路由
- **IP 模式**: 裸 IP 直连，无需域名；稳定 URL 使用 `sslip.io`

## 快速开始

### 构建

```bash
make build          # macOS 二进制
make build-linux    # Linux amd64 二进制
make package        # dist/ 下生成 release 资产和 checksums.txt
make install        # 安装到 /usr/local/bin
```

### 服务端部署

```bash
# 上传 mob-server 到服务器
scp bin/mob-server-linux-amd64 root@your-server:/usr/local/bin/mob-server

# SSH 到服务器运行 init
mob-server init --ssh-host your-server --ssh-key ~/.ssh/id_ed25519
```

### 客户端使用

```bash
mob init    # 输入服务器地址和 API key
mob         # 打开 TUI，选择创建沙盒或进入 SSH/Claude Code
```

## 沙盒环境

每个沙盒包含：
- Claude Code 2.1.123
- Python 3.11 + Node 22
- ttyd (Web 终端)
- Git, curl, wget

## 端口暴露方案

| 方案 | 命令 | 适用 | URL 格式 | 场景 |
|------|------|------|----------|------|
| SSH 隧道 | `mob forward` | 所有模式 | `http://localhost:<port>` | 自己看效果 |
| 预览 URL | `mob url` | 所有模式 | IP: `http://<name>.<server-ip>.sslip.io:9876`；域名: `https://<name>.<domain>` | 临时分享 |
| 永久稳定 URL | `mob expose` | 所有模式 | IP: `http://<name>.<server-ip>.sslip.io:9876`；域名: `https://<name>.<domain>` | 长期对外 |

稳定 URL 可以带服务守护命令。`mob-server daemon` 会自动启动对应 sandbox，并在端口不可达时执行 `--start-command`：

```bash
mob expose <id> 8888 moonshort \
  --health-path /health \
  --start-command 'cd /home/daytona/moonshort-script; nohup python3 -m uvicorn api_server:app --host 0.0.0.0 --port 8888 > api_server.log 2>&1 &'
```

## 项目结构

```
mob-sandbox/
├── cmd/mob-server/     服务端 CLI
├── cmd/mob/            客户端 CLI
├── pkg/
│   ├── config/         配置读写
│   ├── ui/             终端 UI（spinner、颜色）
│   ├── remote/         SSH 客户端 + 端口转发
│   ├── daytona/        Daytona REST API 客户端
│   ├── embedded/       go:embed 模板（compose/Dockerfile）
│   ├── dns/            DNS provider（cloudflare/porkbun/manual）
│   ├── deploy/         20 步 init 部署流程
│   ├── guardian/       daemon 保活/修复/清理
│   ├── control/        HTTP 控制 API（expose 路由管理）
│   └── power/          客户端 Vultr 开关机（SSH 签名 → CF Worker）
├── infra/
│   └── power-worker/   Cloudflare Worker：Vultr 开关机代理（SSH 验签）
├── poc/                PoC bash 脚本（deploy.sh 等）
├── docs/               设计文档、实现报告、运维手册
├── Makefile
└── install.sh
```

## 多 operator 协作

任意一个新加入的 operator（人 / AI agent）都可以拿到 SSH + 开关机权限，**无需共享私钥或 Vultr key**。流程：

1. operator 本机 `ssh-keygen -t ed25519` 生成自己的 keypair
2. 把 pubkey 给管理员
3. 管理员 SSH 到服务器跑 `mob-server operator add <name> -f <name>.pub`
4. 管理员把命令打印的 `{"name":..., "pubkey_b64":...}` 加到 `infra/power-worker/wrangler.toml` 的 `AUTHORIZED_PUBKEYS` 数组里
5. 管理员 `cd infra/power-worker && npx wrangler deploy` 重新部署 Worker
6. operator 跑 `mob power init` 填 Worker URL 和自己的 name
7. 测试：`mob power status` ✓

撤销：管理员 `mob-server operator revoke <name>`，从 Worker 配置里删掉对应条目，重新部署。

完整 onboarding 步骤见 [docs/operator-onboarding.md](docs/operator-onboarding.md)。

## 文档

- [Operator 接入流程](docs/operator-onboarding.md)
- [CLI 设计 Spec](docs/mob-cli-design-spec.md)
- [实现报告（踩坑全记录）](docs/mob-sandbox-implementation-report.md)
- [Vultr + Porkbun 运维手册](docs/ops-vultr-porkbun-runbook.md)
- [Cloudflare Worker（开关机代理）](infra/power-worker/README.md)
- [测试报告](docs/mob-sandbox-platform-test-report-2026-04-29.md)

## 依赖

- Go 1.23+
- Docker CE (服务端)
- cobra, golang.org/x/crypto/ssh, fatih/color, briandowns/spinner
