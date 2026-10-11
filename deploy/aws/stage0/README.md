# Shared Stage0 runtime (historical path)

本目录是 hcloud、AWS standby 与本地开发共用的运行文件 owner，不是现网云平台选择器。
现网入口和残留依赖说明见 [Hetzner fleet](../../hetzner/README.md)。

- compose / Caddy / `.env.example` / `tokenkey-*.sh`：由 hcloud renderer、发布脚本及本地栈消费。
- fingerprint / OAuth baseline JSON：由 backend embed 与对应校验器消费，与云厂商无关。
- `resolve-edge-target.py`：现网 hcloud 清单与诊断矩阵；日常部署通过 `scripts/stage0/` dispatcher。
- `stage0-ec2-*` / `build-cfn.sh` / `cfn-resource-dependencies.py`：AWS standby 脚手架；生成产物仍须保持同步，不能用于重建 hcloud。

保留路径是为了维持现有 release artifact、生成器和审批锚点兼容。新增 hcloud 基础设施放在
`deploy/hetzner/`；不要因目录名恢复 EC2/Lightsail 默认值。
