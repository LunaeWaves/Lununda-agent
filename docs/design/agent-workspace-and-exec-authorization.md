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

目标：每个 agent 的数据收敛到一棵子树
`~/.fastclaw/agents/<id>/`，含 `agent/`（身份/skills/memory）+ `workspace/`（工作产物）。workspace 从 `~/.fastclaw/workspaces/<id>/` 迁到 `~/.fastclaw/agents/<id>/workspace/`。

**方案选择：方案 iii（LocalFS 用回调定位每 agent 的 workspace 根）**

经调研，路径硬编码分散在 config / LocalFS / 三个 sandbox backend / handlers 多处。采用方案 iii：所有路径计算统一收敛到 `config.AgentWorkspaceDir(agentID)`，确保 workspace.Store（LocalFS）、sandbox 挂载（docker/e2b/boxlite）、handler 三处路径**同源**，消除"一处写、一处挂"的不一致隐患（sandbox 模式文件丢失的根因）。

**调研关键结论**：
- `SandboxPool` + `homeFromWorkspace`（pool.go）是**死代码**——`NewPool()` 全项目无调用，当前实际用的是 `DockerExecutorPool`/`E2BExecutorPool`/`BoxliteExecutorPool`（经 `LifecyclePool` 装饰）。重构时顺手删除。
- `skillDirsForAgent(home, agentID)`（活代码）直接用 home 拼 `agents/<id>/agent/skills` + `skills`，**不依赖 workspace 路径格式**——workspace 改位置，skill 挂载完全不受影响。
- 所有 sandbox backend 的 home 都来自 `buildSystemSandboxPool`（userspace.go:100）的 `config.HomeDir()`。
- `LocalFS.Root()` 方法全项目零调用（死方法），可安全删除。

**改动清单**：
1. `config.go` `AgentWorkspaceDir` → 返回 `~/.fastclaw/agents/<id>/workspace`
2. `localfs.go` LocalFS 用回调 `agentWorkspaceRoot(agentID)`（默认 `config.AgentWorkspaceDir`）定位每 agent 根；scopeDir 改用回调；删 `Root()` 死方法
3. `gateway.go:253` `NewLocalFS` 调用适配（去掉 workspaces root，改工厂）
4. `docker_executor.go:262` / `e2b_executor.go` / `boxlite_executor.go`：路径改用 `config.AgentWorkspaceDir(agentID)` + projects/sessions 子目录
5. `handlers.go:1534` / `handlers_agents.go:1331`：改调 `config.AgentWorkspaceDir`
6. `pool.go`：删死代码（`SandboxPool` + `homeFromWorkspace`）

**不变量**：workspace.Store 写路径 == sandbox 挂载 host 路径 == handler 读路径，三者同源调 `AgentWorkspaceDir`。sandbox 模式下容器内 `/workspace` ↔ host `agents/<id>/workspace/...` 必须一致。

**迁移策略**：仅改代码（新 agent 用新路径），**用户手动迁移历史数据**（确认点 2）。不写自动迁移逻辑，避免数据搬运出错。

**风险点**：
- sandbox 挂载改动（#4）在 host 模式测不出，需配 docker 端到端验证容器内 `/workspace` 挂对。改动是机械的路径拼接，风险可控。
- e2b/boxlite 是 `RemoteWorkspace`（非 bind-mount），靠 workspace.Store hydrate/flush 同步——只要 LocalFS 和 hydrate 用同一个 scopeDir 就一致（方案 iii 天然保证）。

### D5. 授权系统（白名单 + 三档模式 + IM 确认）

**模式（类似 Claude Code）**：
- **ask**：workspace 外的写/执行类操作 → 暂停等用户确认（**默认**）
- **auto**：workspace 内自由，workspace 外自动拒绝（不给 LLM 授权通过的幻觉，直接返回"被拒"让 agent 改用 workspace 内方案）
- **yolo**：全部放行（用户自担风险）

**斜杠命令**（复用 `agent.handleSlashCommand`，加进现有 switch）：
- `/yes` — 批准当前 session 挂起的授权请求
- `/no` — 拒绝当前 session 挂起的授权请求
- `/ask` `/auto` `/yolo` — 切换**当前 session** 的模式

