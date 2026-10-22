# 冻结 Tokyo 退役 · 日本代理关停 · 腾讯续费 Runbook

状态：**仅计划，未执行任何停机/删除/续费**（2026-10-22 只读核实）。
舰队事实只在 [舰队 SSOT](../../deploy/hetzner/README.md)；现网身份与宿主 watcher owner 为
[`deploy/tencent/candidate-usw-20261022.json`](../../deploy/tencent/candidate-usw-20261022.json) 的 `target` / `host_watch`。
LUKS 自动解锁另见 [方案](tencent-luks-autounlock-plan.md)。

每步标注：**[可逆]** 可原样恢复；**[不可逆]** 删除后无法恢复；**[审批]** 须用户在对话里明确批准该步（批准不跨步）。

## 0. 只读现状（2026-10-22）

| 资源 | 事实 |
|---|---|
| Tokyo EC2 | `i-045f062ce33e1a5af`，c7g.xlarge，running，EIP `3.114.187.250`，`disableApiTermination=false` |
| Tokyo 栈 | `tokenkey-prod-tokyo-precutover-20261017`（ap-northeast-1）；`DataVolume` 为 `DeletionPolicy/UpdateReplacePolicy: Retain` |
| 卷 | 根 `vol-05046c55cb60cbfcb`（30 GiB）、数据 `vol-0c3efd51b2582b321`（150 GiB） |
| DLM | `policy-089492d7a5b78d21a`（栈内资源），ENABLED，目标 tag `Backup=stage0`，每日 03:00 UTC，**仅保留 7 份** |
| 快照 | 13 份：DLM 日快照 10-17…10-22 各 2 份 + 手工 `snap-05ec45d6be0e08582`（数据卷，10-17 07:38 UTC，非 DLM） |
| 遗留栈 | `tokenkey-tokyo-ebs-restore-20261017` 停在 `REVIEW_IN_PROGRESS`（未执行的 change set，无资源） |
| Lightsail（东京） | `jp-proxy-01..04-micro` + `tokenkey-node1-ingress-jp-20261016-vm`，均 running，各绑静态 IP |
| prod `proxies` | `jp-uk1/uk2/us4/us5`（id 2-5）`status=active`，**live_accounts=0、fallback_origin=0、backup_of=0**；id 10-13 为已软删 `default` 别名（同 host）。id 2/3 各有 1/2 个已软删账号仍引用 |

`jp-*` 绑定复核：

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/probe-proxy-bindings.sh --env PROXY_NAME_PREFIX=jp-
```

（`probe-list-proxies.sh` 仍硬写 `tokenkey-postgres`，第 1 队在修；上面的探针走共享 resolver，腾讯上解析到 `tk-archive-standby`。）

**DLM 说明**：保留 7 份，约 2026-10-24 起轮换掉 10-17 的份。若 Tokyo 自 10-22 冻结后确无业务写入（本次未登录 Tokyo 核实），冻结后的日快照与冻结点等价，轮换不丢冻结态；但切流前（10-22 03:37 UTC 及更早）的快照会在 ~10-29 前后轮换掉。若需要保留「切流前最后一份 DLM 快照」，须在其轮换前复制为手工快照（`copy-snapshot`，**[可逆·审批]**）。最终退役仍以第 2 步的非 DLM 手工快照为准。

## 1. 前置条件（全部满足才进入第 2 步）

1. 切流满 7 天：**不早于 2026-10-29**，且用户确认无回滚需求。 **[审批]**
2. 腾讯新 timeline 备份健康：`tokenkey-postgres-backup` / `archive-check` 回执 ok，至少一次 restore 验证在期内（第 2 队 owner）。
3. 历史只读链 `s3://tokenkey-prod-physical-backup-682751977094-us-east-1/prod/postgres`（Tokyo timeline 1）保持可读；该前缀**不随 Tokyo 退役删除**。
4. 无任何 workflow / 脚本把 `aws-fresh` 作为默认路径（现状：`resolve_prod_ssm_target.py` 已要求 `ALLOW_FROZEN_TOKYO_TARGET=1`）。
5. 腾讯 Lighthouse 续费已决策（第 6 节），避免退役 Tokyo 后腾讯随即到期造成无回退。

## 2. 最终快照与归档核对

1. **[可逆·审批]** 对两块卷打手工快照（不带 `Backup=stage0` 之外的 DLM 标签，tag `Purpose=tokyo-final-retain`、`RetainUntil=<用户决定>`）：
   `aws ec2 create-snapshot --region ap-northeast-1 --volume-id vol-0c3efd51b2582b321 ...`（根卷同理）。
   等 `State=completed` 并记录 `SnapshotId` 到回执 `docs/ops/reports/tokyo-retirement-<date>.json`。
   费用：EBS 快照按增量存储计（ap-northeast-1 约 0.05 USD/GB-月，180 GiB 上限约 9 USD/月）。
