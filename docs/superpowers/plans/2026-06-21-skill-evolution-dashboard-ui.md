# 技能演进 Dashboard UI —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **前端任务：完成后必须起 dev server 在浏览器实测**（见 Task 5）。

**Goal:** 在现有 `agent → 技能管理` 页加：上方控件（自动迭代升级开关 + IM 通知选择）、可升级列表（pending 提案：来源→新技能，per-source 保留勾选，接受/拒绝）、归档视图（列表 + 永久删除）。调 Plan 5 的 API。

**Architecture:** 改 `web/src/app/agents/[id]/skills/page.tsx`（加 state + fetch + 三个 UI 区块）+ `web/src/lib/api.ts`（加 API 函数）。沿用页面现有模式：`"use client"`、shadcn/ui、`useT()` i18n、`Promise.all` fetch、`getConfig()` 读配置。配置（开关/通知）经现有 config 读写 API 存 `MemoryCfg.SkillEvolution`。

**Tech Stack:** Next.js 16（App Router）、React 19、Tailwind 4、shadcn/ui、lucide-react、pnpm。命令：`pnpm dev`（浏览器测）、`pnpm build`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D5 UX、D6 归档视图）。
**依赖 plan：** Plan 5（API：`/api/agents/{id}/skill-proposals` 等）、Plan 6（`SkillEvolutionCfg`）。

---

## Integration points（执行时确认）

1. **配置读写 API**：页面现用 `getConfig()` 读 config blob（含 `memory`）。`SkillEvolutionCfg` 在 `cfg.memory.skillEvolution`。**保存**用现有 config-save 端点（`ConfigureSkillDialog` 存 skill entries 用的那个——确认 `api.ts` 里的 save 函数名，如 `saveConfig` / `setAgentConfig`，复用它写 `memory.skillEvolution`）。
2. **IM 渠道选项来源**：通知选择器要列"该 agent 已绑定的 IM 渠道"。确认有无现成 API 列出 agent 绑定渠道；无则先用文本输入（channel/chatID/accountID），UI 选单留 Plan 7 后续打磨。

---

## File Structure

- **Modify** `web/src/lib/api.ts`：加提案/归档/配置的 API 函数 + 类型。
- **Modify** `web/src/app/agents/[id]/skills/page.tsx`：加 state/fetch + 三区块 UI。

---

### Task 1: api.ts —— API 函数 + 类型

**Files:** Modify `web/src/lib/api.ts`（`getAgentSkills` 旁，约 `:1567`）

- [ ] **Step 1: 加类型 + 函数**

在 `api.ts` 加（仿 `getAgentSkills`/`deleteAgentSkill` 的 `apiFetch` 模式）：

```ts
// ---- 技能演进（curator）----

export interface SkillProposal {
  ID: string;
  AgentID: string;
  Sources: string[];
  TargetName: string;
  TargetContent: string;
  Evidence: string;
  Recommendation: string;
  Status: string; // pending/accepted/rejected/applied
  CreatedAt: string;
}

export interface ArchivedSkill {
  Name: string;
  ArchivedAt: string;
  Path: string;
}

export interface SkillEvolutionNotifyCfg {
  enabled: boolean;
  channel?: string;
  chatID?: string;
  accountID?: string;
}
export interface SkillEvolutionCfg {
  enabled: boolean;
  interval?: number; // 纳秒（time.Duration JSON）
  model?: string;
  notify?: SkillEvolutionNotifyCfg;
}

export async function getAgentSkillProposals(agentId: string): Promise<SkillProposal[]> {
  const res = await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skill-proposals`);
  const data = await res.json();
  return data.proposals || [];
}

export async function acceptSkillProposal(agentId: string, pid: string, keep: string[]): Promise<void> {
  await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skill-proposals/${encodeURIComponent(pid)}/accept`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ keep }),
  });
}

export async function rejectSkillProposal(agentId: string, pid: string): Promise<void> {
  await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skill-proposals/${encodeURIComponent(pid)}/reject`, {
    method: "POST",
  });
}

