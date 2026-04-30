# Operator 接入流程

新加入的 operator（人 / AI agent）拿到 mob-sandbox 服务器 SSH + 开关机权限的完整步骤。

**核心原则：私钥永远不离开本机。** Operator 本地生成 ed25519 keypair，公钥发给管理员，管理员把公钥分别注册到两个地方：

1. 服务器 `/root/.ssh/authorized_keys` —— 给 SSH 访问
2. Cloudflare Worker `AUTHORIZED_PUBKEYS` —— 给 VPS 开关机

之后 operator 用 SSH 签名向 Worker 证明身份，Worker 持有 Vultr API key 代为调用 —— Vultr key 不下发到 operator。

---

## 角色

- **管理员**: 已经能 SSH 上服务器、有 Cloudflare 账号能部署 Worker 的人。第一次的部署也由管理员完成。
- **Operator**: 新加入的人 / AI agent，需要服务器 SSH + 开关机能力。

---

## 一次性：管理员部署 Worker（如果还没部署）

> 如果 Worker 已经部署了，跳过这一节，直接看 §2。

```bash
cd infra/power-worker
npm install
npx wrangler login                           # 浏览器登录 Cloudflare

# 把 Vultr API key 设为 Worker secret（不进 git）
npx wrangler secret put VULTR_API_KEY
# 粘贴 key, 回车

# 编辑 wrangler.toml，把 VM_ID 设成你的 Vultr 实例 UUID
$EDITOR wrangler.toml

# 把自己（管理员）作为第一个 operator 加入
mob-server operator add admin -f ~/.ssh/id_ed25519.pub
# 输出里会有一行 {"name":"admin","pubkey_b64":"..."}

# 把这一行填进 wrangler.toml 的 AUTHORIZED_PUBKEYS 数组里
$EDITOR wrangler.toml

npx wrangler deploy
# 部署完会打印 Worker URL，类似 https://mob-power.<sub>.workers.dev
```

记下这个 Worker URL，下面 operator 配置时会用到。

测试：

```bash
curl https://mob-power.<sub>.workers.dev/health
# → {"ok":true,"vm_id":"..."}

mob power init                                 # 自己也得配
# 填 Worker URL、operator name="admin"、SSH key 路径
mob power status                               # ✓ 应该返回 VPS 状态
```

---

## 1. Operator：本机生成 keypair

> 已经有 `~/.ssh/id_ed25519` 的话跳过。

```bash
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -N ""
# -N "" 表示不设密码（mob power 暂不支持密码保护的 key）
```

把 `~/.ssh/id_ed25519.pub` 的**纯文本内容**发给管理员（不要 OCR、不要 Markdown 链接、不要包代码块以外的修饰），形如：

```
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF... your_name@your_machine
```

同时告诉管理员你想用的 operator name（要小写字母数字短横线下划线，如 `wangbo` / `kaito` / `agent-1`）。

---

## 2. 管理员：注册 operator

SSH 到服务器：

```bash
ssh -i ~/.ssh/id_ed25519 root@<vm-ip>
```

把 operator 给的 pubkey 写到一个临时文件（避免命令行转义麻烦）：

```bash
cat > /tmp/wangbo.pub <<'PUB'
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF... wangbo@machine
PUB

mob-server operator add wangbo -f /tmp/wangbo.pub
# ✓ Added operator "wangbo" to /root/.ssh/authorized_keys
#   fingerprint: SHA256:...
#
# For Cloudflare Worker AUTHORIZED_PUBKEYS, add this entry:
#   {"name":"wangbo","pubkey_b64":"<base64-of-32-bytes>"}
```

记下打印的 JSON 行。

退回管理员本机，更新 Worker 配置：

```bash
cd infra/power-worker
$EDITOR wrangler.toml
# 在 AUTHORIZED_PUBKEYS 数组里加上刚才那行 {"name":"wangbo","pubkey_b64":"..."}
# 数组是 JSON，记得加逗号

npx wrangler deploy
# Worker 重新部署，新 operator 立即生效
```

把以下信息发回给 operator：

- Worker URL（如 `https://mob-power.<sub>.workers.dev`）
- 服务器 SSH host（IP 或域名）
- Operator name（你刚才用的，例如 `wangbo`）
- mob 客户端 API key（如果 operator 也要用沙盒功能 —— 可选；只想做部署/开关机的话不需要）：`mob-server key create wangbo` 输出

---

