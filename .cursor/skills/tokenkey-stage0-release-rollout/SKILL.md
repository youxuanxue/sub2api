---
name: tokenkey-stage0-release-rollout
description: >-
  Drive TokenKey Stage0 release and rolling deploys to Tencent USW prod and
  Tencent Beijing cn-prod, including warm, smoke, legacy recovery, rollback, and
  release-risk checks. Use for release tagging, prod/cn-prod rolling deploy,
  image distribution, structured smoke, or post-release OAuth checks.
---

# TokenKey：Stage0 release → prod / cn-prod rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`。  
现网舰队 / prod 发版入口：[`deploy/hetzner/README.md`](../../../deploy/hetzner/README.md)（**prod-only**）。  
Tencent Beijing cn-prod：[`deploy/domestic/README.md`](../../../deploy/domestic/README.md)
（当前为入口机 + 多应用机 + 数据机；日常发版走 rolling，SSM/TAT 仅是远程执行通道，
禁止常规发版开 SSH/离线 scp）。

## 确定性基线（机械化 vs 真判断）

release、目标解析、canary 选择、rollout、smoke/post-check verdict 均调用现有脚本；模型只判断发布影响和审批时机。具体入口见执行顺序；仅查询其它工具或维护流水线时展开工具表。 见 [操作细则](references/tools.md)。

## 调用参数

“发版/deploy 最新”默认 `target=prod operation=release`；检查影响用只读 `operation=check`，不 bump/tag/deploy。回放用 `operation=replay target=prod`，只准备 inactive color，任何结果都停在 `approval_pending=true`；禁止自动 promote/切流。执行 replay/rollback、修改可选检查或需要完整参数时必须先读细则。release 后默认只读 prod model_mapping check；原生 Anthropic 检查边界见操作细则，已退役的 snapshot/check 工具不再调用。配置漂移/SSM 失败记 yellow，不据此 rollback 镜像。见 [操作细则](references/operations.md)。

## 执行顺序（细节在脚本，不在本 skill 复述）

1. `operation=check`：只读对比 prev release tag → HEAD（`git log/diff` + deploy 契约文件），不 bump/tag/deploy。
2. `operation=release` + `target=prod`：`bash scripts/release-bump-and-tag.sh`（worktree 隔离）→
   watch `release.yml` → `dispatch-prod-deploy.sh --operation warm --revision FULL_SHA` →
   `dispatch-prod-deploy.sh --operation deploy --revision FULL_SHA` →
   prod rolling workflow smoke 验收。warm/deploy 都必须传完整 40 位 revision。
3. 两阶段实测：`ops/observability/run-post-release-check.sh` + `scripts/release_post_check.py`
   （`--phase immediate` / `--phase delayed`；禁止模型自造 verdict）。
   Workflow 步骤名与 Summary 标题必须对齐：`Check PR hooks immediately`、
   `Check traffic and 5xx after 5 minutes`、`### Traffic / 5xx (+5 min)`（含
   completed requests / top paths）。
4. Prod smoke 后 advisory：`check-account-group-bindings.sh` 只读检查每个健康、可调度且有显式 `model_mapping` 的账号；无 active group 时输出 `review`；与同模型 peer 分组不相交及无 peer 的账号模型项只作提示，不能据此补授权或绑组。workflow 必须保持 `continue-on-error`。
5. 切流后 model_mapping advisory：`check-accounts --json --target <本次发版目标>`。**发哪个目标就要有哪个目标的证据**，cn-prod 不能用美西结果代替；参数语义与失败处置见 [操作细则](references/operations.md) 的 `account_model_mapping_check`。

Tag 域 SSOT：git / `release.yml` 用 `vX.Y.Z`；deploy/warm 镜像 input 用裸 `X.Y.Z`。本地入口经 `normalize-deploy-tag.sh`，可传两种形态。

Hard rules：`simple_release` 默认 false；bump/tag 提交不得带 skip-ci 字面标记（见 `CLAUDE.md` §9）。

## Jobs / OPC 默认部署顺序

**默认 `target=prod`：** release build → warm every prod app node → rolling deploy + smoke
→ post-release checks。

例外：

- `target=prod`：只发版/部署腾讯美西 prod（**默认路径**）。
- `target=cn-prod`：复用同一 GHCR 包。`distribute-cn-image.yml` 的 AWS publish job
  上传/校验私有 S3 归档，随后独立的 `tencent-ops` warm job 按 `prod-target.json`
  的 `apps[]` 顺序预下载；warm 固定 `--channel tat`，不能选 `auto`。
  Release 与分发都成功后，用
  `bash scripts/stage0/dispatch-prod-deploy.sh --operation cn-deploy --tag X.Y.Z --revision FULL_SHA`
  触发 `cn-prod-rolling-deploy.yml`。rolling 逐台 drain、换镜像、健康检查和回流，
  公网入口继续保持 `https://api.galaxywaveai.com`，发版不改 DNS 或 `public_origin`。
  `api-cn.tokenkey.dev` 仍指向美西，不能当作国内入口。
- **恢复路径**：`deploy-stage0.yml` 已降为 recovery-only / legacy blue-green；
  仅当 rolling 不可用、需要旧布局恢复或经单独批准时，使用
  `dispatch-prod-deploy.sh --operation legacy-deploy|replay|smoke-only`。
  cn-prod 的 `deploy_via_ssm.sh pull → sync-controls → prepare → candidate-smoke → promote`
  同样是恢复/兼容流程，不是日常入口。禁止手写 `base64 -d > ...`；送文件只用 `put-file`。
  2026-10-25 手写传输把 `/usr/bin/bash` truncate 成 0 字节的教训仍由 preflight 守门。
  `operation=replay` 只准备美西，不能用来切北京。

## 故障速查

发布/烟测失败时按报错查询；fail-stop，恢复后仍由 CI/脚本裁决，不自行编造 green。 见 [操作细则](references/troubleshooting.md)。

普通 gateway/all rollout 不更新独立 raw maintenance；修改 bootstrap 或独立 maintenance pin 时，读 [工具表的镜像发布之外入口](references/tools.md#镜像发布之外的入口)。发版不自动执行 host bootstrap。
