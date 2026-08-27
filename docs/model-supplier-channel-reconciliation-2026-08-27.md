# 模型供应渠道：运营表与 TokenKey 线上账号对账

## 1. 结论

对账基准：

- 运营侧：`~/Codes/tk/模型厂商渠道信息-0827.xlsx`，工作表有效数据为第 2–57 行。
- 线上侧：TokenKey prod `accounts`、`account_groups` 的只读快照，采集于
  `2026-08-27T04:44:31Z`（北京时间 `2026-08-27 12:44:31`）。
- 线上“支持模型”只取账号 `credentials.model_mapping` 的键；它表示账号当前配置的
  TokenKey 模型入口，不等价于上游实测可服务。
- `status=active AND schedulable=true` 才记为当前可调度；账号存在但不可调度会单列。

运营表共 56 条供应记录，涉及 8 类供应渠道。线上能够按地址或明确名称匹配 5 类：
阿里 DashScope、百度千帆、CloudWise、XRToken、TokenSea。佳杰 / VSTECS 与 FMGo
未发现对应账号。

最重要的差异不是单个型号，而是当前没有稳定的运营事实到运行事实投影：

1. 表中没有“计划接入 / 已接入”状态和 TokenKey 账号绑定，未上线渠道无法区分计划项与遗漏。
2. 表中“全系列”、型号大小写、日期后缀和疑似漏小数点折扣不能机械执行。
3. 线上账号没有供应采购比例字段，现有 `priority` 也没有按采购比例形成可解释排序。
4. 一个供应商地址下存在多个合同模型组和不同采购比例，不能合并为一个供应账号。

## 2. 渠道总览

| 运营渠道 | 表格行 | 线上账号 | 当前状态 | 结论 |
| --- | --- | --- | --- | --- |
| 阿里 DashScope | 23 | 60、72、77、78 | 60/72 可调度；77 error；78 不可调度 | 已接入，但线上范围远大于“QWEN 全系列”这一描述 |
| 百度千帆 | 3、4、7、12、14、16 | 90 | active、不可调度 | 已配置但当前不承接流量；模型清单部分不一致 |
| CloudWise | 5、6、10、11、13 | 94、95 | 94 不可调度；95 可调度 | 已接入，线上用家族通配，覆盖范围大于表格 |
| XRToken | 21、22 | 96 | active、可调度 | 已接入；视频型号部分吻合，未发现 Seed 文本 |
| TokenSea | 56、57 | 93、92 | 均 active、可调度 | 已接入；并非严格“全系列”，且两个账号均混合承载其他厂商模型 |
| 佳杰 / VSTECS | 2、8、9、15、17–19、24–55 | 未发现 | — | 尚未绑定线上账号；需先区分计划接入与异常遗漏 |
| FMGo | 20 | 未发现 | — | 尚未绑定线上账号 |

地址比对采用 origin 规范化。CloudWise 表内为 `https://api.cloudwise.ai`，线上为
`https://api.cloudwise.ai/api`，判定为同一渠道但记录路径不一致。

## 3. 已匹配渠道的具体差异

### 3.1 阿里 DashScope

运营表仅有“QWEN / 全系列 / 6.5 折”。线上有 4 个 channel type 17 账号，
`base_url=https://dashscope.aliyuncs.com`，账号级 `priority` 均为 1。

线上模型映射包含：

- Qwen：`qwen-max`、`qwen-plus`、`qwen-turbo`、Qwen 3/3.6/3.7/3.8 多个具体型号；
- GLM：`glm-4.5`、`glm-4.6`、`glm-4.7`、`glm-5`、`glm-5.1`、`glm-5.2` 等；
- DeepSeek：60/72 还包含 `deepseek-v4-pro`、`deepseek-v4-flash`、
  `deepseek-v4-flash-0731`。

差异：

- “全系列”既不能证明覆盖全部 Qwen，也不能表达当前明确清单。
- 运营表没有记录该渠道同时承载 GLM 和 DeepSeek。
- 77 为 error，78 虽 active 但不可调度；表中没有账号数量和健康状态。
- 采购比例 0.65 没有投影为可解释的账号调度优先级。

### 3.2 百度千帆

