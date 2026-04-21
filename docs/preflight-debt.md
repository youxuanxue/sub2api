# Preflight Debt Log

记录 `scripts/preflight.sh` 当前**没有**机械门禁、但已知存在的"流程债务"项。
每条必须有截止日期或明确"不修"理由（dev-rules `agent-contract-enforcement.mdc` 强约束）。

---

## 已知漂移

### 1. sticky-routing 测试函数前缀 `TestUS201_*` ↔ 故事 ID `US-006`（命名约定漂移，非 doc-vs-code 漂移）

- **现象**（2026-04-20 修正后描述）：`docs/approved/sticky-routing.md` §6 表格、`.testing/user-stories/stories/US-006-sticky-routing-prompt-cache.md` 与 `backend/internal/service/sticky_session_*_test.go` 中的实际测试函数三方**一致**使用 `TestUS201_*` 前缀（共 22 个函数）；与故事 ID `US-006` 不匹配（前缀来源于早期"功能编号 201"的草拟阶段）。
- **早期版本误描述（已废弃）**：原版本声称"实际代码中为 `TestUS006_*` / `TestStickySessionInjector_*`"，与现实不符。`grep -rn 'TestUS006_\|TestStickySessionInjector_' backend/ docs/ .testing/` 返回 0 命中，`grep -rn 'TestUS201_' backend/internal/service/sticky_session_*_test.go` 返回 22 命中。**这是 debt log 自身的描述漂移**，2026-04-20 同步修正。
- **来源**：草拟设计时按"功能编号"写了 US-201；实施时按 `.testing/user-stories/index.md` 顺次拿到 US-006，故事文件 + 设计 doc + 测试代码全部沿用 `TestUS201_*` 前缀，未回头与故事 ID 对齐。
- **决策**：**不修**。理由：rename 22 函数 + 跑全套测试，与"消除真实风险"的 ROI 不匹配。下次新增 sticky-routing 相关测试一律遵循 `TestUS006_*` 命名；老命名保留作为历史。
- **未来门禁**：`Linked Tests` 与 `*_test.go` 对齐已由 **§5（closed）** 的 `verify_quality.py` 机械执行；`TestUS201_*` 与故事 ID `US-006` 的纯前缀不一致仍按 §1 决策保留，不由脚本强检。

### 2. CLAUDE.md "Current Gateway Flow" 段未提 sticky routing — **closed (2026-04-20)**

- **现象**：`docs/approved/sticky-routing.md` §10 计划在 CLAUDE.md 加一行 sticky routing 说明，未做。
- **整改**：随 `feature/newapi-fifth-platform` PR 一起补到 CLAUDE.md "Current Gateway Flow" 段尾——新增段落同时讲清调度池分桶（newapi）与 sticky routing（在分桶之上做 prompt-cache 优化）的层叠关系。
- **闭环 commit**：`90d5d90c`（`feature/newapi-fifth-platform` 分支 M8）。

### 3. `scripts/export_agent_contract.py` — 仅 audit 模式，未做 prefix-resolving generator

- **现象**：`feature/newapi-fifth-platform` PR 落地了 `scripts/export_agent_contract.py`，被 dev-rules 模板 `preflight.sh § 4 (agent contract drift)` 自动调用，但**仅作为 audit 工具**：
  - **强检（dev-rules `preflight § 4` hard-fail）**：`docs/agent_integration.md` 的 `# Agent Contract Notes` 段必须提及全部 5 个 first-class 平台（`openai/anthropic/gemini/antigravity/newapi`）。这是新增 newapi 时的 §0 级回归门禁。
  - **软检（warning，不 fail）**：`routes/*.go` 中 `<ident>.METHOD(` 字面量计数 vs `docs/agent_integration.md` 的 `- \`METHOD …\`` 列表条数；超 ±10% 提示人工审计。
