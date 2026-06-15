# 方案：Agent 工作目录与执行授权

## 背景

三个相互关联的问题暴露出来，本方案一并解决：

1. **通过对话安装 skill 不生效**：`install_skill` 工具从未被注册（`internal/agent/tools/skill_install.go` 的 `RegisterSkillInstall` 是死代码，全项目无调用点）。agent 只能用 `exec`（bash）`git clone`，把 skill 放到了 sandbox 相对路径 `./skills/`，而非 FastClaw 的 skill 加载目录（`~/.fastclaw/skills/` 或 `~/.fastclaw/agents/<id>/agent/skills/`），SkillsLoader 根本不读那里。
2. **exec 相对路径落到程序根目录**：host 模式 exec 没设 `cmd.Dir`（`exec.go:195`），命令里的 `./skills/` 由进程 CWD 解析，而 FastClaw 从 `D:\codes\fastclaw` 启动，于是产物污染了代码目录。
3. **目录布局割裂**：每个 agent 的数据散在两处——`~/.fastclaw/agents/<id>/agent/`（SOUL.md、skills、memory）和 `~/.fastclaw/workspaces/<id>/`（工作产物）。用户希望收敛到 `~/.fastclaw/agents/<id>/` 一棵子树。

## 目标

- agent 通过对话安装 skill 能真正落地并生效
- exec 默认在 workspace 内工作，相对路径行为符合预期
- agent 文件系统按 agent 收敛到一棵子树
- 对 workspace 外的操作（尤其写/执行）引入授权机制：白名单 + 用户确认 + 三档模式（ask/auto/yolo）

## 设计决策

### D1. install_skill 工具注册（治本）

把 `RegisterSkillInstall` 真正接进 agent 工具注册流程，装到 agent 私有目录 `~/.fastclaw/agents/<id>/agent/skills/`，`onReload = ag.ReloadWorkspaceFiles` 触发热加载。

skill 安装走 Go 代码，路径硬编码，不经 shell——这是 agent"安装 skill"的正解，不该靠 exec 手动 clone。

### D2. host exec 注入 workspace 为 CWD

exec.go host 路径（`:195`）设 `cmd.Dir = r.userRoot`（registry 已持有 workspace 绝对路径）。

让相对路径默认落在 workspace，行为与 sandbox 模式（`sb.Exec(execCtx, command, "/workspace")`）一致。

**局限声明**：这是"让相对路径行为正确"，**不是**安全沙箱。exec 是任意 shell 命令字符串，host 上无法可靠约束路径（命令可 `cd`、变量、子 shell 逃逸）。真正的隔离靠容器（docker/e2b）——未配置时是 path-sandbox host 模式。

### D3. GitHub 下载走 ghfast 代理（网络）

`InstallFromGitHubRepo` 改用 `github.com/.../archive/...` 路径（tarball 内容与 codeload 一致，但 ghfast.top 这类加速器**只代理 github.com，拒绝 codeload.github.com**——实测 codeload 返回 403 "Invalid input"）。

新增 `FASTCLAW_GH_PROXY` 环境变量，设置后所有 github archive 下载自动加代理前缀。默认空（海外直连可用）；国内部署设 `https://ghfast.top/`。

### D4. 目录布局重构（agent 子树收敛）

改 `AgentWorkspaceDir(agentID)` 返回 `~/.fastclaw/agents/<id>/workspace`（原 `~/.fastclaw/workspaces/<id>/`）。

调用面极小：`AgentWorkspaceDir` 全项目仅在 `config.go:750` 使用，改动集中、风险可控。

**迁移策略**：仅改代码（新 agent 用新路径），**用户手动迁移历史数据**（确认点 2）。不写自动迁移逻辑，避免数据搬运出错。

### D5. 授权系统（白名单 + 三档模式 + IM 确认）

**模式（类似 Claude Code）**：
- **ask**：workspace 外的写/执行类操作 → 暂停等用户确认（默认）
- **auto**：workspace 内自由，workspace 外自动拒绝（不给 LLM 授权通过的幻觉，直接返回"被拒"让 agent 改用 workspace 内方案）
- **yolo**：全部放行（用户自担风险）

**白名单**：
- 存 `~/.fastclaw/agents/<id>/policy.json`
- 只能加该 agent 自己目录（`~/.fastclaw/agents/<id>/`）下的子目录——满足"不能影响其他 agent"约束
- 预置 `temp`/`download`/`data` 子目录入白名单，并写进 agent 上下文告知用途

