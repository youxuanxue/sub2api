# Tokyo prod → 腾讯云美国迁移前置核验

状态：`plan/prepare`。本记录只包含只读核验和进入隔离演练前的准备项；未创建腾讯云资源，未写生产卷，未修改 DNS、备份、网关、Caddy、PostgreSQL 或 Redis。

核验时间：2026-10-20 14:29 UTC。

此为历史前置快照；后续地域、预算、建机、SSM 与加密隔离演练进展见[美西验证报告](tencent-us-lighthouse-proxy-validation-20261021.md)，不以本页旧阻塞项判断当前状态。

## 已核验事实

- 现网入口由 `ops/stage0/resolve_prod_ssm_target.py --target aws-fresh --format json` 解析到 AWS Tokyo prod：`i-045f062ce33e1a5af`，SSM 区域 `ap-northeast-1`，主机名 `tokenkey-prod-tokyo-20261017`。
- 通过 `ops/observability/run-probe.sh` 对该实例执行只读探针，返回的网关、Caddy、PostgreSQL、Redis 容器均在运行；应用、PostgreSQL、Redis 健康状态正常。
- `/var/lib/tokenkey` 约 147 GiB，已用约 57 GiB，可用约 83 GiB；该容量只用于评估源机现状，不代表腾讯云目标盘规格。
- 物理备份回执为 `status=ok`。最新备份时间为 2026-10-20 12:31 UTC；归档 WAL 往返与连续性检查为 `ok`，覆盖 347 个 segment；仓库验证和恢复验收均为 `ok`。
- 已有 `latest-archived` 与 `tk_accept_20261020_100000` 两个恢复证据路径，system identifier 和镜像 SHA 在回执中一致。

## 本地门禁

- `python3 ops/stage0/test_postgres_backup.py`：23 tests，全部通过。
- `python3 -m unittest discover -s ops/observability -p 'test_postgres_backup_verdict.py'`：2 tests，全部通过。
- `python3 ops/stage0/test_resolve_prod_ssm_target.py`：13 tests，全部通过。

## 进入腾讯云隔离演练前仍需准备

1. 确定美国具体地域、按量实例规格、ARM64/x86 架构和预算上限；当前文档只能给出 4 vCPU/8 GiB、独立加密数据盘的参考，不能代替报价或兼容性确认。
2. 固定与源库兼容的 PostgreSQL 18.6、pgBackRest 2.58.0 镜像及 SHA；若目标仅有 x86，先完成扩展、镜像和数据格式兼容验证。
3. 设计腾讯云侧的短期只读备份访问凭据：仅允许读取物理仓库/恢复包，禁止写入、删除和复用生产写角色；凭据不得进入日志、命令行或 git。
4. 补齐腾讯云隔离恢复执行器和自动清理回执。现有 `run_physical_restore.py` 是 AWS 临时恢复验收编排器，不能直接当作腾讯云持久目标库安装器。
5. 补齐持久 standby 安装/追 WAL/重启后保持 recovery/缺 WAL 告警能力。当前仓库没有完成并验收的跨云 standby 路径，因此不能进入正式切流准备。
6. 为演练准备独立安全组和全新 PGDATA；数据库端口不公开，阻断生产网关、后台写者、支付/供应商回调、QA 上传/DROP 和生产 Redis。
7. 另行准备应用镜像、Secrets、Caddy/active-color、对象文件和 Redis 策略；演练应用只能使用独立域名、合成账户和受控供应商替身。

## 明确阻塞条件

- 腾讯云具体地域/规格/预算尚未指定。
- 腾讯云只读跨云凭据、恢复执行器、持久 standby 和清理/回执验收尚未实现。
- 因此当前只到 `plan/prepare`，不创建付费资源、不挂生产卷、不运行真实业务写者、不改 DNS、不 promote、不切流。

## 下一阶段边界

在上述阻塞项完成、并获得单独的资源预算与隔离演练授权后，才可执行第一阶段腾讯云隔离恢复。隔离演练通过也不等于正式迁机授权；正式切流仍需单独维护窗口、源写入隔离、最终 WAL/LSN 闸门、目标 promote 和回滚条件。
