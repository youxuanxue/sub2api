## 2) Provision：创建实例

先从 matrix 取 `confirm_instance`（不要硬编码 `tokenkey-edge-<edge_id>-ls`）：

```bash
CONFIRM=$(python3 deploy/aws/lightsail/resolve-edge-lightsail-target.py \
  --edge-id <edge_id> | awk -F= '/^instance_name=/{print $2}')
TAG=X.Y.Z   # 当前 prod tag（不带 v）；读 backend/cmd/server/VERSION
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance="$CONFIRM"
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

matrix 变更在 feature branch 上时，dispatch 加 `--ref <branch>`（workflow checkout 该 ref 的 matrix）。

观察点：

- Job summary / log 行 `provision complete edge=… ip=…` 的 **ip 必须等于** matrix `porkbun_a_ipv4`（或 adopt 前查到的 Static IP）；
- SSM managed instance 在 ≤15 分钟内拿到 `mi-*`。

**销毁重建**（bare instance、换 bundle、误装坏栈；**保留 Static IP 地址**）：

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance="$CONFIRM" \
  -f recreate=true
```

workflow 仍打印 “Static IP will be DESTROYED” 警告语，但 `provision-edge.sh` 实际 **detach-only**（不 release）。
默认 `recreate=false` → 实例已存在则 `::error::` 直接挂；不会被静默销毁。

**批量 US edge**（如 us2/us3/us4）：按 edge 顺序逐个 dispatch + watch（concurrency 按 edge_id 分组，可并行，但 prepare/OIDC 未完成前不要并发）。

### 2.1 Admin 账密落盘（provision 后必做，禁止打印密码）

GHA workflow **不会**写 operator 本机的 `~/Codes/keys/`。每个新 edge provision 成功后，在本机执行（**stdout 不输出 password**）：

```bash
bash ops/stage0/ensure-edge-admin-credentials.sh --platform lightsail <edge_id>
# 或等价：capture 失败 (exit 3) 时再跑 reset-edge-admin-password.sh
ls -l "$HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt"   # chmod 600，含 email= / password=
```

验收：`email=admin@api-<edge_id>.tokenkey.dev` 与 uk1/us1 文件格式一致。**禁止**在 PR、GHA log、聊天里粘贴 `password=` 行。

**prod 主网关同脚本**：目标传字面量 `prod`（同一套脚本解析到固定 EC2 栈 `tokenkey-prod-stage0`/`us-east-1`，`--platform` 忽略），落盘 `tokenkey-prod-admin-password.txt`。prod 的 bootstrap 日志通常已滚动，`ensure prod` 会自动 fallback 到 reset（轮换）；要直接轮换用 `bash ops/stage0/reset-edge-admin-password.sh prod`。

### 2.2 防火墙 TCP 443（provision 后必验）

`provision-edge.sh` 会尝试开放 80/443，但 **443 可能未生效**（us2 实案：公网 HTTPS 超时、实例内 curl 正常）。

```bash
bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> --enforce-ports
# 机械验收：TCP 443 + 8443、UDP 34567 open，TCP 80 closed；双用途 fingerprint edge 可由
# 本机 `lightsail-ssh-cidr` 把 TCP 22 限定到一个公网 IPv4 /32，其他 22 暴露均为 drift。
```

**禁止**在 443 未开时进入 DNS/smoke——现象是连接超时，不是应用 5xx。

### 2.3 新 edge Anthropic baseline（OAuth 账号就绪后）

DNS cutover 且 admin 可登录后，按 `tokenkey-anthropic-oauth-config` 对新 edge 跑 tier baseline + concurrency mirror verify（Lightsail 矩阵 `deployable=true` 的 edge 已纳入双矩阵 domain 链接）。

**从既有 edge 搬账号（凭据 admin UI 进不去时）：** 新 edge 需要的 OAuth/setup-token 若是从旧 edge 迁移而非重新授权——尤其 kiro OAuth grant 这类 admin UI 无法重新录入的 live credential blob——用 `ops/migration/migrate-edge-accounts.py`，按 `extract`→`build`→`load` 三步离散执行：

```bash
# 旧 edge → 本机 .cache（含列类型，永不打印 secret 值，只列 KEY 名）
python3 ops/migration/migrate-edge-accounts.py extract --from edge:us1 --account-ids 5,6,7
# 本机生成 migrate.sql + 改名（账号/组），打印脱敏摘要
python3 ops/migration/migrate-edge-accounts.py build --rename kiro-us1-real=kiro-us6-real --rename-group kiro-us1=kiro-us6
# 默认 dry-run；--execute 才落地到新 edge
python3 ops/migration/migrate-edge-accounts.py load --to edge:us6 [--execute]
```

raw SQL insert 绕过了 repo 层的 Redis 快照写 + scheduler_outbox 入队，脚本会自动插一条 `full_rebuild` outbox 行重建快照（gateway 也每 `full_rebuild_interval_seconds` 默认 300s 兜底）。`tier_id`/`proxy_id` 是宿主机特异、不可移植，已在脚本里 reset。烟测/拆机用的 `set-schedulable` / `soft-delete` 子命令同样默认 dry-run。
