# 腾讯美西迁机交接

更新：2026-10-21 23:57 UTC / 北京时间 2026-10-22 07:57。**当前未准备就绪，未授权停写或切流。**

## 1. 接手先读

- 用户授权继续准备、隔离验证；**禁止影响 AWS prod，禁止停写、drain、源端 WAL switch、真实 standby 晋升、DNS/TTL 或生产代理绑定修改**。正式窗口须用户另行明确批准。
- 保留 S3、AWS 控制面、日本代理。美西代理调整推迟到主机切流后。
- 实例/IP/区域唯一来源：[舰队入口](../../deploy/hetzner/README.md)。腾讯云地域与其 SSM 注册区域不同，执行前核对。
- 工作区 `/Users/feng/Codes/sub2api-wt-tengxun1020`，分支 `chore/tengxun1020`，HEAD `b757c45c3`。迁机修改混有 staged/unstaged/untracked，**尚未提交；保留全部，不 reset、不换回主工作区**。
- 每条命令绑定该 workdir；按 `git-worktree-submodule` 技能执行 `session-check`。迁机流程 owner：[pgbackrest-host-migration.md](../../ops/stage0/pgbackrest-host-migration.md)。

## 2. 当前最紧急故障：先恢复 standby 的仓库读取

**不要沿用此前“自动续期已完成”的口头状态。安装首次成功，后续暴露运行目录被 systemd 清理的问题；尚未修复。**

23:55:28 UTC 诊断回执 `inv-xa7a1pgx3x`：

| 项目 | 实测状态 |
|---|---|
| 刷新 timer | `tokenkey-backup-identity.timer` 已 stop；此前 enable，未 disable，重启可能再次启动 |
| service 有效配置 | `RuntimeDirectoryPreserve=no`、`RuntimeDirectoryMode=0755`，与预期不符 |
| 消失的目录 | `/run/tokenkey-recovery`、`/run/tokenkey-backup-candidate`，包括 pgBackRest 配置 |
| 真实 standby | `tk-archive-standby` 仍运行，recovery=true、read_only=on，replay=`1C/35000000` |
| 恢复追赶 | **未重新验证，不能宣称仍持续追 WAL**；Docker 仍绑定已删除的旧目录 |

本地安装脚本 `helpers/install-hybrid-backup-identity.sh` 使用未正确续行的多行 `sed ... a`；有效配置证明保留设置未生效。**不要原样重跑该安装器。**

接手修复顺序（只操作腾讯）：

1. 核对 timer 仍暂停；完整重写 unit，正确设置目录保留，或由独立 tmpfiles 管理目录。用 `systemctl show` 验证有效值，不能只看文件文本。
2. 重新生成 tmpfs 配置。工具在目标 `/usr/local/lib/tokenkey-migration/tencent_backup_identity.py`，源码见仓库同名文件；`refresh` 验证主机、STS UserId 后才写配置，不输出密钥。
3. **仅重建同路径不能修复 Docker 旧 bind mount。**核实 `standby.signal`、数据目录、镜像、system identifier 和 timeline 后，安排仅该 standby 的受控重挂载/容器重建；保留 PGDATA，不重新全量恢复、不 promote。普通 `docker restart` 是否重建绑定必须实测，不能假定。
4. 验证容器内真实 S3 `info`/WAL 读取、recovery/read_only/archive_mode、同源 timeline 和 replay 继续前进；再恢复 timer，跨至少两次定时执行验证配置仍存在且容器可读取。
5. 保留失败回执，更新 [准备状态 JSON](reports/tencent-cutover-readiness-20261022.json)。不要打印配置文件、SSM 凭据或 `.zshrc`。

加密盘仍挂载，数据在 `/srv/tokenkey-rehearsal/standby-pg`；DockerRootDir 为 `/srv/tokenkey-rehearsal/docker`。
**不要重启主机/关闭 LUKS 来修此故障**；整机重启仍需人工交付解锁材料。当前未验收生产自动解锁/启动。

## 3. 身份权限：已实际发生的变更

