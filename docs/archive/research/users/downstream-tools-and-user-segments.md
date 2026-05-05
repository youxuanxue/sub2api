# Token 分发平台下游工具与用户群体调研

> 调研时间：2026-04-14
>
> 调研范围：Linux.do 社区、OpenRouter 官方数据、GitHub 开源生态、知乎/SegmentFault 技术社区
>
> 调研对象：OpenRouter、sub2api、New API、One API 等 Token 分发/API 中转平台的下游用户

---

## 一、下游用户使用的产品与工具全景图

Token 分发平台（OpenRouter、sub2api、New API、One API 等）为下游用户提供 OpenAI/Anthropic 兼容的 API 接口。下游用户使用这些 API Key 接入各类工具和产品，涵盖以下六大类别。

### 1. AI 编程工具（Coding Tools）

这是增长最快的使用场景。根据 OpenRouter 2025 State of AI 报告，编程类请求从 2025 年初的约 11% 增长到超过 50%，是 prompt token 消耗量的主要驱动力。


| 工具                   | 类型         | 接入方式                                 | 说明                                          |
| -------------------- | ---------- | ------------------------------------ | ------------------------------------------- |
| **Cursor**           | AI IDE     | 设置 Base URL + API Key                | 社区最热门的 AI 编程工具，Composer Agent 模式深度集成 LLM    |
| **Claude Code**      | 终端 Agent   | `ANTHROPIC_BASE_URL` 环境变量            | Anthropic 官方 CLI agent，支持 Unix 管道，可集成 CI/CD |
| **Cline**            | VS Code 插件 | 配置 API endpoint                      | 开源自主编程 agent，可在 IDE 内编辑文件和执行命令              |
| **Windsurf**         | AI IDE     | 内置配置                                 | Cascade 上下文记忆，免费版性价比高                       |
| **Continue.dev**     | IDE 扩展     | 配置 provider                          | 开源，支持多模型，深度集成 VS Code/JetBrains             |
| **Aider**            | CLI 工具     | `OPENAI_API_BASE` 环境变量               | 命令行编码工具，自动处理 git commit                     |
| **OpenAI Codex CLI** | 终端 Agent   | `OPENAI_API_KEY` + `OPENAI_BASE_URL` | OpenAI 官方 CLI 编码工具                          |
| **Roo Code**         | IDE Agent  | 配置 API                               | AI 编码 agent，支持自动化任务                         |
| **Trae**             | AI IDE     | 内置配置                                 | 字节跳动出品的 AI IDE                              |
| **GitHub Copilot**   | IDE 插件     | 通常使用官方订阅                             | 企业版支持审计日志与合规                                |
| **通义灵码**             | IDE 插件     | 内置配置                                 | 阿里云出品，国内开发者常用                               |


### 2. AI 聊天客户端（Chat Clients）

通用聊天界面是最基础也最广泛的使用场景，用户通过填入 API Key 和 Base URL 即可接入。


| 工具                              | 类型      | 特点                                           |
| ------------------------------- | ------- | -------------------------------------------- |
| **Cherry Studio**               | 桌面客户端   | Linux.do 社区高度推荐，跨平台（iOS/macOS/Windows），多模型管理 |
| **NextChat (ChatGPT-Next-Web)** | Web/桌面  | 轻量级，跨平台，部署简单，社区非常活跃                          |
| **LobeChat**                    | Web/自托管 | 功能丰富，支持插件系统和知识库                              |
| **Chatbox**                     | 桌面客户端   | 界面简洁，支持 Prompt 管理                            |
| **Open WebUI**                  | Web 自托管 | 功能完整的自托管方案                                   |
| **LibreChat**                   | Web 自托管 | 开源多模型聊天 UI，支持 BYOK，类似 ChatGPT 界面             |
| **TypingMind**                  | Web     | 买断制，支持多模型、Prompt 库、团队协作                      |
| **ChatALL**                     | 桌面客户端   | 同时向多个 LLM 发送请求对比回复                           |


### 3. 翻译与写作工具（Translation & Writing）


