# Deployment runbooks

Production releases use the rolling workflows:

- `.github/workflows/prod-rolling-deploy.yml` for Tencent USW prod.
- `.github/workflows/cn-prod-rolling-deploy.yml` for Tencent Beijing cn-prod.

Both default to entry + multiple single-container app nodes + a separate data machine.
Use the corresponding warm/distribution workflow before rolling, and pass the full 40-hex
commit revision. `.github/workflows/deploy-stage0.yml` is recovery-only / legacy blue-green;
do not use it as the normal release path.

现网主机、出口和恢复先读 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。目录名是历史路径，现网是腾讯美西。

[`deploy/aws/README.md`](../../deploy/aws/README.md) 只保留仍在用的 SSM/OIDC、备份和 QA 存储依赖，不是默认执行入口。
