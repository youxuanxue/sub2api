# 专属分组 + Passthrough 配置手册

面向运营：在 Admin UI 上为**单个用户**独占一组账号，并开启**透传**（尽量保留客户端请求头/指纹，TK 只替换认证）。

适用：**Prod 用户入口 + Edge OAuth 账号池**（Anthropic）。Passthrough 开关在**账号**上，不在分组上。

---

## 三个要点

1. **专属分组**：创建时打开「专属分组」；只有被授权的用户能在 API 密钥里选到该分组。
2. **透传**：账号编辑里开「自动透传（仅替换认证）」——Prod 的 apikey stub 与 Edge 的 OAuth 账号**各开各的**。
3. **不要设置 Tier**：Passthrough 账号**禁止**使用账号菜单「设置 Tier」。Tier 会触发 reconciler 把账号对齐到 `tk_canonical_cc_oauth` TLS 与 HTTP mimic，与透传目标冲突。

---

## A. Prod：专属分组 + 授权用户

### A1. 创建分组

**管理后台 → 分组管理 → 创建分组**

| 项 | 设置 |
|----|------|
| 平台 | Anthropic |
| 计费类型 | 标准 |
| **专属分组** | **开** |
| Claude Code 客户端限制 | **关**（显示「允许所有客户端」） |
| Prompt Cache 粘性路由策略 | **passthrough** 或 **off**（不要选 auto） |

保存。记下分组名（Edge 侧建同名分组便于对齐）。

### A2. 绑定 Prod 镜像 stub

**管理后台 → 账号管理 → 编辑**（`platform=anthropic`、`type=apikey`、`base_url` 指向 `api-<edge>.tokenkey.dev` 的 stub）

- **分组**：勾选 A1 的分组
- **自动透传（仅替换认证）**：**开**（API Key 透传）

保存。

### A3. 只授权目标用户

**管理后台 → 用户管理 → 该行 ⋮ → 分组**

在「专属分组」区勾选 A1 的分组 → 保存。

（等价路径：**分组管理 → 专属倍率 → 添加用户**。）

### A4. 用户建 Key

目标用户：**API 密钥 → 创建** → 关闭全能 Key → 分组选该专属分组。

未授权用户看不到该分组，无法使用。

---

## B. Edge：对应分组 + OAuth 透传

登录 **Edge 节点管理后台**（与 stub 指向的 edge 一致）。

### B1. 创建分组

**分组管理 → 创建分组**：与 Prod **同名、同平台（Anthropic）**。

同样：**Claude Code 限制关**，粘性路由 **passthrough/off**。

### B2. 绑定 OAuth 并开透传

**账号管理 → 编辑**（`type=oauth` 或 `setup-token`）

- **分组**：勾选 B1 的分组
- **OAuth 自动透传（仅替换认证）**：**开**

对该分组内每个 OAuth 账号重复。

### B3. 不要设置 Tier

**不要**在账号 ⋮ 菜单点 **「设置 Tier」**。

原因：账号一旦绑定 Tier（`tier_id`），Edge 上 anthropic config reconciler 会周期性执行 `ReapplyBaselineInfra`，自动：

- 打开 `enable_tls_fingerprint`
- 绑定 TLS 模板 `tk_canonical_cc_oauth`
- 补齐 credentials 自保护 / mimic 模板

这与 OAuth 透传（跳过 fingerprint、mimic、canonical 改写）**直接冲突**，表现为透传开了仍被改指纹。

若账号已误设 Tier，停止继续调 Tier；联系研发清除该账号的 `tier_id` 后再验透传。

---

## C. 验收

| 检查 | 预期 |
|------|------|
| 未授权用户 Key | 选不到 / 用不了该分组 |
| 授权用户 Key | 正常 200，用量记在该分组 |
| Prod stub | 已绑分组 + API Key 透传开 |
| Edge OAuth | 已绑分组 + OAuth 透传开 |
| Tier | OAuth 账号**无** Tier 绑定 |

---

## D. 常见错误

| 现象 | 处理 |
|------|------|
| 用户绑不了专属分组 | 用户管理 → 分组，补勾专属分组 |
| 403 / Claude Code only | 分组编辑 → 关闭 Claude Code 限制 |
| 开了透传仍像被改 UA/TLS | 查 Prod stub 是否也开了 API Key 透传；查 OAuth 是否误设 Tier |
| Edge 无可用账号 | Edge 账号是否绑进同名分组 |

---

## 附录：Admin 菜单对照

| 操作 | 路径 |
|------|------|
| 建专属分组 | 分组管理 → 创建分组 |
| 绑分组 | 账号管理 → 编辑 → 分组 |
| 授权用户 | 用户管理 → ⋮ → 分组 |
| API Key 透传 | 账号管理 → anthropic apikey → 自动透传（仅替换认证） |
| OAuth 透传 | 账号管理 → anthropic oauth → OAuth 自动透传（仅替换认证） |
| **禁止** | 账号管理 → ⋮ → **设置 Tier**（Passthrough OAuth 账号） |

透传仍会替换 `Authorization`、做计费/并发/审计及部分安全头过滤；不是「完全零改写」。