| 工具                              | 类型       | 说明                                                         |
| ------------------------------- | -------- | ---------------------------------------------------------- |
| **沉浸式翻译 (Immersive Translate)** | 浏览器扩展    | 支持配置自定义 API Key，网页双语对照翻译                                   |
| **Bob**                         | macOS 翻译 | 通过 `bob-plugin-openai-translator` 插件接入 OpenAI 兼容 API       |
| **OpenAI Translator**           | 浏览器扩展    | 基于 OpenAI API 的翻译工具                                        |
| **Obsidian AI 插件**              | 笔记插件     | `obsidian-smart-connections`、`obsidian-weaver` 等，在笔记中集成 AI |
| **Notion AI**                   | 写作工具     | 部分用户通过中转 API 替代官方 AI 功能                                    |


### 4. 角色扮演与创意应用（Roleplay & Creative）

根据 OpenRouter 数据，角色扮演是仅次于编程的第二大使用场景，近 60% 的 roleplay token 属于"游戏/角色扮演"类别。


| 工具                        | 类型        | 说明                                                   |
| ------------------------- | --------- | ---------------------------------------------------- |
| **SillyTavern**           | Web 自托管   | 最流行的 AI 角色扮演前端，支持多 API 后端                            |
| **Agnai**                 | Web       | SillyTavern 替代品，更现代的技术栈                              |
| **RisuAI**                | Web/PWA   | 角色扮演工具，支持多种模型                                        |
| **OpenClaw (原 ClawdBot)** | CLI Agent | 高自主性 TypeScript agent，支持多渠道（Telegram/WhatsApp/Slack） |
| **各类 Telegram Bot**       | 即时通讯      | 基于 API 构建的聊天机器人                                      |


### 5. Agent 框架与自动化（Agent Frameworks & Automation）

开发者和企业使用 API Key 驱动各类 Agent 框架：


| 工具/框架                     | 类型         | 说明                  |
| ------------------------- | ---------- | ------------------- |
| **LangChain / LangGraph** | Agent 框架   | 行业标准，图结构工作流编排       |
| **CrewAI**                | 多 Agent 框架 | 角色分工的多 agent 协作     |
| **AutoGen**               | 多 Agent 框架 | 微软出品，异步多 agent 对话   |
| **Dify**                  | 低代码平台      | 可视化 Agent 构建，支持 RAG |
| **Coze (扣子)**             | 低代码平台      | 字节跳动出品，可视化 Bot 构建   |
| **Flowise**               | 低代码平台      | 拖拽式 LangChain 流程搭建  |
| **n8n / Zapier**          | 自动化平台      | 工作流自动化，集成 AI 节点     |
| **MindStudio**            | Agent 平台   | 200+ 模型接入，可视化构建     |


### 6. API 代理与转换工具（Proxy & Conversion）

这类工具将非标准 API（如订阅制 Web Session）转换为 OpenAI 兼容格式，是 Token 分发生态的关键基础设施：


| 工具                                  | 功能                                          |
| ----------------------------------- | ------------------------------------------- |
| **sub2api**                         | 将 Claude/OpenAI/Gemini/Antigravity 订阅转为 API |
| **New API**                         | API 聚合管理与分发，额度管控                            |
| **One API**                         | LLM API 管理与二次分发                             |
| **Antigravity Tools**               | 本地 AI 网关，Web Session → OpenAI 格式 API        |
| **CLIProxyAPI**                     | 将 Claude Code/Gemini CLI 等封装为 OpenAI 兼容 API |
| **claude-code-proxy / code-switch** | Claude Code 多供应商代理                          |
| **aiproxy**                         | 高性能 AI 网关，智能错误处理                            |
| **VoAPI**                           | 基于 New API 的高性能分发系统                         |


---

## 二、用户群体画像与工具偏好差异

### 1. 学生 / 个人爱好者

**特征**：价格敏感，追求免费或极低成本，技术能力参差不齐。

**典型工具组合**：

- 聊天：Cherry Studio / NextChat（一键部署）
- 编程：Windsurf 免费版 / Cline（自备 API Key 无订阅费）
- 翻译：沉浸式翻译 + 自定义 API
- 角色扮演：SillyTavern

**行为特征**：