**分级**：
- file 工具（write_file/edit_file/delete）：严格可靠。workspace 外写 → 按模式处理；读放宽（`resolvePathSandboxed` 已有基础）
- exec 工具：启发式。静态扫描命令文本里的绝对路径/`..`/`~/`/重定向/写类命令（`rm`/`mv`/`>`/`git clone`/`curl -o`），命中 workspace 外目标 → 按模式处理。可被绕过（变量、子 shell），不保证完美

**授权等待语义（确认点 4 选定）**：
- 复用 steer 机制（loop.go `DrainSteer`/`appendSteer`）——用户可中途发消息，循环在工具轮次间折入，授权不需要从零造暂停/恢复
- 发出授权请求 → 会话写一条"待确认"特殊消息 + 启动 timer
- 用户回复"允许" → 取消 timer，放行
- **超时（默认 10 分钟，agent 设置可调）→ 自动生成"拒绝"的 tool_result 喂回 LLM**，agent 自行决定下一步
- 超时被拒后用户又回复"允许"：当作普通 steer 处理，**不保证重试**——用户需主动重新发起任务（确认点 4）

**web 快捷选项**：聊天框在授权请求下方提供"允许 / 拒绝 / 切到 yolo"点选，点击自动填充到输入框（类似 Claude Code 的选项式确认），用户也可手打。

## Considered Options（为何这么选）

- **install_skill 用 exec 替代**：exec clone 到 sandbox 相对路径，路径不可控、产物不被 loader 识别。结构化工具（Go 代码硬编码路径）才是正解。
- **exec 安全沙箱靠静态分析命令**：shell 命令是任意字符串，无法可靠静态约束。只能启发式 + 分级，真正隔离靠容器。诚实声明局限，不假装安全。
- **目录硬迁移（自动搬运）vs 仅改代码**：选仅改代码 + 手动迁移。自动搬运历史 workspace 有数据丢失风险，且调用面小到不需要兼容层。
- **授权无限挂起 vs 超时拒绝**：选超时拒绝。agent loop 是事件驱动 + steer 折入，"无限挂起"与架构冲突（需改会话语义），超时拒绝顺势且自愈。
- **白名单存 DB vs 文件**：选文件（`policy.json`），天然按 agent 隔离，满足"只加自己目录"约束，也便于用户查看/编辑。

## Consequences

- install_skill 注册后，agent 可对话安装 skill 并即时生效；装到 agent 私有目录，web 技能设置页（读私有目录）可见。
- host exec 相对路径不再污染程序目录，默认落 workspace。
- ghfast 代理让国内网络也能装 github skill（需配 `FASTCLAW_GH_PROXY`）。
- 目录重构后，每个 agent 一棵子树 `~/.fastclaw/agents/<id>/`，含 `agent/`（身份/skills/memory）+ `workspace/`（工作产物）+ `policy.json`（白名单）+ 预置子目录。
- 授权系统让 workspace 外操作可控，但 exec 启发式**不是**安全保证——多租户/不可信场景仍需配置 docker/e2b 容器隔离。
- yolo 模式下所有操作放行，用户需明确知晓风险。

## 实施阶段

每阶段独立可测、独立 commit。

### 阶段 1（低风险，立即受益）
- D1：loop.go 注册 `RegisterSkillInstall`（已改）
- D2：exec.go host 路径设 `cmd.Dir`（待改）
- D3：github.go archive 路径 + `FASTCLAW_GH_PROXY`（已改）
- 验证：编译重启 → 微信对话安装 huashu-design → 落到 `~/.fastclaw/agents/<id>/agent/skills/huashu-design/` 且技能生效

### 阶段 2（中风险）
- D4：改 `AgentWorkspaceDir` 返回 `agents/<id>/workspace`
- 用户手动迁移历史数据
- 验证：新 agent 的工作产物落在新路径；旧 agent（迁移后）正常

### 阶段 3（较大）
- D5：policy.json + 三档模式 + IM 确认（复用 steer）+ web 快捷选项 + 超时拒绝（默认 10 分钟）
- file 工具分级接入；exec 启发式扫描接入
- 验证：ask 模式下越界操作触发确认；auto 模式自动拒；yolo 全放行；超时自愈

## 开放问题（实施时再定）
- exec 启发式的写类命令黑名单具体清单（`rm`/`mv`/`>`/`git clone`/`curl -o`/`tee`/`dd`/...）
- 预置目录（temp/download/data）的语义说明文案
- 授权请求在 IM 端的呈现格式（文字 + 选项卡片）
