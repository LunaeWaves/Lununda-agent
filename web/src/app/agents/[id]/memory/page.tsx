"use client";

import { useEffect, useState, useCallback } from "react";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Database, Boxes, Settings2, Check, Loader2 } from "lucide-react";
import {
  getAgentMemory,
  setAgentMemory,
  type MemoryConfig,
  type MemoryEmbeddingConfig,
  type MemoryRerankerConfig,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

// Per-agent Memory page — mirrors the top-level /memory page design, but
// reads/writes the agent-scope override. Runtime precedence:
//   agent-scope memory > owner-user memory > system memory
// `hasOverride` from GET tells us whether an agent-scope row exists, so
// we can render Inheriting/Override + offer Clear override. Saving always
// writes the full block to agent scope; clearing deletes the row.
export default function AgentMemoryPage() {
  const t = useT();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [hasOverride, setHasOverride] = useState(false);

  const [embedding, setEmbedding] = useState<MemoryEmbeddingConfig>({
    enabled: false,
    provider: "",
    model: "",
    apiKey: "",
    apiBase: "",
    dim: 1024,
  });
  const [reranker, setReranker] = useState<MemoryRerankerConfig>({
    enabled: false,
    provider: "",
    model: "",
    apiKey: "",
    apiBase: "",
  });
  const [settings, setSettings] = useState<{ enabled?: boolean }>({
    enabled: true,
  });

  const refresh = useCallback(async () => {
    try {
      const res = await getAgentMemory(agentId);
      setHasOverride(res.hasOverride);
      const mem: MemoryConfig = res.memory || {};
      if (mem.embedding) {
        setEmbedding({
          enabled: mem.embedding.enabled ?? false,
          provider: mem.embedding.provider || "",
          model: mem.embedding.model || "",
          apiKey: mem.embedding.apiKey || "",
          apiBase: mem.embedding.apiBase || "",
          dim: mem.embedding.dim || 1024,
        });
      }
      if (mem.reranker) {
        setReranker({
          enabled: mem.reranker.enabled ?? false,
          provider: mem.reranker.provider || "",
          model: mem.reranker.model || "",
          apiKey: mem.reranker.apiKey || "",
          apiBase: mem.reranker.apiBase || "",
        });
      }
      if (mem.settings) {
        setSettings({ enabled: mem.settings.enabled ?? true });
      }
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const flashSaved = () => {
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await setAgentMemory(agentId, { embedding, reranker, settings });
      flashSaved();
      await refresh();
    } finally {
      setSaving(false);
    }
  };

  // Clear override deletes the agent-scope row so the agent falls back to
  // system + owner-user. We then reload to repopulate the form with the
  // inherited effective values.
  const handleClearOverride = async () => {
    if (!confirm(t("memory.clearConfirm") || "Clear this agent's memory override and inherit system/user defaults?")) return;
    setSaving(true);
    try {
      await setAgentMemory(agentId, null);
      flashSaved();
      await refresh();
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-32" />
        ))}
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("memory.title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("memory.agentSubtitle").replace("{name}", agentName || agentId)}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {hasOverride ? (
            <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
              {t("memory.override")}
            </Badge>
          ) : (
            <Badge variant="outline" className="text-[10px]">
              {t("memory.inheriting")}
            </Badge>
          )}
          <Button
            onClick={handleSave}
            disabled={saving}
            variant={saved ? "outline" : "default"}
            className={saved ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400" : ""}
          >
            {saved ? (
              <>
                <Check className="h-4 w-4 mr-2" />
                {t("common.saved")}
              </>
            ) : saving ? (
              <>
                <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                {t("common.saving")}
              </>
            ) : (
              t("common.save")
            )}
          </Button>
          {hasOverride && (
            <Button
              variant="ghost"
              size="sm"
              className="h-9 text-xs"
              onClick={handleClearOverride}
              disabled={saving}
            >
              {t("memory.clearOverride")}
            </Button>
          )}
        </div>
      </div>

      {/* Settings — master switch */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Settings2 className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{t("memory.memorySettings")}</h3>
              <p className="text-sm text-muted-foreground mt-1">{t("memory.settingsDesc")}</p>
              <p className="text-xs text-muted-foreground/70 mt-1">{t("memory.enableRecall")}</p>
            </div>
          </div>
          <Switch
            checked={settings.enabled ?? true}
            onCheckedChange={(v: boolean) => setSettings({ enabled: v })}
            aria-label={t("memory.memorySettings")}
          />
        </div>
      </div>

      {/* Embedding */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Database className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h3 className="font-medium">{t("memory.embedding")}</h3>
                {embedding.enabled ? (
                  <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15 text-[10px]">
                    {t("memory.configured")}
                  </Badge>
                ) : (
                  <Badge variant="outline" className="text-muted-foreground text-[10px]">
                    {t("memory.notConfigured")}
                  </Badge>
                )}
              </div>
              <p className="text-sm text-muted-foreground mt-1">{t("memory.embeddingDesc")}</p>
            </div>
          </div>
          <Switch
            checked={embedding.enabled}
            onCheckedChange={(v: boolean) => setEmbedding({ ...embedding, enabled: v })}
            aria-label={t("memory.embedding")}
          />
        </div>

        {embedding.enabled && (
          <div className="mt-5 pt-5 border-t border-border space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("memory.provider")}</Label>
                <Input
                  value={embedding.provider || ""}
                  onChange={(e) => setEmbedding({ ...embedding, provider: e.target.value })}
                  placeholder={t("memory.providerPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("memory.model")}</Label>
                <Input
                  value={embedding.model || ""}
                  onChange={(e) => setEmbedding({ ...embedding, model: e.target.value })}
                  placeholder={t("memory.modelPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label>{t("memory.apiBase")}</Label>
              <Input
                value={embedding.apiBase || ""}
                onChange={(e) => setEmbedding({ ...embedding, apiBase: e.target.value })}
                placeholder={t("memory.apiBasePlaceholder")}
                className="font-mono text-sm"
              />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("memory.apiKey")}</Label>
                <Input
                  type="password"
                  value={embedding.apiKey || ""}
                  onChange={(e) => setEmbedding({ ...embedding, apiKey: e.target.value })}
                  placeholder={t("memory.apiKeyPlaceholder")}
                  className="font-mono text-sm placeholder:text-muted-foreground/70"
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("memory.dimensions")}</Label>
                <Input
                  type="number"
                  value={embedding.dim || 1024}
                  onChange={(e) =>
                    setEmbedding({ ...embedding, dim: parseInt(e.target.value) || 1024 })
                  }
                  placeholder="1024"
                  className="font-mono text-sm"
                />
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Reranker */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Boxes className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h3 className="font-medium">{t("memory.reranker")}</h3>
                {reranker.enabled ? (
                  <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15 text-[10px]">
                    {t("memory.configured")}
                  </Badge>
                ) : (
                  <Badge variant="outline" className="text-muted-foreground text-[10px]">
                    {t("memory.notConfigured")}
                  </Badge>
                )}
              </div>
              <p className="text-sm text-muted-foreground mt-1">{t("memory.rerankerDesc")}</p>
            </div>
          </div>
          <Switch
            checked={reranker.enabled}
            onCheckedChange={(v: boolean) => setReranker({ ...reranker, enabled: v })}
            aria-label={t("memory.reranker")}
          />
        </div>

        {reranker.enabled && (
          <div className="mt-5 pt-5 border-t border-border space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("memory.provider")}</Label>
                <Input
                  value={reranker.provider || ""}
                  onChange={(e) => setReranker({ ...reranker, provider: e.target.value })}
                  placeholder={t("memory.providerPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("memory.model")}</Label>
                <Input
                  value={reranker.model || ""}
                  onChange={(e) => setReranker({ ...reranker, model: e.target.value })}
                  placeholder={t("memory.rerankerModelPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label>{t("memory.apiBase")}</Label>
              <Input
                value={reranker.apiBase || ""}
                onChange={(e) => setReranker({ ...reranker, apiBase: e.target.value })}
                placeholder={t("memory.rerankerApiBasePlaceholder")}
                className="font-mono text-sm"
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t("memory.apiKey")}</Label>
              <Input
                type="password"
                value={reranker.apiKey || ""}
                onChange={(e) => setReranker({ ...reranker, apiKey: e.target.value })}
                placeholder={t("memory.apiKeyPlaceholder")}
                className="font-mono text-sm placeholder:text-muted-foreground/70"
              />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
