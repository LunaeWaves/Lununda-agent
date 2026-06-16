<div align="center">

# Lununda Agent

基于 Go 的轻量级 AI Agent 运行时。

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev)

**单一二进制 · 任意大模型 · 多智能体 · 沙箱隔离 · 云端就绪**

[快速开始](#快速开始) · [架构](#架构) · [特性](#特性) · [Lununda Agent 的新功能](#lununda-agent-的新功能) · [许可证](#许可证)

</div>

> **Lununda Agent** 是 [FastClaw](https://github.com/fastclaw-ai/fastclaw) 的社区分支，
> 原始项目由 ThinkAny 团队创建并慷慨开源。我们感谢 FastClaw 作者构建了扎实的基础。
> Lununda Agent 继承了相同的核心理念——单一二进制、纯环境变量启动、Agent 工厂——
> 同时在品牌视觉、用户体验和工作区隔离方面持续迭代，由 [LunaeWaves](https://github.com/LunaeWaves) 组织维护。

---

## 什么是 Lununda Agent？

Lununda Agent 是一个 **智能体工厂**——它创建、管理和运行 AI 智能体。每个智能体拥有独立的
人格（SOUL.md）、记忆、技能和工具。Lununda Agent 处理大模型通信、工具执行、沙箱隔离和会话管理。

```bash
# 安装（将二进制文件放到 ~/.local/bin 并添加到 PATH）
curl -fsSL https://raw.githubusercontent.com/LunaeWaves/Lununda-agent/main/install.sh | bash
```

## 快速开始

### 1. 首次运行

```bash
lununda
# 前台模式启动，^C 停止。使用 `lununda daemon start` 后台运行，
# 或 `lununda daemon install` 注册为 launchd/systemd 服务。
```

### 2. 控制面板

打开 `http://localhost:18953`，使用管理员账号登录。

- **智能体** — 创建和管理智能体，每个智能体有独立的人格和模型
- **技能** — 安装共享技能
- **模型** — 配置大模型提供商（OpenAI, Anthropic, Ollama, OpenRouter 等）
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
- **模型** — 智能体专属的模型覆盖
- **通道** — 连接 IM 机器人（Telegram、Discord、Slack、微信、飞书、LINE），让终端用户在自有平台上对话
- **调度器** — 查看和管理智能体通过 `create_cron_job` 创建的定时任务
- **会话** — 对话历史

**分享。** 编辑对话框中提供"公开访问"开关（默认关闭）。开启后任何人可通过聊天 URL
与智能体对话；会话、记忆、USER.md 按对话者隔离，SOUL / IDENTITY / 技能共享自拥有者。

## 架构

```
~/.lununda/
  lununda.db                # SQLite 数据库 — 用户、智能体、会话、API 密钥、
                             #   配置、智能体文件均存储于此
  skills/                    # 共享技能（内置 + 用户安装）
  agents/
    <agentId>/agent/skills/  # 智能体私有技能（仅文件系统）
```

所有数据以数据库为真源（技能文件夹除外）。默认使用 SQLite；
设置 `LUNUNDA_STORAGE_DSN` 指向 PostgreSQL 以支持多节点部署。

**没有 `lununda.json` 配置文件。** 启动配置（端口、绑定地址、存储 DSN、沙箱后端）
来自 `LUNUNDA_*` 环境变量；所有用户可见配置（提供商、通道、设置、默认值）
存储在 `configs` 表中，通过控制面板或 `lununda agents config` 编辑。

## 特性

### 大模型提供商
- OpenAI、Anthropic、Ollama、OpenRouter、Groq、DeepSeek、Mistral 及任何兼容 OpenAI 接口的 API
- 每个智能体可独立覆盖模型提供商
- 提示缓存支持（RawAssistant 保留）

### 通道
- 每个智能体可绑定 Telegram / Discord / Slack / 微信 / 飞书 / LINE 机器人
- 保存前验证 Token 有效性
- 会话按通道 + chatID 隔离

### 工具与沙箱
- 内置：exec、read_file、write_file、list_dir、web_fetch、web_search、memory_search
- E2B 云端沙箱或 Docker 沙箱 — 自动同步技能 + 工作区
- 默认 Docker 镜像：**`ghcr.io/lunaewaves/lununda-sandbox:latest`** — 由本仓库的 `deploy/docker/sandbox/Dockerfile` 自动构建发布。预装 Python 3、Node 22、Camoufox（防检测 Firefox）、git、curl 及常用 fetch/parse/preview 依赖，让 `camoufox-cli` 等内置技能在第一轮对话即可使用，无需 pip/npm 安装。
- MCP 服务器支持
- 插件系统（JSON-RPC 子进程）

### 技能
- 内置技能：camoufox-cli、skill-creator、find-skills 等
- 智能体私有或全局共享

### 记忆
- MEMORY.md — 长期记忆，由心跳自动更新
- 基于会话的上下文，完整历史保留
- 思考/推理内容保留用于记忆提取

### Wiki 与知识库
- **Wiki 生成器** — 一键生成结构化的文档站点，涵盖平台上的智能体、模型、技能和运行时配置。Markdown 页面带交叉链接，与仪表盘同域名（`/wiki/`）提供。
- **知识库工具** — `kb_search` 和 `kb_ingest` 允许智能体查询和填充每个智能体的向量存储。支持纯文本、Markdown、PDF 或网页作为知识来源。
- **自动查询 Hook** — KB Hook 拦截每条用户消息，对知识库执行语义搜索，将最相关的结果注入系统提示词，智能体无需用户显式搜索即可给出领域上下文回答。每个智能体可独立配置：是否启用、自动模式（关键词触发或始终开启）、返回条数、可选的回复标识行（"根据 KB 文章 X…"）。
- **Wiki 缓存** — 知识库层可与 Wiki 生成器同步，让面向智能体的搜索结果和面向人类的 Wiki 站点保持内容一致。

### 工作区隔离
- 文件按 `session_key` 命名空间隔离，而非 `chat_id`
- IM `/new` 创建全新工作区，同一线程的多个会话不再互相可见文件
- 主题感知 Logo（深色主题自动使用浅色 Logo，浅色主题自动使用深色 Logo）

### API
- OpenAI 兼容 `/v1/chat/completions`（流式）
- Web 聊天 `/api/chat/stream`（SSE）
- 智能体实时推送 `/api/chat/subscribe`（SSE）— 定时任务等异步回复无需刷新即可显示
- 会话管理 `/api/chat/sessions`
- 智能体 CRUD `/api/agents`
- 每智能体调度器 `/api/agents/{id}/cron`
- 技能安装 `/api/skills/install`
- API 密钥管理 `/api/apikeys`
- 用户管理 `/api/users`（管理员）
- 应用用户供应 `POST /v1/users` — 第三方应用为终端用户创建稳定 ID

### 多租户 App-User 流程

使用 `type=user` 的 API 密钥配合 `X-Lununda-End-User` 请求头，无需预注册即可实现
每终端用户的数据隔离：

```
Authorization: Bearer <user-key-token>
X-Lununda-End-User: <your-app-user-id>
```

Lununda Agent 为每个唯一的 `(api_key_id, external_id)` 对自动创建稳定的内部用户。
会话、记忆和文件在终端用户之间完全隔离。

## Lununda Agent 的新功能

- **Lunae Waves 品牌视觉** — 统一的调色板（深海浪底 Deep Abyss Blue、月面青 Moon Cyan、
  月光紫 Lunar Violet、月光银 Lunar Silver、月光白 Pale Moon）覆盖浅色与深色双主题，
  支持主题感知 Logo 自动切换。详见 [DESIGN.md](web/DESIGN.md)。
- **按会话隔离的工作区** — 工作区文件按 `session_key` 而非 `chat_id` 隔离，IM 中
  `/new` 创建真正干净的工作区。同一通道线程的兄弟会话不再共享文件。
- **自动配置迁移** — 首次启动自动将 `~/.fastclaw` 迁移至 `~/.lununda`，
  包括数据库文件名转换（`fastclaw.db` → `lununda.db`）。现有用户无需任何手动操作。
- **完整 Favicon 资产集** — 全尺寸 favicon（16–512px PNG、多尺寸 ICO、
  apple-touch-icon、Android Chrome 图标、PWA manifest、og:image），
  覆盖各大平台和社交分享。

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
| `LUNUNDA_SANDBOX_BACKEND` | 控制面板 | `docker` 或 `e2b` |
| `LUNUNDA_SANDBOX_IMAGE` | 控制面板 | Docker 镜像或 E2B 模板 ID |
| `LUNUNDA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## Docker 部署

```bash
cd deploy/docker && ./start.sh
```

## 构建

```bash
make build                  # 构建前端 + Go 二进制 → bin/lununda
make install                # 安装到 $HOME/.local/bin
make release-local          # 交叉编译到 dist/
```

## 许可证

Lununda Agent 基于 [Lununda Agent Community License](LICENSE) 开放源代码，
在 Apache License 2.0 基础上增加额外条款。

**简而言之：**
- ✅ 作为自有产品的后端商业使用
- ✅ 组织内部部署
- ❌ 为无关组织托管 Lununda Agent 即服务（需商业许可）
- ❌ 移除或修改控制面板中的 Lununda Agent 品牌标识

商业许可咨询请联系 ThinkAny 团队：support@thinkany.ai。
