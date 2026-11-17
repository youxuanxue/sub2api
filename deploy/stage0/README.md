# Shared Stage0 runtime (historical path)

本目录是 现网 prod（腾讯美西 Lighthouse，2026-10-22 起；AWS Tokyo 已于 2026-10-23 退役）、历史 Hcloud 模板与本地开发共用的运行文件 owner，不是现网云平台选择器。
现网入口和残留依赖说明见 [Hetzner fleet](../../hetzner/README.md)。

- compose / Caddy / `.env.example` / `tokenkey-*.sh`：由现役 prod 发布脚本、本地栈及历史 renderer 消费。
- fingerprint / OAuth baseline JSON：由 backend embed 与对应校验器消费，与云厂商无关。
- `stage0-ec2-*` / `build-cfn.sh` / `cfn-resource-dependencies.py`：历史 AWS 脚手架；生成产物仍须保持同步，不能用于重建 hcloud。

保留路径是为了维持现有 release artifact、生成器和审批锚点兼容。新基础设施必须更新现网清单与相应实现；不要根据历史目录名选择云平台或恢复退役身份。
