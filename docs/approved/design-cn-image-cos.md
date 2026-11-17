---
title: cn-prod 镜像分发改腾讯 COS
status: approved
approved_by: "feng（2026-11-17 会话：同意 Tokyo 镜像桶改 COS，要求精简）"
approved_at: 2026-11-17
risk: high
authors: [agent]
supersedes_destination: cn-prod-image-distribution.md
---

# cn-prod 镜像分发改腾讯 COS

## 决定

原话：「Tokyo 镜像分发桶 → COS。同意。请继续。务必精简、高效，不要复杂化。」

## 范围（只做这些）

1. 北京 COS 桶承载 `cn-prod/images/*`（30 天过期），替代 Tokyo AWS `cfn-deploy`。
2. `image-distribution.json` 改 COS bucket/region/endpoint。
3. `publish` 用现有 `tencent-ops` 的 `TENCENTCLOUD_SECRET_ID/KEY` + S3 兼容 API；去掉 AWS OIDC。
4. warm / rolling / receipt / 预签名 GET 契约不变。

## 明确不做

- 不改 prod GHCR warm 路径
- 不搬历史 Tokyo 对象
- 不新建第二套分发协议或第二套 workflow
- 不把镜像写进 backup / ops-archive 桶

## 验收

- 单测：client 带 COS endpoint；配置拒绝 AWS amazonaws endpoint（可选）
- 一次真实 `distribute-cn-image` publish+warm receipt（合并后）
- 文档与 `deploy/hetzner/README.md` 残留表：Tokyo 镜像桶可退
