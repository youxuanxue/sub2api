---
title: cn-prod 镜像分发改腾讯 COS
status: approved
approved_by: "feng（2026-11-17 会话：同意 Tokyo 镜像桶改 COS，要求精简）"
approved_at: 2026-11-17
risk: high
authors: [agent]
revised: 2026-11-20（并入原 cn-prod-image-distribution 预下载/rolling 边界；Tokyo 目的地文档删除）
---

# cn-prod 镜像分发改腾讯 COS

## 决定

原话：「Tokyo 镜像分发桶 → COS。同意。请继续。务必精简、高效，不要复杂化。」

## 范围（只做这些）

1. 北京 COS 桶承载 `cn-prod/images/*`（30 天过期），替代 Tokyo AWS `cfn-deploy`。
2. `image-distribution.json` 改 COS bucket/region/endpoint。
3. `publish` 用现有 `tencent-ops` 的 `TENCENTCLOUD_SECRET_ID/KEY` + S3 兼容 API；去掉 AWS OIDC。
4. warm / rolling / receipt / 预签名 GET 契约不变。

## 标准路径（2026-11-17）

原话：「北京的镜像应该从 prod 导出上传到 北京 cos，再从北京 cos 拉下来使用。这条变为标准路径」

```
prod 主机（USW，可 crane 拉 GHCR）
  → crane pull --format=legacy + registry provenance
  → 上传北京 COS（egress 自 prod，非 GHA→北京）
  → cn-prod apps[] 同城 COS warm
  → cn-prod rolling
```

说明：现代 `docker save` 产出 OCI 布局（含目录成员），与 `verify_archive` 的
crane-legacy 契约不兼容；标准路径在 prod 上用 `crane pull --format=legacy`。

- Owner：`image_distribution.publish_from_docker` + `probe_publish_cn_from_prod.sh`；
  `distribute-cn-image.yml` 默认 `transport=prod`（Release 自动触发亦然）。
- 根因：GHA（美东）→ 北京 COS 易 `UserNetworkTooSlow`；prod（腾讯美西）→ 北京 COS 可用。
- 回退：`transport=s3`（GHA crane→COS）、`transport=ghcr`（节点直拉 GHCR）仅应急。

## 预下载与 rolling 边界

将已验证的私有对象存储中转纳入发布流水线，去掉运维电脑下载/上传环节。
复用 GHCR 同一构建的 amd64 镜像，不另行构建国内镜像。预下载只把已验证镜像导入
每个应用节点，不重启正在服务的容器，不改入口路由、网关、数据库或 Redis。
预下载完成后，正式流量切换仍由独立的 cn-prod rolling 审批与执行 job 负责。
prod 分发保持原路径。

## 契约与 owner

- `image-distribution.json` 持有 COS 桶/region/endpoint；`distribute-cn-image.yml`
  默认在 Release `workflow_run` 成功后跑主分支代码。也允许 `workflow_dispatch`
  传入已成功 Release 的 `release_run_id` 做回补/重试。两种触发都只 checkout
  default_branch，只从该成功 run 的 `cn-image-source` artifact 读 tag/revision 并与
  git tag 核对；不执行发布 ref 的代码。
  **标准 publish**（2026-11-17）：在 **prod** 主机 `crane pull --format=legacy`
  后上传北京 COS（`publish_from_docker` / `probe_publish_cn_from_prod.sh`），再 `warm`；
  `distribute-cn-image.yml` 默认 `transport=prod`。GHA runner 上 `crane pull`→COS
  （`transport=s3`）与节点直拉 GHCR（`transport=ghcr`）仅应急。
  `publish`/`publish-from-prod` 与 `warm` 使用 `tencent-ops` 腾讯凭据写 COS / TAT 预下载；
  `publish-from-prod` 另需 AWS OIDC 走 SSM（角色 trust 含 `environment:tencent-ops`）。
  render artifact 含短期签名 URL，保留 1 天且从不打印；publish receipt 保留 7 天。
