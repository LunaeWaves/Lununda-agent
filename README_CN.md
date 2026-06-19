<div align="center">

# Lununda Agent

基于 Go 的轻量级、多租户 AI Agent 运行时。

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev)

**单一二进制 · 任意大模型 · 多智能体 · 沙箱隔离 · 云端就绪**

[快速开始](#快速开始) · [特性](#特性) · [Lununda Agent 的新功能](#lununda-agent-的新功能) · [架构](#架构) · [配置](#配置) · [许可证](#许可证)

</div>

> **Lununda Agent** 是 [FastClaw](https://github.com/fastclaw-ai/fastclaw) 的社区分支，
> 原始项目由 ThinkAny 团队创建并慷慨开源。我们感谢 FastClaw 作者构建了扎实的基础。
> Lununda Agent 继承了相同的核心理念——单一二进制、纯环境变量启动、Agent 工厂——
> 同时在品牌视觉、用户体验、授权模型和工作区隔离方面持续迭代，
> 由 [LunaeWaves](https://github.com/LunaeWaves) 组织维护。

---

<p align="center">
  <img src="previews/admin.png" alt="Lununda Agent 管理面板" width="900">
  <br>
  <em>平台管理：智能体、模型、技能、用户、API 密钥</em>
</p>

<p align="center">
  <img src="previews/agent.png" alt="Lununda Agent 智能体管理" width="900">
  <br>
  <em>每个智能体的独立管理：对话、自定义、作用域内的模型/技能/通道/调度器</em>
</p>

## 什么是 Lununda Agent？

Lununda Agent 是一个 **智能体工厂**——它创建、管理和运行 AI 智能体。
每个智能体拥有独立的人格（SOUL.md）、记忆、技能和工具。运行时统一处理
大模型通信、工具执行、沙箱隔离、会话管理以及 IM 通道桥接，全部封装在
单一二进制中。

**为什么选 Lununda Agent？**

- **一个二进制，所有大模型。** 对接任何 OpenAI 兼容端点（OpenAI、Anthropic、
  Ollama、OpenRouter、Groq、DeepSeek、Mistral……）。每个智能体可独立覆盖
  提供商与模型，并按 系统 → 用户 → 智能体 三级作用域继承。
- **真正的沙箱隔离。** Docker、E2B、Boxlite 三种后端把 `exec`、`write_file`
  与技能代码与宿主隔离。内置 `ghcr.io/lunaewaves/lununda-sandbox` 镜像预装
  Python 3、Node 22、Camoufox 与常用 fetch/parse/preview 依赖。
- **可推理的授权模型。** 三档门控（`ask` / `auto` / `yolo`）配合 hardline
  底线与危险命令黑名单，让智能体只在用户明确许可时才走出工作区——
  斜杠命令 `/yes` `/no` `/ask` `/auto` `/yolo` 在 IM 与 Web 通用。
- **开箱即用的多租户。** 仅靠一个 `X-Lununda-End-User` 请求头实现终端用户
  级数据隔离；会话、记忆、文件绝不跨用户泄漏。
- **智能体原生构建块。** 长时 Goal、正则 Hook、插件 Hook、定时任务、
  MCP 服务、知识库、Wiki 生成器同源出厂，而非外挂拼装。

```bash
# 安装（将二进制文件放到 ~/.local/bin 并添加到 PATH）
curl -fsSL https://raw.githubusercontent.com/LunaeWaves/Lununda-agent/main/install.sh | bash
```

## 快速开始

### 1. 首次运行

```bash
lununda
# 启动向导 → 配置大模型提供商 → 创建默认智能体。
# 前台模式启动，^C 停止。使用 `lununda daemon start` 后台运行，
# 或 `lununda daemon install` 注册为 launchd/systemd 服务。
```

### 2. 控制面板

打开 `http://localhost:18953`，使用管理员账号登录。

- **智能体** — 创建和管理智能体，每个智能体有独立的人格和模型
- **技能** — 安装共享技能（ClawHub 或 GitHub）
- **模型** — 配置大模型提供商（OpenAI、Anthropic、Ollama、OpenRouter 等）
- **API 密钥** — 颁发编程凭证（管理员 / 用户 / 智能体等级别）
- **设置** — 通用（主题）、账户（资料 + 密码）、运行时（沙箱配置；仅管理员）

> 非管理员用户默认可使用 **模型**、**API 密钥** 和 **设置（通用 + 账户）**。
> 他们看到管理员共享的资源标记为 `继承`，并可在其上叠加自己的私有覆盖——
> 与智能体运行时使用相同的继承模式。

### 3. 智能体管理

点击智能体进入管理面板：

- **对话** — 与智能体聊天（调试/测试）
- **文件** — 编辑 SOUL.md、IDENTITY.md、MEMORY.md 等
- **技能** — 智能体私有技能
- **模型** — 智能体专属的模型覆盖（按名称遮蔽系统项；agent-scope 的 `agents.defaults.model` 覆盖系统默认）
- **通道** — 连接 IM 机器人（Telegram、Discord、Slack、微信、飞书、LINE），让终端用户在自有平台上对话
- **调度器** — 查看和管理智能体通过 `create_cron_job` 创建的定时任务（"每天 9 点提醒我"、"5 分钟后叫我"）；可在 UI 暂停/删除
- **会话** — 对话历史

**分享。** 编辑对话框中提供"公开访问"开关（默认关闭）。开启后任何人可通过聊天 URL
（`/agents/{id}/chat/`）与智能体对话；会话、记忆、USER.md 按对话者隔离，
SOUL / IDENTITY / 技能共享自拥有者。关闭后只有拥有者（或 super_admin）可访问。

## 架构

```
~/.lununda/
  lununda.db                # SQLite 数据库 — 用户、智能体、会话、API 密钥、
                             #   配置、智能体文件均存储于此
  skills/                    # 共享技能（内置 + 用户安装）
  agents/
    <agentId>/
      agent/                 # 身份文件、私有技能、记忆
        skills/
      workspace/             # 按会话命名空间隔离的工作产物（session_key）
      policy.json            # 该智能体的路径白名单
```

所有数据以数据库为真源（技能文件夹除外）。默认使用 SQLite；
设置 `LUNUNDA_STORAGE_DSN` 指向 PostgreSQL 以支持多节点部署。

**没有 `lununda.json` 配置文件。** 启动配置（端口、绑定地址、存储 DSN、沙箱后端）
来自 `LUNUNDA_*` 环境变量；所有用户可见配置（提供商、通道、设置、默认值）
存储在 `configs` 表中，通过控制面板或 `lununda agents config` 编辑。

### 请求流程

1. **入站消息** 通过 IM 通道、Web 聊天 SSE 或 OpenAI 兼容 API 到达。
2. `bus.InboundMessage` 经 `channels.Manager` 路由 → 智能体解析（通道绑定或默认）。
3. `gateway.Gateway` 解析归属用户的 `UserSpace`（懒加载）及其 `agent.Manager`。
4. 智能体执行 **ReAct 循环**：系统提示 → LLM 调用 → 工具执行 → LLM 调用 → …… 直到无更多工具调用。
5. 工具通过 `tools.Registry` 分发，作用域为智能体级，由合并后的 系统→用户→智能体 配置构建。
6. 回复流回原始通道；异步结果（cron、goal）通过 `/api/chat/subscribe` 实时推送到打开的聊天面板。

### Lununda Agent 存什么

| 数据 | 归属 | 后端存储 |
|------|------|---------|
| 智能体记录、SOUL.md / IDENTITY.md / MEMORY.md / agent.json | 智能体 | DB（`agent_files` 表） |
| 会话（聊天历史） | 智能体 × 用户 | DB（`sessions` 表） |
| API 密钥、用户、作用域配置（提供商/通道/设置） | 平台 | DB |
| 技能 | 智能体 / 全局 | 文件系统（`skills/`、`agents/<id>/agent/skills/`） |
| 用户账户、计费 | 应用 | 你的应用（如 ChatClaw） |
| 输出文件 | 应用 | 你的应用 / S3 |

## 特性

### 大模型提供商

- OpenAI、Anthropic、Ollama、OpenRouter、Groq、DeepSeek、Mistral 及任何兼容 OpenAI 接口的 API
- **每智能体** 提供商 + 模型覆盖（agent-scope 遮蔽 user-scope 遮蔽 system）
- **从服务端拉取模型 ID** — 点击"Fetch from server"，免去手输模型名（系统/用户/智能体三层都支持）
- **自定义 `base_url`** — Replicate、ElevenLabs、Fish Audio 等所有工具提供商都支持，自托管或代理端点无需改代码
- **按对话者覆盖模型** — 让每个 IM 对话者在共享智能体上选自己的模型（opt-in）
- 提示缓存支持（RawAssistant 保留）

### 通道（IM 桥接）

- 每智能体可绑定：Telegram、Discord、Slack、**微信**、**飞书**、**LINE**
- 保存前验证 Token 有效性（`getMe`、`/users/@me`、`auth.test` 等）
- 会话按通道 + chatID 隔离——同一用户的 Telegram 线程和 Discord 线程互不干扰
- **按对话者的记忆和时区** — MEMORY.md / USER.md 按发送者隔离；提示词与 cron 调度遵循对话者时区
- **多气泡回复** — 长回复可拆分为多个 IM 气泡（每智能体三态：继承 / 开 / 关）
- **斜杠命令** — `/new`、`/whoami`、`/yes`、`/no`、`/ask`、`/auto`、`/yolo` 全通道通用
- **聊天内授权提示** — 智能体需走出工作区时，IM 通道弹出可点击的提示，用户可就地批准

### 工具与沙箱

内置工具（智能体作用域注册，经 `tools.Route` 分发）：

| 工具 | 用途 |
|---|---|
| `exec` / `bash_session` | Shell 执行——host 模式 CWD 默认为工作区；沙箱模式在容器内运行 |
| `read_file` / `write_file` / `list_dir` / `apply_patch` | 文件操作，强制工作区边界 |
| `web_fetch` / `web_search` | HTTP 抓取（防 SSRF）与搜索 |
| `memory_search` | 查询智能体的 MEMORY.md |
| `image_gen` | 图像生成（Replicate 与 OpenAI 兼容提供商） |
| `tts` | 文本转语音（ElevenLabs、Fish Audio 及兼容提供商） |
| `create_cron_job` | 每智能体定时任务（"每天 9 点提醒我"） |
| `delegate` / `subagent` | 把子任务交给另一个智能体 |
| `install_skill` | 从 ClawHub 或 GitHub 安装技能（任意 `github.com` 镜像经 `LUNUNDA_GH_PROXY` 配置，如 `https://ghfast.top/`） |
| `load_skill` / `find_skills` | 渐进式技能披露——技能按需加载 |
| `request_authorization` | 当操作需用户批准时由循环自动插入（详见"授权与安全"） |

**沙箱后端**（每智能体可配）：

- **Docker** — 同级容器模式，工作区以 bind mount 挂载到宿主。默认镜像 **`ghcr.io/lunaewaves/lununda-sandbox:latest`**，预装 Python 3、Node 22、Camoufox（防检测 Firefox）、git、curl 及常用 fetch/parse/preview 依赖。内置技能 `camoufox-cli` 第一轮对话即可使用，无需 pip/npm 安装。
- **E2B** — 云端沙箱，全托管隔离。
- **Boxlite** — 基于 REST 的远端沙箱，提供 snapshot/image 原语。

每个后端在 exec 前把工作区灌入沙箱，并在每次工具调用后把沙箱侧文件镜像回持久存储。技能目录与智能体身份文件同样同步。

**MCP 与插件**

- **MCP 服务支持** — 每智能体连接任意 Model Context Protocol 服务（stdio 或 HTTP 传输），其工具自动加入智能体的注册表。
- **插件系统** — JSON-RPC 子进程插件。插件可注册为工具提供商、通道适配器或 Hook 适配器。

### 授权与安全

Lununda Agent 借鉴现代编码智能体的对话式审批模型，并将其适配到多通道聊天场景。

**三档模式（按 session）：**

| 模式 | 行为 |
|---|---|
| `ask`（默认） | 工作区外的写/exec 或危险命令执行前，暂停并询问用户（4 选项：`/yes` `/no` `/auto` `/yolo`） |
| `auto` | 工作区内自由；工作区外**自动拒绝**（智能体看到拒绝后会改路线） |
| `yolo` | 除 hardline 底线外全部放行（用户自担风险） |

**三层强制执行**（按此顺序检查）：

1. **Hardline 底线** — 灾难性命令（`rm -rf /`、`mkfs`、fork bomb、关机……）在**任何**模式下都被拦截，包括 `yolo`。
2. **危险命令黑名单** — 高风险但可恢复的操作（`git reset --hard`、`git push --force`、`chmod -R 777`、`DROP TABLE`、`curl | sh`……）按当前模式处理。
3. **工作区边界** — 工作区外的文件*写* 与 *exec* 按模式处理；工作区外的*读* 允许（不改变状态）。

**Auth-as-tool 设计。** 当一个操作需要批准时，循环拦截原始 `tool_call` 并替换为真实的 `request_authorization` tool_call。这让对话历史保持连贯（无 orphan 工具结果、无合成消息），并且在 Web 聊天、IM 通道和 OpenAI 兼容 API 上行为一致。

```text
assistant: tool_calls: [request_authorization(reason: 写工作区外 → C:\Temp\x)]
tool:       "approved"
assistant: tool_calls: [write_file(C:\Temp\x)]
tool:       "written"
```

斜杠命令 `/yes` `/no` `/ask` `/auto` `/yolo` 仅作用于当前 session（不持久化）；
新 session 从全局默认（`agents.defaults.authMode`）启动。多租户 / 不可信场景
仍需搭配真正的容器沙箱（Docker / E2B / Boxlite）——`ask`/`auto`/`yolo` 是护栏，
不是安全边界。

### 技能

- **内置：** `code-runner`、`image-gen`、`data-analysis`、`translation`、`web-search`、`skill-creator`、`camoufox-cli`、`find-skills`
- **安装来源** [ClawHub](https://clawhub.ai)、[skills.sh](https://skills.sh) 或任意 GitHub 仓库。设置 `LUNUNDA_GH_PROXY` 为任意 `github.com` 镜像前缀（如 `https://ghfast.top/`），用于 GitHub 被屏蔽或限速的网络。
- **智能体私有或全局共享** — 私有技能位于 `agents/<id>/agent/skills/`
- **渐进式披露** — 技能正文按需加载，保持系统提示精简
- **对话式安装** — `install_skill` 工具允许智能体在对话中安装技能；新技能热加载到运行中的智能体

### 记忆

- **MEMORY.md** — 长期事实，由心跳自动更新
- **按对话者的 USER.md** — 按发送者身份隔离的对话者专属笔记
- **基于会话的上下文**，完整历史保留
- **思考/推理内容** 保留用于记忆提取
- **心跳驱动更新** — 智能体周期性回顾并修订自己的记忆

**跨会话语义召回** — 除了每会话上下文，Lununda 还把每段结束的对话
蒸馏成一条*对话摘要*（summary + keywords + 指向逐字原文的
`(session_key, seq_start, seq_end)` 指针），并建索引供跨所有会话召回：

- **触发** — 在上下文压缩、手动 `/compact`、会话结束（`/new`/`/reset`，
  IM **和** web 都生效）时提炼摘要。
- **提炼** — LLM 蒸馏该范围，用**对话原始语言**写摘要+关键词（中文对话
  用中文搜索可命中），并打 **importance(1–5)** 分。低分不丢弃——靠后续
  衰减自然沉底（见*软遗忘*）。
- **向量化** — 配置 embedding 服务后（system→user→agent 三级 scope），
  每条摘要存入时即时 embedding 到 `vec0`(SQLite)/`vector`(Postgres) 索引。
  后台安全网 loop 补齐漏向量的摘要；**强制重新向量化** 按需重建。
- **召回** — `memory_search` 每次查询走三阶段管道：
  1. LIKE 关键词召回（CJK 感知的 bigram 重排），按当前对话者隔离（多租户）。
  2. vec0/pgvector KNN 向量召回，fetch 后同样按对话者过滤。
  3. 可选 cross-encoder reranker（兼容 Jina/Cohere）→ top-K。
  `fetch_messages` 工具再按指针取逐字原文。
- **评分与强化** — 重排综合三因子 `importance × recency × access`。
  每次摘要被召回，`access_count` +1、近因时钟刷新（强化），所以常被
  想起的记忆更牢固、不被想起的软遗忘（排名下沉）——不做硬清除。

完整设计见 `docs/superpowers/plans/2026-06-18-conversation-summaries.md`。

### Goals（异步自主任务）

`goal` 系统让智能体在无需重新提示的情况下，跨多轮追逐一个多步目标。
一个 goal 捕获目标、预算与延续契约；下一次入站消息或调度 tick 时，
循环会从上次中断处继续。基于现有 ReAct 循环构建，无需额外运行时。

### Hook

两套互补的 Hook 系统让智能体在 LLM turn **之前** 响应消息：

- **Regex Hook** — 智能体级消息拦截。按模式匹配入站文本并内联触发工具调用；聊天 UI 显示 Hook 指示徽章，让用户看到 Hook 触发。用于关键词触发、KB 自动查询等拦截器。
- **Plugin Hook** — `chat.send` 及其他生命周期事件分发给 JSON-RPC 插件。每智能体 opt-in；插件接收完整消息信封，可回复、转换或否决。

### Wiki 与知识库

- **Wiki 生成器** — 一键从平台上的智能体、模型、技能和运行时配置生成结构化文档站点。Markdown 页面带交叉链接与 `vis-network` 图谱视图，与仪表盘同域名（`/wiki/`）提供。生成时显示实时进度（已完成/总数）；页面可删除，KB 源删除时级联清理。
- **知识库工具** — `knowledgebase_search` 和 `knowledgebase_ingest` 允许智能体查询和填充每个智能体的向量存储。支持纯文本、Markdown、PDF 或 ingest 时抓取的网页作为知识来源。
- **自动查询 Hook** — KB Hook 拦截每条用户消息，对知识库执行配置好的语义搜索，将最相关的结果注入系统提示词，智能体无需用户显式搜索即可给出领域上下文回答。每智能体独立配置：是否启用、自动模式（关键词触发或始终开启）、返回条数、可选的回复标识行（"根据 KB 文章 X…"）。
- **Wiki 缓存** — 知识库层与 Wiki 生成器同步，让面向智能体的搜索结果和面向人类的 Wiki 站点保持内容一致。

### Coding Runtime（项目模式）

专为编码智能体工作流设计的运行时：启动一个项目作用域的智能体预览，
挂载工作区，用与常规智能体相同的沙箱 + 工具链迭代代码，但配以项目感知的
UI 优化。在仪表盘的 **Projects** 板块呈现。

### 工作区隔离

- 文件按 `session_key`（而非 `chat_id`）命名空间隔离——IM `/new` 创建真正干净的工作区；同一通道线程的兄弟会话不再共享文件。
- 每智能体工作区子树 `agents/<id>/workspace/`——工作区路径由 `config.AgentWorkspaceDir` 统一计算，LocalFS 存储、沙箱 bind mount 与文件 handler 三者路径同源。
- `policy.json` 白名单——智能体自身子树下的额外可写路径（预置：`temp/`、`download/`、`data/`）。

### API

- OpenAI 兼容 `/v1/chat/completions`（流式）
- Web 聊天 `/api/chat/stream`（SSE）
- **智能体实时推送** `/api/chat/subscribe`（SSE）— 定时任务触发的回复、goal 续跑结果及其他异步输出无需刷新即可显示在打开的聊天面板
- 会话管理 `/api/chat/sessions`
- 智能体 CRUD `/api/agents`（`?all=true` 返回跨租户视图，仅管理员）
- 每智能体调度器 `/api/agents/{id}/cron`（list / toggle / delete）
- 提供商管理 `/api/config`
- 技能安装 `/api/skills/install`（ClawHub + GitHub）
- API 密钥管理 `/api/apikeys`（按用户；等级：admin / user / agent）
- 用户管理 `/api/users`（管理员）— 顶层 CRUD + 嵌套的
  `/api/users/{id}/apikeys` 与 `/api/users/{id}/agents`，用于管理员驱动配置。
  `agents` 端点接受 `forkFrom`，将现有智能体的身份（SOUL / IDENTITY / 技能 /
  模型默认）克隆到新用户的命名空间——"用户购买机器人"流程的主要构建块。
  每用户的 `agent_quota` 限制非管理员可自建智能体数（`-1` = 无限，
  `0` = 仅管理员配置）。
- App-user 供应 `POST /v1/users` — 第三方应用为终端用户铸造稳定的 lununda user_id，按 `(api_key, external_id)` 幂等。或在 `/v1/chat/completions` 上传 `user`（或 `X-Lununda-End-User` header）实现首次调用时懒铸造。

### 多租户 App-User 流程

使用 `type=user` 的 API 密钥配合 `X-Lununda-End-User` 请求头，无需预注册即可实现
每终端用户的数据隔离：

```
Authorization: Bearer <user-key-token>
X-Lununda-End-User: <your-app-user-id>
```

Lununda Agent 为每个唯一的 `(api_key_id, external_id)` 对自动创建稳定的内部用户。
会话、记忆和文件在终端用户之间完全隔离——在 `/v1/chat/completions`、
`/api/chat/stream`、`/api/chat/subscribe` 上传同一 header，即获得每用户智能体。

## Lununda Agent 的新功能

Lununda Agent 在 FastClaw 基础上多方向演进，亮点包括：

- **Lunae Waves 品牌视觉** — 统一调色板（深海浪底 Deep Abyss Blue、月面青 Moon Cyan、月光紫 Lunar Violet、月光银 Lunar Silver、月光白 Pale Moon）覆盖浅色与深色双主题，支持主题感知 Logo 自动切换。详见 [DESIGN.md](web/DESIGN.md)。
- **对话式授权** — 三档门控（`ask` / `auto` / `yolo`）配合 hardline 底线 + 危险命令黑名单，以真实 `request_authorization` tool_call 呈现。Web 聊天与 IM 体验一致。
- **Goals** — 基于 ReAct 循环 + 延续契约的异步、多轮自主任务。
- **Regex & Plugin Hook** — LLM turn 之前的智能体级消息拦截；JSON-RPC 插件 `chat.send` Hook。
- **Wiki + 知识库** — 一键文档站点、每智能体向量存储，以及把 KB 上下文注入每条用户消息的自动查询 Hook。
- **按对话者的模型覆盖 + 记忆 + 时区** — 让 IM 对话者个性化共享智能体而不泄漏状态。
- **多气泡 IM 回复** — 长回复拆分为多个 Telegram / Discord / 微信 / 飞书 气泡（每智能体三态）。
- **按会话隔离的工作区** — 工作区文件按 `session_key` 而非 `chat_id` 隔离，IM 中 `/new` 创建真正干净的工作区。
- **i18n** — 全站英文 / 简体中文本地化，覆盖每个页面与组件。
- **项目运行时** — 编码智能体预览，配以项目作用域工作区与仪表盘优化。
- **自动配置迁移** — 首次启动自动将 `~/.fastclaw` 迁移至 `~/.lununda`，包括数据库文件名转换（`fastclaw.db` → `lununda.db`）。现有用户无需任何手动操作。
- **完整 Favicon 资产集** — 全尺寸 favicon（16–512px PNG、多尺寸 ICO、apple-touch-icon、Android Chrome 图标、PWA manifest、og:image），覆盖各大平台和社交分享。

## 配置

启动配置为 **纯环境变量**。所有运行时可变配置（提供商、模型、通道、默认值、沙箱开关）
存储在数据库中，通过控制面板或 `lununda agents config` 编辑。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `LUNUNDA_HOME` | `~/.lununda` | SQLite 数据库和技能文件夹路径 |
| `LUNUNDA_PORT` | `18953` | 网关 HTTP 端口 |
| `LUNUNDA_BIND` | `loopback` | `loopback` (127.0.0.1) 或 `all` (0.0.0.0) |
| `LUNUNDA_STORAGE_TYPE` | `sqlite` | `sqlite` 或 `postgres` |
| `LUNUNDA_STORAGE_DSN` | 空 | PostgreSQL DSN，为空时使用 SQLite |
| `LUNUNDA_STORAGE_AUTO_MIGRATE` | `true` | 启动时自动执行 schema 迁移 |
| `LUNUNDA_SANDBOX_ENABLED` | 控制面板 | 覆盖设置中的沙箱开关 |
| `LUNUNDA_SANDBOX_BACKEND` | 控制面板 | `docker`、`e2b` 或 `boxlite` |
| `LUNUNDA_SANDBOX_IMAGE` | 控制面板 | Docker 镜像或 E2B / Boxlite 模板 ID |
| `LUNUNDA_OBJECT_STORE_*` | 未设置 | S3 兼容对象存储（多副本部署的技能/文件灌入） |
| `LUNUNDA_GH_PROXY` | 空 | 任意 `github.com` 镜像前缀（如 `https://ghfast.top/`），会拼到 `install_skill` tarball URL 前。空=直连 |
| `LUNUNDA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

未在此列表中的任何项——提供商、模型、默认模型、技能目录、通道、插件配置、调度器、
授权模式——均通过 Web UI（`http://localhost:18953`）或 CLI（`lununda agents config`、
`lununda provider`、`lununda skill`）在运行时配置。

## 部署

### 本地

```bash
lununda                    # 前台（^C 停止）
lununda daemon start       # 后台（日志在 ~/.lununda/daemon.log）
lununda daemon status
lununda daemon stop
lununda daemon install     # 注册为 launchd / systemd 服务
```

### 通过 CLI 管理智能体（`lununda agents …`）

`lununda agents` 子命令是仪表盘所用同一存储的薄封装。CLI 创建的智能体
会出现在 Web UI 中，反之亦然——每个 `LUNUNDA_HOME` 只有一份 lununda 部署。

```bash
# 一条命令从零到一个可聊的智能体。在全新安装上会创建 admin 用户
# （随机密码仅打印一次），并启动 gateway 守护进程（若未运行）。
lununda agents init alpha \
  --provider openai \
  --model openai/gpt-4o-mini \
  --api-key-env OPENAI_API_KEY

# 设置每智能体覆盖（模型、温度、沙箱……）。
lununda agents config alpha set temperature 0.7
lununda agents config alpha set sandbox.enabled true

# 上传智能体的身份文件。
lununda agents files put alpha SOUL.md ./SOUL.md
lununda agents files put alpha IDENTITY.md ./IDENTITY.md

# 查看。
lununda agents ls
lununda agents config alpha get
lununda agents files ls alpha

# 拆除。
lununda agents rm alpha
```

CLI 命令接受显示名或 `agt_…` id。CLI 直接打开操作者的存储
（`~/.lununda/lununda.db` 的 sqlite，或 `LUNUNDA_STORAGE_DSN` 指向的 DSN），
走与 gateway 相同的写入路径。它不要求 gateway 运行——但 `agents init`
会在后台拉起一个 gateway，让新智能体立即可在 `http://localhost:18953` 访问。
后续 CLI 写入通过 `SIGHUP` 让 gateway 热重载；Windows 上回退到
`lununda daemon restart`。

智能体文件名白名单：`SOUL.md`、`IDENTITY.md`、`USER.md`、`BOOTSTRAP.md`、
`MEMORY.md`、`HEARTBEAT.md`、`AGENTS.md`、`TOOLS.md`、`agent.json`。

### 通过 CLI 管理 API 密钥（`lununda apikey …`）

| 类型 | 作用域 | 用例 |
|------|-------|------|
| `admin` | 全平台访问，所有智能体 | 管理员自动化、CI/CD |
| `user` | 拥有者的智能体；支持 `X-Lununda-End-User` 实现 app_user 供应 | SaaS 代理层、多租户应用 |
| `agent` | 仅显式指定的智能体列表；不能创建智能体 | 机器人、单一用途集成 |

```bash
lununda apikey create --name "my-key" --type user [--owner <user-id>]
lununda apikey list [--owner <user-id>]
lununda apikey rotate --id <apikey-id>     # 旧 token 失效，新 token 仅显示一次
lununda apikey delete --id <apikey-id>
```

### Docker

```bash
cd deploy/docker
cp .env.example .env       # 按需修改默认值
docker compose -f docker-compose.ghcr.yml up -d
open http://localhost:18953
```

v1 前：默认使用 `dev` 标签（CI 从 `dev` 分支发布）。`latest` 留给 main 分支稳定版。

仓库提供五个 compose 文件——用 `-f` 叠加：

| 文件 | 用途 |
|---|---|
| `docker-compose.yml` | 本地源码构建 + Postgres（开发 / 贡献） |
| `docker-compose.ghcr.yml` | 从 GHCR 拉取预构建镜像 + SQLite（**自托管推荐**） |
| `docker-compose.ghcr-sandbox.yml` | GHCR + Docker 沙箱一步到位（host bind mount 路径与容器内一致） |
| `docker-compose.postgres.yml` | 覆盖：切换到 Postgres 后端 |
| `docker-compose.sandbox.yml` | 覆盖：启用 Docker 沙箱（叠加在 `ghcr.yml` 之上） |

完整运维指南（日志、升级、数据卷、同级容器模式、故障排查）见 [`deploy/docker/README.md`](deploy/docker/README.md)。

### Kubernetes

```yaml
env:
  - name: LUNUNDA_BIND
    value: "all"
  - name: LUNUNDA_STORAGE_TYPE
    value: "postgres"
  - name: LUNUNDA_STORAGE_DSN
    valueFrom:
      secretKeyRef:
        name: lununda-db
        key: dsn
  - name: LUNUNDA_OBJECT_STORE_ENDPOINT
    value: "s3.amazonaws.com"
  - name: LUNUNDA_OBJECT_STORE_BUCKET
    value: "lununda-skills"
```

无需挂载配置文件——启动配置纯环境变量。完整 manifest 见 `deploy/k8s/`，
Helm chart 见 `deploy/helm/`，多副本说明见 `deploy/multi-pod/`。

## 构建

```bash
make build                  # 构建前端 + Go 二进制 → bin/lununda
make install                # 安装到 $HOME/.local/bin（可用 PREFIX= 覆盖）
make release-local          # 交叉编译 darwin / linux / windows 到 dist/
```

Makefile 通过 `-ldflags` 把版本、commit 和构建日期烤进二进制。
CI 也使用这些 target——详见 `.github/workflows/`。

## 许可证

Lununda Agent 基于 [Lununda Agent Community License](LICENSE) **源代码开放**，
在 Apache License 2.0 基础上增加额外条款。

**简而言之：**
- ✅ 作为自有产品的后端商业使用
- ✅ 组织内部部署
- ❌ 为无关组织托管 Lununda Agent 即服务（需商业许可）
- ❌ 移除或修改控制面板中的 Lununda Agent 品牌标识

完整的 Apache 2.0 文本 reproduced 在 [LICENSE](LICENSE) 文件的附录中。
商业许可咨询请联系：support@thinkany.ai。
