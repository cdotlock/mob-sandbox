# Vultr 服务器 + Porkbun 域名 运维手册

给 coding agent 用的完整操作手册。所有操作通过 API 完成，不需要登录 Web 界面。

> **⚠️ 安全提醒**
>
> 这份 runbook 早期版本曾把 Vultr API key、Porkbun API key/secret 以**明文**形式提交到了 git 历史里。即使现在已经替换为占位符，**已泄露的 key 仍在 git history 中**，必须立即在对应控制台 rotate（重新生成）：
> - Vultr: https://my.vultr.com/settings/#settingsapi （Re-generate API Key）
> - Porkbun: https://porkbun.com/account/api （Delete + Create New）
>
> rotate 之后把新值写入本机 `.env`（**已 gitignore，不会再进库**），不要再贴任何明文进 markdown / 代码 / commit message。

---

## 凭证管理

所有凭证从本机 `.env`（gitignored）读取。复制 `.env.example` 到 `.env`，填好以下字段：

```
VULTR_API_KEY=<your-vultr-api-key>
PORKBUN_API_KEY=<your-porkbun-api-key>
PORKBUN_SECRET_KEY=<your-porkbun-secret-key>
```

跑 runbook 里的命令前先 `set -a && source .env && set +a`，所有 `${VULTR_API_KEY}` 等变量就有了。

实例信息（这些不是密钥，公开记下没问题）：
- VM_ID: 在 `.env` 里 `VM_ID=...`（Vultr 实例 UUID）
- VM_IP: 在 `.env` 里 `VM_IP=...`（VPS 公网 IP）
- 域名: 在 `.env` 里 `DOMAIN=...`
- SSH key: 默认 `~/.ssh/poc_ed25519`（私钥永远不上传，不进 git）
- SSH 连接: `ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@${VM_IP}`

---

## 1. Vultr 服务器操作

### API 基础

```bash
# 从 .env 加载（推荐）
set -a && source .env && set +a
VULTR_API="https://api.vultr.com/v2"
```

所有请求带 header: `Authorization: Bearer ${VULTR_API_KEY}`

### 1.1 查看当前服务器状态

```bash
curl -sf "$VULTR_API/instances/${VM_ID}" \
  -H "Authorization: Bearer ${VULTR_API_KEY}" \
  | python3 -c "
import sys,json
d = json.load(sys.stdin)['instance']
print(f'Status: {d[\"status\"]}  Power: {d[\"power_status\"]}  IP: {d[\"main_ip\"]}')"
```

- `power_status: running` = 开机中
- `power_status: stopped` = 已关机（不计算费用，但保留磁盘和 IP）

### 1.2 开机

```bash
curl -sf -X POST "$VULTR_API/instances/${VM_ID}/start" \
  -H "Authorization: Bearer ${VULTR_API_KEY}"
```

开机后需要等 30-60 秒 SSH 才可用。Docker 服务会自动启动（systemd），但沙盒平台的 compose stack 可能需要手动拉起：

```bash
ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@${VM_IP} \
  "cd /opt/poc && docker compose -f docker-compose.traefik.yml up -d && \
   docker compose -f docker-compose.daytona.yml up -d && \
   docker compose -f docker-compose.openhands.yml up -d"
```

> **提示：** 协作场景下，optr 不需要直接拿 `VULTR_API_KEY`。
> 推荐用 `mob power start/stop/status`（走 Cloudflare Worker，SSH 私钥签名验证），见 [`infra/power-worker/README.md`](../infra/power-worker/README.md)。

### 1.3 关机

```bash
curl -sf -X POST "$VULTR_API/instances/${VM_ID}/halt" \
  -H "Authorization: Bearer ${VULTR_API_KEY}"
```

关机保留 IP 和磁盘数据。下次开机一切还在。

### 1.4 重启

```bash
curl -sf -X POST "$VULTR_API/instances/${VM_ID}/reboot" \
  -H "Authorization: Bearer ${VULTR_API_KEY}"
```

### 1.5 创建新服务器

如果需要从零开一台新的：

