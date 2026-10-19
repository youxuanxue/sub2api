---
name: tokenkey-stage0-release-rollout
description: >-
  Drive TokenKey Stage0 release, AWS prod deploy, UCloud cn-prod SSM deploy,
  smoke, rollback, and release-risk checks. Use for release tagging,
  deploy-stage0, cn-prod deploy_via_ssm, structured smoke, or post-release
  OAuth checks.
---

# TokenKey：Stage0 release → prod / cn-prod rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`。  
现网舰队 / AWS 发版入口：[`deploy/hetzner/README.md`](../../../deploy/hetzner/README.md)（**prod-only**）。  
UCloud cn-prod：[`deploy/domestic/README.md`](../../../deploy/domestic/README.md)（**SSM Hybrid 默认**，禁止常规发版开 SSH/离线 scp）。

## 确定性基线（机械化 vs 真判断）

release、目标解析、canary 选择、rollout、smoke/post-check verdict 均调用现有脚本；模型只判断发布影响和审批时机。具体入口见执行顺序；仅查询其它工具或维护流水线时展开工具表。 见 [操作细则](references/tools.md)。

## 调用参数

“发版/deploy 最新”默认 `target=prod operation=release`；检查影响用只读 `operation=check`，不 bump/tag/deploy。回放用 `operation=replay target=prod`，只准备 inactive color，任何结果都停在 `approval_pending=true`；禁止自动 promote/切流。执行 replay/rollback、修改可选检查或需要完整参数时必须先读细则。release 后默认只读 prod model_mapping check；原生 Anthropic 检查边界见操作细则，已退役的 snapshot/check 工具不再调用。配置漂移/SSM 失败记 yellow，不据此 rollback 镜像。见 [操作细则](references/operations.md)。

## 执行顺序（细节在脚本，不在本 skill 复述）

1. `operation=check`：只读对比 prev release tag → HEAD（`git log/diff` + deploy 契约文件），不 bump/tag/deploy。
2. `operation=release` + `target=prod`：`bash scripts/release-bump-and-tag.sh`（worktree 隔离）→
   watch `release.yml` → warm（可选：`dispatch-prod-deploy.sh --operation warm`）→
   `dispatch-prod-deploy.sh --operation deploy` → CI `tk_post_deploy_smoke: OK`。
3. 两阶段实测：`ops/observability/run-post-release-check.sh` + `scripts/release_post_check.py`
   （`--phase immediate` / `--phase delayed`；禁止模型自造 verdict）。
   Workflow 步骤名与 Summary 标题必须对齐：`Check PR hooks immediately`、
   `Check traffic and 5xx after 5 minutes`、`### Traffic / 5xx (+5 min)`（含
   completed requests / top paths）。
4. Prod smoke 后 advisory：`check-account-group-bindings.sh` 只读检查每个健康、可调度且有显式 `model_mapping` 的账号；无 active group 时输出 `review`；与同模型 peer 分组不相交及无 peer 的账号模型项只作提示，不能据此补授权或绑组。workflow 必须保持 `continue-on-error`。

Tag 域 SSOT：git / `release.yml` 用 `vX.Y.Z`；deploy/warm 镜像 input 用裸 `X.Y.Z`。本地入口经 `normalize-deploy-tag.sh`，可传两种形态。

Hard rules：`simple_release` 默认 false；bump/tag 提交不得带 skip-ci 字面标记（见 `CLAUDE.md` §9）。

## Jobs / OPC 默认部署顺序

**默认 `target=prod`：** release build → prod upgrade + full smoke → post-release checks。

例外：

- `target=prod`：只发版/部署 AWS prod（**默认路径**）。
- `target=cn-prod`：复用同一 GHCR 包，经 `deploy/domestic/deploy_via_ssm.sh`
  `pull` → `sync-controls` → `prepare`，再候选实测与 `bluegreen.py promote`。
  **禁止**默认走运维机 `crane`/`docker save` + 开放 UCloud SSH 22；离线 tar 仅恢复。

## 故障速查

发布/烟测失败时按报错查询；fail-stop，恢复后仍由 CI/脚本裁决，不自行编造 green。 见 [操作细则](references/troubleshooting.md)。

普通 gateway/all rollout 不更新独立 raw maintenance；修改 bootstrap 或独立 maintenance pin 时，读 [工具表的镜像发布之外入口](references/tools.md#镜像发布之外的入口)。发版不自动执行 host bootstrap。
