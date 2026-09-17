## 调用参数

本 skill 默认按用户语义解析；用户未写完整参数时，先按下面语义补全，仍有歧义再问。

```text
/tokenkey-stage0-release-rollout target=<prod|edge-<edge_id>|all> [tag=X.Y.Z] [operation=<check|release|replay|deploy|smoke|rollback>] [replay_receipt=SHA256] [previous_tag=X.Y.Z] [anthropic_config_check=false] [account_model_mapping_check=false] [main_via_edge=false]
```

| 参数 | 语义 |
|---|---|
| `operation=check` | 只做预发布风险检查：对比上一个 release tag 到待发布 HEAD 的代码事实，判断上线 prod/Edge 的潜在影响；不 bump、不 tag、不 dispatch deploy。 |
| `operation=replay` | **仅允许 `target=prod`**：以 `STAGE0_BLUEGREEN_STAGE=prepare` 部署 inactive color，再用测试 universal key 对该候选执行完整用例。请求并发与间隔由 host runner 固定；每条用例均生成结果，停止时保留剩余义务。不得调用 promote、Caddy reload 或 edge rollout；回执交用户审核，不是切流授权。 |
| `target=prod` | release（必要时 bump/tag/build）→ `deploy-stage0.yml -f tag=…`（绑定 **`prod`** Environment）→ prod smoke → **默认** Anthropic OAuth snapshot/check + Account model_mapping check。 |
| `target=edge-<edge_id>` | 默认 tag 已存在：用 **`bash scripts/stage0/dispatch-edge-deploy.sh`**（edges 均为 Lightsail，路由到 `deploy-edge-lightsail-stage0.yml`）→ watch → 按 phase 验收 smoke。`operation=smoke` 只 smoke；`operation=rollback` 用 `previous_tag`。不要手选 workflow 或手填 confirm_instance。 |
| `target=all` | release 一次 → canary **upgrade (full)** → prod deploy（CI smoke）→ **默认跳过** canary `main-via-edge` → 其余 Edge **infra rollout** → followup → **默认** Anthropic OAuth snapshot/check + Account model_mapping check。`main_via_edge=true` 才跑可选段。 |
| `main_via_edge` | 默认 **false**。`target=all` 时不跑 prod→Edge 中转 smoke；缺 key 或 by-design 503 不得据此 rollback。 |
| `anthropic_config_check` | 默认 **true**（`operation=release` 且 smoke 验收通过后）。跑 `/tokenkey-anthropic-oauth-config` 的 **Stage 1–2 only**（snapshot + check，只读）。`anthropic_config_check=false` 跳过。`operation=check/smoke/rollback` 默认不跑。 |
| `account_model_mapping_check` | 默认 **true**（`operation=release` 且 smoke 验收通过后）。跑 `manage-account-model-mapping-runtime.py check-accounts --json`（默认 prod only），只读 diff prod 显式 `model_mapping` 与 Go SSOT floor/policy metadata。violation 或 SSM/OIDC 失败记 **yellow**，不 rollback 镜像。`account_model_mapping_check=false` 跳过。edge 空 mapping 不纳入 post-release 检查；需显式 `--include-edges` 才查 edge。 |

### 回放与审核

用户说“回放 / 只部署 prod，不切流”时，使用 `operation=replay target=prod`。
必要时先完成 release/build，再 dispatch `deploy-stage0.yml -f operation=replay -f tag=X.Y.Z`。
已有同 tag 且指纹一致的 prepared candidate 直接复用。仅 ops 验证器变更时可从通过
preflight 的版本化工作区运行同一个 `scripts/stage0/replay-prod-release.py`，验证已有镜像。

入口默认执行版本化账号供给清单及合成 fixture，不再用历史 capture 定义覆盖分母。
`gateway_capability_host.py` 直连正常蓝绿候选内部地址，按生产库 `api_keys.name`
解析测试 universal key（默认名 `TK_FULLTEST_KEY`，可用 `--test-key-name` 指定；
这不是 GitHub `secrets.TK_FULLTEST_KEY` 密钥材料），执行工具续轮/媒体语义、串行限速和 usage 归因。
不创建额外网关/PostgreSQL/Redis，不复制数据库，不创建或重绑测试身份，不设 replay 专属 load/PSI 门槛。
请求正常计费和记录 usage；SQL 查询只读。账号由正常 universal 路由选择，结果记录实际账号、
是否匹配计划账号类，以及回执中的 `account_class_coverage`。能力不支持、真实错误和未执行项均保留逐条原因，不删分母。

workflow 上传 `replay-receipt.json` 与 `replay-results.json`；本地入口输出相同工件。
执行器的 `green/red` 是唯一功能验收结论，审批须同时看 `account_class_coverage`。任何结果都停在 `approval_pending=true`，
**不是切流授权**。账号供给 receipt 的 `deployment_gate=false`，不能传给历史
`replay_receipt` promote 入口。历史实验回执保留原文件和原校验契约，不回填新结果。

替换不同 tag 候选必须传当前 prepared fingerprint（`--replace-receipt` / workflow
`replace_receipt`），走 bluegreen owner 的替换门禁。验证结束向用户交付结果、覆盖限制、
账号类命中、测试 key usage 与线上指纹对比；禁止自动继续 prod deploy、smoke 或 edge rollout。

如果用户只说“发版 / deploy 最新 / ship production”，默认 `target=prod operation=release`。如果用户说“全部 / 所有网关 / prod + edge / all”，默认 `target=all operation=release`。如果用户说“检查 / 预判 / 评估上线影响 / release check”，默认 `operation=check target=all`。
