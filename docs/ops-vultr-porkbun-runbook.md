# Vultr 服务器 + Porkbun 域名 运维手册

给 coding agent 用的完整操作手册。所有操作通过 API 完成，不需要登录 Web 界面。

---

## 凭证

所有凭证在 `/Users/Clock/sandbox-test/.env` 中：

```
VULTR_API_KEY=45ZBJDE3OTVQ6KZT4GTQPXRXUJTYLGA3YSEA
PORKBUN_API_KEY=pk1_6f97d72bf1a58de15954489d1bf2f22b7c03e4f05f546152da487449eb7570bb
PORKBUN_SECRET_KEY=sk1_2e3cd94597e934ce8b2946e396eeb35c3f41ad687d7c55d6918fcb961ef6e4e0
```

当前实例信息：
- VM_ID: `bbea8db7-4e34-4054-b6c2-6717eb3de436`
- VM_IP: `45.32.25.73`
- 域名: `mobai.beauty`
- SSH key: `~/.ssh/poc_ed25519`
- SSH 连接: `ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@45.32.25.73`

---

## 1. Vultr 服务器操作

### API 基础

```bash
VULTR_KEY="45ZBJDE3OTVQ6KZT4GTQPXRXUJTYLGA3YSEA"
VULTR_API="https://api.vultr.com/v2"
```

所有请求带 header: `Authorization: Bearer $VULTR_KEY`

### 1.1 查看当前服务器状态

```bash
curl -sf "$VULTR_API/instances/bbea8db7-4e34-4054-b6c2-6717eb3de436" \
  -H "Authorization: Bearer $VULTR_KEY" \
  | python3 -c "
import sys,json
d = json.load(sys.stdin)['instance']
print(f'Status: {d[\"status\"]}  Power: {d[\"power_status\"]}  IP: {d[\"main_ip\"]}')"
```

- `power_status: running` = 开机中
- `power_status: stopped` = 已关机（不计算费用，但保留磁盘和 IP）

### 1.2 开机

```bash
curl -sf -X POST "$VULTR_API/instances/bbea8db7-4e34-4054-b6c2-6717eb3de436/start" \
  -H "Authorization: Bearer $VULTR_KEY"
```

开机后需要等 30-60 秒 SSH 才可用。Docker 服务会自动启动（systemd），但沙盒平台的 compose stack 可能需要手动拉起：

```bash
ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@45.32.25.73 \
  "cd /opt/poc && docker compose -f docker-compose.traefik.yml up -d && \
   docker compose -f docker-compose.daytona.yml up -d && \
   docker compose -f docker-compose.openhands.yml up -d"
```

### 1.3 关机

```bash
curl -sf -X POST "$VULTR_API/instances/bbea8db7-4e34-4054-b6c2-6717eb3de436/halt" \
  -H "Authorization: Bearer $VULTR_KEY"
```

关机保留 IP 和磁盘数据。下次开机一切还在。

### 1.4 重启

```bash
curl -sf -X POST "$VULTR_API/instances/bbea8db7-4e34-4054-b6c2-6717eb3de436/reboot" \
  -H "Authorization: Bearer $VULTR_KEY"
```

### 1.5 创建新服务器

如果需要从零开一台新的：

```bash
# 列出可用机房（推荐 nrt = Tokyo）
curl -sf "$VULTR_API/regions" -H "Authorization: Bearer $VULTR_KEY" \
  | python3 -c "import sys,json; [print(f'{r[\"id\"]:6} {r[\"city\"]}') for r in json.load(sys.stdin)['regions']]"

# 列出可用机型
curl -sf "$VULTR_API/plans" -H "Authorization: Bearer $VULTR_KEY" \
  | python3 -c "
import sys,json
for p in json.load(sys.stdin)['plans']:
    if p['type'] == 'vc2' and p['vcpu_count'] >= 4 and p['ram'] >= 8192:
        print(f'{p[\"id\"]:20} {p[\"vcpu_count\"]}C/{p[\"ram\"]//1024}G \${p[\"monthly_cost\"]}/mo')"

# 列出已上传的 SSH key
curl -sf "$VULTR_API/ssh-keys" -H "Authorization: Bearer $VULTR_KEY" \
  | python3 -c "import sys,json; [print(f'{k[\"id\"]}  {k[\"name\"]}') for k in json.load(sys.stdin)['ssh_keys']]"

# 创建实例
curl -sf -X POST "$VULTR_API/instances" \
  -H "Authorization: Bearer $VULTR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "region": "nrt",
    "plan": "vc2-4c-8gb",
    "os_id": 2284,
    "label": "mob-sandbox",
    "sshkey_id": ["3e3461ec-091d-4284-9498-fc01463f2c68"],
    "backups": "disabled"
  }'
```

- `os_id: 2284` = Ubuntu 24.04 LTS（用 `GET /v2/os` 查最新 ID）
- 创建后 1-2 分钟拿到 IP，5 分钟 SSH 可用
- 记下返回的 `id` 和 `main_ip`

### 1.6 上传新 SSH key

