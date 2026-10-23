---
name: tokenkey-stage0-release-rollout
description: >-
  Drive TokenKey Stage0 release, Tencent USW prod deploy, Tencent Beijing cn-prod SSM deploy,
  smoke, rollback, and release-risk checks. Use for release tagging,
  deploy-stage0, cn-prod deploy_via_ssm, structured smoke, or post-release
  OAuth checks.
---

# TokenKey：Stage0 release → prod / cn-prod rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`。  
现网舰队 / prod 发版入口：[`deploy/hetzner/README.md`](../../../deploy/hetzner/README.md)（**prod-only**）。  
Tencent Beijing cn-prod：[`deploy/domestic/README.md`](../../../deploy/domestic/README.md)（**SSM Hybrid 默认**，禁止常规发版开 SSH/离线 scp）。

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

- `target=prod`：只发版/部署腾讯美西 prod（**默认路径**）。
- `target=cn-prod`：复用同一 GHCR 包，经 `deploy/domestic/deploy_via_ssm.sh`
  `pull --tag X.Y.Z --revision FULL_SHA` → `sync-controls` → `prepare`，再候选实测与 `bluegreen.py promote`。
  Release 成功后，等待对应 release run 触发的 `distribute-cn-image.yml` 成功（私有 S3 + SSM 预下载）；
  不以 Release 构建绿灯代替分发绿灯。重复 pull 复用已验镜像，初始化权限与保留期见国内 README。
  `--transport ghcr` 为显式备用路径，S3 缺失/失败不自动降级。
  **禁止**默认走运维机 `crane`/`docker save` + 开放 cn-prod SSH 22；运维机搬运离线 tar 仅恢复；CI 校验归档经私有 S3 + SSM 分发是默认路径。
  `promote --public-url` 必须等于 `deploy/domestic/prod-target.json` 的 `public_origin`。这是当前公网验收入口，不是北京网卡地址，也不是 `api.galaxywaveai.com` 或 `api-cn.tokenkey.dev`。`post_release.py` 在切流前拒绝域名和其他公网 IP。
  `api.galaxywaveai.com` 和 `154.8.214.179` 共用一条 `reverse_proxy`。切流换北京活动容器时，这个域名、静态 IP，以及转到这台机器的 `public_origin` 一起换版本。发版不改 DNS，也不把该域名写成 `public_origin` 或 `SERVER_FRONTEND_URL`。`api-cn.tokenkey.dev` 仍暂时指向美西，日后退役，不能当作国内长期入口。
  `operation=replay` 只准备美西，不能用来切北京。候选打开 telemetry archive 时 `bluegreen.py` 直接拒绝。

## 故障速查

发布/烟测失败时按报错查询；fail-stop，恢复后仍由 CI/脚本裁决，不自行编造 green。 见 [操作细则](references/troubleshooting.md)。

普通 gateway/all rollout 不更新独立 raw maintenance；修改 bootstrap 或独立 maintenance pin 时，读 [工具表的镜像发布之外入口](references/tools.md#镜像发布之外的入口)。发版不自动执行 host bootstrap。
