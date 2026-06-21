"use client";
import { useT } from "@/lib/i18n";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Sparkles,
  Trash2,
  Download,
  Search,
  Loader2,
  Check,
  ExternalLink,
  Settings,
  Upload,
  Files,
  Info,
} from "lucide-react";
import {
  getAgentSkills,
  deleteAgentSkill,
  installSkill,
  uploadSkill,
  searchSkills,
  getConfig,
  getAgentMemory,
  setAgentMemory,
  getAgentSkillProposals,
  acceptSkillProposal,
  rejectSkillProposal,
  getArchivedSkills,
  deleteArchivedSkill,
  getStaleSkills,
  archiveOneSkill,
  togglePinSkill,
  listAgentChannels,
  getLastSessionByChannel,
  type SkillInfo,
  type SkillSearchResult,
  type SkillProposal,
  type ArchivedSkill,
  type SkillEvolutionCfg,
  type AgentChannel,
} from "@/lib/api";
import { ConfigureSkillDialog, type SkillEntryView } from "@/components/configure-skill-dialog";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

export default function AgentSkillsPage() {
  const t = useT();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [installOpen, setInstallOpen] = useState(false);
  const [configureTarget, setConfigureTarget] = useState<SkillInfo | null>(null);
  // Skill env entries are GLOBAL (keyed by skill name), so the same
  // /api/config blob feeds both the global /skills page and this
  // agent-scoped one. Lets the user configure FAL_KEY etc. from
  // whichever entry point they're already on.
  const [skillEntries, setSkillEntries] = useState<Record<string, SkillEntryView>>({});
  // File input ref + upload state for the local-zip t("skills.upload") button.
  // The server unzips to <agent>/skills/<name>/ and hot-reloads the
  // agent so the new skill shows up without a refresh.
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const [proposals, setProposals] = useState<SkillProposal[]>([]);
  const [archived, setArchived] = useState<ArchivedSkill[]>([]);
  const [evoCfg, setEvoCfg] = useState<SkillEvolutionCfg>({ enabled: false });
  const [evoSaving, setEvoSaving] = useState(false);
  const evoSavingRef = useRef(false);
  const [keepMap, setKeepMap] = useState<Record<string, Record<string, boolean>>>({});
  const [archiveDelete, setArchiveDelete] = useState<ArchivedSkill | null>(null);
  const [stale, setStale] = useState<string[]>([]);
  const [agentChannels, setAgentChannels] = useState<AgentChannel[]>([]);
  const [evoError, setEvoError] = useState<string | null>(null);

  const fetchSkills = useCallback(() => {
    setLoading(true);
    Promise.all([
      getAgentSkills(agentId).catch(() => [] as SkillInfo[]),
      getConfig().catch(() => null),
      getAgentSkillProposals(agentId).catch(() => [] as SkillProposal[]),
      getArchivedSkills(agentId).catch(() => [] as ArchivedSkill[]),
      getAgentMemory(agentId).catch(() => null),
      getStaleSkills(agentId).catch(() => [] as string[]),
      listAgentChannels(agentId).catch(() => [] as AgentChannel[]),
    ])
      .then(([list, cfg, props, arch, mem, staleList, chans]) => {
        setSkills(list || []);
        // Per-agent override map first (this page edits there); merge
        // global defaults underneath so the "configured" badge still
        // lights up when only the global value is set.
        const skillsCfg = cfg?.skills as
          | {
              entries?: Record<string, SkillEntryView>;
              agentEntries?: Record<string, Record<string, SkillEntryView>>;
            }
          | undefined;
        const globalEntries = skillsCfg?.entries || {};
        const agentMap = skillsCfg?.agentEntries?.[agentId] || {};
        const merged: Record<string, SkillEntryView> = { ...globalEntries };
        for (const [name, entry] of Object.entries(agentMap)) {
          merged[name] = entry;
        }
        setSkillEntries(merged);
        setProposals(props || []);
        setArchived(arch || []);
        setStale(staleList || []);
        setAgentChannels(chans || []);
        setEvoCfg(mem?.memory?.skillEvolution || { enabled: false });
        const km: Record<string, Record<string, boolean>> = {};
        for (const p of props || []) {
          km[p.ID] = Object.fromEntries((p.Sources || []).map((s) => [s, true]));
        }
        setKeepMap(km);
      })
      .finally(() => setLoading(false));
  }, [agentId]);

  useEffect(() => {
    fetchSkills();
  }, [fetchSkills]);

  // Auto-fill chatID/accountID from the agent's most recent session on
  // the selected channel. Fires on channel change + once channels load.
  // Skips when chatID is already set (user override or saved value).
  const notifyChannel = evoCfg.notify?.channel || "";
  useEffect(() => {
    if (!agentId || !notifyChannel) return;
    if (evoCfg.notify?.chatID) return;
    let cancelled = false;
    getLastSessionByChannel(agentId, notifyChannel)
      .then((s) => {
        if (cancelled || !s) return;
        const next = {
          ...evoCfg,
          notify: {
            ...evoCfg.notify,
            enabled: evoCfg.notify?.enabled ?? false,
            chatID: s.chatId,
            accountID: s.accountId || evoCfg.notify?.accountID || "",
          },
        };
        setEvoCfg(next);
        void saveEvoCfg(next);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId, notifyChannel, evoCfg.notify?.chatID]);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    await deleteAgentSkill(agentId, deleteTarget);
    setDeleteTarget(null);
    fetchSkills();
  };

  const saveEvoCfg = async (next: SkillEvolutionCfg) => {
    // Ref guard fires synchronously (disabled={evoSaving} only takes effect
    // next render), so two quick toggles in one batch can't interleave their
    // read-modify-write and clobber each other.
    if (evoSavingRef.current) return;
    evoSavingRef.current = true;
    setEvoCfg(next);
    setEvoSaving(true);
    try {
      const cur = await getAgentMemory(agentId).catch(() => null);
      const base = cur?.memory || {};
      await setAgentMemory(agentId, { ...base, skillEvolution: next });
    } finally {
      setEvoSaving(false);
      evoSavingRef.current = false;
    }
  };

  const runEvoAction = async (label: string, fn: () => Promise<void>) => {
    setEvoError(null);
    try {
      await fn();
      fetchSkills();
    } catch (e) {
      setEvoError(`${t("skills.evolution.actionFailed")}: ${e instanceof Error ? e.message : label}`);
    }
  };

  const handleAcceptProposal = (p: SkillProposal) =>
    runEvoAction("accept", async () => {
      const keep = Object.entries(keepMap[p.ID] || {})
        .filter(([, v]) => v)
        .map(([k]) => k);
      await acceptSkillProposal(agentId, p.ID, keep);
    });

  const handleRejectProposal = (p: SkillProposal) =>
    runEvoAction("reject", () => rejectSkillProposal(agentId, p.ID));

  const handleArchiveDelete = () =>
    runEvoAction("delete", async () => {
      if (!archiveDelete) return;
      await deleteArchivedSkill(agentId, archiveDelete.Name, archiveDelete.ArchivedAt);
      setArchiveDelete(null);
    });

  const handlePinStale = (name: string) =>
    runEvoAction("pin", () => togglePinSkill(agentId, name, true));
  const handleArchiveStale = (name: string) =>
    runEvoAction("archive", () => archiveOneSkill(agentId, name));

  const handleUploadConfirm = async () => {
    if (!uploadFile || !agentId) return;
    setUploading(true);
    setUploadError(null);
    try {
      const resp = await uploadSkill(uploadFile, agentId);
      if (!resp.ok) {
        // Backend rejects zips that don't contain SKILL.md at the
        // skill root — surface the message inside the dialog so the
        // user can fix the zip and retry without re-opening it.
        setUploadError(resp.error || "upload failed");
        return;
      }
      // Success — close, reset, refresh the grid.
      setUploadOpen(false);
      setUploadFile(null);
      fetchSkills();
    } catch (e) {
      setUploadError(e instanceof Error ? e.message : "upload failed");
    } finally {
      setUploading(false);
      if (uploadInputRef.current) uploadInputRef.current.value = "";
    }
  };

  // Drop dialog state when the dialog closes (cancel or success), so the
  // next open is fresh — no stale file or error from a previous attempt.
  const handleUploadOpenChange = (open: boolean) => {
    setUploadOpen(open);
    if (!open) {
      setUploadFile(null);
      setUploadError(null);
      setDragOver(false);
      if (uploadInputRef.current) uploadInputRef.current.value = "";
    }
  };

  // Filter dropped/selected files to a single .zip — the dropzone accepts
  // multi-drop in the browser, but a skill bundle is one archive so we
  // take the first .zip and reject the rest with an inline message.
  const acceptDroppedFiles = (files: FileList | null) => {
    if (!files || files.length === 0) return;
    if (files.length > 1) {
      setUploadError(t("skills.dropOne"));
      return;
    }
    const f = files[0];
    if (!/\.zip$/i.test(f.name)) {
      setUploadError(t("skills.mustBeZip"));
      return;
    }
    setUploadFile(f);
    setUploadError(null);
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("skills.title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("skills.agentSubtitle")} <strong>{agentName}</strong> {t("skills.agentSubtitleSuffix")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => setUploadOpen(true)}>
            <Upload className="h-4 w-4 mr-2" />
            Upload Skills
          </Button>
          <Button variant="outline" onClick={() => setInstallOpen(true)}>
            <Download className="h-4 w-4 mr-2" />
            Install Skill
          </Button>
        </div>
      </div>

      {/* 技能迭代升级控件 */}
      <div className="rounded-lg border p-4 flex flex-wrap items-center gap-4">
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
            disabled={evoSaving}
          />
        </label>
        <div className="flex items-center gap-2 text-sm">
          <span className="text-xs text-muted-foreground">{t("skills.evolution.notifyChannel")}</span>
          <Select
            value={evoCfg.notify?.channel || ""}
            onValueChange={(v) => saveEvoCfg({ ...evoCfg, notify: { ...evoCfg.notify, enabled: true, channel: v ?? "", chatID: "" } })}
            disabled={evoSaving}
          >
            <SelectTrigger className="w-32 h-8">
              <SelectValue placeholder={t("skills.evolution.notifyChannel")} />
            </SelectTrigger>
            <SelectContent>
              {Array.from(new Set(agentChannels.map((c) => c.type))).map((type) => (
                <SelectItem key={type} value={type}>
                  {type}
                </SelectItem>
              ))}
              {evoCfg.notify?.channel && !agentChannels.some((c) => c.type === evoCfg.notify?.channel) && (
                <SelectItem value={evoCfg.notify.channel}>{evoCfg.notify.channel}</SelectItem>
              )}
            </SelectContent>
          </Select>
        </div>
        {evoCfg.notify?.chatID && (
          <div className="flex items-center gap-2 text-sm">
            <span className="text-xs text-muted-foreground">{t("skills.evolution.notifyChatID")}</span>
            <Badge variant="outline" className="font-mono text-[10px] max-w-[200px] truncate" title={evoCfg.notify.chatID}>
              {evoCfg.notify.chatID}
            </Badge>
          </div>
        )}
        {evoCfg.notify?.accountID && (
          <div className="flex items-center gap-2 text-sm">
            <span className="text-xs text-muted-foreground">{t("skills.evolution.notifyAccountID")}</span>
            <Badge variant="outline" className="font-mono text-[10px] max-w-[180px] truncate" title={evoCfg.notify.accountID}>
              {evoCfg.notify.accountID}
            </Badge>
          </div>
        )}
        {evoSaving && <Loader2 className="h-4 w-4 animate-spin" />}
        {evoError && (
          <p className="w-full rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive break-words">
            {evoError}
          </p>
        )}
      </div>

      {/* 可升级技能提案 */}
      {proposals.length > 0 && (
        <div>
          <h2 className="mb-2 text-sm font-semibold flex items-center gap-2">
            <Sparkles className="h-4 w-4" /> {t("skills.evolution.proposals")}
          </h2>
          <div className="space-y-3">
            {proposals.map((p) => (
              <div key={p.ID} className="rounded-lg border p-3">
                <div className="mb-2 text-sm">
                  <span className="font-mono text-xs">{(p.Sources || []).join(" + ")}</span>
                  <span className="mx-2">→</span>
                  <span className="font-medium">{p.TargetName}</span>
                  {p.Evidence && (
                    <span className="ml-2 text-xs text-muted-foreground">{p.Evidence}</span>
                  )}
                </div>
                <div className="mb-2 flex flex-wrap gap-3">
                  {(p.Sources || []).map((s) => (
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
                  <Button size="sm" onClick={() => handleAcceptProposal(p)}>
                    <Check className="h-3 w-3 mr-1" /> {t("skills.evolution.accept")}
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => handleRejectProposal(p)}>
                    {t("skills.evolution.reject")}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-40" />
          ))}
        </div>
      ) : skills.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Sparkles className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">
              No agent-scoped skills yet
            </p>
            <p className="text-xs text-muted-foreground/60 mb-4 max-w-sm text-center">
              {t("skills.noAgentSkillsDesc")}
            </p>
            <Button variant="outline" size="sm" onClick={() => setInstallOpen(true)}>
              <Download className="h-4 w-4 mr-2" />
              Install Skill
            </Button>
          </div>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {skills.map((skill) => (
            <div
              key={skill.name}
              className="group rounded-lg border border-border bg-card p-5 transition-colors hover:bg-muted/50"
            >
              <div className="flex items-start justify-between mb-3">
                <div className="flex items-center gap-2.5">
                  <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10">
                    <Sparkles className="h-4 w-4 text-primary" />
                  </div>
                  <div>
                    <p className="text-sm font-medium">{skill.name}</p>
                    <Badge variant="outline" className="mt-1 text-[10px]">
                      {skill.type || "skill"}
                    </Badge>
                  </div>
                </div>
                <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-foreground"
                    onClick={() => setConfigureTarget(skill)}
                    title={t("skills.configure")}
                  >
                    <Settings className="h-3.5 w-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    onClick={() => setDeleteTarget(skill.name)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
              <p className="text-sm text-muted-foreground line-clamp-2">
                {skill.description || t("skills.noDescription")}
              </p>
              {(skillEntries[skill.name]?.apiKey ||
                Object.keys(skillEntries[skill.name]?.env || {}).length > 0) && (
                <div className="mt-2 inline-flex items-center gap-1 text-[10px] text-emerald-500">
                  <Check className="h-3 w-3" />
                  configured
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* 陈旧技能 */}
      {stale.length > 0 && (
        <div>
          <h2 className="mb-2 text-sm font-semibold flex items-center gap-2">
            <Info className="h-4 w-4" /> {t("skills.evolution.stale")}
          </h2>
          <div className="space-y-1">
            {stale.map((name) => (
              <div
                key={name}
                className="flex items-center justify-between rounded border px-3 py-1 text-sm"
              >
                <span className="font-mono text-xs">{name}</span>
                <div className="flex gap-1">
                  <Button size="sm" variant="ghost" onClick={() => handlePinStale(name)}>
                    {t("skills.evolution.pin")}
                  </Button>
                  <Button size="sm" variant="ghost" aria-label={t("skills.evolution.archiveAction")} onClick={() => handleArchiveStale(name)}>
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 已归档技能 */}
      {archived.length > 0 && (
        <div>
          <h2 className="mb-2 text-sm font-semibold flex items-center gap-2">
            <Files className="h-4 w-4" /> {t("skills.evolution.archived")}
          </h2>
          <div className="space-y-1">
            {archived.map((a) => (
              <div
                key={a.Name + a.ArchivedAt}
                className="flex items-center justify-between rounded border px-3 py-1 text-sm"
              >
                <span>
                  <span className="font-mono text-xs">{a.Name}</span>
                  <span className="ml-2 text-xs text-muted-foreground">{a.ArchivedAt}</span>
                </span>
                <Button size="sm" variant="ghost" aria-label={t("skills.evolution.permanentDelete")} onClick={() => setArchiveDelete(a)}>
                  <Trash2 className="h-3 w-3" />
                </Button>
              </div>
            ))}
          </div>
        </div>
      )}

      <AlertDialog open={!!archiveDelete} onOpenChange={(o) => !o && setArchiveDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("skills.evolution.permanentDelete")}</AlertDialogTitle>
            <AlertDialogDescription>{archiveDelete?.Name}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleArchiveDelete}>{t("common.delete")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog open={uploadOpen} onOpenChange={handleUploadOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("skills.uploadSkill")}</DialogTitle>
          </DialogHeader>

          {/* Hidden input — both the drop zone click and the "click to upload"
              text trigger it. accept= filters the OS picker to .zip; we still
              re-validate in JS for drops since accept doesn't apply there. */}
          <input
            ref={uploadInputRef}
            type="file"
            accept=".zip,application/zip,application/x-zip-compressed"
            className="hidden"
            onChange={(e) => acceptDroppedFiles(e.target.files)}
          />

          {/* Drop zone. Keeps a constant footprint (32rem-ish content,
              ~12rem tall) so the dialog doesn't jump when a file is
              picked — empty state shows the icon + prompt, populated
              state shows the chosen filename inline. */}
          <button
            type="button"
            onClick={() => uploadInputRef.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              setDragOver(true);
            }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragOver(false);
              acceptDroppedFiles(e.dataTransfer.files);
            }}
            className={`flex h-48 w-full flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed bg-muted/20 px-6 py-8 text-center transition-colors hover:bg-muted/40 ${
              dragOver ? "border-primary bg-primary/5" : "border-border"
            }`}
          >
            <Files
              className={`h-10 w-10 ${
                uploadFile ? "text-primary" : "text-muted-foreground/60"
              }`}
              strokeWidth={1.4}
            />
            {uploadFile ? (
              <div className="space-y-1">
                <p className="text-sm font-medium break-all">{uploadFile.name}</p>
                <p className="text-xs text-muted-foreground">
                  {(uploadFile.size / 1024).toFixed(1)} KB · click to choose a different file
                </p>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">
                Drag and drop or click to upload
              </p>
            )}
          </button>

          <div className="space-y-2">
            <p className="text-sm font-medium">{t("skills.fileRequirements")}</p>
            <ul className="space-y-1.5 text-sm text-muted-foreground">
              <li className="flex gap-2">
                <span className="text-muted-foreground/60">•</span>
                <span>
                  <code className="text-foreground">.zip</code> {t("skills.zipWithSkillMd")}
                </span>
              </li>
              <li className="flex gap-2">
                <span className="text-muted-foreground/60">•</span>
                <span>
                  <code className="text-foreground">SKILL.md</code> {t("skills.skillMdYaml")}
                </span>
              </li>
            </ul>
          </div>

          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Info className="h-3.5 w-3.5 shrink-0" />
            <a
              href="https://docs.claude.com/en/docs/claude-code/skills"
              target="_blank"
              rel="noreferrer"
              className="underline hover:text-foreground"
            >
              Read more about creating skills
            </a>
          </div>

          {uploadError && (
            <p className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive break-words">
              {uploadError}
            </p>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <Button
              variant="outline"
              onClick={() => handleUploadOpenChange(false)}
              disabled={uploading}
            >
              Cancel
            </Button>
            <Button
              onClick={handleUploadConfirm}
              disabled={!uploadFile || uploading}
            >
              {uploading ? (
                <>
                  <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                  {t("skills.uploading")}
                </>
              ) : (
                "Upload"
              )}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!deleteTarget} onOpenChange={() => setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("skills.removeSkill")}</AlertDialogTitle>
            <AlertDialogDescription>
              Remove <strong>{deleteTarget}</strong> from{" "}
              <strong>{agentName}</strong>? Other agents are
              unaffected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <InstallSkillDialog
        agentId={agentId}
        agentName={agentName}
        open={installOpen}
        onOpenChange={setInstallOpen}
        onInstalled={() => {
          setInstallOpen(false);
          fetchSkills();
        }}
        installedNames={new Set(skills.map((s) => s.name))}
      />

      <ConfigureSkillDialog
        skill={configureTarget}
        agentId={agentId}
        existing={configureTarget ? skillEntries[configureTarget.name] : undefined}
        onClose={() => setConfigureTarget(null)}
        onSaved={() => {
          setConfigureTarget(null);
          fetchSkills();
        }}
      />
    </div>
  );
}

function InstallSkillDialog({
  agentId,
  agentName,
  open,
  onOpenChange,
  onInstalled,
  installedNames,
}: {
  agentId: string;
  agentName: string;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onInstalled: () => void;
  installedNames: Set<string>;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [installingId, setInstallingId] = useState<string | null>(null);
  const [installError, setInstallError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (!open) {
      setQuery("");
      setResults([]);
      setInstallError(null);
    }
  }, [open]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (!open) return;
    if (!query.trim()) {
      setResults([]);
      setSearching(false);
      return;
    }
    setSearching(true);
    debounceRef.current = setTimeout(() => {
      searchSkills(query)
        .then((r) => setResults(r))
        .catch(() => setResults([]))
        .finally(() => setSearching(false));
    }, 300);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, open]);

  const visible = useMemo(() => results.slice(0, 20), [results]);

  const handleInstall = async (r: SkillSearchResult) => {
    setInstallError(null);
    setInstallingId(r.id);
    try {
      // agent: agentId → backend installs into ~/.lununda/agents/<id>/skills
      const resp = await installSkill({
        source: "skillssh",
        name: r.skillId,
        agent: agentId,
      });
      if (!resp.ok) {
        setInstallError(resp.error || "install failed");
        return;
      }
      onInstalled();
    } catch (e) {
      setInstallError(e instanceof Error ? e.message : "install failed");
    } finally {
      setInstallingId(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("skills.installFor", { name: agentName })}</DialogTitle>
          <DialogDescription>
            Search skills.sh and install into{" "}
            <code className="font-mono text-xs">
              ~/.lununda/agents/{agentId}/skills/
            </code>
            . Only this agent will see the new skill.
          </DialogDescription>
        </DialogHeader>

        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground/70" />
          <Input
            autoFocus
            placeholder="pdf, translation, web scraping…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="pl-9"
          />
        </div>

        <div className="min-h-[240px] max-h-[420px] overflow-y-auto -mx-1 px-1">
          {!query.trim() ? (
            <div className="flex flex-col items-center justify-center py-12 text-center">
              <Sparkles className="h-8 w-8 text-muted-foreground/40 mb-3" />
              <p className="text-sm text-muted-foreground">
                Start typing to search skills.sh
              </p>
            </div>
          ) : searching ? (
            <div className="space-y-2 py-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-14" />
              ))}
            </div>
          ) : visible.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-10 text-center">
              <p className="text-sm text-muted-foreground mb-1">
                No skills found for{" "}
                <strong className="text-foreground">{query}</strong>
              </p>
            </div>
          ) : (
            <>
              <p className="text-[10px] uppercase tracking-wider text-muted-foreground/70 mb-1.5 px-1">
                Results from skills.sh
              </p>
              <div className="space-y-1.5 py-1">
                {visible.map((r) => {
                  const already = installedNames.has(r.skillId);
                  const busy = installingId === r.id;
                  const detailUrl = `https://skills.sh/${r.id}`;
                  return (
                    <div
                      key={r.id}
                      className="flex items-center gap-3 rounded-md border border-border bg-card p-3 hover:bg-muted/40 transition-colors"
                    >
                      <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 shrink-0">
                        <Sparkles className="h-4 w-4 text-primary" />
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <p className="text-sm font-medium truncate">{r.skillId}</p>
                          <span className="text-[10px] text-muted-foreground">
                            {r.installs.toLocaleString()} {t("skills.installs")}
                          </span>
                        </div>
                        <a
                          href={detailUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground font-mono truncate"
                          title={`View on skills.sh: ${r.id}`}
                        >
                          {r.source}
                          <ExternalLink className="h-3 w-3 shrink-0" />
                        </a>
                      </div>
                      <Button
                        size="sm"
                        variant={already ? "outline" : "default"}
                        disabled={already || busy}
                        onClick={() => handleInstall(r)}
                      >
                        {already ? (
                          <>
                            <Check className="h-3.5 w-3.5 mr-1.5" /> {t("skills.installed")}
                          </>
                        ) : busy ? (
                          <>
                            <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> {t("skills.installing")}
                          </>
                        ) : (
                          t("skills.installBtn")
                        )}
                      </Button>
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>

        {installError && (
          <p className="text-xs text-destructive break-all">{installError}</p>
        )}
      </DialogContent>
    </Dialog>
  );
}
