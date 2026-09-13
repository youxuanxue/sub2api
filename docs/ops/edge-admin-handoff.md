# Edge 一键管理：信任配置与发布

实现与 owner 以 [审批设计](../approved/edge-admin-handoff-v2.md) 为准。
无配置时服务正常启动，管理入口提供直接登录。配置不可读或格式/权限不合法时，
关闭整份交接信任并记录 `edge_admin_handoff_disabled` 错误事件，网关继续启动。
不得将交接就绪与进程健康混为一谈。配置在启动时加载，修改后须重启对应实例。

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

## 本地配置校验

在 backend 目录执行，返回非零即不允许启用该实例的交接配置：

```bash
go run ./cmd/edge-handoff-config --check /absolute/prod.json --role signer
go run ./cmd/edge-handoff-config --check /absolute/edge-e1.json --role receiver
```

该命令复用运行时严格解析 owner，检查存在性、格式、签名文件权限及所需角色；
只读、不打印密钥。它不证明远端版本、管理员状态、公私钥配对和浏览器访问已就绪。

## 接入现有 Stage0 owner

沿用 Stage0 的 `/var/lib/tokenkey/app` → `/app/data` 数据卷。
经现有受保护配置分发路径，将该实例对应文件安装为
`/var/lib/tokenkey/app/edge-handoff.json`，文件 owner 必须是应用 UID/GID 1000:1000，权限 0600。
容器默认读取 `/app/data/edge-handoff.json`；自定义路径使用 `EDGE_HANDOFF_FILE`。
文件不进入镜像账号记录、公开设置或 compose 明文环境变量。

发布前必须单独批准交接切换窗口，并先验证操作者确实能直接登录每个 Edge。
本次首发覆盖通用 release skill 的 canary-first 顺序：**先切换 prod，再升级 Edge**。
旧 prod 会把新版 Edge 的旧 mint 410 转换为 502，触发 Caddy 被动摘除承流后端；
因此不能让旧 prod 与新版 Edge 搭配承接交接请求。新版 prod 与旧 Edge 搭配时，
一键交接暂不可用，管理员直接登录；普通网关不依赖交接配置。

1. 按现有 prod 蓝绿 owner 准备新版、验证并切流；回放与切流审批遵循该 owner。
   暂不启用交接信任，验证普通登录、完整 gateway smoke 与切流后的流量/5xx。
2. 执行 `python3 ops/stage0/check_edge_handoff_rollout.py --tag <Edge目标版本>`。
   脚本读取目标 tag 的路由判断是否需要新协议，并从公开 prod 入口 GET configuration，
   要求响应带 `X-TokenKey-Handoff-Isolation: 1`、`no-store` 且状态为 200/503。
   缺失配置的 503 可通过，旧版 404、重定向、网络故障均阻断。该信号证明承流代码的
   隔离能力，不表示信任关系就绪；inactive 新容器与 `/health` 均不能替代它。
   Edge workflow 在 provision/upgrade/rollback 的任何部署写入前再次执行同一门禁。
3. 选择低流量 Edge 做 canary，配置校验后安装受信公钥与管理员映射，升级并执行 full
   smoke。通过后其余 Edge 沿现有 rollout owner 逐个升级（parallel=1），失败即停。
4. 安装并严格校验 prod 对应私钥配置，通过正常蓝绿部署加载。先验证 canary 的真实
   域名 UI 交接，再逐个验收其余 Edge；记录版本、origin、kid、权限和管理员 ID，
   不记录私钥、交接码、verifier 或会话。

配置分发、各 Edge 管理员状态/信任配对和真实域名 UI 验收仍需上线时执行；现有
gateway smoke 不验收交接，不能凭 `/health` 通过就宣布交接恢复。
部署和远端配置写入需要单独授权。发布窗口内串行推进 prod 与 Edge 操作，避免门禁
通过后又并发将 prod 回滚到旧协议；门禁不是跨部署的分布式锁。

使用真实 UI 从两个既有入口验证一键进入、刷新后会话继续、失败恢复。
旧 `POST /api/v1/edge/admin-session` 必须返回 410，即使带镜像 Key；新 mint 不接受镜像 Key。
版本或信任不匹配时直接登录；禁止恢复旧 token URL 协议。

## 轮换、停用与撤销

轮换先在目标 Edge 的 `receiver.public_keys` 加入新 kid、公钥并重启；
再切换控制台该 Edge 的 `signers` 项并重启。确认新交接后移除旧 kid 并重启 Edge。
多个 kid 的短暂并存只发生在显式公钥表。关闭单个 Edge 交接可移除该 signer 或 receiver。
交接本身故障时停用一键交接，使用直接登录，保留具备隔离能力的网关版本。
尚未升级任何 Edge 时，prod 可按既有蓝绿流程回滚到原版。已有新版 Edge 后，不得直接
回滚 prod 到缺少隔离能力的版本；优先回滚到另一个具备隔离能力的版本。若必须回到
旧协议组合，先回滚所有已升级 Edge 并验证，再回滚 prod，避免重现 410→502 链路。
这不是对普通网关发布零中断的承诺：Edge 换容器、长连接和共享资源的风险仍按现有
release owner 的 smoke、容量、drain 与回滚机制处理。

成功交接日志只记录 initiator、issuer、Edge user ID、attempt、kid、family。
`family` 可交给现有 `RefreshTokenCache.DeleteTokenFamily` owner 定向撤销刷新族；
它不立即撤销已签发 access token。需要立即阻止该管理员时，使用现有用户停用/
TokenVersion 机制，并评估它影响该用户其他会话的范围。本任务不执行历史会话撤销。