## 3. Operator：拉代码 + 配置客户端

```bash
git clone https://github.com/cdotlock/mob-sandbox.git
cd mob-sandbox
make build
sudo make install                              # 把 mob 装到 /usr/local/bin/

# 配置开关机
mob power init
#   ? Power Worker URL: https://mob-power.<sub>.workers.dev
#   ? Operator name:    wangbo
#   ? SSH private key:  (回车用默认 ~/.ssh/id_ed25519)
# ✓ Saved → ~/.config/mob/config.yaml

# 测试开关机
mob power status
# 应该返回类似 {"status":"active","power_status":"running","ip":"...","region":"nrt"}

# 测试 SSH（直接用 SSH 客户端，不需要 mob CLI）
ssh -i ~/.ssh/id_ed25519 root@<vm-ip>
# 第一次会问 yes/no，敲 yes 回车
```

到这一步全部能跑通就接入完成。

---

## 4. （可选）Operator：用沙盒功能

如果 operator 也要用 mob-sandbox 平台开沙盒：

```bash
mob init
#   ? Server URL: https://daytona.<your-domain>   或者 http://<vm-ip>:3986
#   ? API Key:    <从管理员拿>

mob ssh                                        # 一键开沙盒并 SSH 进
mob ps                                         # 看自己的沙盒
```

---

## 撤销 operator

管理员：

```bash
ssh root@<vm-ip>
mob-server operator revoke wangbo

# 退回本机
cd infra/power-worker
$EDITOR wrangler.toml
# 从 AUTHORIZED_PUBKEYS 删除 wangbo 那一行

npx wrangler deploy
```

撤销后 operator 立即失去 SSH 和开关机两个能力。

---

## 故障排查

| 现象 | 原因 | 修法 |
|---|---|---|
| `ssh root@...` permission denied | pubkey 还没加到 `authorized_keys` 或加错了 | 管理员重跑 `operator add`，确认输出的 fingerprint 跟 operator 本机 `ssh-keygen -lf ~/.ssh/id_ed25519.pub` 一致 |
| `mob power status` → `unknown operator` | Worker 的 `AUTHORIZED_PUBKEYS` 没加这个 name，或拼错了 | 管理员检查 wrangler.toml，重新 deploy |
| `mob power status` → `bad signature` | Worker 里的 `pubkey_b64` 跟 operator 实际私钥不匹配 | 重跑 `mob-server operator add` 拿到正确的 pubkey_b64，更新 Worker |
| `mob power status` → `timestamp out of window` | operator 本机时间偏差 > 5 分钟 | 同步系统时间（`sudo ntpdate ...` 或开启系统 NTP） |
| `mob power status` → Vultr 401 | Worker 里的 `VULTR_API_KEY` secret 不对，或这个 key 有 IP 白名单且 Cloudflare 出口 IP 不在白名单内 | `wrangler secret put VULTR_API_KEY` 重设；或在 Vultr 控制台解掉这个 key 的 IP 白名单（Worker 用专门的 key 即可） |
| `mob power start` 报 worker 5xx | Worker 自己挂了，或者 wrangler.toml 里 `VM_ID` / `AUTHORIZED_PUBKEYS` 是占位符 | `npx wrangler tail` 看实时 log，修配置后重新 deploy |
| `parse SSH key` 报错 | 私钥是带密码的，或不是 ed25519 | `ssh-keygen -p -f ~/.ssh/id_ed25519` 去掉密码；或新生成一把 ed25519 |

---

## 安全须知

- **私钥永不外发**：operator 的 `~/.ssh/id_ed25519` 永远只在本机；连截图、聊天记录都不要贴。
- **Vultr API key 永不下发**：Vultr key 只在 Worker secret 里。任何要求把 Vultr key 发给 operator 的步骤都是错的，立即拒绝。
- **wrangler.toml 不放秘密**：里面只有 VM_ID 和 pubkey 列表（pubkey 是公开的，可以进 git）。Vultr key 走 `wrangler secret put`。
- **撤销要彻底**：撤销时**两个地方都要清**（authorized_keys + Worker AUTHORIZED_PUBKEYS），少删一处就漏了。
- **rotate 触发时机**：怀疑私钥泄露 → operator 本机重新 `ssh-keygen` 并通知管理员重新注册；怀疑 Vultr key 泄露 → Vultr 控制台 regenerate + `wrangler secret put VULTR_API_KEY`。
