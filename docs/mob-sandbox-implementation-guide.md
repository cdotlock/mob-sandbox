---
title: mob-sandbox 平台完整实现指南
---

# mob-sandbox 平台完整实现指南

完整报告见项目仓库：`docs/mob-sandbox-implementation-report.md`

本 wiki 页为核心要点索引，详细步骤、代码片段、踩坑记录请查阅原始报告。

## 架构

```
Traefik v3.3 (TLS, DNS-01 ACME via Porkbun)
  ├─ daytona.{domain}           → daytona-api:3000
  ├─ openhands.{domain}         → openhands:3000
  └─ *.node.proxy.{domain}     → daytona-proxy:4000

Docker networks:
  edge              ← Traefik + 外部暴露容器
  daytona-network   ← Daytona 9 服务
  runner-bridge     ← sandbox 容器 (10.100.0.0/24)
```

## 部署 18 步（deploy.sh / mob-server deploy）

1. 验证配置 2. DNS 3. Bootstrap VM 4. 上传文件
5. 生成 SSH 密钥（RSA 4096） 6. 生成 API key（SHA256）
7. Traefik 8. Daytona stack 9. 等健康检查
10. 提取 toolbox binary 11. systemd 服务 12. registry /etc/hosts
13. API key 入 DB 14. 构建镜像 15. 注册 snapshot
16. OpenHands 17. Smoke test 18. 输出信息

## P0 级踩坑（阻断性）

| 问题 | 修复 |
|------|------|
| SSH Gateway 没接 runner-bridge 网络 | compose 加 runner-bridge |
| API key DB 字段名 key/hash 不存在 | 用 keyHash/keyPrefix/keySuffix |
| Dex bcrypt hash 无效 | Python bcrypt 重新生成 |
| Toolbox binary 缺失 | 从 runner 容器 cat 提取 |

## 两个 Go CLI

- **mob-server**: 运维侧，deploy/destroy/vm create|start|stop/status/logs/key
- **mob**: 消费侧，sandbox ls|create|rm/code/openhands/ps

设计 spec: `docs/superpowers/specs/2026-04-29-mob-cli-design.md`