export async function getArchivedSkills(agentId: string): Promise<ArchivedSkill[]> {
  const res = await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skills/archived`);
  const data = await res.json();
  return data.archived || [];
}

export async function deleteArchivedSkill(agentId: string, name: string, at: string): Promise<void> {
  await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skills/archived/${encodeURIComponent(name)}?at=${encodeURIComponent(at)}`, {
    method: "DELETE",
  });
}
```

- [ ] **Step 2: 构建（类型检查）**

Run: `cd web && pnpm build`（或 `pnpm tsc --noEmit`）
Expected: 无类型错误（函数未被调用时不报未使用——若报，先在 Task 2 调用）。

- [ ] **Step 3: 提交**

```bash
git add web/src/lib/api.ts
git commit -m "feat(web): 技能演进 API 客户端（提案/归档 CRUD）"
```

---

### Task 2: 页面 state + fetch + 上方控件（开关 + 通知）

**Files:** Modify `web/src/app/agents/[id]/skills/page.tsx`

- [ ] **Step 1: 加 import + state**

import 加（第 39-48 行 `from "@/lib/api"` 块里）：

```ts
  getAgentSkillProposals,
  acceptSkillProposal,
  rejectSkillProposal,
  getArchivedSkills,
  deleteArchivedSkill,
  type SkillProposal,
  type ArchivedSkill,
  type SkillEvolutionCfg,
```

组件内 state（`uploadOpen` 等 state 旁，约 `:75`）加：

```ts
  const [proposals, setProposals] = useState<SkillProposal[]>([]);
  const [archived, setArchived] = useState<ArchivedSkill[]>([]);
  const [evoCfg, setEvoCfg] = useState<SkillEvolutionCfg>({ enabled: false });
  const [evoSaving, setEvoSaving] = useState(false);
  // 每个提案的 per-source 保留勾选：{ [proposalID]: { [sourceName]: bool } }
  const [keepMap, setKeepMap] = useState<Record<string, Record<string, boolean>>>({});
```

- [ ] **Step 2: 扩展 fetch（ proposals + archived + evoCfg）**

改 `fetchSkills`（`:77`）的 `Promise.all`，并入新请求；或新加 `fetchEvolution`。推荐并入：

```ts
  const fetchSkills = useCallback(() => {
    setLoading(true);
    Promise.all([
      getAgentSkills(agentId).catch(() => [] as SkillInfo[]),
      getConfig().catch(() => null),
      getAgentSkillProposals(agentId).catch(() => [] as SkillProposal[]),
      getArchivedSkills(agentId).catch(() => [] as ArchivedSkill[]),
    ])
      .then(([list, cfg, props, arch]) => {
        setSkills(list || []);
        // ... 既有 skillEntries 合并逻辑不变 ...
        setProposals(props || []);
        setArchived(arch || []);
        setEvoCfg((cfg?.memory?.skillEvolution as SkillEvolutionCfg) || { enabled: false });
        // 默认勾选：每个来源默认"保留"（最安全）
        const km: Record<string, Record<string, boolean>> = {};
        for (const p of props || []) {
          km[p.ID] = Object.fromEntries((p.Sources || []).map((s) => [s, true]));
        }
        setKeepMap(km);
      })
      .finally(() => setLoading(false));
  }, [agentId]);
```

- [ ] **Step 3: 加保存配置 handler + 上方控件 UI**

handler：

```ts
  const saveEvoCfg = async (next: SkillEvolutionCfg) => {
    setEvoCfg(next);
    setEvoSaving(true);
    try {
      // 用现有 config-save 端点写 memory.skillEvolution（integration note 1）
      await saveAgentMemoryConfig(agentId, { skillEvolution: next }); // 见下：复用/封装现有 save
    } finally {
      setEvoSaving(false);
    }
  };
```

> `saveAgentMemoryConfig(agentId, {skillEvolution})`：封装现有 config-save（写 `memory.skillEvolution`）。若 `api.ts` 已有 `saveConfig`/`setAgentConfig`，直接调它传 `{ memory: { skillEvolution: next } }`；确认现有函数名后替换。

在技能列表**上方**（页面 header/工具栏区，渲染 `skills` grid 之前）插入控件：

```tsx
{/* 技能迭代升级控件 */}
<div className="mb-4 rounded-lg border p-4 flex flex-wrap items-center gap-4">
  <label className="flex items-center gap-2 text-sm font-medium">
    <input
      type="checkbox"
      checked={evoCfg.enabled}
      onChange={(e) => saveEvoCfg({ ...evoCfg, enabled: e.target.checked })}
      disabled={evoSaving}
    />
    {t("skills.evolution.autoUpgrade")}
  </label>
  <label className="flex items-center gap-2 text-sm">
    {t("skills.evolution.notify")}
    <input
      type="checkbox"
      checked={!!evoCfg.notify?.enabled}
      onChange={(e) => saveEvoCfg({ ...evoCfg, notify: { ...evoCfg.notify, enabled: e.target.checked } })}
    />
    <Input
      placeholder="channel"
      className="w-28"
      value={evoCfg.notify?.channel || ""}
      onChange={(e) => saveEvoCfg({ ...evoCfg, notify: { ...evoCfg.notify, enabled: true, channel: e.target.value } })}
    />
    <Input
      placeholder="chatID"
      className="w-28"
      value={evoCfg.notify?.chatID || ""}
      onChange={(e) => saveEvoCfg({ ...evoCfg, notify: { ...evoCfg.notify, enabled: true, chatID: e.target.value } })}
    />
  </label>
  {evoSaving && <Loader2 className="h-4 w-4 animate-spin" />}