```bash
# 列出可用机房（推荐 nrt = Tokyo）
curl -sf "$VULTR_API/regions" -H "Authorization: Bearer ${VULTR_API_KEY}" \
  | python3 -c "import sys,json; [print(f'{r[\"id\"]:6} {r[\"city\"]}') for r in json.load(sys.stdin)['regions']]"

# 列出可用机型
curl -sf "$VULTR_API/plans" -H "Authorization: Bearer ${VULTR_API_KEY}" \
  | python3 -c "
import sys,json
for p in json.load(sys.stdin)['plans']:
    if p['type'] == 'vc2' and p['vcpu_count'] >= 4 and p['ram'] >= 8192:
        print(f'{p[\"id\"]:20} {p[\"vcpu_count\"]}C/{p[\"ram\"]//1024}G \${p[\"monthly_cost\"]}/mo')"

# 列出已上传的 SSH key
curl -sf "$VULTR_API/ssh-keys" -H "Authorization: Bearer ${VULTR_API_KEY}" \
  | python3 -c "import sys,json; [print(f'{k[\"id\"]}  {k[\"name\"]}') for k in json.load(sys.stdin)['ssh_keys']]"

# 创建实例（sshkey_id 从上一条命令拿）
curl -sf -X POST "$VULTR_API/instances" \
  -H "Authorization: Bearer ${VULTR_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{
    \"region\": \"nrt\",
    \"plan\": \"vc2-4c-8gb\",
    \"os_id\": 2284,
    \"label\": \"mob-sandbox\",
    \"sshkey_id\": [\"${VULTR_SSH_KEY_ID}\"],
    \"backups\": \"disabled\"
  }"
```

- `os_id: 2284` = Ubuntu 24.04 LTS（用 `GET /v2/os` 查最新 ID）
- 创建后 1-2 分钟拿到 IP，5 分钟 SSH 可用
- 记下返回的 `id` 和 `main_ip`

### 1.6 上传新 SSH key

```bash
PUB_KEY=$(cat ~/.ssh/poc_ed25519.pub)
curl -sf -X POST "$VULTR_API/ssh-keys" \
  -H "Authorization: Bearer ${VULTR_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"mob-deploy\",\"ssh_key\":\"$PUB_KEY\"}"
```

### 1.7 销毁服务器（不可逆！）

```bash
curl -sf -X DELETE "$VULTR_API/instances/${VM_ID}" \
  -H "Authorization: Bearer ${VULTR_API_KEY}"
```

---

## 2. Porkbun 域名操作

### API 基础

```bash
# 从 .env 加载（推荐）
set -a && source .env && set +a
PB_API="https://api.porkbun.com/api/json/v3"
```

所有请求用 POST，body 里带 apikey + secretapikey。

### 2.1 列出当前 DNS 记录

```bash
curl -sf -X POST "$PB_API/dns/retrieve/${DOMAIN}" \
  -H "Content-Type: application/json" \
  -d "{\"apikey\":\"${PORKBUN_API_KEY}\",\"secretapikey\":\"${PORKBUN_SECRET_KEY}\"}" \
  | python3 -c "
import sys,json
for r in json.load(sys.stdin).get('records',[]):
    print(f'{r[\"id\"]:>12}  {r[\"type\"]:5}  {r[\"name\"]:40}  {r[\"content\"]}')"
```

### 2.2 创建 A 记录

```bash
pb_create() {
  local name="$1" ip="$2"
  curl -sf -X POST "$PB_API/dns/create/${DOMAIN}" \
    -H "Content-Type: application/json" \
    -d "{\"apikey\":\"${PORKBUN_API_KEY}\",\"secretapikey\":\"${PORKBUN_SECRET_KEY}\",\"name\":\"$name\",\"type\":\"A\",\"content\":\"$ip\",\"ttl\":\"300\"}"
}
```

mob-sandbox 平台所需的完整 DNS 记录（IP 来自 `.env` 里的 `VM_IP`）：

```bash
pb_create ""              "${VM_IP}"   # ${DOMAIN}
pb_create "*"             "${VM_IP}"   # *.${DOMAIN}
pb_create "daytona"       "${VM_IP}"   # daytona.${DOMAIN}
pb_create "openhands"     "${VM_IP}"   # openhands.${DOMAIN}
pb_create "*.proxy"       "${VM_IP}"   # *.proxy.${DOMAIN}
pb_create "*.node.proxy"  "${VM_IP}"   # *.node.proxy.${DOMAIN} — Daytona 预览 URL
```

**注意：** Porkbun 泛域名 name 格式是 `*.node.proxy`（不是 `node.proxy.*`）。

### 2.3 删除 DNS 记录

```bash
# 需要记录的 ID（从 retrieve 接口拿）
RECORD_ID="123456789"
curl -sf -X POST "$PB_API/dns/delete/${DOMAIN}/$RECORD_ID" \
  -H "Content-Type: application/json" \
  -d "{\"apikey\":\"${PORKBUN_API_KEY}\",\"secretapikey\":\"${PORKBUN_SECRET_KEY}\"}"
```