- 复用 `tokenkey-lightsail-ssm-hybrid`，未新建角色或长期访问密钥。
- 腾讯 SSM Agent 在 `/root/.aws/credentials` 维护 default 临时身份，mode 0600；STS 已证实是该腾讯主机的 managed-instance session（`inv-0a79rjgkqt`）。
- **已添加并读回核对** inline policy `TencentUswPhysicalBackup`：所有语句限定该腾讯主机 UserId；物理桶 `prod/postgres/` 只读且显式拒绝非读取对象操作，`candidates/tencent-usw-20261022/` 可管理备份对象。既有 `EdgePgdumpPutOnly` 未替换。策略生成器：[tencent_backup_identity.py](../../ops/stage0/tencent_backup_identity.py)。
- 首次刷新后真实源仓库读取成功，最新标签 `20261019-234616F_20261021-223007I`（`inv-xa79x40t8s`）；这不证明定时刷新可靠，见上节故障。
- 当前操作账号缺 `iam:SimulatePrincipalPolicy`，官方策略模拟未通过权限检查；不能写成模拟通过。已完成的是本地边界测试、IAM 安装读回、目标 STS 与真实源仓库读取。
- 原手工 STS 到期时间为北京时间 **10 月 22 日 10:21:37**，只是历史会话到期；其配置已被后续流程替换/清理，**不能依赖该时间判断当前有效性**。
- 尚无外部告警通知；刷新 service 返回 success 不等于 S3/WAL 或应用健康。

## 4. 已验证与剩余放行项

| 已验证 | 限制 |
|---|---|
| 真实 S3 最新/历史恢复、跨架构及账务校验 | 不是整站上线验收；详见[实验报告](reports/tencent-us-lighthouse-proxy-validation-20261021.md) |
| standby 断网、容器重启、过期凭据不晋升 | 本次目录故障后的恢复尚未验证 |
| amd64 `1.8.313` 已缓存；合成空库启动约 8–9 秒 | 未验收生产配置、密钥、真实 UI；源若继续发版须重核 |
| 合成本地仓库：完整链复制后晋升，新 timeline 增量及恢复成功 | **不必因新 timeline 一律重做全量**；生产 S3 预复制的一致性、完整依赖和最终补齐仍待验收 |
| 最终 WAL 离线校验器、快照比较器 | 不证明实际写隔离，不授权 promote，现场回执采集器/完整切流执行器未验收 |

真实 S3 合成接力实验 `inv-0a7a0pgpg9` 在创建 tmpfs 配置目录时失败，**尚未进入备份/晋升步骤**。
可能遗留 `/srv/tokenkey-rehearsal/synthetic-s3-timeline-*` 空工作目录，核实归属后处理，禁止泛化清理。

放行前还须完成：

1. 修复并持续验证上述身份/挂载故障；正式存储、异机密钥恢复和重启契约。
2. 腾讯正式应用配置、密钥、Caddy/TLS、定向域名及真实 UI 验收；避免隔离副本触发真实业务回调。
3. 发布适配：prod resolver 默认仍为 Tokyo；尚不支持腾讯。Hybrid 存储检查仍限定 Hetzner 卷，不能绕过守卫冒用身份。腾讯 `/var/lib/tokenkey` 正式布局未安装。
4. 新 timeline 的真实 S3 备份/恢复、完整链预复制和健康检查交接；现有连续性检查遇到跨 timeline 会拒绝健康。
5. 现场写者清单、最终 WAL 回执、可中断续接的执行流程，以及新库写入后的回退方案。

最新源端只读检查：23:38:39 UTC，`1.8.313`、healthy、draining=false、在途 5、归档失败数 0（SSM `a204c560-deea-41d4-8ef8-b291f14fcf04`）。这是历史样本，接手须重新只读核对。

## 5. 最短停写窗口与会话断线处理

**本节是另行获批窗口的顺序，不是已验收的自动执行脚本。当前不得执行。**

提前做完：锁定镜像/配置、下载与备份链预复制、TLS/入口定向验收、身份续期、目标启动和备份保护验证。
窗口只保留：**冻结发布与写者 → 排空在途 → 最终命名点与主动 WAL 归档 → 入仓/重放/一致性确认 → 晋升 → 新链保护及应用验收 → 入口切换**。
写者清单须包含网关、长流、旧颜色、QA cleanup/provision、cron、外部维护/直连；`archive-check` 调用 `pgbackrest check`，须避免与最终归档竞争。DB 事务为零不等于 HTTP 已排空。