线上账号 90：`platform=newapi`、`channel_type=46`、`priority=1`、
`status=active`、`schedulable=false`。

| 运营表型号 | 线上匹配 | 判断 |
| --- | --- | --- |
| `DeepSeek-V4-Pro-0813` | `deepseek-v4-pro` | 仅家族/主版本近似；线上没有 `0813` 标识，不能判为精确一致 |
| `DeepSeek-V4-Flash-0731` | `deepseek-v4-flash-0731` | 规范化后匹配 |
| `MiniMax-M2.7` | 无 | 表有、线上映射无 |
| `GLM 5.1` | `glm-5.1` | 规范化后匹配；表内重复两次 |
| `GLM 5.3` | 无 | 表有、线上映射无 |

线上额外包含 `glm-5`、`glm-5.2`、`kimi-k2.6`，以及 ERNIE、OCR、Embedding、
Qwen 等大量表外型号。账号当前不可调度，因此这些只能称为“已配置”，不能称为
“当前可供应”。

### 3.3 CloudWise

线上账号 94（Anthropic transport）和 95（OpenAI transport）均指向
`https://api.cloudwise.ai/api`；94 不可调度、priority 100，95 可调度、priority 150。

运营表中的 DeepSeek V4 Flash 与 GLM 5.0/5.1/5.2 被线上
`deepseek-*`、`glm-*` 家族通配覆盖；线上还声明 `kimi-*`、`minimax-*`、`claude-*`。

差异与证据边界：

- 通配映射是路由配置，不证明通配范围内每个型号都经真实上游调用验证。
- 表内的 `DeepSeek-V4-Flash-0731`、`deepseek-v4-flash` 不能仅凭 `deepseek-*`
  判定为实测可服务。
- 表中没有 Kimi、MiniMax、Claude 家族。
- 表内地址缺 `/api` 路径。
- 5–5.5 折没有形成线上优先级依据，且两个 transport 的优先级差异无法由运营事实解释。

### 3.4 XRToken

线上账号 96：`platform=newapi`、`channel_type=54`、priority 1、active 且可调度。
模型映射包含 Seedance 1.5、2.0、2.0 Fast/Mini、2.5。

差异：

- 表内 Seedance 2.0 4K 可映射到同家族，但 `model_mapping` 不携带分辨率能力，
  必须以视频协议实测确认 4K。
- 表内“Seed 文本”在线上账号映射中未发现。
- 线上有表内未登记的 Seedance 1.5、2.5 与 2.0 Fast/Mini。

### 3.5 TokenSea

| 表内描述 | 线上账号 | 线上实际 |
| --- | --- | --- |
| Claude 全系列，采购比例 0.16 | 93 `tokensea-cc` | priority 80；除 Claude 外还包含 GPT、GLM、Kimi、Qwen、MiniMax、DeepSeek、Gemini 图像 |
| GPT 全系列，采购比例 0.05 | 92 `tokensea` | priority 100；除 GPT 外还包含 Claude、Gemini 图像 |

差异：

- 两个“全系列”都不是可验证清单。
- GPT 采购比例更低，却拥有更大的 priority 数值，调度顺序没有按采购比例表达。
- 两个账号的跨厂商模型混装让供应事实与 transport 事实混在一起。

## 4. 尚未接入渠道

### 4.1 佳杰 / VSTECS

线上没有发现 `token.vstecscloud.com` 或明确对应账号。表格包含大量独立模型组，
例如 `stsjk-sanfang`、`stbl-5`、`zq-gd`、`dxbl`、`zq-qwen`、`minmax原厂`。
它们的采购比例、容量和扩容能力不同，必须作为独立供应源接入，不能合并成一个
“佳杰账号”。

表中第 2、8、9、15、17–19 行没有模型组/容量；第 24–55 行信息更完整。
同一模型也可能由多个模型组供应，这正是后续按采购比例派生调度顺序的输入。

### 4.2 FMGo

线上没有发现 `fmgo.top` 对应账号。表内只有 Seedance 2.0 480p/720p、采购比例 0.50。
是否能自动接入取决于上游是否实现 TokenKey 已支持的标准视频协议；若为私有异步任务协议，
自动探测应停止并明确报告“需要供应商适配器”，不能猜测 channel type 后上线。