2. 只读核对：`describe-snapshots` 确认最终快照 + 手工 `snap-05ec45d6be0e08582` 均 completed。
3. 只读核对 S3 历史链与 `legacy-node-archive` 前缀仍在（见 [AWS 退役边界](aws-retirement-20261017.md)）。

## 3. DLM 处置

DLM 策略是 Tokyo 栈资源。两种选择 **[审批]**：

- A（推荐）：保持 ENABLED 到删栈，删栈时随栈删除；DLM 创建的快照**不会**因策略删除而删除，之后按手工快照管理。**[不可逆：策略本身]**
- B：先 `update-lifecycle-policy --state DISABLED` 冻结现有 7 份不再轮换。**[可逆]**；但会造成栈漂移，删栈前需恢复或接受漂移。

## 4. 仓库侧清理（单 PR，删除顺序固定）

Tokyo 删机前合并，保证没有任何代码路径还能把 SSM 发往 `i-045f062ce33e1a5af`：

1. `ops/stage0/resolve_prod_ssm_target.py`：删 `aws-fresh` 分支（`resolve_fresh_aws`、`FRESH_MATRIX`、`ALLOW_FROZEN_TOKYO_TARGET`），`ALLOWED_ENV_TARGETS` / `--target` choices 去掉 `aws-fresh`，改为与 `aws` / `hetzner` 同样的「retired」拒绝；同步 `ops/stage0/ssm_execution.py`（`aws-fresh` 分支与 `FRESH_MATRIX` 区域回退）。
2. 测试：`ops/stage0/test_resolve_prod_ssm_target.py`、`test_ssm_execution.py`、`scripts/checks/check-stage0-ssm-host-parse.sh`（`TEST_FRESH_PROD_MATRIX` stub 改为不再需要 Tokyo 行）、`ops/observability/test_host_https_watch.py`（`self.frozen` 断言删除）。
3. sentinel `scripts/sentinels/gateway-tk.json` 中 `resolve_prod_ssm_target.py` 条目：`ALLOW_FROZEN_TOKYO_TARGET` 改为退役拒绝文案的锚点（sentinel 先改、代码后删会让 preflight 失败，必须同 PR）。
4. 删除 `deploy/aws/fresh-migration-20261015.json`（`host_watch` owner 已在 2026-10-22 迁到腾讯矩阵，删文件不影响 watcher）。`git grep fresh-migration-20261015` 只允许出现在 `docs/ops/reports` 与历史回执。
5. `.github/workflows/ops-stage0-host-mem-guard.yml`：EC2-only，现网 Hybrid 不可用。建议**删除**（payload owner 仍是 `stage0-ec2-bootstrap.sh`，未来 EC2 重建自带）；若保留须改为 `workflow_dispatch` 下显式拒绝。**[审批：删 workflow]**
6. `deploy/aws/cloudformation/cicd-oidc.yaml` 已支持 `mi-*`（2026-10-22）；OIDC 栈 retarget 见第 5.4 步。
7. 文档：舰队 SSOT、`aws-retirement-20261017.md`、`.cursor/skills/tokenkey-host-replacement` / `tokenkey-stage0-release-rollout` 中的 `aws-fresh` 指引改为历史说明。
8. `./scripts/preflight.sh` PASS。

## 5. Tokyo 停机与删除

1. **[可逆·审批]** `aws ec2 stop-instances --instance-ids i-045f062ce33e1a5af`。停机期间 EIP 仍计费（约 3.6 USD/月），卷继续计费。观察 ≥24h：腾讯 `/health`、daily diagnostics、HTTPS watcher 无 Tokyo 相关告警。
2. **[不可逆·审批]** 删栈 `tokenkey-prod-tokyo-precutover-20261017`：实例、EIP、SG、VPC、DLM、SSM 参数随栈删；`DataVolume` 因 Retain **保留**，需单独决定：
   - 删除数据卷（最终快照已在第 2 步 completed）**[不可逆·审批]**；
   - 根卷随实例删除（`DeleteOnTermination`，删前确认）。
3. **[不可逆·审批]** 删除遗留空栈 `tokenkey-tokyo-ebs-restore-20261017`（REVIEW_IN_PROGRESS，无资源）。
4. **[审批：IAM]** OIDC 栈 `us-east-1/tokenkey-cicd-oidc` 仍授权 `instance/i-045f062ce33e1a5af`。用
   `python3 ops/stage0/retarget_prod_oidc.py --live-template <get-template 输出> --output <rendered.json>` 渲染，
   以 `TargetInstanceId=mi-080ed76dc6d97d8b5`、`TargetInstanceRegion=eu-west-2` 建 change set、人工审阅后执行。
   现网 Hybrid SendCommand 实际由栈外 inline policy `tokenkey-lightsail-edge-addon`（`managed-instance/*`）授予，retarget 不会移除它。
   注意：线上栈模板与仓库模板已漂移（线上多 `ReadQaBundleRuntimeForDiagnostics` / `ReadQaBundleBucketState`，线上 `ReadEbsSnapshotsForDiagnostics` 缺 `ec2:DescribeInstances`）；`retarget_prod_oidc.py` 只替换 SendCommand 与诊断条件，保留其余线上语句。
