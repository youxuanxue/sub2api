# 腾讯美西切流后续（2026-10-22）

与 [docs/ops/tencent-cutover-checklist.md](../../docs/ops/tencent-cutover-checklist.md) 对齐的活状态；本文件在 `deploy/*` 下以便 scoped preflight。

## 已完成（本轮无感）

- [x] 外部飞书备份/入口告警接到腾讯机：`tokenkey-https-watch.timer`；监控 cn-prod `/health` + 备份/archive/qa-cleanup；凭据在告警用 `/var/lib/tokenkey/.env`（仅 `TOKENKEY_FEISHU_*`）
- [x] LUKS **只读**核验：异机 vault 指纹匹配；`unlock-health-readonly` severity=ok。**自动解锁 / 整机重启仍未验收**

## 仍打开（另批）

- [ ] Stage0 布局收敛（阻塞默认 `deploy-stage0`）— 见 [stage0-layout-convergence.md](stage0-layout-convergence.md)
- [ ] Tokyo 保留 ≥7 天再退役
- [ ] 日本 Lightsail 代理关停
