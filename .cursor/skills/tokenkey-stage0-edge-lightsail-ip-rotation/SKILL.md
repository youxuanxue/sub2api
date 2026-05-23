---
name: tokenkey-stage0-edge-lightsail-ip-rotation
description: >-
  Rotate the egress Static IP of a TokenKey Stage0 Lightsail Edge (uk1-ls /
  us1-ls / fra1-ls / sg1-ls) when the live IP has been risk-blocked
  ("polluted") by Anthropic / OpenAI / Google. Mirrors the EC2 EIP rotation
  posture: a single primitive (ops/lightsail/rotate-static-ip.sh) swaps the
  Static IP, the operator updates Porkbun DNS, and external verification runs
  from a clean-egress host. No CloudFormation drift step because Lightsail
  Edge is not CloudFormation-owned.
---

# TokenKey：Lightsail Edge 静态 IP 轮换（污染快速恢复）

适用于通过 `deploy-edge-lightsail-stage0.yml` 部署的 Lightsail Edge（与 EC2 EIP 路径不通用）。
EC2 路径见 `tokenkey-stage0-edge-ip-rotation`。Lightsail 路径**没有 CloudFormation**，所以**没有 drift IMPORT** 这一节。

## 确定性基线

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 edge → region / instance / static IP name / domain | 机械 | `ops/lightsail/rotate-static-ip.sh`（jq 读 `edge-targets-lightsail.json`） |
| 计划输出（dry run） | 机械 | 同上脚本默认行为（无 `--apply`） |
| 三步 swap（allocate → detach → attach → release） | 机械 | 同上脚本 `--apply` |
| 把 ssm_prefix/public_ip 改成新 IP | 机械 | 同上脚本 |
| Porkbun DNS A 记录更新 | 真判断 | prompt（人工操作 Porkbun） |
| 外部干净出口验证 | 真判断 | prompt（选择哪个 observation host 取决于运营当时态势） |

## 调用参数

```text
/tokenkey-stage0-edge-lightsail-ip-rotation edge_id=<id> [step=plan|swap|verify|all]
```

- `step=plan`（默认）— `ops/lightsail/rotate-static-ip.sh <edge_id>` 打印计划，无 AWS 变更。
- `step=swap` — `ops/lightsail/rotate-static-ip.sh <edge_id> --apply`，**销毁旧 IP**。
- `step=verify` — 用上一步产生的 new_ip 做 SNI 验证 + smoke。
- `step=all` — 串起 plan → 等用户确认 → swap → 提示 DNS → verify。

## 0) 前置

```bash
git fetch origin main && git checkout main && git pull --ff-only
bash scripts/preflight.sh
aws sts get-caller-identity   # 确认目标账户
```

确认 edge 已经在 `deploy/aws/lightsail/edge-targets-lightsail.json` 里 `deployable=true`，且 `ssm_prefix/public_ip` 与 `aws lightsail get-static-ip` 报告的 ipAddress 一致；不一致先用 `aws ssm put-parameter` 把 SSM 修对，再开始轮换。

## 1) Plan

```bash
bash ops/lightsail/rotate-static-ip.sh <edge_id>
```

输出形如：

```text
=== Lightsail Edge IP rotation plan ===
edge_id           : uk1
lightsail_region  : eu-west-2
instance_name     : tokenkey-edge-uk1-ls
domain            : api-uk1.tokenkey.dev
old_static_ip_name: tokenkey-edge-uk1-ls-ip
old_ip            : 18.x.x.x
new_static_ip_name: tokenkey-edge-uk1-ls-ip-rot-<ts>
ssm_prefix        : /tokenkey/lightsail/uk1
```

用户确认 `old_ip` 确实被污染，再进入 swap。

## 2) Swap

```bash
bash ops/lightsail/rotate-static-ip.sh <edge_id> --apply
```

脚本顺序：

1. `aws lightsail allocate-static-ip` 新名字
2. `aws lightsail detach-static-ip` 旧名字
3. `aws lightsail attach-static-ip` 新名字到 instance
4. `aws lightsail get-static-ip` 读出 NEW ip
5. `aws ssm put-parameter` 写 `${ssm_prefix}/public_ip = <new_ip>`
6. `aws lightsail release-static-ip` 旧名字（**该 IP 进入 AWS pool**）

输出最后会打印 NEW ip 和 DNS / smoke 提示。

> **同账户 IP 池注意**：Lightsail 在同账户同 region 复用 IP 池，新分配的 IP 极少撞回相同的旧 IP，但概率不是零。如果 NEW ip 仍被污染，重跑一次 swap 即可（旧 IP 已 release，不会回收到自己手里）。

## 3) DNS

去 Porkbun 把 `api-<edge_id>.tokenkey.dev` 的 A 记录改成 NEW ip。等 1 分钟。

## 4) Verify（独立观察点）

不要用刚改 DNS 的本地，去 `tk_post_deploy_smoke.sh` 或者任意干净出口：

```bash
curl -sS --resolve api-<edge_id>.tokenkey.dev:443:<new_ip> \
  https://api-<edge_id>.tokenkey.dev/health
```

应当 200。然后跑一次 workflow smoke：

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> -f operation=smoke \
  -f confirm_instance=tokenkey-edge-<edge_id>-ls
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

最后用 `tokenkey-online-traffic-profile` 或 `tokenkey-online-log-troubleshooting` 看 5 分钟内 Anthropic upstream 是否回到正常 2xx 比例。

## 5) Acceptance（机械化输出）

```text
edge_id : <id>
old_ip  : <old_ip>
new_ip  : <new_ip>
domain  : api-<id>.tokenkey.dev
verify  : curl <new_ip>:443 → 200; smoke run → green
```

## 6) 失败模式

| 现象 | 解读 | 应对 |
|---|---|---|
| `allocate-static-ip` 报 `ServiceQuotaExceededException` | 该 region 还有未释放的 Static IP（默认 quota=5） | `aws lightsail get-static-ips` 看清单，释放无主的 |
| `attach-static-ip` 报 `OperationFailureException` | instance 处于非 `running` 状态 | `aws lightsail get-instance` 查 state，等到 `running` 再重试 |
| 切完 NEW ip 仍被污染 | 撞回同 region 老 IP，或同 ASN 被整段封 | 立刻重跑一次 swap；如再次撞回考虑跨 region 增设新 edge |
| DNS 没改但 smoke 通过 | 漏掉 verify 的 `--resolve` | 必须显式 `--resolve` 强制 SNI 到新 IP |
