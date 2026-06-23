# Kiro 账号配置 · 运营操作指南

> 给运营：把一个已经完成授权的 Kiro 账号配置到 TokenKey 指定 edge 后台。运营只做页面操作。

## 分工

运营负责：

- 登录 Kiro，确认账号可用。
- 在 TokenKey 后台新建或编辑 Kiro OAuth 账号。
- 把账号挂到 `kiro` group，并确认账号可调度。
- 保存配置结果和异常现象。

技术负责人负责：

- 安全提取 Kiro OAuth 字段。
- 把字段通过受控方式交给运营，或现场协助粘贴到后台。
- 完成真实 Kiro 请求验证。

## 安全红线

- 不把 Access Token、Refresh Token、Client Secret 发到群里、工单、PR 或普通聊天窗口。
- 不截图、不长期保存任何包含 token 的页面或文件。
- 如果需要他人协助，只让技术负责人通过受控方式处理敏感字段。
- 配置完成后，清空剪贴板里残留的 token 内容。

## 1. 下载并登录 Kiro

1. 打开 Kiro 官网下载安装：<https://kiro.dev>
2. 打开 Kiro。
3. 在登录页选择组织/公司 SSO。
4. 起始地址填写：

```text
https://d-906671b2ce.awsapps.com/start
```

5. 浏览器打开授权页后点击批准。
6. 回到 Kiro，确认能进入会话页。

如果已经登录过，但这次是修复 401、Invalid bearer token 或授权失效问题，请先退出 Kiro 后重新登录授权。

## 2. 准备后台填写字段

运营不要自行提取 token。请技术负责人提供以下字段：

| 字段 | 说明 |
| --- | --- |
| Access Token | 必填，敏感字段 |
| Refresh Token | 必填，敏感字段 |
| Region | 通常是 `us-east-1` |
| 认证方式 | 通常是 `idc`；如果是社交登录则为 `social` |
| Client ID | `idc` 必填 |
| Client Secret | `idc` 必填，敏感字段 |
| Profile ARN | 有值就填，没有就留空 |

收到字段后，只用于 TokenKey 后台填写，不转发、不截图、不保存副本。

## 3. 进入目标 edge 后台

目标 edge 后台地址由负责人提供，通常格式是：

```text
https://api-<edge>.tokenkey.dev/admin
```

进入后台后打开“账号管理”。

## 4. 新建或编辑 Kiro 账号

如果是新账号：

1. 点击“新建账号”。
2. 平台选择 `Kiro`。
3. 类型选择 `OAuth`。

如果是修复已有账号：

1. 在账号管理里找到目标 Kiro OAuth 账号。
2. 核对账号名称，避免改错 edge 或改错账号。
3. 点击编辑。

按下面字段填写：

| 后台字段 | 填写 |
| --- | --- |
| 平台 | Kiro |
| 类型 | OAuth |
| Access Token | 技术负责人提供的 Access Token |
| Refresh Token | 技术负责人提供的 Refresh Token |
| Region | 技术负责人提供的 Region，通常是 `us-east-1` |
| 认证方式 | 技术负责人提供的认证方式，通常是 `idc` |
| Client ID | 技术负责人提供的 Client ID；`idc` 必填 |
| Client Secret | 技术负责人提供的 Client Secret；`idc` 必填 |
| Profile ARN | 有值就填，没有就留空 |
| 接受 Kiro 服务条款 | 必须勾选 |

保存后记录：

- edge 名称
- 账号 ID
- 账号名称
- 配置时间

## 5. 挂到 Kiro Group

在后台把账号挂到 `kiro` group。

保存后确认：

- 账号状态是 `active`。
- 可调度已开启。
- 没有错误信息。
- 账号已在 `kiro` group 下。

如果后台有 RPM、并发、优先级等配置，按负责人给出的值填写；没有特别要求时，沿用同 edge 上其他 Kiro 账号的配置。

## 6. 验证

运营侧检查：

- 账号保存成功。
- 账号在 `kiro` group 中。
- 账号状态为 active。
- 可调度开启。
- 页面没有错误信息。

技术侧验证：

- 请技术负责人发起一次真实 Kiro 请求验证。
- 验证通过后，记录“已完成真实请求验证”。

不要用“后台刷新账号”或“后台 usage 强制刷新”判断 Kiro 是否可用；Kiro 是否真正可用，以技术负责人真实请求验证结果为准。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 后台提示缺 Access Token / Refresh Token | 找技术负责人重新提供完整字段 |
| 后台提示缺 Client ID / Client Secret | 认证方式是 `idc` 时这两个字段必填，找技术负责人补齐 |
| 后台提示缺 ToS | 勾选“接受 Kiro 服务条款” |
| 请求返回 No available accounts | 检查账号是否挂到 `kiro` group，且可调度已开启 |
| 后续出现 401 / Invalid bearer token | 重新登录 Kiro 后，请技术负责人重新提取字段并更新后台 |
| 不确定该新建还是编辑 | 修复已有账号时优先编辑原账号；新增账号必须先确认不会造成重复账号混淆 |

## 汇报模板

配置完成后，只汇报安全摘要，不附 token：

```text
edge:
账号 ID:
账号名称:
操作类型: 新建 / 编辑
状态: active / 非 active
可调度: 是 / 否
Group: kiro 已挂 / 未挂
页面错误: 无 / 有（摘要）
真实请求验证: 已通过 / 待技术验证 / 未通过
```