**模式 session 级语义**：
- 新 session 启动用全局默认（`agents.defaults.authMode`，默认 `ask`）
- `/ask` `/auto` `/yolo` 只改当前 session，**不持久化、不跨 session**
- session 结束恢复默认

**白名单**：
- 存 `~/.fastclaw/agents/<id>/policy.json`，字段 `allowWrite []string`（相对 agent 根的路径前缀）
- 只能加该 agent 自己目录（`~/.fastclaw/agents/<id>/`）下的子目录——满足"不能影响其他 agent"约束
- 预置 `temp`/`download`/`data` 子目录入白名单，并写进 agent 上下文告知用途
- 加载时合并预置目录 + policy.json 配置项

**分级**（确认点 1/2）：
- file 工具（write_file/edit_file/delete）：workspace 外**写** → 按模式处理；workspace 外**读** → **直接允许**（读不改变状态，风险低，不打扰用户）
- exec 工具：**一律**——workspace 内自由，workspace 外（任何命令，不区分读写）→ 按模式处理。理由：shell 命令是任意字符串，静态区分读/写不可靠（`cat` 能读、`cp` 能写、管道组合更难判定），一律授权比启发式更安全且语义清晰

**三层安全模型**（参考 hermes-agent approval.py，确认点：hardline 底线 + 危险黑名单）：
1. **hardline 底线**：灾难性命令（`rm -rf /`、`mkfs`、`dd of=/dev/sd*`、fork bomb、关机/重启等）**任何模式都拦截，yolo 也不能绕过**。理由：yolo 是信任 agent 操作文件/服务，不是信任它清盘/关机——这是 yolo 之下的地板。
2. **危险命令黑名单**（dangerous patterns）：高风险但可恢复的操作（`git reset --hard`、`git push --force`、`chmod -R 777`、`rm -rf`、`DROP TABLE`、`curl|sh` 等）→ 按模式处理（ask 询问 / auto 拒 / yolo 放行）。作为 workspace 边界的**补充层**——workspace 内的危险命令也拦。
3. **workspace 边界**：位置维度（workspace 内自由，外则按模式）。

检测顺序：hardline → dangerous → workspace 边界。hardline 命中直接拒绝（不可授权）；其余按模式 + 单次授权。

**拒绝消息防绕过措辞**：所有拒绝（hardline / auto 拒 / /no / 超时）的 tool_result 明确告诉 LLM「用户未授权，不要重试、不要换措辞、不要换工具绕过，停下等用户」。防 prompt injection 换方式绕过（参考 hermes 的 "silence is not consent" 契约）。

**授权流程（对话式，BeforeToolCall hook 拦截）**：
授权不是一个阻塞子流程，而是**两个 turn 之间的对话**，复用现有 hook 机制：

1. LLM 发出 tool_call → `BeforeToolCall` hook 检查是否越界（workspace 外 + 非白名单 + 写/执行类）
2. 越界且需要授权（ask 模式）→ hook 拦截，**不执行工具**，发消息「⚠️ 需要执行 <描述>，回复 /yes 继续，/no 取消」，当前 turn 结束
3. 用户回复：
   - `/yes` → session 标记"下一次 tool_call 单次授权"，LLM 在新 turn 重新发 tool_call（或系统重新注入），hook 消耗授权放行
   - `/no` → 清 pending，LLM 收到"被拒"换方案
   - 其它内容 → A 严格模式不识别为授权，作为普通消息进对话历史，LLM 自行应对
4. 不回复 → turn 自然结束，无副作用（**不需要 timer/超时**）

**单次授权语义**（确认点：防授权蔓延）：
- `/yes` 只对**紧接下来的那一次 tool_call** 生效，消耗即失效
- 下一个 tool_call（即使参数完全相同）越界 → 重新拦截询问
- 天然防"LLM 重发 → hook 再拦"的死循环，也防"一次授权永久放行同类操作"的安全漏洞
- **不做 `/yes always`**——"相同操作自动同意"的判定（参数匹配/模糊匹配）复杂度高且易误放行，收益不明确，砍掉

**统一授权模型（判定集中、工具无感）**：
授权判定**全部集中在 loop 层**（`filterAuthorizedCalls` + `authGate`），工具 callback **不重复判定逻辑**。每个 tool_call 带 `approved/denied/waiting` 标签（loop 设定，一次性）：