```bash
PUB_KEY=$(cat ~/.ssh/poc_ed25519.pub)
curl -sf -X POST "$VULTR_API/ssh-keys" \
  -H "Authorization: Bearer $VULTR_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"mob-deploy\",\"ssh_key\":\"$PUB_KEY\"}"
```

### 1.7 销毁服务器（不可逆！）

```bash
curl -sf -X DELETE "$VULTR_API/instances/bbea8db7-4e34-4054-b6c2-6717eb3de436" \
  -H "Authorization: Bearer $VULTR_KEY"
```

---

## 2. Porkbun 域名操作

### API 基础

```bash
PB_KEY="pk1_6f97d72bf1a58de15954489d1bf2f22b7c03e4f05f546152da487449eb7570bb"
PB_SECRET="sk1_2e3cd94597e934ce8b2946e396eeb35c3f41ad687d7c55d6918fcb961ef6e4e0"
DOMAIN="mobai.beauty"
PB_API="https://api.porkbun.com/api/json/v3"
```

所有请求用 POST，body 里带 apikey + secretapikey。

### 2.1 列出当前 DNS 记录

```bash
curl -sf -X POST "$PB_API/dns/retrieve/$DOMAIN" \
  -H "Content-Type: application/json" \
  -d "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\"}" \
  | python3 -c "
import sys,json
for r in json.load(sys.stdin).get('records',[]):
    print(f'{r[\"id\"]:>12}  {r[\"type\"]:5}  {r[\"name\"]:40}  {r[\"content\"]}')"
```

### 2.2 创建 A 记录

```bash
pb_create() {
  local name="$1" ip="$2"
  curl -sf -X POST "$PB_API/dns/create/$DOMAIN" \
    -H "Content-Type: application/json" \
    -d "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\",\"name\":\"$name\",\"type\":\"A\",\"content\":\"$ip\",\"ttl\":\"300\"}"
}
```

mob-sandbox 平台所需的完整 DNS 记录（把 `$IP` 换成服务器 IP）：

```bash
IP="45.32.25.73"
pb_create ""              "$IP"   # mobai.beauty
pb_create "*"             "$IP"   # *.mobai.beauty
pb_create "daytona"       "$IP"   # daytona.mobai.beauty
pb_create "openhands"     "$IP"   # openhands.mobai.beauty
pb_create "*.proxy"       "$IP"   # *.proxy.mobai.beauty
pb_create "*.node.proxy"  "$IP"   # *.node.proxy.mobai.beauty — Daytona 预览 URL
```

**注意：** Porkbun 泛域名 name 格式是 `*.node.proxy`（不是 `node.proxy.*`）。

### 2.3 删除 DNS 记录

```bash
# 需要记录的 ID（从 retrieve 接口拿）
RECORD_ID="123456789"
curl -sf -X POST "$PB_API/dns/delete/$DOMAIN/$RECORD_ID" \
  -H "Content-Type: application/json" \
  -d "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\"}"
```

### 2.4 修改 DNS 记录（IP 变了的时候）

如果换了服务器 IP，需要更新所有 A 记录：

```bash
NEW_IP="1.2.3.4"
# 先 retrieve 拿到所有记录 ID，然后逐个修改
curl -sf -X POST "$PB_API/dns/editByNameType/$DOMAIN/A" \
  -H "Content-Type: application/json" \
  -d "{\"apikey\":\"$PB_KEY\",\"secretapikey\":\"$PB_SECRET\",\"content\":\"$NEW_IP\",\"ttl\":\"300\"}"
```

或者更简单：删掉所有旧记录，重建新的（用 `poc/setup-dns.sh` 就是这个逻辑）。

### 2.5 验证 DNS 生效

```bash
dig +short daytona.mobai.beauty @8.8.8.8
dig +short openhands.mobai.beauty @8.8.8.8
dig +short test.node.proxy.mobai.beauty @8.8.8.8
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
    root@45.32.25.73
```

这个 `-o KexAlgorithms=curve25519-sha256` 在所有 SSH/SCP 操作中都必须带上。

### SCP 文件到服务器

```bash
scp -o KexAlgorithms=curve25519-sha256 \
    -o StrictHostKeyChecking=no \
    -i ~/.ssh/poc_ed25519 \
    localfile root@45.32.25.73:/remote/path
```

---

## 4. 典型操作流程

### 4.1 日常：开机 → 验证 → 使用 → 关机

```bash
# 1. 开机
curl -sf -X POST "$VULTR_API/instances/$VM_ID/start" -H "Authorization: Bearer $VULTR_KEY"

# 2. 等 60 秒，验证 SSH
sleep 60
ssh -o KexAlgorithms=curve25519-sha256 -i ~/.ssh/poc_ed25519 root@45.32.25.73 "mob-server status"

# 3. 如果 compose stack 没自动起来
ssh ... "cd /opt/poc && docker compose -f docker-compose.traefik.yml up -d && docker compose -f docker-compose.daytona.yml up -d && docker compose -f docker-compose.openhands.yml up -d"

# 4. 用完后关机
curl -sf -X POST "$VULTR_API/instances/$VM_ID/halt" -H "Authorization: Bearer $VULTR_KEY"
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
- Porkbun mobai.beauty 域名: ~$10/年
- Let's Encrypt TLS: 免费