## 5. 运营表逐行判定

判定词：

- `匹配`：地址/渠道与规范化型号均在线上明确出现；
- `近似`：家族或版本相近，但不能证明精确型号；
- `配置覆盖待探测`：线上通配映射覆盖，尚不能视为实测可服务；
- `表有线上无`：渠道已存在，但该型号未出现在其线上映射；
- `渠道未接入`：线上未发现对应供应渠道账号。

| 行 | 供应商 / 模型组 | 模型 | 采购信息 | 对账结果 |
| ---: | --- | --- | --- | --- |
| 2 | 佳杰 / 未填 | deepseek-v4-pro | 5.5 折 | 渠道未接入 |
| 3 | 百度 / 未填 | DeepSeek-V4-Pro-0813 | 7 折 | 近似 `deepseek-v4-pro`，版本后缀不一致 |
| 4 | 百度 / 未填 | DeepSeek-V4-Flash-0731 | 7 折 | 匹配；账号当前不可调度 |
| 5 | CloudWise / 未填 | DeepSeek-V4-Flash-0731 | 7 折 | 配置覆盖待探测 |
| 6 | CloudWise / 未填 | deepseek-v4-flash | 5 折 | 配置覆盖待探测 |
| 7 | 百度 / 未填 | MiniMax-M2.7 | 5 折 | 表有线上无 |
| 8 | 佳杰 / 未填 | MiniMax-M3 | 5 折 | 渠道未接入 |
| 9 | 佳杰 / 未填 | MinimaxH3 | 9 折 | 渠道未接入；型号需运营确认 |
| 10 | CloudWise / 未填 | GLM 5.0 | 5.5 折 | 配置覆盖待探测 |
| 11 | CloudWise / 未填 | GLM 5.1 | 5.5 折 | 配置覆盖待探测 |
| 12 | 百度 / 未填 | GLM 5.1 | 5 折 | 匹配；账号当前不可调度 |
| 13 | CloudWise / 未填 | GLM 5.2 | 5.5 折 | 配置覆盖待探测 |
| 14 | 百度 / 未填 | GLM 5.1 | 5 折 | 与第 12 行重复 |
| 15 | 佳杰 / 未填 | GLM 5.3 | 6 折 | 渠道未接入 |
| 16 | 百度 / 未填 | GLM 5.3 | 8.5 折 | 表有线上无 |
| 17 | 佳杰 / 未填 | kimi-k3 | 7 折 | 渠道未接入 |
| 18 | 佳杰 / 未填 | kimi-k2.6 | 6 折 | 渠道未接入 |
| 19 | 佳杰 / 未填 | kimi-k2.7-code | 6 折 | 渠道未接入 |
| 20 | FMGo / 未填 | Seedance 2.0 480p/720p | 5 折 | 渠道未接入 |
| 21 | XRToken / 未填 | Seedance 2.0 4K | 9 折 | 家族近似；4K 能力待协议实测 |
| 22 | XRToken / 未填 | Seed 文本 | 8 折 | 表有线上无 |
| 23 | 阿里 / 未填 | QWEN 全系列 | 6.5 折 | 渠道已接入；“全系列”不可执行，须展开 |
| 24 | 佳杰 / stsjk-sanfang | GLM-5 | 6 折 | 渠道未接入 |
| 25 | 佳杰 / stsjk-sanfang | GLM-5.1 | 6 折 | 渠道未接入 |
| 26 | 佳杰 / stbl-5 | GLM-5.2 | 5 折 | 渠道未接入 |
| 27 | 佳杰 / zq-gd | GLM-5.2 | `43折` | 渠道未接入；采购比例格式无效 |
| 28 | 佳杰 / 自部署-glm | GLM-5.2 自部署 | 4 折 | 渠道未接入 |
| 29 | 佳杰 / 智谱官 key | GLM-5.1、GLM-5V-Turbo、GLM-5.2 | `55折` | 渠道未接入；须拆成明确型号，采购比例格式无效 |
| 30 | 佳杰 / zq-gdlk | GLM-5.3 | 9 折 | 渠道未接入 |
| 31 | 佳杰 / zp-glm5.3 | GLM-5.3 官 key | `55折` | 渠道未接入；采购比例格式无效 |
| 32 | 佳杰 / stsjk-sanfang | DeepSeek-V4-Flash | 6 折 | 渠道未接入 |
| 33 | 佳杰 / stbl-5 | DeepSeek-V4-Pro | 5 折 | 渠道未接入 |
| 34 | 佳杰 / stbl-7 | DeepSeek-V4-Flash0731 | 7 折 | 渠道未接入 |
| 35 | 佳杰 / zq-gd | DeepSeek-V4-Pro | `43折` | 渠道未接入；采购比例格式无效 |
| 36 | 佳杰 / zq-gdlk | DeepSeek-V4-Flash | 9 折 | 渠道未接入 |
| 37 | 佳杰 / dxbl | DeepSeek-V4-Pro | `45折` | 渠道未接入；采购比例格式无效 |
| 38 | 佳杰 / stsjk-sanfang | Kimi-K2.7-Code | 6 折 | 渠道未接入 |
| 39 | 佳杰 / stsjk-sanfang | Kimi-K2.6 | 6 折 | 渠道未接入 |
| 40 | 佳杰 / zq-bl | Kimi-K2.7-Code | 6 折 | 渠道未接入 |
| 41 | 佳杰 / zq-bl | Kimi-K2.6 | 6 折 | 渠道未接入 |
| 42 | 佳杰 / zq-gdlk | Kimi-K3 | 9 折 | 渠道未接入 |
| 43 | 佳杰 / tb-k3 | Kimi-K3 | 7 折 | 渠道未接入 |
| 44 | 佳杰 / stsjk-qwen | Qwen-3.6 Plus | `55折` | 渠道未接入；采购比例格式无效 |
| 45 | 佳杰 / stsjk-qwen | Qwen-3.7 Plus | `55折` | 渠道未接入；采购比例格式无效 |
| 46 | 佳杰 / stsjk-qwen | Qwen-3.7 Flash | `55折` | 渠道未接入；采购比例格式无效 |
| 47 | 佳杰 / stsjk-qwen | Qwen-3.7 Max | `55折` | 渠道未接入；采购比例格式无效 |
| 48 | 佳杰 / stsjk-qwen | Qwen-3.6 Flash | `55折` | 渠道未接入；采购比例格式无效 |
| 49 | 佳杰 / stsjk-qwen3.8 | Qwen-3.8 Max | `73折` | 渠道未接入；采购比例格式无效 |
| 50 | 佳杰 / zq-qwen | Qwen-3.6 Plus | 5 折 | 渠道未接入 |
| 51 | 佳杰 / zq-qwen | Qwen-3.7 Plus | 5 折 | 渠道未接入 |
| 52 | 佳杰 / zq-qwen | Qwen-3.7 Max | 5 折 | 渠道未接入 |
| 53 | 佳杰 / dxbl-minimax-m3 | Minmax-M3 | 5 折 | 渠道未接入；型号拼写需确认 |
| 54 | 佳杰 / minmax 原厂 | Minmax-M3 官 key | 5 折 | 渠道未接入；型号拼写需确认 |
| 55 | 佳杰 / minmax 原厂 | Minmax-M2.7 官 key | 5 折 | 渠道未接入；型号拼写需确认 |
| 56 | TokenSea | Claude 全系列 | 1.6 折 | 渠道已接入；须展开明确型号，线上还混有非 Claude 模型 |
| 57 | TokenSea | GPT 全系列 | 0.5 折 | 渠道已接入；须展开明确型号，线上还混有非 GPT 模型 |

## 6. 不能由本次对账证明的事项

- 运营表价格是否仍为当前合同价格；线上没有采购比例事实可供反查。
- 通配映射内每个模型是否真实可服务。
- XRToken Seedance 2.0 是否支持表述中的 4K。
- 未发现账号的佳杰、FMGo 是“计划接入”还是“本应上线但遗漏”。
- `43折`、`45折`、`55折`、`73折` 是否分别代表 4.3、4.5、5.5、7.3 折；
  系统不得自动纠正。

这些不确定性应在供应源首次保存时由运营明确，之后由系统探测和账号绑定消除，
而不是继续留在自由文本 Excel 中。