- `denied` / `waiting` → **loop 层直接不执行**，生成拒绝/等待 tool_result，工具根本不被调用
- `approved` → 进入工具执行，loop 同时标记该 call 的 `sandboxBypass=true`

工具内部（file 的 `resolvePathSandboxed`、exec 的 cmd.Dir 边界等）**只读一个 flag**：`sandboxBypass` 为 true 则放开 workspace 边界限制（hardline 路径/命令仍拦）。工具不感知 hardline/dangerous/mode——那些全在 loop 层决定。

**为什么这样**：避免"每加一个带路径检查的工具就要再打通一处授权"的循环重复。判定逻辑单一来源（loop），工具只读一个 bool。`/yes` 只改 loop 层状态，全链路自动生效。后续任何工具只要读 `sandboxBypass` flag 就自动支持授权，零额外改动。

flag 传递：通过 registry 的 per-call 标记（按 toolCallID），loop 在执行前设置，工具执行时读取、执行后清除。

**web 快捷选项**：聊天框在授权请求下方提供"允许 / 拒绝"点选，点击自动填充 `/yes` / `/no` 到输入框，用户也可手打。

## Considered Options（为何这么选）

- **install_skill 用 exec 替代**：exec clone 到 sandbox 相对路径，路径不可控、产物不被 loader 识别。结构化工具（Go 代码硬编码路径）才是正解。
- **exec 安全沙箱靠静态分析命令**：shell 命令是任意字符串，无法可靠静态约束。只能启发式 + 分级，真正隔离靠容器。诚实声明局限，不假装安全。
- **目录硬迁移（自动搬运）vs 仅改代码**：选仅改代码 + 手动迁移。自动搬运历史 workspace 有数据丢失风险，且调用面小到不需要兼容层。
- **授权阻塞回调 vs 对话式 hook 拦截**：选后者。在工具 callback 里阻塞等 timer/steer 要改 agent loop 的会话语义、引入 timer 超时等复杂度。对话式方案——BeforeToolCall hook 拦截越界工具、发询问、turn 结束、用户 /yes 后下个 turn 重新执行——复用现有 turn + hook 机制，无 timer、无阻塞、无 pending 状态机，且用户不回就自然结束（无超时问题）。
- **`/yes` 单次 vs `/yes always` 同类放行**：选单次。`always` 需要"同类操作"判定（参数精确匹配？模糊？工具+路径前缀？），复杂且易误放行，授权蔓延风险高。单次 `/yes` 只对下一个 tool_call 生效，安全且实现简单，防循环/防蔓延天然成立。
- **用户回复识别（严格 vs 模糊 vs 交 LLM）**：选严格（A）——只认 `/yes` `/no`，其它当普通消息进对话。无歧义、默认拒绝（安全）、实现最简；用户自然回复由 LLM 在对话中自行理解应对。
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

### 阶段 1（低风险，立即受益）— ✅ 已完成
- D1：loop.go 注册 `RegisterSkillInstall`
- D2：exec.go host 路径设 `cmd.Dir` = workspace
- D3：github.go archive 路径 + `FASTCLAW_GH_PROXY`
- 验证：对话安装 huashu-design → 落到 `agents/<id>/agent/skills/huashu-design/` 且技能生效 ✓

### 阶段 2（中风险）— ✅ 已完成
- D4：改 `AgentWorkspaceDir` 返回 `agents/<id>/workspace`（方案 iii，路径同源）
- 验证：write_file / exec 相对路径 / 文件 API 全部落新路径 ✓

### 阶段 3（较大）
- D5 批 A（后端授权核心）：
  - slash.go 加 `/yes` `/no` `/ask` `/auto` `/yolo`
  - Session 加 `authMode` 字段（session 级，默认 ask）
  - policy.json 白名单加载（预置 temp/download/data）
  - file 工具：workspace 外写 → 按 mode 处理（读直接允许）
  - exec 工具：workspace 外一律 → 按 mode 处理（不区分读写）
  - ask 模式授权流程：复用 steer + 超时（默认 10 分钟）拒绝
- D5 批 B（前端）：web 聊天框授权请求下显示 `/yes` `/no` 快捷按钮
- 验证：ask 越界触发确认；auto 自动拒；yolo 全放行；/yes 放行；超时自愈

## 开放问题（实施时再定）
- 预置目录（temp/download/data）的语义说明文案（写进 agent 上下文）
- 授权请求消息的具体文案格式
