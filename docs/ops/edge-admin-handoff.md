# Edge 一键管理：信任配置与发布

实现与 owner 以 [审批设计](../approved/edge-admin-handoff-v2.md) 为准。
无配置时服务正常启动，管理入口提供直接登录。配置存在但格式/权限不合法时启动失败，
避免无声降级。配置在启动时加载，修改后须重启对应实例。

## 本地生成待部署包

公共 manifest 只包含目标、主体 ID 和 key ID，不含密钥。例如：

```json
{
  "issuer": "https://tokenkey.example",
  "edges": [
    {"id": "us1", "origin": "https://api-us1.tokenkey.dev", "admin_user_id": 1, "key_id": "handoff-v1"}
  ]
}
```

使用实际控制台 origin、Edge origin 与经过核对的 Edge 管理员 ID；示例不是生产事实。
在 backend 目录执行 `go run ./cmd/edge-handoff-config --manifest /absolute/public-manifest.json --out /absolute/private-new-bundle`。
工具拒绝已存在目录、重复目标和不合法 origin，每个 Edge 独立生成 Ed25519 私钥。
新目录权限 0700，文件 0600；输出不显示密钥。不要把 bundle 放在仓库、日志或 CI 工件里。

`prod.json` 含全部签名私钥，`edge-<id>.json` 只含该 Edge 的受信公钥与固定管理员映射。
生成包只写本地，不推送配置、不调用远端。

## 接入现有 Stage0 owner

沿用 Stage0 的 `/var/lib/tokenkey/app` → `/app/data` 数据卷。
经现有受保护配置分发路径，将该实例对应文件安装为
`/var/lib/tokenkey/app/edge-handoff.json`，文件 owner 必须是应用 UID/GID 1000:1000，权限 0600。
容器默认读取 `/app/data/edge-handoff.json`；自定义路径使用 `EDGE_HANDOFF_FILE`。
文件不进入镜像账号记录、公开设置或 compose 明文环境变量。

发布顺序：先部署支持新协议的 Edge 并安装公钥信任，验证配置/管理员可用；
再部署控制台并安装对应私钥。记录每个实例版本、origin、kid、配置文件权限与管理员 ID，
不记录私钥、交接码、verifier 或会话。部署和远端配置写入需要单独授权。

使用真实 UI 从两个既有入口验证一键进入、刷新后会话继续、失败恢复。
旧 `POST /api/v1/edge/admin-session` 必须返回 410，即使带镜像 Key；新 mint 不接受镜像 Key。
版本或信任不匹配时直接登录；禁止恢复旧 token URL 协议。

## 轮换、停用与撤销

轮换先在目标 Edge 的 `receiver.public_keys` 加入新 kid、公钥并重启；
再切换控制台该 Edge 的 `signers` 项并重启。确认新交接后移除旧 kid 并重启 Edge。
多个 kid 的短暂并存只发生在显式公钥表。关闭单个 Edge 交接可移除该 signer 或 receiver。
回滚方案是停用一键交接，使用直接登录。

成功交接日志只记录 initiator、issuer、Edge user ID、attempt、kid、family。
`family` 可交给现有 `RefreshTokenCache.DeleteTokenFamily` owner 定向撤销刷新族；
它不立即撤销已签发 access token。需要立即阻止该管理员时，使用现有用户停用/
TokenVersion 机制，并评估它影响该用户其他会话的范围。本任务不执行历史会话撤销。
