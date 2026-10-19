---
title: cn-prod private S3 image distribution
status: approved
approved_by: "feng — 同意由发布 CI 直接把镜像放到已验证可用的私有 S3，再让 cn-prod 提前下载"
risk: high
---

# cn-prod 镜像预下载

## 审批范围

将已验证的私有 S3 中转纳入发布流水线，去掉运维电脑下载/上传环节。
复用 GHCR 同一构建的 amd64 镜像，不另行构建国内镜像。仅预下载和导入，
不启动候选、不切流、不重启 Docker、网关、数据库或 Redis。prod 分发保持原路径。

## 契约与 owner

- `image-distribution.json` 持有现有私有桶和区域；`image-distribution-policy.yaml`
  只授予 CI 镜像目录读写及 cn-prod SSM 操作，不扩展 OIDC 信任域。
  `distribute-cn-image.yml` 默认在 Release `workflow_run` 成功后跑主分支代码，复用既有
  main OIDC 信任；也允许 `workflow_dispatch` 传入已成功 Release 的 `release_run_id`
  做回补/重试。两种触发都只 checkout default_branch，只从该成功 run 的
  `cn-image-source` artifact 读 tag/revision 并与 git tag 核对；不执行发布 ref 的代码。
- `image_distribution.py` 按源 amd64 manifest digest 用 `crane pull --format=legacy`
  下载（docker-save，便于主机 `docker load`），只将归档内加载标签改为专用 digest
  标签。receipt 保留 registry 压缩 manifest 作 provenance；legacy 归档内层是
  未压缩 tar，按 config `rootfs.diff_ids` 校验（不得用 registry `layers[].size`
  / compressed digest 比对归档成员）。并校验架构、版本、完整 revision。
  S3 路径为版本/完整 revision/归档 SHA；条件创建拒绝覆盖，manifest 最后写入。
- `image_archive.py` 是校验与主机导入 owner。通过短期签名 GET 下载，无长期主机密钥。
  SHA、manifest、config/diff_ids 校验成功才 docker load；不使用 config digest 作为
  Docker 29 containerd image ID。加载后核对 diff_ids、版本、架构和 revision。
- 下载限速 512 KiB/s、串行、有限超时，复用共享 bluegreen host lock；
  预留磁盘空间，临时归档在成功和失败后均清理。nice/ionice 只约束下载进程，
  不声称限制 Docker daemon 导入开销。已验证缓存可复用，拒绝替换不同的本地版本标签。
- `image_distribution_lifecycle.py` 只管理专用前缀的 30 天保留，保留其他生命周期规则。
  不清理主机旧镜像，不变更回滚镜像保留策略。

## 失败与回退

分发/预下载失败使独立分发 workflow 明确失败，不伪装发布验收成功；构建产物和现网
保持原样。S3 pull 默认需要已审完整 revision；过期/缺失/冲突必须显式处理，
不自动回退到卡顿的 GHCR。可显式 `--transport ghcr --digest ...` 使用原路径。
合并仅接入未来发布；IAM 和生命周期须按 runbook 初始化后才发布包含本变更的版本。

## 验证

聚焦测试覆盖完整归档、篡改、错误架构/revision/digest、S3 冲突、失败不导入、
主机锁、缓存与导入后的标签保护。真实镜像验证使用既有发布归档；不以单测冒充在线
发布验收。真实 CI OIDC/SSM 成功以合并后的首次分发回执为准。
