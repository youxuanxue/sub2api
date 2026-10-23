# AWS deployment

现网身份、出口代理与恢复入口见 [现网运维 SSOT](../hetzner/README.md)。2026-10-22 起现网 prod 为腾讯美西 Lighthouse（SSM Hybrid `mi-*`）；Tokyo EC2 已于 2026-10-23 退役删除；本目录只保留仍在用的 SSM/OIDC、备份与 QA 存储依赖，以及历史 CFN 模板。

共享 compose、Caddy 与宿主脚本位于 `stage0/`。修改其嵌入源后运行 `bash deploy/aws/stage0/build-cfn.sh`，使用 `--check` 核对产物。

应用镜像发布使用 `scripts/stage0/dispatch-prod-deploy.sh`；Caddy 配置热更新使用 `ops/stage0/sync_caddyfile_via_ssm.sh`。恢复流程见 [AWS DR](RUNBOOK-disaster-recovery.md)。
