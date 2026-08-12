---
name: tokenkey-stage0-edge-platform-migration
description: >-
  Use when migrating an existing TokenKey Stage0 Edge from Lightsail to EC2, preparing a candidate, planning zero-loss cutover, observing, or rolling back.
---

# TokenKey：Lightsail Edge 迁移到 EC2

权威设计是 `docs/approved/design-edge-ec2-zero-loss-migration.md`，可执行 Runbook 是
`ops/migration/README.md`。命令、参数和状态派生全部复用其中的机械入口，不在 skill 复制第二套流程。

硬边界：

- 一次只处理一个 Edge；普通 rollout 默认跟随唯一 `deployable=true` owner。
- EC2 candidate 必须显式 `--platform ec2`，不能被 `auto` 选中。
- 公开状态只有 `prepared -> cutting_over -> observing -> stable`。
- 内部 action lock/marker 只守卫迁移动作、普通 Edge deploy 和 candidate update 的冲突，不增加状态；
  账号及其他 host 配置写入仍须按 Runbook 在执行审批中冻结。
- 迁移控制器默认 plan-only；线上 `--execute`、candidate provision、DNS 和 owner 切换分别审批。
- 正常成功切换的源端停写上限为 120 秒；异常回滚以数据完整性优先，可能超过该上限。
- PostgreSQL、Redis、账号、凭证、密钥和应用文件必须对称验证。
- `stable` 不删除 Lightsail，也不自动改变 deployable owner；退役另行审批。
- 一次只处理一个 Edge，不批量循环，不执行 DNS 写入，不在仓库存储 state、bundle、预签名 URL 或历史过程记录。

入口：candidate 基础设施用 `.github/workflows/deploy-edge-stage0.yml`；数据迁移用
`ops/migration/edge-ec2-migration.sh`。任何失败先按 Runbook 的写入边界选择直接恢复或反向同步，
禁止直接把 DNS 指回旧数据库。
