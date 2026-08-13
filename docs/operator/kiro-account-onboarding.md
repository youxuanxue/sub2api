# Kiro 账号配置 · 极简操作指南

> 给运营：使用真实 Kiro CLI 登录，并把本机 CLI 已生成的凭证安全录入 TokenKey。
> 凭证只在本机缓存与 TokenKey 后台之间传递；不要在终端打印、聊天转发、截图或写入仓库。

## 4 步搞定

**1. 安装 Kiro CLI 并登录**

```bash
brew install --cask kiro-cli

# Builder ID / 社交登录
kiro-cli login --license free

# 组织 Identity Center；替换为实际 start URL 和 region
kiro-cli login --license pro \
  --identity-provider 'https://example.awsapps.com/start' \
  --region us-east-1
```

浏览器完成授权后，只检查非敏感身份元数据：

```bash
kiro-cli whoami
```

**2. 在后台直接粘贴本机缓存 JSON**

TokenKey 后台已支持 Kiro CLI 缓存的原始 JSON，不需要把 token 拆开打印：

- **Token JSON**：在本地编辑器中打开 `~/.aws/sso/cache/kiro-auth-token.json`，全选并直接粘贴到后台。后台只解析 `accessToken`、`refreshToken`、`region` 和 `authMethod`。
- **Registration JSON**：仅 IdC 需要。在 `~/.aws/sso/cache/` 中找到本次登录生成、同时包含 `clientId` 与 `clientSecret` 的 registration JSON，在本地编辑器中全选并直接粘贴到后台。

安全纪律：

- 不运行会把 JSON 或字段输出到终端的 `cat`、`jq`、Python 提取脚本。
- 不把凭证复制到聊天、工单、日志、截图或临时文本文件。
- 不提交 `~/.aws/sso/cache/`、`.kiro_tls/`、pcap 或 mitm 日志。
- 后台编辑页不会回显已保存的 secret；不换凭证时留空即可。

**3. 后台新建账号**

账号管理 → 新建 → 平台选 **Kiro**：

| 后台字段 | 填写方式 |
| --- | --- |
| Token JSON | 粘贴 `kiro-auth-token.json` 原文 |
| Region | 通常由 Token JSON 自动带出；默认 `us-east-1` |
| 认证方式 | 按登录结果选择 Social / IdC |
| Registration JSON | 仅 IdC：粘贴含 `clientId` / `clientSecret` 的缓存 JSON |
| Machine ID | 留空，除非现有运营策略明确要求 |
| Profile ARN | 留空，系统会按协议解析并持久化 |
| 接受 Kiro 服务条款 | **必须勾选** |

确认后台显示“已解析”后保存。TokenKey 会保存必要字段并自动刷新短期 access token。

**4. 挂分组**

把账号挂到对应 group，按常规设置 RPM / 并发。账号进入 group 且可调度后即开始服务，无需额外开关。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `kiro-cli whoami` 未登录或过期 | 重新运行第 1 步对应的 `kiro-cli login` |
| 后台提示 Token JSON 无效 | 确认粘贴的是完整 `kiro-auth-token.json`，且包含 access/refresh token |
| 后台提示缺 registration | IdC 必须再粘贴包含 `clientId` / `clientSecret` 的 registration JSON |
| 创建提示确认 ToS | 勾选“接受 Kiro 服务条款” |
| 调用报 `No available accounts`（429） | 确认账号已挂进对应 group 且处于可调度状态 |
| 持续 401 / 刷新失败 | 用 Kiro CLI 重新登录，再在**编辑账号**中粘贴新 Token JSON；IdC registration 变化时一并更新 |

> Access Token 正常过期无需人工处理；系统使用 refresh token 自动续期。只有 refresh 失效时才需要重新登录和录入。
