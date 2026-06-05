# TokenKey × Claude Code:最省心的 AI 编码方式

> 把世界上最强的编码模型,交到你手里——只要两行配置。
>
> 📖 Claude Code 官方使用指南:https://docs.claude.com/en/docs/claude-code

---

## 为什么是 TokenKey + Claude Code

好的工具不该让你研究它,而该让你忘了它的存在。

**Claude Code** 是 Anthropic 官方的编码代理:它读你的代码、改你的代码、跑你的命令,自己管理上下文、自己处理重试与流式。你只管表达意图,剩下的它来做。

**TokenKey** 是专为 Claude Code 这套真实流量形态深度调优的网关。模型、系统提示、流式、长上下文——这些最吃稳定性的路径,正是我们重点保障的路径。换句话说:**Claude Code 怎么用最舒服,TokenKey 就把那条路修得最平。**

两者合在一起,你得到的是:

- **开箱即用** —— 鉴权、重试、流式解析、上下文窗口,全都不用你操心。
- **最高的稳定性与成功率** —— 走的是被反复打磨的主路径,而不是自己拼装的旁路。
- **同一套手感,从交互到自动化** —— 白天敲代码,晚上跑批量,命令几乎不变。

> 你当然也能拿 API Key 自己拼 `/v1/messages` 请求。但那意味着自己处理模型名、请求头、流式细节和排障——把本该消失的复杂度又请了回来。**需要程序化集成时,用 `claude -p`(第四节)或 Claude Agent SDK(第四节末),依然不必碰这些底层。**

---

## 一、装上它

```bash
npm install -g @anthropic-ai/claude-code
```

> ⚠️ **一定要装 `@anthropic-ai/claude-code`，不是 `claude-cli`。**
> 两者是完全不同的包：`claude-code` 安装后提供 `claude` 命令，发出的 User-Agent 是 `claude-code/X.Y.Z`，能通过 TokenKey 的 CC 客户端校验；而 `claude-cli`（`@anthropic-ai/claude-cli` 或社区同名包）发出的是 `claude-cli/X.Y.Z (external, cli)`，会被 `default` 分组拒绝并返回 429。
>
> 用 `claude --version` 可确认安装的是哪个版本；输出里带 `claude-code` 就对了。

## 二、两行配置,接入 TokenKey

```bash
export ANTHROPIC_BASE_URL="https://api.tokenkey.dev"
export ANTHROPIC_AUTH_TOKEN="<你的 TokenKey API Key>"   # 形如 sk-xxxxx
```

写进 `~/.zshrc` / `~/.bashrc`,或项目里的 `.claude/settings.json` 的 `env` 块,从此再不用管它。

**两条约定,记住即可:**

- **Claude 模型走 `default` 分组** —— 确保你的 API Key 归属 `default` 分组,即可调度到 Claude 模型池。`default` 分组仅允许 Claude Code 客户端，其他工具（curl、claude-cli、OpenAI SDK 等）会被拒绝。
- **别让旧变量捣乱** —— shell 里残留的 `ANTHROPIC_API_KEY` 或冲突的 `ANTHROPIC_AUTH_TOKEN` 会覆盖上面的配置,清掉它们。

## 三、日常:一个命令

进到项目目录,敲一个词:

```bash
cd /path/to/your/project
claude
```

然后像和一位资深工程师结对一样,直接说你要做什么。这是最推荐、也最舒服的用法。

## 四、批量与自动化:加一个 `-p`

要在脚本、CI、定时任务里无人值守地跑,只需把交互换成 `claude -p`:

```bash
# 单条任务,结果直接打到 stdout
claude -p "总结 README.md 的核心功能,输出 5 条要点"

# 批量处理
for f in src/*.py; do
  claude -p "审查 $f 的潜在 bug,只列出问题,不要改代码" 2>&1 | tee "review-$(basename "$f").txt"
done
```

几条让自动化更稳的习惯:

- **限定工具** —— `--allowedTools "Read Grep Glob"`(只读)或按需放开,避免卡在权限确认。
- **结构化输出** —— `--output-format stream-json` 便于程序解析;纯文本默认即可。
- **预算护栏** —— `--max-budget-usd <金额>`,防止单次跑飞。
- **管道安全** —— 脚本里加 `set -o pipefail`,否则失败会被当成功。

> 要把 AI 深度嵌进自己的服务(Python / TypeScript),用 **Claude Agent SDK**(原 Claude Code SDK)——它把 Claude Code 的工具、agent 循环、上下文管理封装成一个库:
> 📖 https://code.claude.com/docs/en/agent-sdk

---

## 五、万一卡住了

| 现象 | 怎么办 |
| --- | --- |
| 401 / 鉴权失败 | API Key 写错,或 shell 残留 `ANTHROPIC_API_KEY` 覆盖了配置 —— `env \| grep ANTHROPIC` 查一下,清掉多余的 |
| 调不到模型 / 无可用账号 | 确认 API Key 归属 **`default` 分组** |
| 连接失败 | 确认 `ANTHROPIC_BASE_URL=https://api.tokenkey.dev`(是 https,后面不带多余路径) |
| **429 "this group only allows Claude Code clients"** | 你用的不是 Claude Code，而是 `claude-cli` 或其他工具。运行 `claude --version` 确认——输出应包含 `claude-code`。若不是，重新安装：`npm install -g @anthropic-ai/claude-code` |
| 用量 / 余额 | 登录 TokenKey 控制台查看 |

---

**一句话:** 两行配置(base URL + key,Claude 走 `default` 分组),日常 `claude`,批量 `claude -p`,集成用 Agent SDK。复杂留给我们,简单留给你。
