# edge-fra1 400 排查事实清单（2026-05-13）

> 范围：仅记录本次会话中**已查询到的事实数据**，不扩展未验证推断。
> 目标：`edge-fra1`（`eu-west-3`，实例 `i-05350d8cc7c838355`），账号 `cc-fr-fra-ec2-5-1-a`。

---

## 1. 基础目标与环境

- Edge 目标信息（来自 `deploy/aws/stage0/edge-targets.json`）
  - `fra1.region = eu-west-3`
  - `fra1.domain = api-fra1.tokenkey.dev`
  - `fra1.stack = tokenkey-edge-fra1-stage0`
- 实例：`i-05350d8cc7c838355`
- 重点账号：`cc-fr-fra-ec2-5-1-a`
- 重点分组：`default`

---

## 2. 账号与分组配置快照（数据库）

### 2.1 账号快照（accounts）

查询结果（UTC）：
- `id=1`
- `name=cc-fr-fra-ec2-5-1-a`
- `platform=anthropic`
- `type=oauth`
- `status=error`
- `error_message=Organization disabled (400): This organization has been disabled.`
- `created_at=2026-05-13 03:00:59.014016+00`
- `updated_at=2026-05-13 10:13:12.588561+00`
- `concurrency=3`
- `session_window_status=allowed`
- `rate_limited_at` 为空
- `rate_limit_reset_at` 为空

`extra_json` 已提取的关键字段：
- `base_rpm=8`
- `max_sessions=8`
- `session_idle_timeout_minutes=8`
- `rpm_strategy=tiered`
- `rpm_sticky_buffer=3`
- `org_uuid=12fee4ed-e049-4559-8027-7b22d0f52889`
- `account_uuid=58d0fc8c-300e-45d1-a0a1-b9ba5e1163c9`
- 存在 `model_rate_limits` 历史记录（示例：`claude-3-5-sonnet-20240620`）

### 2.2 分组快照（groups）

- `group.name=default`
- `group.id=1`
- `group.platform=anthropic`
- `group.status=active`
- `group.rpm_limit=8`

### 2.3 分组账号可用性

`default` 分组账号映射：
- 仅 1 个账号：`cc-fr-fra-ec2-5-1-a`
- 该账号状态：`error`
- 汇总：`active_accounts=0`, `total_accounts=1`

---

## 3. 从“首呼成功”到“最终 400”的调用数据（usage_logs + 网关日志）

### 3.1 时间边界

- 首次成功调用：`2026-05-13 07:50:52.308561+00`
- 最后一条成功调用：`2026-05-13 10:03:35.085653+00`
- 上游 400 触发时刻：`2026-05-13T10:13:12.587Z`（日志）
- 首呼成功到 400 前成功请求总量：`107`

### 3.2 10:13 最终 400 的链路事实（日志）

同一请求链路内已观测到：
- `content_moderation.gateway_check_start`
  - `path=/v1/messages`
  - `method=POST`
  - `model=claude-opus-4-7`
  - `stream=true`
  - `body_bytes=951352`
  - `request_id=590be4ef-04c7-4e11-946c-3a16ec13365f`
  - `client_request_id=67a459db-69ec-4413-a43f-da4aab404a6c`
  - `trajectory_id=09931517-7fd0-49c2-9b6f-918736359172`
- `sticky.scheduler_entry`
  - `session_hash=5835f950`
  - `sticky_account_id=1`
- `[Forward] Using account: ID=1 Name=cc-fr-fra-ec2-5-1-a`
- 上游错误：
  - `Status=400`
  - `message=This organization has been disabled.`
  - 上游 `request_id=req_011CazPb8fsDkTm3Q1TLHvk3`

### 3.3 400 后续现象

- 连续出现 `gateway.select_account_no_available`
- 错误文本：`no available accounts`
- 与 §2.3 一致（分组仅此账号，且状态 error）

---

## 4. 请求分布事实

### 4.1 5 分钟桶（首呼成功到 400 前）

- 07:50: 2
- 08:10: 2
- 08:15: 2
- 09:20: 20
- 09:25: 7
- 09:35: 7
- 09:40: 19
- 09:45: 26
- 09:50: 8
- 09:55: 11
- 10:00: 3

### 4.2 10 分钟桶（首呼成功到 400 前）

- 07:50: 2
- 08:10: 4
- 09:20: 27
- 09:30: 7
- 09:40: 45
- 09:50: 19
- 10:00: 3

### 4.3 每分钟总体

- 时间窗总分钟数：`144`
- 活跃分钟：`32`
- 静默分钟：`112`
- 每分钟峰值：`8`（`09:48`）
- Top 分钟：
  - `09:48`=8
  - `09:43`=7
  - `09:56`=7
  - `09:24`=6
  - `09:49`=6