### 2.4 修改 DNS 记录（IP 变了的时候）

如果换了服务器 IP，需要更新所有 A 记录：

```bash
NEW_IP="1.2.3.4"
# 先 retrieve 拿到所有记录 ID，然后逐个修改
curl -sf -X POST "$PB_API/dns/editByNameType/${DOMAIN}/A" \
  -H "Content-Type: application/json" \
  -d "{\"apikey\":\"${PORKBUN_API_KEY}\",\"secretapikey\":\"${PORKBUN_SECRET_KEY}\",\"content\":\"$NEW_IP\",\"ttl\":\"300\"}"
```

或者更简单：删掉所有旧记录，重建新的（用 `poc/setup-dns.sh` 就是这个逻辑）。

### 2.5 验证 DNS 生效

```bash
dig +short daytona.${DOMAIN} @8.8.8.8
dig +short openhands.${DOMAIN} @8.8.8.8
dig +short test.node.proxy.${DOMAIN} @8.8.8.8
```

Porkbun TTL 300 秒，实际生效通常 1-2 分钟。

---

## 3. SSH 连接注意事项

Vultr 的 SSH 有一个必须注意的坑：

```bash
# 必须指定 KEX 算法，否则 connection refused
ssh -o KexAlgorithms=curve25519-sha256 \
    -o StrictHostKeyChecking=no \
    -i ~/.ssh/poc_ed25519 \
    root@${VM_IP}
```

这个 `-o KexAlgorithms=curve25519-sha256` 在所有 SSH/SCP 操作中都必须带上。

### SCP 文件到服务器

```bash
scp -o KexAlgorithms=curve25519-sha256 \
    -o StrictHostKeyChecking=no \
    -i ~/.ssh/poc_ed25519 \
    localfile root@${VM_IP}:/remote/path
```

### 多 operator 协作

如果多个人/AI 需要 SSH 上服务器，**不要分发同一把私钥**。每个人本机生成自己的 keypair，把 pubkey 交给管理员，管理员跑：

```bash
# 在服务器上（已 SSH 登录）
mob-server operator add <name> --pubkey-file <name>.pub
```

这条命令会把 pubkey 追加到 `/root/.ssh/authorized_keys`，并打印 Cloudflare Worker 用的 base64 公钥（用于开关机授权，见 [`infra/power-worker/README.md`](../infra/power-worker/README.md)）。

要撤销访问：

```bash
mob-server operator revoke <name>
```

---

## 4. 典型操作流程

### 4.1 日常：开机 → 验证 → 使用 → 关机

**推荐路径（走 Cloudflare Worker，operator 不需要 Vultr key）：**

```bash
mob power start                                  # 开机
sleep 60
ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/id_ed25519 root@${VM_IP} "mob-server status"
mob power stop                                   # 关机
```

**裸 API 路径（管理员调试用）：**

```bash
# 1. 开机
curl -sf -X POST "$VULTR_API/instances/${VM_ID}/start" -H "Authorization: Bearer ${VULTR_API_KEY}"

# 2. 等 60 秒，验证 SSH
sleep 60
ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@${VM_IP} "mob-server status"

# 3. 如果 compose stack 没自动起来
ssh ... "cd /opt/poc && docker compose -f docker-compose.traefik.yml up -d && docker compose -f docker-compose.daytona.yml up -d && docker compose -f docker-compose.openhands.yml up -d"

# 4. 用完后关机
curl -sf -X POST "$VULTR_API/instances/${VM_ID}/halt" -H "Authorization: Bearer ${VULTR_API_KEY}"
```

### 4.2 换域名 / 换服务器 IP

1. 在 Porkbun 更新 DNS 记录指向新 IP（§2.4）
2. 等 DNS 生效（dig 验证）
3. SSH 到服务器，修改 compose 文件中的域名/IP
4. 重启 Traefik + Daytona stack

### 4.3 从零部署到新服务器

1. 创建 Vultr 实例（§1.5），拿到 IP
2. 配置 Porkbun DNS 指向新 IP（§2.2）
3. SSH 到新服务器，运行 `mob-server init`（或手动 `deploy.sh`）
4. 验证：`mob init` → `mob ssh` → `claude --version`

---

## 5. 费用参考

- Vultr vc2-4c-8gb: $48/月（运行时按小时计费，关机不收计算费但收存储费 ~$6/月）
- Porkbun `.beauty` 域名: ~$10/年
- Cloudflare Worker（开关机代理）: 免费档每天 10 万次请求
- Let's Encrypt TLS: 免费
