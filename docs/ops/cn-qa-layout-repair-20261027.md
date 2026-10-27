# cn-prod QA 目录修复验收 — 2026-10-27

用户授权：修复 cn-prod 文件清理缺口，保持线上网关服务。

补齐缺失的 qa_hourly_storage_cutover_utc = 2026-10-27T09:00:00Z。
通过既有宿主生命周期锁和 QAMA 锁，两轮核验并整理 50,801 个 legacy 文件；
按原 captured_at 建立小时目录硬链接，更新四个数据库 URI 字段，提交后移除旧目录项。
原始压缩内容逐文件 SHA 校验，过期删除仍由既有 cleanup owner 执行。

既有 timer 自动处理第一批 48 小时；dry-run 后调用同一 cleanup service 处理剩余 33 小时。
09:36 UTC 验收：legacy 文件 0、过期小时目录 0；保留 4,623 个压缩文件，114,567,532 字节。
修复前 50,781 个旧文件共 1,221,228,196 字节（在线流量持续写入，前后不是封闭样本）。
最近五分钟 22 条 QA 全部使用小时路径；当前 runtime pending/inflight/failed/degraded 均为 0。
网关 ID、镜像、StartedAt 保持不变，RestartCount=0，healthy，公网 health 正常。

修复前后缺失文件均为 53 个，文件身份摘要一致：
9df5a6bc92933cf7ec89f9847acac70003e2741406aa3c136055fab2e383f672。
均位于 2026/10/26 的旧 00–08 前缀，与小时目录误识别风险吻合；本次没有恢复这些已缺失的内容。
9 个历史 runtime_discontinuity 保留，不能把本次布局修复描述为全历史 capture 健康。

清理每五分钟运行；整小时所有数据均满 24 小时后才删除本地元数据及对应 raw 文件，
实际保留约 24–25 小时加调度延迟。每小时运行的是未来分区预建。S3 上传仍暂停。

验证：真实 PostgreSQL + zstd 集成与文件保全/故障测试通过（8 项）；full preflight PASS。
修复工具与测试见 `ops/qa/repair_capture_layout.py` 和 `test_repair_capture_layout*.py`。
本次授权沿用 [QA raw 生命周期契约](../approved/qa-raw-only.md) 的本地保留策略。
09:37 UTC 下一轮定时清理成功，过期分区、文件小时及待处理小时均为 0。
归档 PR 另补进程管道释放、libzstd 缺失时报错以及 CI 测试接线；这些收尾调整未再次部署。
主机精确计划与 apply 回执：/var/lib/tokenkey/qa-local/layout-repair-20261027-{a,b}.json[.applied.json]。
计划 SHA：
- a: 4035302ff6d03a300afef39bfbf6bd5674018c05a8e44e2e0e2fd9864b7fa84c
- b: ccc2d2d684dace38347f927d0c1941f6c30469063ff4534631fe79c37a4b19f8

执行端日志：/tmp/cn-qa-layout-{before,baseline-detail,mid,cleanup,final,closeout}.log。
