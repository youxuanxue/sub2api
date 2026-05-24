---
name: tokenkey-stage0-edge-platform-migration
description: >-
  Migrate a TokenKey Stage0 Edge **from EC2/CFN to AWS Lightsail** on the same
  `<edge_id>`, sharing the DNS domain `api-<id>.tokenkey.dev`. Drives the full
  sequence: pre-flight (read-only AWS checks), one-time IAM addon setup (GHCR
  PAT only when the image is private — TokenKey GHCR is public by default),
  matrix flip (exclusivity gate enforces single-platform), Lightsail provision
  + smoke without DNS cut, operator OAuth re-login on the fresh edge DB, DNS
  cut at Porkbun, post-cut smoke from an independent vantage point, and finally
  CFN-native decommission of the retired EC2 stack (auto EBS snapshot, optional
  EIP release). EC2/CFN remains the default Edge path; this skill exists for
  the one-way EC2→Lightsail migration. The reverse direction (Lightsail→EC2)
  is intentionally NOT implemented — build at first need rather than ship an
  unimplemented API; see §7.
---

# TokenKey：Edge 平台迁移全流程（EC2 → Lightsail，单向）

适用于 TokenKey fork of sub2api。本 skill **只迁移同一个 `<edge_id>` 在两个平台之间**（共享 DNS `api-<id>.tokenkey.dev`），不是"新增一个 edge"——后者用 `tokenkey-stage0-edge-expansion` 或 `tokenkey-stage0-edge-lightsail-expansion`。

权威纪律：仓库根 `CLAUDE.md`、`docs/spec-delta-edge-lightsail.md`、规划 `docs/deploy/tokenkey-multiregion-egress-gateway-plan.md` §6.1（EC2/CFN 为默认 Edge 路径）。

## 确定性基线（机械化 vs 真判断）

| 步骤 | 类型 | 承载 |
|---|---|---|
| 矩阵一致性 + 平台 exclusivity 检查 | 机械 | `scripts/checks/edge-platform-exclusivity.py`（已接入 preflight） |
| 各阶段实操前置检查 | 机械 | `ops/migration/edge-platform-migration-preflight.sh <edge_id> --phase=<plan|provision|cutover|decommission>` |
| Lightsail provision / 升级 / smoke / Static IP rotation | 机械 | `deploy-edge-lightsail-stage0.yml` |
| EC2 decommission（含 EBS snapshot + 可选 EIP release） | 机械 | `deploy-edge-stage0.yml operation=decommission` |
| 矩阵翻位（哪边 deployable=true） | 真判断 | prompt（迁移的边界决策；通过 PR 落地） |
| DNS A 记录 swap | 真判断 | Porkbun 人工编辑（独立人审批入口） |
| 新 edge 上 OAuth 账号重新登录 | 真判断 | 管理员 UI 操作（不可机械化的鉴权流） |
| 是否 release 旧平台 EIP / 是否保留 snapshot rollback 窗 | 真判断 | prompt（保留成本 vs 回滚窗口） |

## 调用参数

```text
/tokenkey-stage0-edge-platform-migration edge_id=<id> [tag=X.Y.Z] [phase=<plan|provision|cutover|decommission|all>] [keep_eip=true]
```

| 参数 | 语义 |
|---|---|
| `edge_id` | 要迁移的 edge，如 `uk1` / `us1` / `fra1` / `sg1` |
| `tag` | 新平台 provision 用的 image tag（无 v 前缀） |
| `phase` | 分阶段执行；默认 `all`（=plan→provision→cutover→decommission） |
| `keep_eip` | 旧 EC2 EIP 是否保留（默认 `true`，便于 30 天回滚窗内复用） |

> **方向**：本 skill 当前**只实现 EC2 → Lightsail** 单向迁移。反向迁移（Lightsail → EC2）所需的 Lightsail decommission primitive 与 EC2 provision-from-snapshot 路径尚未建；首次需要反向时再补，避免在 API 上写一个未实现的选项（参见 §7）。

## 0) 前置（一次性 / 每个 AWS 账户）

```bash
# 1) Lightsail IAM addon — 整个 AWS account 一次
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-lightsail-addon \
  --template-file deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml \
  --parameter-overrides GitHubOidcRoleName=tokenkey-gha-us-east-1-error-clustering \
  --capabilities CAPABILITY_IAM

# 2) GitHub Environment edge-<edge_id> 已存在（与 EC2 共用），确认变量齐
#    EDGE_ACME_EMAIL / EDGE_MAIN_GATEWAY_ALLOWED_CIDR / EDGE_MAIN_GATEWAY_BASE_URL
#    secret: MAIN_GATEWAY_EDGE_SMOKE_API_KEY
```

