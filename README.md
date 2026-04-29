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
mob-server key create alice
mob-server key list
mob-server key revoke alice
mob-server daemon
```

### mob — 客户端 CLI

运行在开发者笔记本上，一键开沙盒。

```bash
mob init                          # 连接服务器
mob create                        # 创建沙盒
mob ssh [id]                      # SSH 进沙盒（不给 id 自动创建）
mob ps                            # 列出沙盒
mob rm <id>                       # 删除沙盒
mob forward <id> <port>           # SSH 隧道转发到 localhost
mob url <id> <port>               # 预览 URL（域名模式）
mob expose <id> <port> [name]     # 永久子域名路由（域名模式）
mob openhands                     # 打开 OpenHands 浏览器
```

## 两种模式

- **域名模式**: 自动 TLS、预览 URL、永久子域名路由
- **IP 模式**: 裸 IP 直连，无需域名，用 SSH 隧道转发端口

## 快速开始

### 构建

```bash
make build          # macOS 二进制
make build-linux    # Linux amd64 二进制
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
mob ssh     # 创建沙盒并 SSH 进入
```

## 沙盒环境

每个沙盒包含：
- Claude Code 2.1.123
- Python 3.11 + Node 22
- ttyd (Web 终端)
- Git, curl, wget

## 端口暴露方案

| 方案 | 命令 | 适用 | 场景 |
|------|------|------|------|
| SSH 隧道 | `mob forward` | 所有模式 | 自己看效果 |
| 预览 URL | `mob url` | 域名模式 | 临时分享 |
| 永久子域名 | `mob expose` | 域名模式 | 长期对外 |

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
│   └── control/        HTTP 控制 API（expose 路由管理）
├── poc/                PoC bash 脚本（deploy.sh 等）
├── docs/               设计文档、实现报告、运维手册
├── Makefile
└── install.sh
```

## 文档

- [CLI 设计 Spec](docs/mob-cli-design-spec.md)
- [实现报告（踩坑全记录）](docs/mob-sandbox-implementation-report.md)
- [Vultr + Porkbun 运维手册](docs/ops-vultr-porkbun-runbook.md)
- [测试报告](docs/mob-sandbox-platform-test-report-2026-04-29.md)

## 依赖

- Go 1.23+
- Docker CE (服务端)
- cobra, golang.org/x/crypto/ssh, fatih/color, briandowns/spinner