断线时的新会话/人工操作员：

1. 使用**不经过待切网关的本机 CLI + TAT/SSM**接管。切流前必须准备独立操作终端；目前没有经过验收的无人值守切流执行器，不能依赖本会话自动完成。
2. 每次提交远程命令，先保存 `CommandId/InvocationId` 和阶段回执；超时/断线不等于命令未执行。先查询原任务，再决定下一步，**不盲目重发**。
3. 回执至少记录 UTC、双方实例身份、发布 digest、写者隔离结果、最终 LSN/WAL/timeline、目标 recovery 状态、是否已产生目标写入、入口状态。落盘并同步到独立控制端，不只留聊天。
4. 无法确定阶段时先只读核验：源写隔离、目标是否 primary/已写、DNS 与定向健康；保持单写，停止进一步切换，不根据 DNS 一项推断数据库角色。
5. **晋升前失败**：按已验证步骤撤销本次源端隔离、恢复源服务。**已晋升或写入状态不明**：先查证，不能自动解冻源。**目标已有写入**：禁止直接切回旧库；先停目标写者、保护新数据，再按验证过的反向迁移恢复。

生产窗口时长尚无完整实测，不能承诺此前 3–8 分钟。合成小库的秒级结果不是生产停写估计。旧机保留至少七天，释放另行授权。

## 6. 接手入口与证据

- 共享入口：[Draft PR #2753](https://github.com/youxuanxue/sub2api/pull/2753)，分支 `chore/tengxun1020`。本地未提交状态描述的是交接生成时的快照；远端可复用版本以 PR 最新提交为准。其他 agent 按 worktree 技能建立自己的工作区并取该分支，先读本文，再核对最新回执，禁止多人同时修改腾讯运行状态。
- **异机接手**：PR 包含迁机工具、测试和脱敏报告，**不包含**下面的私有 `helpers/`、原始回执、云凭据或 LUKS 密钥。没有本地 helper 时，使用已授权 AWS CLI/SSM 或腾讯 TAT 直接连接舰队 SSOT 中的验证主机；先只读检查身份及 service/timer 配置。目标已安装的身份工具位于 `/usr/local/lib/tokenkey-migration/`。需要原始证据或解锁材料时走既有受控交付，不能从 PR 恢复秘密，也不能假定这些私有脚本已发布。
- 本地私有状态目录：`~/.local/state/tokenkey-tencent-usw-validation/`；`helpers/` 有已用脚本，`*-invocation.json`/`*-result.json` 有命令回执。**文件可能含密钥或密文，不批量 cat，不进 git**。
- TAT 查询既有任务（只读）：

  ```bash
  zsh -lc 'source ~/.zshrc >/dev/null 2>&1; python3 ~/.local/state/tokenkey-tencent-usw-validation/helpers/tokenkey-usw-tool.py result diagnose-identity-runtime'
  ```

- 修复脚本经检查后，用同一 helper 的 `run rehearsal <唯一任务名> <绝对脚本路径> <超时秒数>` 提交，紧接着保存返回 ID；禁止直接重跑旧安装脚本。
- 源只读观测：`ops/observability/run-probe.sh` + `probe-pg-migration-snapshot.sh` / `probe-migration-readiness.sh`；显式 `--expected-instance-id` 取舰队 SSOT。不使用旧 dump/尾差冻结脚本。
- 本地新工具：`ops/stage0/tencent_backup_identity.py`、`verify_final_wal.py` 及测试；`archive_standby_rehearsal.py` 负责隔离 standby，**没有 promote 功能**。
- 最近完整 `preflight --worktree`：PASS，93 秒，日志 `preflight-r1eo40p4.log`（本地 `/tmp/codeg-acp-501/1094-28b8f81b/`）；随后新增的身份权限边界测试也通过，共 6 项。门禁成功不替代远端 systemd 运行验收。
- 本地 Colima 根盘满导致的恢复 fixture 失败已改为有上限的 tmpfs 并实测恢复/账务误差/超时清理通过；未改生产恢复代码，未做磁盘清理。

**接手第一句话应是“先修复腾讯凭据目录及 standby 挂载，再验证追赶”；不是“已准备好，开始停写”。**