- **未做**：完整的 prefix-resolving generator —— Gin 嵌套 `Group("/x").Group("/y")` 跨函数调用（如 `registerAccountRoutes(admin, h)`）需要 Go AST walker 或运行时 `engine.Routes()` dump（需 Wire DI + handler stub）。本 PR 试过 Python 字面提取，结果会把 `accounts.GET("/:id")` 错出成裸 `/:id`，反而退化 doc。
- **决策**：拆为 follow-up PR。理由（Jobs 聚焦）：本 PR 是"newapi 接入"，不是"contract generator 重写"；当下 audit 已经能挡住 §0 级"忘了写新平台"的回归，超出 ROI 反成包袱。
- **门禁**：dev-rules `preflight § 4` 已自动接入；route-count 警告留给 follow-up PR 把它升成 hard-fail。
- **截止日期**：next routes 重构 PR 之前必须做完（无固定日期，但下次有人新增/删除路由族系前会被 warning 提醒）。

### 4. newapi-as-fifth-platform US-008/009/010 e2e 缺口 — **故事降级到 Draft，acknowledged gap**

- **现象**：`docs/approved/newapi-as-fifth-platform.md` §5.2 要求 US-008/009/010 跑 testcontainer 化的端到端集成测试（HTTP→Auth→scheduler→bridge dispatch→真 PG → 真 newapi upstream）。本 PR 实际交付：
  - **已落（mock 单测，34 个 case）**：compat-pool predicate / scheduler-tier load-balance / gateway-tier sticky / messages_dispatch sanitize 的行为测试。这 34 个 case 覆盖了 US-011/012/013/014/015 五个故事的全部核心 AC（混池防御、池空报错、sticky 漂移降级、channel_type=0 排除、平台分桶、回归基线）。
  - **未落（US-008/009/010 核心 AC 直接未覆盖）**：
    - US-008 `POST /v1/chat/completions` 真 HTTP→Auth→bridge→newapi upstream e2e — **零 e2e 测试**
    - US-009 `POST /v1/messages` Anthropic→OpenAI 协议转换 + 真上游 e2e — **零 e2e 测试**
    - US-010 `POST /v1/responses` 入口端到端 — **零专属测试**（连 unit-tier 也没有 `TestUS010_*`，仅靠 scheduler 传递性覆盖）

- **诚实承认**（2026-04-20 audit）：

  原 §4 写过 3 条延期理由，全部站不住，已删除：
  1. "单测已锁死全部 §3 注入点的不变量" — 真，但 US-008/009/010 的核心 AC 不是"调度不变量"，而是"端到端走通"。这是用 sub-AC 替换 super-AC，是滑坡。
  2. "design §7.2 单 PR 原则 / 21 个单测保证行为契约" — 反向自圆其说。§7.2 原话是"实现 + 行为契约不可分"，恰恰支持 e2e 与实现一起落，而不是支持延后。
  3. "e2e 与本 PR 正交，延后不增合并风险" — 这一条**部分成立**，是唯一站得住的论据。

  **真实理由**（保留）：
  1. e2e 需要 docker daemon + testcontainer + Wire DI 完整启动 + 真 PG schema migration + 真 newapi upstream stub（含 channel_type=1 真 endpoint 联通）— 估 0.5–1 d 工作量。
  2. 本 PR 已经 11 commits + 1 merge，再扩大 e2e 测试 + fixture 会让 review 失焦、合并周期延长。
  3. e2e 相关 `*_integration_test.go` 与本 PR 现有代码正交（仅追加新文件，不改 production code），延后到 follow-up PR 不增加 production 风险。

- **决策**：
  - **本 PR**：US-008/009/010 status 从 `InTest` **降级回 `Draft`**（与"端到端 AC 未覆盖"事实对齐，遵守 `test-philosophy.mdc §6` 验证纪律）。
  - **Follow-up PR `feature/newapi-fifth-platform-e2e`**：交付 testcontainer 化的真 HTTP e2e；US-008/009/010 status 跑通后升 `InTest → Done`，本 debt §4 标 closed。
- **门禁**：follow-up PR 必须 (a) `go test -tags=integration -run 'TestUS00[89]_HTTP_|TestUS010_HTTP_' ./internal/handler/...` 全绿；(b) 附 testcontainer 日志到 evidence；(c) 同步把 index.md + 3 个 story 文件 status 升 `InTest`（runtime 通过即升 `Done`）；(d) 删除 3 个 story 文件里的 "Honest status note" 段。
- **截止日期**：2026-05-03（两周内）。
- **跨参考**：`docs/approved/newapi-as-fifth-platform.md` §9 第 5 行（acknowledged gap 标注）+ §11.4（本 PR 的诚实清单）。