5. 保留：手工最终快照、DLM 遗留快照（按 `RetainUntil` 另批清理）、S3 归档与历史 pgBackRest 链。

## 6. 日本 Lightsail 代理关停

范围：`jp-proxy-01..04-micro` 与 `tokenkey-node1-ingress-jp-20261016-vm`（后者为 10-16 node1 ingress，确认无 DNS/Caddy 指向后同批处理）。

1. 只读门禁：`probe-proxy-bindings.sh` 输出中每行 `live_accounts`、`fallback_origin_accounts`、`backup_of_live_proxies` 全为 0（2026-10-22 已满足）。另确认 cn-prod `proxies` 不引用这些 IP（`--target cn-prod` 跑同一探针）。
2. **[可逆·审批]** prod `proxies` 行 2-5 改 `status=inactive`（Admin UI 编辑，或 `PUT /api/v1/admin/proxies/:id`）。观察 24h 无 `ErrProxyInUse` / 账号报错。
3. **[可逆·审批]** `aws lightsail stop-instance` 逐台停机；静态 IP 保持附着（以 Lightsail 当期计费规则为准；停机实例仍按套餐计费）。观察 ≥24h。
4. **[可逆·审批]** 每台 `create-instance-snapshot`（`jp-proxy-0N-final-<date>`）。代理机无业务数据，快照只为保留 GOST 配置；用户可选择跳过。
5. **[不可逆·审批]** `delete-instance`；`release-static-ip`（释放后 IP 不可找回，任何外部白名单将失效）。
6. **[可逆·审批]** prod `proxies` 行 2-5 软删：Admin UI 删除或 `DELETE /api/v1/admin/proxies/:id`（`DeleteProxy` 只统计 `deleted_at IS NULL` 的账号引用，已软删账号不阻塞）。不得手写 SQL 硬删；id 10-13 已软删不动。
7. 更新舰队 SSOT「日本出口代理」一节为已关停，回执写 `docs/ops/reports/`。

## 7. 腾讯 Lighthouse 续费（2026-10-22 只读核实）

`tccli lighthouse DescribeInstances` / `InquirePriceRenewInstances`（na-siliconvalley，只读）：

| 实例 | 角色 | 套餐 | 到期 (UTC) | 续费标志 | 1 月 | 12 月 |
|---|---|---|---|---|---:|---:|
| `lhins-fcwpwymt` | prod | 锐驰 4C8G | **2026-11-21 08:31:14** | NOTIFY_AND_MANUAL_RENEW | 235 | 2397（85 折） |
| `lhins-56ogvsid` | usw-eval1 | 锐驰 2C2G | **2026-11-21 08:31:11** | NOTIFY_AND_MANUAL_RENEW | 40 | 480（无折扣） |
| `lhins-mtiwmfod` | usw-eval2 | 锐驰 2C2G | 2026-11-22 02:58:55 | NOTIFY_AND_MANUAL_RENEW | 40 | 480 |
| `lhins-n1w2c5qr` | usw-eval3 | 锐驰 2C2G | 2026-11-22 02:59:01 | NOTIFY_AND_MANUAL_RENEW | 40 | 480 |
| `lhins-h12i2d2r` | usw-eval4 | 锐驰 2C2G | 2026-11-22 02:59:15 | NOTIFY_AND_MANUAL_RENEW | 40 | 480 |

金额 CNY；合计 1 月 395，12 月 4317。不续即到期停机（数据保留期内可续回，但 prod 停机即全站不可用）。

决策选项 **[审批：任一续费都是扣费外部副作用]**：

- A. 手动续 1 个月（395 CNY）：保留灵活度，下月同样到期风险；需日历提醒 2026-11-14 前处理。
- B. 改自动续费（`ModifyInstancesRenewFlag` → `NOTIFY_AND_AUTO_RENEW`）：每月自动扣 395，余额不足仍会到期，需保持账户余额告警。
- C. 包年（4317 CNY，prod 省 423）：仅当确认腾讯美西为至少一年的长期 prod；proxy 包年无折扣，可只对 prod 包年、proxy 走 B。
- 建议：prod 走 C 或 B，proxy 走 B；无论选哪项，**截止 2026-11-14** 完成，留一周缓冲。

## 8. 审批清单（按紧急度）

1. ~2026-10-28 前：是否把切流前最后一份 DLM 快照（10-22 03:37 UTC 两份）复制为手工快照保留（第 0 节 DLM 说明）。
2. 2026-11-14 前：腾讯续费方案（第 7 节）。
3. ≥2026-10-29：Tokyo 退役日期、仓库清理 PR、停机→删栈→数据卷处置、OIDC retarget。
4. 任意时间：`jp-*` 关停（门禁已满足），含静态 IP 释放与 `proxies` 软删。
5. LUKS 演练预算（见 [方案](tencent-luks-autounlock-plan.md)）。