- 热衷于 Linux.do 社区讨论的免费额度获取方式
- 使用 EDU 邮箱、GitHub 学生包等权益
- 倾向于拼车共享 API 额度以降低成本
- 对工具的易用性要求高，不愿意复杂配置

### 2. 独立开发者 / 自由职业者

**特征**：具备技术背景，追求效率与灵活性，对 API 稳定性和响应速度敏感。

**典型工具组合**：

- 编程：Cursor + Claude Code（主力）、Aider / Continue.dev（辅助）
- 聊天：Cherry Studio / TypingMind（日常问答）
- Agent：LangChain / Dify（项目开发）
- 管理：New API / One API（自建中转站，多模型路由）
- 翻译/写作：Obsidian AI 插件、Bob

**行为特征**：

- 关注 LMSYS 模型排行榜，根据性价比选择模型
- 自建 API 中转服务，使用 One-API / New-API 聚合管理
- 参与开源项目贡献（MCP 服务器、AI 工具插件）
- 在 Linux.do "开发调优"板块活跃，分享技术方案

### 3. 技术团队 / 小型企业

**特征**：追求生产环境稳定性，有预算但需控制成本，关注合规性。

**典型工具组合**：

- 编程：Cursor Business / GitHub Copilot Enterprise
- Agent 框架：LangGraph / CrewAI（生产级编排）
- 平台：Dify / Coze（快速搭建内部 AI 工具）
- 网关：OpenRouter（failover + 成本控制）/ 自建 New API
- CI/CD：Claude Code + GitHub Actions

**行为特征**：

- 使用 OpenRouter 等平台实现 provider failover 和预算管控
- 关注 SLA、数据隐私、并发处理能力
- 需要审计日志、RBAC 权限控制
- 通过 GitHub Actions 集成 AI agent 到开发流程

### 4. AI 创作者 / 内容社区用户

**特征**：非技术背景居多，重视对话体验和创作自由度，对价格中等敏感。

**典型工具组合**：

- 角色扮演：SillyTavern / Agnai / RisuAI
- 聊天：NextChat / Cherry Studio / ChatALL
- 写作辅助：沉浸式翻译、各类浏览器插件
- 社交 Bot：Telegram Bot / Discord Bot

**行为特征**：

- 大量使用 Claude（Sonnet/Opus）进行角色扮演和创意写作
- 对模型的创造力和上下文理解能力有较高要求
- 是 Token 消耗的主力用户之一（长对话 + 高频使用）
- 在 Linux.do "搞七捻三"板块讨论 API 渠道和价格

### 5. DevOps / 后端工程师

**特征**：命令行原住民，追求自动化和可脚本化，对终端工具有强烈偏好。

**典型工具组合**：

- 编程：Claude Code（终端原生）、Codex CLI、Aider
- 自动化：n8n / Zapier + AI 节点
- 基础设施：自建 API 网关（aiproxy / axonhub）
- 部署：Docker + Cloudflare + Caddy 构建反代链路

**行为特征**：

- 偏好 CLI 工具，追求 Unix 管道集成
- 构建自动化发布链路（Cloudflare + Lucky/Caddy）
- 维护自托管的 API 管理服务
- 使用 MCP 协议将 AI agent 接入外部工具和数据

---

## 三、不同分发平台的用户工具生态对比


| 平台             | 主要用户群            | 核心下游工具                                      | 特色                                            |
| -------------- | ---------------- | ------------------------------------------- | --------------------------------------------- |
| **OpenRouter** | 全球开发者、企业         | Claude Code、Cursor、Agent SDK、GitHub Actions | Provider failover、成本控制、usage analytics、研究数据发布 |
| **sub2api**    | Linux.do 社区、拼车用户 | Cherry Studio、NextChat、Cursor、Cline         | 订阅转 API、成本分摊、多平台订阅统一接入                        |
| **New API**    | 技术型站长、中转站运营者     | 所有 OpenAI 兼容工具                              | 额度管理、渠道路由、二次分发、adaptor 层支持 100+ 渠道类型          |
| **One API**    | 个人/小团队           | 聊天客户端、编程工具                                  | 简单易用、多 provider 聚合、Key 管理                     |