> **GHCR auth**：TokenKey 的 `ghcr.io/<owner>/sub2api` 当前是 public，anonymous pull 可用，**不需要 PAT**。workflow 默认 `ghcr_pat_required=false`。
> 仅当镜像未来转 private 时：(a) 跑 `aws ssm put-parameter --region <ls_region> --name /tokenkey/lightsail/<edge_id>/ghcr/pat --type SecureString --value 'ghp_…'`；(b) dispatch provision 时加 `-f ghcr_pat_required=true`（或在 Environment 设 `EDGE_GHCR_PAT_SSM_NAME`）。

可机械验证：

```bash
bash ops/migration/edge-platform-migration-preflight.sh <edge_id> --phase=provision
```

未通过则按报错指引补齐再进入 Phase 1。

## 1) Phase plan：迁移意图落码

EC2→Lightsail 方向，前置假设：
- EC2 `<edge_id>` 当前 `deployable=false`（已退役 ops 矩阵）
- Lightsail `<edge_id>` 当前 `deployable=false`（PR #380 默认）
- DNS `api-<id>.tokenkey.dev` 当前指向 EC2 EIP 或已经空

**机械检查**：

```bash
bash ops/migration/edge-platform-migration-preflight.sh <edge_id> --phase=plan
```

**手工 PR**：把 Lightsail `<edge_id>` 翻成 `deployable=true`，提交 PR。`edge-platform-exclusivity` preflight 会强制 EC2 同名仍是 `false`；CI 全绿后由操作员合并。

> **不要在同一 PR 里同时翻两边的 deployable**——分两步可以让 exclusivity gate 始终强一致。

## 2) Phase provision：实机起 Lightsail，**不切 DNS**

合并完矩阵 PR 后，dispatch Lightsail provision：

```bash
TAG=<current_prod_tag>
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance=tokenkey-edge-<edge_id>-ls
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

Job summary 输出 Static IP；**不要立刻改 DNS**。先用 `--resolve` 旁路验证：

```bash
LS_IP=$(aws lightsail get-static-ip --region <ls_region> \
  --static-ip-name tokenkey-edge-<edge_id>-ls-ip \
  --query 'staticIp.ipAddress' --output text)
curl -sS --resolve api-<edge_id>.tokenkey.dev:443:$LS_IP \
  https://api-<edge_id>.tokenkey.dev/health
# 应当 200 + 含 tokenkey 字段
```

**机械验证**：

```bash
bash ops/migration/edge-platform-migration-preflight.sh <edge_id> --phase=cutover
```

应当报告 `info: DNS … currently → <旧 IP>; cutover will swap`。

## 3) Phase cutover：DNS 切换 + 操作员 OAuth

按用户回答 ②（fresh-DB 起步），新 Lightsail 实例上的 Postgres 是空的，需要重新创建 Anthropic OAuth 账号：

1. **手工 Porkbun**：把 `api-<edge_id>.tokenkey.dev` A 记录改成 Lightsail Static IP。等 1 分钟。
2. **独立观察点 smoke**（避开本地 DNS 缓存）：
   ```bash
   curl -sS https://api-<edge_id>.tokenkey.dev/health   # 无 --resolve；走真实 DNS
   ```
3. **管理员 UI**：登录 `https://api-<edge_id>.tokenkey.dev/admin`（管理员密码在 provision 输出的 `.env.secret`，由 `tokenkey-stage0-edge-lightsail-expansion` skill 提示的 SSH 取回）：
   - 重新登录所有 Anthropic OAuth 账号（旧 EC2 token 失效，新 edge DB 没有）
   - 跑 `tokenkey-anthropic-oauth-config` skill 的 plan-edge-account-tier 把 tier baseline 写进新库
4. **prod stub 一致性**：`api-<edge_id>.tokenkey.dev` 域名不变，所以 prod 的 anthropic api-key stub 不需要改。但底下 edge DB 是新的；跑 `tokenkey-anthropic-oauth-config` skill 的 verify 步骤确认 stub.concurrency 等镜像值与新 edge 一致。
5. **smoke workflow**：
   ```bash
   gh workflow run deploy-edge-lightsail-stage0.yml \
     -f edge_id=<edge_id> -f operation=smoke \
     -f confirm_instance=tokenkey-edge-<edge_id>-ls
   ```