</div>
```

> i18n key `skills.evolution.autoUpgrade`/`.notify` 加到 i18n 文件（参考现有 `t("skills.*")` 用法）。channel/chatID 文本输入是 v1（integration note 2：渠道选单后续打磨）。

- [ ] **Step 4: 构建 + 提交**

Run: `cd web && pnpm build`

```bash
git add web/src/app/agents/[id]/skills/page.tsx web/src/lib/api.ts
git commit -m "feat(web): 技能演进上方控件（自动升级开关 + 通知配置）"
```

---

### Task 3: 可升级列表（pending 提案：保留勾选 + 接受/拒绝）

**Files:** Modify `web/src/app/agents/[id]/skills/page.tsx`

- [ ] **Step 1: 加 handler + 列表 UI**

handler：

```ts
  const handleAccept = async (p: SkillProposal) => {
    const keep = Object.entries(keepMap[p.ID] || {})
      .filter(([, v]) => v)
      .map(([k]) => k);
    await acceptSkillProposal(agentId, p.ID, keep);
    fetchSkills();
  };
  const handleReject = async (p: SkillProposal) => {
    await rejectSkillProposal(agentId, p.ID);
    fetchSkills();
  };
```

在控件区块**下方**、技能 grid 之前插入（仅当有 pending 提案时显示）：

```tsx
{proposals.length > 0 && (
  <div className="mb-6">
    <h2 className="mb-2 text-sm font-semibold flex items-center gap-2">
      <Sparkles className="h-4 w-4" /> {t("skills.evolution.proposals")}
    </h2>
    <div className="space-y-3">
      {proposals.map((p) => (
        <div key={p.ID} className="rounded-lg border p-3">
          <div className="mb-2 text-sm">
            <span className="font-mono text-xs">{p.Sources.join(" + ")}</span>
            <span className="mx-2">→</span>
            <span className="font-medium">{p.TargetName}</span>
            <span className="ml-2 text-xs text-muted-foreground">{p.Evidence}</span>
          </div>
          <div className="mb-2 flex flex-wrap gap-3">
            {p.Sources.map((s) => (
              <label key={s} className="flex items-center gap-1 text-xs">
                <input
                  type="checkbox"
                  checked={keepMap[p.ID]?.[s] ?? true}
                  onChange={(e) =>
                    setKeepMap((m) => ({ ...m, [p.ID]: { ...(m[p.ID] || {}), [s]: e.target.checked } }))
                  }
                />
                {t("skills.evolution.keep")} {s}
              </label>
            ))}
          </div>
          <div className="flex gap-2">
            <Button size="sm" onClick={() => handleAccept(p)}>
              <Check className="h-3 w-3" /> {t("skills.evolution.accept")}
            </Button>
            <Button size="sm" variant="outline" onClick={() => handleReject(p)}>
              {t("skills.evolution.reject")}
            </Button>
          </div>
        </div>
      ))}
    </div>
  </div>
)}
```

> i18n keys `skills.evolution.{proposals,keep,accept,reject}` 同步加。

- [ ] **Step 2: 构建 + 提交**

Run: `cd web && pnpm build`

```bash
git add web/src/app/agents/[id]/skills/page.tsx
git commit -m "feat(web): 可升级技能列表（per-source 保留勾选 + 接受/拒绝）"
```

---

### Task 4: 归档视图（列表 + 永久删除）

**Files:** Modify `web/src/app/agents/[id]/skills/page.tsx`

- [ ] **Step 1: 加删除 handler + 归档区 UI**

handler（复用页面现有 `AlertDialog` 删除确认模式——`deleteTarget` state 那套）：

```ts
  const [archiveDelete, setArchiveDelete] = useState<ArchivedSkill | null>(null);
  const handleArchiveDelete = async () => {
    if (!archiveDelete) return;
    await deleteArchivedSkill(agentId, archiveDelete.Name, archiveDelete.ArchivedAt);
    setArchiveDelete(null);
    fetchSkills();
  };