---

## 四、工具接入方式总结

绝大多数下游工具通过以下两种方式接入 Token 分发平台：

### OpenAI 兼容接入（最通用）

```bash
OPENAI_API_KEY=sk-xxx
OPENAI_API_BASE=https://your-gateway.example.com/v1
```

支持此方式的工具：Cursor、Cline、Aider、Continue.dev、Cherry Studio、NextChat、LobeChat、Chatbox、SillyTavern、LangChain、Dify 等绝大多数工具。

### Anthropic 兼容接入

```bash
ANTHROPIC_API_KEY=sk-xxx
ANTHROPIC_BASE_URL=https://your-gateway.example.com/api
```

支持此方式的工具：Claude Code、Anthropic Agent SDK、claude-code-action (GitHub Actions)。

---

## 五、关键趋势观察

### 1. 编程场景爆发式增长

OpenRouter 数据显示，编程类请求从 2025 年初的 ~11% 增长到 2025 年底的 >50%。Cursor、Claude Code、Cline 是核心驱动力。编程 prompt 长度是通用 prompt 的 3–4 倍，Token 消耗巨大。

### 2. Agent 化趋势

工具使用正从单轮问答向多步骤、工具调用的 Agent 模式转变。Claude Sonnet 和 Gemini Flash 是 Agent 工作流中 tool invocation 最集中的模型。MCP (Model Context Protocol) 正成为 Agent 连接外部工具的通用标准。

### 3. BYOK（Bring Your Own Key）模式普及

越来越多工具支持用户自带 API Key，而非绑定特定服务商。这使得 Token 分发平台的用户可以无缝切换到几乎任何 AI 工具。

### 4. 本地化与自托管需求强烈

Linux.do 社区用户普遍倾向于自托管（Open WebUI、LibreChat、LobeChat），对数据隐私和可控性有较高要求。这也推动了 sub2api、New API 等自部署方案的流行。

### 5. 多模型路由成为刚需

用户不再固定使用单一模型，而是根据任务类型、成本、速度动态选择模型。这使得具备多模型路由能力的网关（OpenRouter、New API）成为基础设施级需求。

---

## 六、对 sub2api/TokenKey 的启示

1. **优先保障编程工具兼容性**：确保 Cursor、Claude Code、Cline 等高频编程工具的无缝接入和稳定性。
2. **OpenAI + Anthropic 双协议支持**：覆盖最广泛的下游工具生态。
3. **额度与成本可视化**：为不同用户群提供用量统计、预算控制功能。
4. **多模型路由能力**：支持按任务类型、成本、延迟智能路由到不同模型/渠道。
5. **聊天客户端兼容性测试**：Cherry Studio、NextChat 是 Linux.do 社区用户使用最多的客户端，确保兼容性。
6. **Agent 模式 / Tool Calling 支持**：随着 Agent 化趋势加速，需要完整支持 function calling / tool use 协议。

---

## 参考来源

- [OpenRouter State of AI 2025](https://openrouter.ai/state-of-ai) — 基于 100T+ token 的经验性使用研究
- [OpenRouter Claude Code Integration Guide](https://openrouter.ai/docs/guides/coding-agents/claude-code-integration)
- [Linux.do 开发调优板块](https://linux.do/c/dev/) — API 使用讨论
- [Linux.do 跨平台 AI 客户端推荐讨论](https://linux.do/t/topic/267144/19)
- [Linux.do New API 国内使用方式](https://linux.do/t/topic/1805875)
- [Linux.do Cursor 使用自己的 API Key](https://linux.do/t/topic/1066874)
- [知乎：2025 年 AI 编程工具深度测评](https://zhuanlan.zhihu.com/p/1978875918194873471)
- [SegmentFault：主流 AI 编程工具横向对比 2026](https://segmentfault.com/a/1190000047700163)
- [CustomGPT: 100s of OpenAI-Compatible Tools](https://customgpt.ai/100s-of-openai-compatible-tools-connect-to-rag-api/)
- [Tembo: 2026 Guide to Coding CLI Tools](https://www.tembo.io/blog/coding-cli-tools-comparison)