### 4.4 模型分布（107 成功请求）

- `claude-opus-4-7`: 87（81.31%）
- `claude-haiku-4-5-20251001`: 14（13.08%）
- `claude-sonnet-4-6`: 6（5.61%）

### 4.5 UA + stream 分布（107 成功请求）

- `claude-cli/2.1.140 (external, sdk-cli)` + `stream=true`: 55（51.40%）
- `claude-cli/2.1.140 (external, cli)` + `stream=true`: 45（42.06%）
- `curl/8.5.0` + `stream=false`: 3（2.80%）
- `curl/8.5.0` + `stream=true`: 3（2.80%）
- `claude-cli/2.1.140 (external, sdk-cli)` + `stream=false`: 1（0.93%）

### 4.6 请求唯一性

- `total_rows=107`
- `distinct_request_ids=107`
- `duplicate_rows=0`

结论（事实描述）：这 107 条成功请求中，每条 request_id 唯一，不是同一个 request_id 重复发送。

### 4.7 端点分布

- 成功请求全在 `/v1/messages`（107）

### 4.8 时延与 token 统计（107 成功请求整体）

- `avg_duration_ms=12816`
- `p50_duration_ms=8829`
- `p95_duration_ms=41615`
- `avg_input_tokens=16`
- `p50_input_tokens=1`
- `p95_input_tokens=78`
- `avg_output_tokens=907`
- `p50_output_tokens=451`
- `p95_output_tokens=3319`

---

## 5. sticky / session 相关观测事实

### 5.1 sticky session hash 样本（日志）

已明确观测到的 `session_hash` 样本：
- `157uj178`（08:17，sonnet）
- `2cobbhzi`（08:17，sonnet）
- `950d1261`（09:20 起多次，opus）
- `5835f950`（10:13 400 触发请求）

### 5.2 `sticky_account_id=0` 观测

- 观测到一次 `sticky_account_id=0`（09:20:03）
- 随后同 hash 继续命中 `sticky_account_id=1`

---

## 6. curl 流量定位（首呼成功到 400 前）

`curl/8.5.0` 共 6 条，全部成功，全部 `/v1/messages`，模型均 `claude-sonnet-4-6`：
- 07:50:52.308561（stream=true）
- 07:50:53.846097（stream=false）
- 08:14:58.111951（stream=true）
- 08:14:59.349747（stream=false）
- 08:17:22.380859（stream=true）
- 08:17:23.503438（stream=false）

分布在早期低量阶段（07:50 / 08:14 / 08:17）。

---

## 7. 本机与 edge 凭证来源核对（只读）

### 7.1 本机环境变量

- 存在：`ANTHROPIC_BASE_URL=https://api.tokenkey.dev`
- 未发现：`ANTHROPIC_API_KEY`

### 7.2 edge-fra1 容器环境

- `tokenkey` 容器 `env` 与 `docker inspect` 相关筛选中，未观测到 `ANTHROPIC_API_KEY`（本次筛选条件下）

### 7.3 edge 账号凭证存储

账号 `cc-fr-fra-ec2-5-1-a`：
- `has_credentials=true`
- `credentials_len=807`
- 说明该账号存在数据库凭证字段（非空）

---

## 8. 代码层 sticky 注入行为事实（与本次问题关联）

- Anthropic API-key 透传路径在请求转发前存在 sticky 注入逻辑：
  - 调用 `DeriveStickyKey`
  - 条件满足时调用 `InjectAnthropicMessagesBody` 写入 `metadata.user_id`
- `InjectAnthropicMessagesBody` 行为：
  - 仅在允许注入且 key 存在且 `metadata.user_id` 为空时写入
  - 对真实 Claude Code UA（`IsClaudeCodeUA=true`）不注入

---

## 9. 事实边界（本文件不越界）

本文件仅列事实，不把“可能性”写成结论。当前已确定事实是：

1. 10:13:12 请求命中上游 400，消息为 `This organization has been disabled.`
2. 该账号随后为 `status=error`，分组仅此账号，导致后续 `no available accounts`。
3. 在 400 前已有 107 条成功调用，且 request_id 全唯一。

未在本次会话内直接验证的事项（因此不作为结论写死）：
- 上游 Console 侧导致 `organization disabled` 的最终根因归类（账单/风控/合规/人工封禁等）。

---

## 10. 数据来源说明

- 全部数据来自本次会话中的只读查询：
  - AWS SSM 远程命令
  - `docker logs tokenkey`
  - `docker exec tokenkey-postgres psql ...`
  - 本地环境变量读取
- 若后续需要可复核，建议以本文件中的时间戳与关键字段重新执行同类查询。