```

在页面底部（技能 grid 之后）加归档区：

```tsx
{archived.length > 0 && (
  <div className="mt-8">
    <h2 className="mb-2 text-sm font-semibold flex items-center gap-2">
      <Files className="h-4 w-4" /> {t("skills.evolution.archived")}
    </h2>
    <div className="space-y-1">
      {archived.map((a) => (
        <div key={a.Name + a.ArchivedAt} className="flex items-center justify-between rounded border px-3 py-1 text-sm">
          <span>
            <span className="font-mono text-xs">{a.Name}</span>
            <span className="ml-2 text-xs text-muted-foreground">{a.ArchivedAt}</span>
          </span>
          <Button size="sm" variant="ghost" onClick={() => setArchiveDelete(a)}>
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      ))}
    </div>
  </div>
)}

{/* 永久删除确认（复用现有 AlertDialog 模式）*/}
<AlertDialog open={!!archiveDelete} onOpenChange={(o) => !o && setArchiveDelete(null)}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>{t("skills.evolution.permanentDelete")}</AlertDialogTitle>
      <AlertDialogDescription>{archiveDelete?.Name}</AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
      <AlertDialogAction onClick={handleArchiveDelete}>{t("delete")}</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>
```

> i18n keys `skills.evolution.{archived,permanentDelete}` + 复用现有 `cancel`/`delete`。

- [ ] **Step 2: 构建 + 提交**

Run: `cd web && pnpm build`

```bash
git add web/src/app/agents/[id]/skills/page.tsx
git commit -m "feat(web): 归档技能视图（列表 + 永久删除确认）"
```

---

### Task 5: 浏览器实测（CLAUDE.md 要求）

**Files:** 无代码改动——验证 Task 2-4 的 UI。

- [ ] **Step 1: 起 web dev server**

Run: `cd web && pnpm dev`（或按项目惯例），浏览器开 agent 技能管理页。

- [ ] **Step 2: 测 golden path**

- 上方控件：开关切换 → 刷新仍在（配置持久）；通知填 channel/chatID → 保存。
- 可升级列表：有 pending 提案时显示；勾选/取消保留来源；接受 → 新技能出现在列表、未保留的消失（归档）；拒绝 → 提案消失。
- 归档视图：接受后归档项出现；永久删除 → 二次确认 → 消失。
- 无提案时列表区隐藏（不报错）。

- [ ] **Step 3: 嵌入新 web 到 binary（若要测嵌入态）**

按 memory `web-build-embed-cache`：`cd web && rm -rf .next out && pnpm build` → `rm -rf internal/setup/web && cp -r web/out internal/setup/web` → `go build -a ./cmd/lununda`（`-a` 绕过 embed 缓存）。

- [ ] **Step 4: 修复实测发现的问题 + 提交**

```bash
git add -A
git commit -m "fix(web): 技能演进 UI 浏览器实测修复"
```

---

## 自审

- **Spec 覆盖**：D5 UX（上方控件 Task 2、可升级列表 Task 3）、D6 归档视图（Task 4）、D5 "默认勾保留"（keepMap 初值全 true）、D6 "永久删除手动 + 二次确认"（Task 4 AlertDialog）。✓
- **接地**：页面结构 `"use client"` + shadcn/ui + `useT()` + `getConfig()` + `Promise.all` fetch（`agents/[id]/skills/page.tsx:1-103` 实证）；API 客户端 `apiFetch` 模式（`api.ts:1567` 实证）；删除确认复用页面现有 `AlertDialog`/`deleteTarget` 模式（实证）。✓
- **占位符**：每步完整 TSX；2 个 integration note（config-save 函数名、IM 渠道选单）给了复用/v1 降级方案，非空洞 TBD；i18n key 列明需同步加。✓
- **类型一致**：`SkillProposal{ID,Sources,TargetName,...}`、`ArchivedSkill{Name,ArchivedAt,Path}`、`SkillEvolutionCfg{enabled,interval,model,notify}`——api.ts 与 page 一致；与后端 JSON（Plan 3/5/6 Go 结构）字段名对齐（`ID`/`Sources`/`TargetName` 大写首字母，Go JSON 默认导出字段名）。✓
- **未覆盖（Plan 8）**：生命周期清退（stale→archive）。本计划完成 dashboard UI；至此 spec 的 D0-D9 全部有 plan 覆盖（D10 生命周期 = Plan 8）。✓