### 5. `.testing/user-stories/verify_quality.py` — **closed (2026-04-20)**

- **现象（历史）**：`test-philosophy.mdc §5` 要求维护 `.testing/user-stories/verify_quality.py`，本仓库曾长期缺失；`dev-rules/templates/preflight.sh § 5` 对该段为 **skip**，`Linked Tests` 与真实 `func Test…` 的对齐依赖 review。
- **整改**：落地 `.testing/user-stories/verify_quality.py`（结构字段与章节、`Status` 词汇、`InTest`/`Done` 时 `*.go::TestFunc` 存在性、`Risk Focus` 四类风险、`运行命令` 可执行提示、`*(planned)*` 显式缺口）；报告输出 `.testing/user-stories/attachments/story-quality-report.md`。
- **门禁**：脚本存在时 `dev-rules/templates/preflight.sh § 5` **hard-fail**（不再 skip）；sub2api 经 `scripts/preflight.sh` 委托同一模板，本地与 CI 强度一致。
- **不再发生的依据**：每次 preflight 跑通即证明当前 `stories/US-*.md` 与引用测试未漂移；命名前缀与故事 ID 不一致类问题仍按 §1「不修」人眼处理。

### 6. 数字漂移历史 — design doc §11.2 单测计数

- **现象**：`docs/approved/newapi-as-fifth-platform.md` §11.2 在 2026-04-19 首版写"M5a 21 case"；merge 后审计发现实际 newapi-related 单测共 34（compat-pool 9 + scheduler 8 + sticky 5 + dispatch 12），数字源头是 M5a 提交里只统计了 scheduler+sticky 两类，遗漏了 M3 提交里已落的 compat_pool/dispatch test。
- **整改**（2026-04-20，本 PR merge 阶段）：标题更正为 "34 case"，并加入按文件细分的明细列表；preflight-debt §4 同步更新。
- **不再发生的依据**：design doc §11.2 现在提供按文件 `grep -cE "^func Test"` 的可复算明细；下次任何人加测试时，只要本 PR 的覆盖矩阵列表与统计一起改即可。
- **未来门禁**：可在 `docs/approved/*.md` 中新增 `<!-- stat:newapi-tests -->34<!-- /stat -->` 块，由 `dev-rules/sync-stats.sh --check`（preflight § 8）机械核对——目前未做，因为只有一处数字、人工 audit 成本低于建表本身。

### 7. dev-rules `templates/preflight.sh § 2` 在 worktree 内 commit hook 中假 fail — **closed (2026-04-20)**

- **现象**：worktree (`git worktree add`) 内 `./scripts/preflight.sh` 直接跑 PASS，但 `git commit` 触发 pre-commit hook 时 § 2 报 `FAIL: submodule SHA ... not found in dev-rules — submodule was not committed first`。
- **根因**（2026-04-20 复现确认）：git 在 hook 阶段把 `GIT_DIR=/path/to/sub2api/.git/worktrees/<name>` / `GIT_INDEX_FILE=...` 注入子进程；`templates/preflight.sh § 2` 内 `(cd dev-rules && git cat-file -e $sub_sha)` 子 shell 不 unset GIT_DIR，git 仍按上级 worktree 的 GIT_DIR 解析对象库 —— 而那个对象库是 sub2api 的，不存在 dev-rules 子模块的 SHA，所以 cat-file 必然 fail。复现脚本：`(export GIT_DIR=/.../sub2api/.git GIT_INDEX_FILE=... && bash dev-rules/templates/preflight.sh)` → §2 FAIL；unset GIT_DIR 后 PASS。
- **影响**（修复前）：
  - 所有从 worktree 发起的合法 commit 被 false fail 卡死。
  - 三次 sub2api commit（feature/newapi-fifth-platform 期间）被迫使用 `git commit --no-verify`，违反"hook 必须通过"硬规则；后续 PR 一直用"手动 preflight 已 PASS"作为脆弱补偿。
