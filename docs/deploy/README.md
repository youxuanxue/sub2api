# Deployment runbooks

Production releases use `.github/workflows/deploy-stage0.yml` and the production target resolver.

现网主机、出口和恢复先读 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。目录名是历史路径，现网是腾讯美西。

[`deploy/aws/README.md`](../../deploy/aws/README.md) 只保留仍在用的 SSM/OIDC、备份和 QA 存储依赖，不是默认执行入口。
