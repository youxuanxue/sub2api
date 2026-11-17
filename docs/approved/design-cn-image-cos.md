---
title: cn-prod 镜像分发改腾讯 COS
status: approved
approved_by: "feng（2026-11-17 会话：同意 Tokyo 镜像桶改 COS，要求精简）"
approved_at: 2026-11-17
risk: high
authors: [agent]
supersedes_destination: cn-prod-image-distribution.md
revised: 2026-11-17（标准路径改为 prod 导出→北京 COS→同城 warm，见 decided.md）
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
prod 主机（已有或可 docker pull 的 tag/revision）
  → docker save + crane provenance
  → 上传北京 COS
  → cn-prod apps[] 同城 COS warm
  → cn-prod rolling
```

- Owner：`image_distribution.publish_from_docker` + `probe_publish_cn_from_prod.sh`；
  `distribute-cn-image.yml` 默认 `transport=prod`（Release 自动触发亦然）。
- 根因：GHA（美东）→ 北京 COS 易 `UserNetworkTooSlow`；prod（腾讯美西）→ 北京 COS 可用。
- 回退：`transport=s3`（GHA crane→COS）、`transport=ghcr`（节点直拉 GHCR）仅应急。

## 明确不做

- 不改 prod 自身的 GHCR warm 路径（美西 prod 仍走 GHCR）
- 不搬历史 Tokyo 对象
- 不把镜像写进 backup / ops-archive 桶
- 不以跨境 GHCR pull 作为 cn-prod 默认预热

## 验收

- 单测：client 带 COS endpoint；配置拒绝 AWS amazonaws endpoint（可选）；publish-docker 契约
- 一次真实 `distribute-cn-image`（transport=prod）publish+warm receipt
- 文档与 `deploy/hetzner/README.md` 残留表：Tokyo 镜像桶可退