- **整改**（2026-04-20）：
  - 上修到 dev-rules 仓库 [PR #2](https://github.com/youxuanxue/dev-rules/pull/2) — 提取 `git_sub <subdir> <args...>` 辅助函数，在 subshell 内 `unset` 全部 8 个 git context env vars (`GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE / GIT_NAMESPACE / GIT_OBJECT_DIRECTORY / GIT_ALTERNATE_OBJECT_DIRECTORIES / GIT_COMMON_DIR / GIT_PREFIX`) 再调 git；§ 2 三处子 shell 调用全部走 helper。
  - dev-rules main HEAD = `7b69490`（从 `5fc8988`→`7b69490`，仅 1 个 fix commit）。
  - sub2api 本 PR bump submodule pointer 到 `7b69490`，hook 拦截恢复硬门禁，今后 worktree commit 不再需要 `--no-verify`。
- **回归矩阵**（在 sub2api 实测）：

  | 上下文 | 修前 | 修后 |
  |---|---|---|
  | 正常 CLI（无 GIT_DIR） | PASS | PASS（无 regression） |
  | Hook 上下文（GIT_DIR 已 set） | **FAIL（false fail）** | **PASS** |
  | dev-rules `verify-rules.sh` 自检 | PASS | PASS（8/8） |

- **不再发生的依据**：`git_sub()` 是结构性修复，所有未来需要在 hook 上下文调用子模块 git 的检查段都可以复用，不会再忘 unset。
- **附记**：修复后 `git -c core.hooksPath=/dev/null commit ...` 与 `--no-verify` 不再是"日常工具"，仅保留作 emergency override。

### 8. commit message body 提及 skip-marker 字面触发 GitHub Actions skip — **closed (2026-04-20)**

- **现象**：v1.4.0 release 准备阶段，VERSION bump commit (`4d82eb32 chore: bump VERSION to 1.4.0`) 的 message subject 干净，但 body 把 skip-marker 当字面字符串讨论（用于解释"不要带"的注意事项）。结果：
  - `git push origin main` → 没触发 CI workflow（`[CI]`/`[Security Scan]` 都没排队）
  - `git push origin v1.4.0` → 没触发 release workflow（release.yml 被静默吞掉，与 v1.3.0 同款）
  - 必须手动 `gh workflow run release.yml -f tag=v1.4.0 -f simple_release=false` 补救。
- **根因**：GitHub Actions 的 skip-message 检测对**整个 commit message（含 body）**做子串匹配，不区分上下文（代码块/反引号/转义都不豁免）。
- **复盘**（2026-04-20 audit）：
  - **CLAUDE.md §9.2 早已升级**（v1.3.0 事故后落地）：line 269 明确写 _"the trap goes further than the literal commit body... matched the literal substring inside the explanation"_，line 271/335 强制要求 `bash scripts/release-tag.sh vX.Y.Z`。
  - **`scripts/release-tag.sh` 早已存在**（5100 字节，5 项校验包括 `git log -1 --format=%B | grep -qE '\[skip ci\]|\[ci skip\]'`），用它本应被拦截。
  - **真实根因**：操作者直接 `git tag v1.4.0 && git push origin v1.4.0` 跳过 helper —— 这是 PR Checklist 里写明要用 helper 的 §9.2 规则被绕过，不是规则缺失。
  - **唯一缺的环节**：`deploy/aws/README.md §发版纪律` 第 1 条还是 v1.3.0 之前的旧措辞，没有同步到 CLAUDE.md §9.2 强度，也没指向 helper。运维路径文档与开发纪律文档不对齐，加大了"忘记 helper"的概率。
- **整改**（2026-04-20，本 fix PR）：
  1. `deploy/aws/README.md §发版纪律` 第 1 条重写到与 CLAUDE.md §9.2 相同强度，明确：(a) 任何位置 skip-marker 都会触发，(b) 必须用 `bash scripts/release-tag.sh vX.Y.Z`，(c) 唯一允许带的是 release.yml `sync-version-file` job 自动回写 commit。
  2. README 新增 §发版 SOP（开发者侧）+ §生产升级 SOP（运维侧），把 v1.4.0 实测路径标准化。
- **不再发生的依据**：CLAUDE.md §9.2 + helper + 本次同步过的 README，三处口径一致。下次发版只要走 `bash scripts/release-tag.sh vX.Y.Z`，helper 在 push 前就会精确 grep 拦截，事故无法重现。
- **未做（明确 out-of-scope）**：把 helper 的 grep 检查上提到 dev-rules `commit-msg` hook —— ROI 不高，因为 helper 本身已经强制（仅当 helper 被绕过时才会重演事故，而绕过 helper 本身就违反 §9.2）。如果未来又有人再次绕过 helper 直接 git tag，再考虑把 helper 升成 commit-msg hook + tag-pre hook。
- **跨参考**：v1.4.0 完整事故时间线见 docs/preflight-debt.md（git log），以及 `gh run view 24660924811` 的 manual dispatch recovery 记录。

### 9. AWS Stage-0 CFN 模板：改 ImageTag 触发实例 replace = PG 数据丢失风险 — **9.b closed (2026-04-20)**

- **现象**（2026-04-20 v1.4.0 发版时确认）：
  - `deploy/aws/cloudformation/stage0-single-ec2.yaml` 把 `ImageTag` 直接 substitute 进 `AWS::EC2::Instance.UserData`（line 272 `IMAGE_TAG='${ImageTag}'`）。
  - CFN 默认行为：`AWS::EC2::Instance` 的 `UserData` 字段任何变化触发 **Replacement: True** → 旧 instance terminate + 新 instance launch。
  - 模板**没有独立的 `AWS::EC2::Volume` / `AWS::EC2::VolumeAttachment`**，所有数据（PG / Redis / Caddy certs / pgdumps）都在 EC2 root EBS 里（`/var/lib/tokenkey/*` 全在 root）。`DeleteOnTermination: false` 保住了旧 EBS 不被删，但**新 instance 挂的是新 EBS**，旧 EBS 变孤儿，服务从空 PG 起来。
  - `deploy/aws/README.md §107`/§89 写的"生产升级方式：改 CFN 参数 + `aws cloudformation deploy`"在 stage-0 实际是 destructive 操作 — README 在这一段是错的。
- **暴露**：v1.4.0 准备 ship 时计划走 README §107 路径，验证 EBS 配置后发现实例 replace = PG 全丢；改走 README §141（"测试栈用 SSM"）的 in-place 路径。**CFN 当前显示 `ImageTag=1.2.0`**，但实例上一次升级（→1.3.1）已经走 SSM in-place、本次（→1.4.0）也走 SSM — CFN 已 drift 2 个 minor。
- **影响**：
  - 任何不知情者执行 `aws cloudformation deploy ... ImageTag=X` → 触发实例重建 → **prod 用户/配额/key/账单数据全丢**。这是 P0 级隐患。
  - CFN drift 让 `aws cloudformation describe-stacks --query Drift` 显示偏差，长期累积会让回滚/扩容/迁移决策失去 IaC 兜底。
- **决策**：拆 closed/open 两段：

  **9.a — README 紧急修订（closed 2026-04-20，本 fix PR）**

  - 表格 §升级 / 发版：prod 升级方式从"改 CFN 参数 + deploy"改成"**唯一安全路径**：SSM `docker compose pull && up -d tokenkey`"，附明确警告"会触发实例 replace、root EBS 上的 PG / Redis / Caddy / pgdumps 全部变孤儿，从空 PG 起来"。
  - 新增 §发版 SOP（开发者侧 — `bash scripts/release-tag.sh vX.Y.Z`）+ §生产升级 SOP（运维侧 — 完整 `aws ssm send-command` 模板，含 `.env` 备份 + healthcheck 等待 + 回滚），把 v1.4.0 实测路径标准化。
  - Quick Start 段 `ImageTag=1.1.0` 硬编码改为 `gh release list -L 1` 自动取，并注明"仅用于 stack 初始化时的首次镜像拉取，后续升级**不要**改这个参数"。
  - 测试栈段同样统一到 SSM 路径（删掉之前不一致的"测试栈用 SSM、prod 用 CFN"双轨）。
  - drift 现状告知：CFN `describe-stacks` 显示的 `ImageTag` 会与实际运行版本漂移，这是 stage-0 模板限制下的有意 trade-off，CFN 参数视为"初始化默认值"，实际版本以 `.env` 内 `TOKENKEY_IMAGE` 为准。

  **9.b — CFN 模板拆独立 volume（closed 2026-04-20，本 fix PR）**

  - **整改**（issue #8 在此 PR closed）：
    - `deploy/aws/cloudformation/stage0-single-ec2.yaml` 新增 `DataVolume` (`AWS::EC2::Volume`) + `DataVolumeAttachment` (`AWS::EC2::VolumeAttachment` 挂 `/dev/sdf`)，带 `DeletionPolicy: Retain` + `UpdateReplacePolicy: Retain` + `Encrypted: true`，新参数 `DataVolumeSizeGiB` 默认 30 GiB。
    - `UserData` 新增 §2a 段：候选块设备探测 (`/dev/nvme1n1` / `/dev/nvme2n1` / `/dev/nvme3n1` / `/dev/xvdf` / `/dev/sdf`，最多等 90s) + 排除 root device + `blkid` 检测既有 filesystem（首次 boot `mkfs.ext4 -L tokenkey-data`，后续 boot 复用）+ 写 `/etc/fstab` (`LABEL=tokenkey-data ... defaults,nofail`) + mount 到 `/var/lib/tokenkey`。
    - `UserData` 新增 §4 改造：秘密拆到 `/var/lib/tokenkey/.env.secret`（`POSTGRES_PASSWORD` / `JWT_SECRET` / `TOTP_ENCRYPTION_KEY`），首次 boot 生成后**永不重写**；`/var/lib/tokenkey/.env` 仍每次 boot 重生成接收 CFN 参数变化。这样实例替换时 PG 用户密码 + 已签发 JWT 会话 + TOTP 记录都活下来。
    - 新增 `DataVolumeId` Output。
    - DLM `SnapshotPolicy` 复用现有 `Backup=stage0` tag 自动覆盖（`ResourceTypes: [INSTANCE]` 会快照实例上所有附加卷）。
  - **变更门验证**（2026-04-20，对当前 prod stack `tokenkey-prod-stage0` 跑 dry-run change-set 后立即 `delete-change-set`，未 execute）：
    - **Add**：`DataVolume` (`AWS::EC2::Volume`)、`DataVolumeAttachment` (`AWS::EC2::VolumeAttachment`) — 符合预期。
    - **Modify**：`Instance` (`AWS::EC2::Instance`, Replacement: Conditional)、`EIPAssoc` (`AWS::EC2::EIPAssociation`, Replacement: Conditional) — UserData 变化触发，符合预期；仍是首次迁移必经路径。
    - 数据保护语义：本次 deploy 之后，**未来**任何 ImageTag / UserData 改动都只 replace `Instance`，`DataVolume` 仍 attach 到新实例 → filesystem 已存在 → 跳过 mkfs → 直接 mount → 数据零丢失。
  - **首次迁移**：现有 prod stack 数据**还在 root EBS 上**，本 PR 落地后第一次 `aws cloudformation deploy` 仍会触发实例 replace 并启动**空** PG（DataVolume 是新创建的）。运维侧需要按 `deploy/aws/README.md §现有 prod 栈迁移到 DataVolume` 的 7 步 SOP（snapshot → 停服 → tar → S3 → CFN deploy → 复制旧密钥 → restore → 验证）做一次 5–10 min 停机窗口的迁移。SOP 含完整 rollback 路径（snapshot 留 7 天）。新栈 deploy 不受影响。
  - **drift 短期防御**（依然推荐做）：preflight 加一段对比 stack `ImageTag` 与实例实际运行的 `TOKENKEY_IMAGE` tag —— 但这是 follow-up 改进，不是 9.b 闭环条件；优先级低于凭据泄露类 P0（见下方 **§10**，二者勿混为一谈）。
- **实操记录**（v1.4.0）：
  - 升级路径：`aws ssm send-command i-04a8afd18c997b8ac` → `sed .env 1.3.1 → 1.4.0` → `docker compose --env-file .env pull tokenkey && up -d --no-deps tokenkey` → 35s 内 healthy。
  - 验证：external `/health` HTTP 200，bootstrap 日志全绿，3 min 内 0 错误，多架构 manifest 4 tag 一致。
  - 实际 downtime：仅 `tokenkey` 容器 ~30s；caddy / postgres / redis 不重启。

---

## 历史事件

### 2026-04-18: sticky-routing 实施先于审批

- **事件**：`docs/approved/sticky-routing.md`（created 2026-04-17, status=pending）未走审批门禁；
  单提交 `a68dee5b` 直接落地 main 并上线 prod，包含 schema + injector + 6 处接入点 + 单测 + UI。
- **暴露的根因**：当时缺少机械门禁，靠"自觉"遵守 `product-dev.mdc` §阶段 2→审批→阶段 3 顺序，在追产物的压力下被绕过。
- **整改**（2026-04-19）：
  1. 回填 `docs/approved/sticky-routing.md` frontmatter `status=shipped` + `related_commits: [a68dee5b]` + `related_stories: [US-006]`；新增 §11 实施情况章节。
  2. 新增 `scripts/check_approved_docs.py`：拒绝 `status=pending` 但 `related_prs/related_commits` 非空的 doc（即"shipped under pending"同款）。
  3. 新增 `scripts/preflight.sh § 1` 段调用上述脚本；本日起 commit / CI 强制运行。
- **后续演进**（2026-04-19 当日，见下条）：脚本于同日上提到 dev-rules submodule，执行链改为 `dev-rules/templates/preflight.sh § 7 → dev-rules/scripts/check_approved_docs.py`；项目级 `scripts/preflight.sh § 1` 段被删除，但拦截语义不变（R1-R5 同步生效在所有消费者项目）。
- **不再发生的依据**：`dev-rules/scripts/check_approved_docs.py` R3 规则机械拦截。任何 doc 一旦在 frontmatter 写了 commit / PR，必须同时把 status 翻为 `shipped`，否则 hook fail。

### 2026-04-19: 接入 dev-rules submodule + 上提 check_approved_docs.py

- **事件**：`scripts/preflight.sh` 与 `scripts/check_approved_docs.py` 都是 sub2api 私有，但前者只调用后者一行、后者本身是「跨项目共享的 frontmatter 不变量」；同时 dev-rules 仓库已存在 `templates/preflight.sh` 模板（8 段，覆盖本项目所需全部检查）。两份冗余实现导致：
  1. 任何对 frontmatter 规则的演进（如 `ALLOWED_STATUS` 加 `approved` 以兼容 zw-brain GATE 模型）都要同时改两处；
  2. 项目 wrapper 仅 1 段、模板有 8 段，本地 commit 实际只跑 1 段就放行——CI 与 hook 强度不一致。
- **整改**（2026-04-19）：
  1. 在 dev-rules 仓库新增 `dev-rules/scripts/check_approved_docs.py`（ALLOWED_STATUS = {draft, pending, approved, shipped, archived}），由 `dev-rules/templates/preflight.sh § 7 R1-R4` 在任何分支上调用；R5 (`approved_by: pending`) 仍仅在 main/master 拦截。
  2. 改 `dev-rules/templates/install-hooks.sh`：项目无 `scripts/preflight.sh` 时，pre-commit hook 自动 fallback 到 `dev-rules/templates/preflight.sh`（8 段全跑）。
  3. sub2api 接入 dev-rules 为 git submodule（`dev-rules/`），删除项目级 `scripts/preflight.sh` + `scripts/check_approved_docs.py`，沿用 dev-rules 模板（CLAUDE.md §10 记录此选择）。
  4. CI `.github/workflows/backend-ci.yml` 新增 `preflight` job（`submodules: recursive`），与 pre-commit hook 走同一脚本，本地与 CI 强度对齐。
- **不再发生的依据**：单一事实来源（dev-rules）+ 本地与 CI 调用同一脚本 + dev-rules-convention.mdc §"Git 提交顺序" 与 preflight § 2 子模块指针检查共同保障"先子模块后父仓库"。