**机械验证**：

```bash
bash ops/migration/edge-platform-migration-preflight.sh <edge_id> --phase=decommission
```

报告必须包含 `ok: DNS api-<edge_id>.tokenkey.dev → Lightsail Static IP`。否则 cutover 没完成，不要进 Phase 4。

## 4) Phase decommission：拆 EC2 栈

**前置条件**（preflight 强校验）：EC2 矩阵 `deployable=false`、Lightsail 矩阵 `deployable=true`、DNS 已切到 Lightsail Static IP。

```bash
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=decommission \
  -f confirm_stack=tokenkey-edge-<edge_id>-stage0 \
  -f i_understand_destroys_data=true \
  -f release_eip=false   # 默认；改 true 仅当你确认旧 EIP 不再用作回滚通道
gh run watch --exit-status $(gh run list -w deploy-edge-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

Workflow 自动：

1. EBS root volume **快照**（30 天保留 tag）
2. 读取 stack outputs 里的 `EipAllocationId` 留作 Job summary 审计
3. `aws cloudformation delete-stack` + wait for `DELETE_COMPLETE`
4. 若 `release_eip=true` 则 `aws ec2 release-address`（默认保留，防止误删）

> **回滚窗**：在快照保留期内（30 天），可以通过 `operation=provision` 重新建栈，并把新 root EBS 替换为 snapshot 还原。EIP 若保留，公网 IP 可以无缝接回。

## 5) Acceptance（机械化输出）

完成迁移后给一个 6 行 acceptance：

```text
edge_id        : <id>
direction      : ec2-to-lightsail
old_platform   : ec2 (stack=tokenkey-edge-<id>-stage0, deleted at <ts>)
new_platform   : lightsail (instance=tokenkey-edge-<id>-ls, region=<ls_region>)
old_ip_handling: kept (alloc=eipalloc-…) | released
ebs_snapshot   : snap-… (RetentionDays=30, expires <ts+30d>)
dns_state      : api-<id>.tokenkey.dev → <ls_static_ip>
```

数据来自 workflow Job summary + preflight 输出，不要手抄常量。

## 6) 已知失败模式与定位

| 现象 | 阶段 | 根因 | 处理 |
|---|---|---|---|
| `Lightsail … must be deployable=true` | provision preflight | 矩阵 PR 未合 | 合并矩阵翻位 PR |
| `tokenkey-cicd-lightsail-addon not deployed` | provision preflight | 一次性 addon 未跑 | 按 §0 跑 `aws cloudformation deploy` |
| `Lightsail instance … already exists` | provision | 之前 provision 半成功 | `gh workflow run … recreate=true`（destructive） |
| Provision step `BOOTSTRAP_FAIL: amazon-ssm-agent -register failed` | provision | activation expired / 网络 | Lightsail 浏览器 SSH 看 `/var/log/tokenkey-lightsail-bootstrap.log` |
| `--resolve` smoke 200，但真实 DNS 还是旧 IP | cutover | Porkbun A 未改 / TTL 未过 | 等 TTL；用 `dig +short @1.1.1.1` 验证 |
| `DNS … still points at <old_ip>` | decommission preflight | cutover 未完成 | 回 Phase 3 完成 DNS swap |
| Decommission 工作流报 `requires deployable=false` | decommission | EC2 矩阵还没翻 false（不应发生于此 skill 路径） | 提 PR 翻矩阵 |
| Decommission 后 EIP 还在账户里 | 迁移完 | `release_eip=false`（默认） | 单独 `aws ec2 release-address` 或留作下一个 edge 使用 |

## 7) 反方向（Lightsail → EC2）：未实现

本 skill **不主张提供未实现的 API**。反向迁移目前需要以下 primitive，均**尚未建**：

1. Lightsail decommission workflow op（参照本 skill 创建的 `deploy-edge-stage0.yml operation=decommission`，给 `deploy-edge-lightsail-stage0.yml` 加一个对称 op；需要 instance snapshot + Static IP release）
2. EC2 provision-from-snapshot（如果要从 Lightsail 迁回时复用此前 EC2 拍的 snap，CFN 模板需要支持 `--root-snapshot-id` 参数）

**首次需要反向迁移时再补这两块**；不在本 skill 范围里画饼。
