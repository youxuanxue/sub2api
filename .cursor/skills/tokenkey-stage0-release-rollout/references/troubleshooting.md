## 故障速查

| 现象 | 处理 |
|------|------|
| `release-bump-and-tag.sh` 无输出且 exit 1（action=tag-only） | 已修：`field()` grep 无匹配 + `set -e` 静默退出。升级后重跑；临时绕过 = worktree @ origin/main + `release-tag.sh vX.Y.Z`。 |
| `push origin HEAD:main` / GH006 **Protected branch** | 先 `bash scripts/release-configure-main-bypass.sh`；仍失败则 fallback `release-bump-via-pr.sh`。 |
| bump PR CI 仅 **preflight** flaky fail | `gh run rerun <run_id> --failed`，再 `release-bump-via-pr.sh --pr <N>`；不要改 VERSION 对冲。 |
| 发版后残留 `sub2api-release-*` / `sub2api-bump-pr-*` worktree | `git worktree list` → `git worktree remove --force <path>`；否则后续 `worktree add` / `gh pr merge --delete-branch` 会失败。 |
| release 时主 checkout 在别的分支 / 有别人的 WIP | 正常现象（并行 agent），不要去切分支、stash 或还原别人的文件；release 脚本本来就不读写当前 checkout。 |
| `release-bump-and-tag.sh` push 被拒（origin/main moved，非 protected） | 期间有新 PR 合入；直接重跑脚本，它会基于新的 origin/main 重建 worktree。 |
| `release-tag.sh` 报 HEAD 含 skip-ci 标记 | 修改触发打 tag 的最近一次提交说明后重试，或按 `CLAUDE.md` 用 `gh workflow dispatch` 触发 `release.yml`。 |
| `tag already exists on origin` | 升 `VERSION` 再打新 tag，或仅 dispatch deploy 已有 tag。 |
| deploy 报单架构 manifest | 重新跑 `release.yml` 且 `simple_release=false`；prod 都不要 override。 |
| 误 dispatch 了一个多余 prod deploy run | release 不再自动 queue，多出来的一定是手动重复 dispatch；取消多余 run、watch 留下的那个即可。 |
| prod workflow `tag must match X.Y.Z, got: v…` | 本地入口已修：`dispatch-prod-deploy.sh` 经 `normalize-deploy-tag.sh` 剥 `v`。若仍出现，说明绕过了这些入口（手写 `gh workflow run … -f tag=v…`）；改传裸 `X.Y.Z` 或走脚本重试。 |
| `gh run watch` 被工具超时打断 | 用同一 run id 再执行 `gh run watch <id> --exit-status` 接到终态。 |
| 发版后 tick 报 `No such container: tokenkey` | 先确认在用新版 `ops/observability/probe-post-release-tick.sh`；它默认 `CONTAINER=auto` 会解析 prod blue/green active container。不要手工猜 `tokenkey-green`；若仍失败，看 tick stdout 的 `container_resolution`。 |
| `TK_SMOKE_GITHUB_ENV=prod` 报 `unexpected gh variables response` | 旧版 `load_smoke_github_env.py` 对单页 gh api 响应断言成 list 的 bug，已修；若复现先 `gh api repos/{owner}/{repo}/environments/prod/variables` 看原始形状。 |
| prod `Deploy via SSM Run-Command` 报 `AccessDenied(ssm:SendCommand)` | 先 `python3 ops/stage0/resolve_prod_ssm_target.py --format json`，以返回的 `instance_id`（现网为腾讯 Hybrid `mi-*`）和 `ssm_region` 核对 OIDC/SSM 权限；身份见舰队清单；`aws` / `aws-fresh` / `hetzner` 均已退役并失败关闭，不要钉死。 |
| cn-prod 发版误走运维机 `crane`/scp + 开放 SSH 22 | 改走 `deploy/domestic/deploy_via_ssm.sh`（SSM Hybrid）；CI 私有 S3 归档 + SSM 是默认分发；运维机搬运 tar 仅恢复路径，见 `deploy/domestic/README.md`。 |
| cn-prod `prepare` 报 `candidate name ... is held by a running/restarting/paused container` | 该名字上有活动容器，不是上一轮残留。**不要删**，先查清是谁（可能是在途发版或当前活动色）。已停止的残留现在由 `reclaim_candidate_name` 自动回收，不再需要人工 `docker rm`。 |
| cn-prod `deploy_via_ssm.sh pull --transport s3` 报 cannot import boto3 | S3 归档渲染器需要 boto3，而本地 PATH 上的 `python3` 常常没有。按提示换一个有 boto3 的解释器；`--transport ghcr` 是显式退路。不要绕过入口直接调 `bluegreen.py`。 |
| cn-prod promote 报 `No such file or directory: .../prepared.json` | `--state` 给错了。取 `prepare` 回执里的 `state` 字段（形如 `/opt/tokenkey/releases/<version>-<shortsha>`），不要照抄文档示例。 |
| cn-prod `deploy_via_ssm.sh` 报 SSM identity not ready | 核对 `prod-target.json` 的 `ssm_instance_id` / `ssm_region`，与 `aws ssm describe-instance-information` 的 Name=`tokenkey-cn-prod`（也接受 `cn-prod`）、PingStatus=`Online`。 |
| cn-prod promote 报 `public-url does not match the domestic target manifest` 或 `public static IP` | `--public-url` 用报错里的 `public_origin`。`api.galaxywaveai.com` 是北京第二站点，不是这个验收地址。不要改清单来迁就域名。 |
| cn-prod 切流报 `galaxywave api site must not declare its own reverse_proxy` | `api.galaxywaveai.com` 必须 `import tk_upstream`，不能再写一条 `reverse_proxy`。蓝绿只改共享的那一条。 |
| prod smoke Gemini tools **429** + soft-skip + **`tk_post_deploy_smoke: OK`** | 运行时资源/cooldown，**不是** passthrough 路由回归；verdict green/yellow，不 rollback。若要 200 证据，cooldown 后重跑 deploy-stage0 smoke。 |
| prod smoke Gemini tools **400** + Codex 账号文案 | 路由回归（#1168 类）；**red**，rollback `previous_tag` 。 |
| prod smoke 报 configured smoke model not listed in GET /v1/models | 不是代码回归，改 **`prod`** Environment 对应的 **`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS`** 为 `TK_SMOKE_API_KEY` 可见模型后重跑。 |
| `gh` 请求持续报 `read ... 127.0.0.1:7890: connection reset by peer` | 先用 `env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh <cmd>` 做无代理重试；恢复后再继续 watch/dispatch。 |
| 无代理后 dispatch 报 `HTTP 403 Must have admin rights to Repository` | `gh` 可能切到另一个账号；先 `env -u GH_TOKEN ... gh auth status`，必要时 `gh auth switch -u <repo-owner>` 后重试 dispatch。 |
| 发版后 Account model_mapping `check-accounts` 报 violation | **不要** rollback 镜像；审 `$JOBDIR/post-release-account-model-mapping-check.json` 的账号/group diff。期望与 forbidden policy 均来自 Go SSOT；确认要覆盖 live 配置时走 `/tokenkey-modelops-planner`：`sync-runtime` 先对单个显式 target 做 dry-run，批准后用 CLI 固定短语写入；账号持久层另走 `apply-accounts --confirm yes-apply-account-model-mapping`。 |
| 发版后 Account model_mapping `check-accounts` SSM 失败 | 记 yellow；prod 镜像仍有效。补 OIDC/实例在线后重跑 `python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json`。 |
| 发版后账号分组检查报 `review` | **不要** rollback。查看 `findings[].code`：`no_active_group` 先审 `candidate_groups`；`model_group_peer_mismatch` 比对同模型 peer 证据。该检查只建议、不写线上，也不把无 peer 的新模型当违规。 |