- `image_distribution.py`：标准路径与 legacy CI 均 `crane pull --format=legacy`；标准路径
  只是把导出+COS 上传放到 prod 主机（规避 GHA→北京慢）。现代 `docker save` OCI 布局
  含目录成员，不能直接进 `verify_archive`。只将归档内加载标签改为专用 digest 标签。
  legacy 归档内层是未压缩 tar，按 config `rootfs.diff_ids` 校验（不得用 registry
  `layers[].size` / compressed digest 比对归档成员）。并校验架构、版本、完整 revision。
  COS 路径为版本/完整 revision/归档 SHA；条件创建拒绝覆盖，manifest 最后写入。同
  tag/revision/archive_sha 已完成时复用现有 manifest（允许 receipt 可选字段演进，不重写）。
  prod 主机 botocore < 1.36 时跳过 checksum kwargs；probe 在缺 crane 时引导静态二进制
  （tarball SHA256 固定校验）。`publish_source` 为 `prod-crane`（CLI 动作名
  `publish-docker` 仅为兼容别名）。
- `warm_fanout.py` 是 per-node render/dispatch owner。它按 `prod-target.json` 的
  `apps[]` 顺序 fail-stop，校验 render 的节点身份、tag、完整 revision、channel 和脚本
  SHA，并固定 `--channel tat` 下发；每个节点成功回执写入
  `/var/lib/tokenkey/image-distribution/<tag>-<revision>.json`，聚合 warm receipt 保留在
  CI artifact。某节点失败后不再尝试后续节点，不修改运行中的容器。
- `image_archive.py` 是校验与主机导入 owner。通过短期签名 GET 下载，无长期主机密钥。
  SHA、manifest、config/diff_ids 校验成功才 docker load；不使用 config digest 作为
  Docker 29 containerd image ID。内容身份是架构/版本/revision + `diff_ids`：GHCR
  pull 与 docker load 的 image ID 可不同；同内容视为已对齐（`cached=true`），仅当
  本地不可变标签指向不同内容时拒绝替换。新 receipt 可带 `diff_ids` 以便跳过重下。
- 下载限速 512 KiB/s、串行、有限超时，复用共享 host deploy lock；
  预留磁盘空间，临时归档在成功和失败后均清理。nice/ionice 只约束下载进程，
  不声称限制 Docker daemon 导入开销。
- `image_distribution_lifecycle.py` 只管理专用前缀的 30 天保留，保留其他生命周期规则。
  不清理主机旧镜像，不变更回滚镜像保留策略。

## 失败与回退

分发/预下载失败使独立分发 workflow 明确失败，不伪装发布验收成功；构建产物和现网
保持原样。S3 pull 默认需要已审完整 revision；过期/缺失/冲突必须显式处理，
不自动回退到卡顿的 GHCR。可显式 `--transport ghcr --digest ...` 使用原路径。
合并仅接入未来发布；IAM 和生命周期须按 runbook 初始化后才发布包含本变更的版本。
分发成功后仍需 `cn-prod-rolling-deploy.yml` 的 `prod` 审批和 `tencent-ops` 执行；
预下载 receipt 只证明镜像就绪，不是切流或线上验收证据。

## 验证

聚焦测试覆盖完整归档、篡改、错误架构/revision/digest、S3 冲突、失败不导入、
主机锁、缓存、导入后的标签保护以及 per-node render/tag/channel fail-stop。真实镜像
验证使用既有发布归档；不以单测冒充在线发布验收。真实 CI OIDC/S3 与腾讯 TAT 成功
以合并后的首次分发回执为准。

## 明确不做

- 不改 prod 自身的 GHCR warm 路径（美西 prod 仍走 GHCR）
- 不搬历史 Tokyo 对象
- 不把镜像写进 backup / ops-archive 桶
- 不以跨境 GHCR pull 作为 cn-prod 默认预热

## 验收

- 单测：client 带 COS endpoint；配置拒绝 AWS amazonaws endpoint（可选）；publish-docker 契约
- 一次真实 `distribute-cn-image`（transport=prod）publish+warm receipt
- 文档与 `deploy/hetzner/README.md` 残留表：Tokyo 镜像桶可退
